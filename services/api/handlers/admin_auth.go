package handlers

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Depo-dev/trident/services/api/internal/httputil"
	"github.com/Depo-dev/trident/services/api/internal/metrics"
	"github.com/Depo-dev/trident/services/api/middleware"
)

const (
	defaultAdminMaxFailures = 5
	defaultAdminLockoutDur  = 5 * time.Minute
)

// AdminAuth validates admin credentials, resolves individual operator identity,
// enforces IP/network allowlists, and protects against brute-force attacks (issue #792).
type AdminAuth struct {
	adminKey     string
	operators    map[string]string // operator name -> secret key
	allowedCIDRs []*net.IPNet
	internalOnly bool
	lockout      *adminLockout
	mu           sync.RWMutex
}

// adminLockout tracks consecutive authentication failures per IP address.
type adminLockout struct {
	mu          sync.Mutex
	failures    map[string]int
	lockedUntil map[string]time.Time
	maxFailures int
	lockoutDur  time.Duration
}

func newAdminLockout(maxFailures int, dur time.Duration) *adminLockout {
	if maxFailures <= 0 {
		maxFailures = defaultAdminMaxFailures
	}
	if dur <= 0 {
		dur = defaultAdminLockoutDur
	}
	return &adminLockout{
		failures:    make(map[string]int),
		lockedUntil: make(map[string]time.Time),
		maxFailures: maxFailures,
		lockoutDur:  dur,
	}
}

func (l *adminLockout) isLocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	until, exists := l.lockedUntil[ip]
	if !exists {
		return false
	}
	if time.Now().After(until) {
		delete(l.lockedUntil, ip)
		delete(l.failures, ip)
		return false
	}
	return true
}

func (l *adminLockout) recordFailure(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	count := l.failures[ip] + 1
	l.failures[ip] = count
	if count >= l.maxFailures {
		l.lockedUntil[ip] = time.Now().Add(l.lockoutDur)
	}
}

func (l *adminLockout) recordSuccess(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.failures, ip)
	delete(l.lockedUntil, ip)
}

// NewAdminAuth creates an AdminAuth instance with explicit configuration.
func NewAdminAuth(adminKey string, operators map[string]string, allowedCIDRStrings []string, internalOnly bool) *AdminAuth {
	auth := &AdminAuth{
		adminKey:     strings.TrimSpace(adminKey),
		operators:    make(map[string]string),
		internalOnly: internalOnly,
		lockout:      newAdminLockout(defaultAdminMaxFailures, defaultAdminLockoutDur),
	}

	for op, key := range operators {
		opTrim := strings.TrimSpace(op)
		keyTrim := strings.TrimSpace(key)
		if opTrim != "" && keyTrim != "" {
			auth.operators[opTrim] = keyTrim
		}
	}

	for _, raw := range allowedCIDRStrings {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if !strings.Contains(raw, "/") {
			if ip := net.ParseIP(raw); ip != nil {
				if ip.To4() != nil {
					raw = raw + "/32"
				} else {
					raw = raw + "/128"
				}
			}
		}
		_, cidr, err := net.ParseCIDR(raw)
		if err == nil && cidr != nil {
			auth.allowedCIDRs = append(auth.allowedCIDRs, cidr)
		} else {
			slog.Warn("admin: invalid CIDR in allowlist", "cidr", raw, "err", err)
		}
	}

	// If internalOnly is set and no explicit CIDRs were given, default to private/loopback CIDRs
	if internalOnly && len(auth.allowedCIDRs) == 0 {
		defaultInternal := []string{
			"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "::1/128", "fd00::/8",
		}
		for _, s := range defaultInternal {
			_, cidr, _ := net.ParseCIDR(s)
			if cidr != nil {
				auth.allowedCIDRs = append(auth.allowedCIDRs, cidr)
			}
		}
	}

	return auth
}

// NewAdminAuthFromEnv loads admin auth settings from environment variables.
func NewAdminAuthFromEnv() *AdminAuth {
	adminKey := os.Getenv("ADMIN_API_KEY")
	operators := parseOperators(os.Getenv("ADMIN_OPERATORS"))
	allowedCIDRs := parseCSV(os.Getenv("ADMIN_ALLOWED_CIDRS"))
	internalOnly := strings.EqualFold(os.Getenv("ADMIN_INTERNAL_ONLY"), "true")

	return NewAdminAuth(adminKey, operators, allowedCIDRs, internalOnly)
}

func parseOperators(raw string) map[string]string {
	res := make(map[string]string)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return res
	}

	// Try parsing JSON format: {"alice": "secret1", "bob": "secret2"}
	if strings.HasPrefix(raw, "{") {
		var m map[string]string
		if err := json.Unmarshal([]byte(raw), &m); err == nil {
			for k, v := range m {
				if k != "" && v != "" {
					res[k] = v
				}
			}
			return res
		}
	}

	// Fallback to comma-separated key=value or key:value format
	parts := strings.Split(raw, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		var name, key string
		if idx := strings.IndexAny(p, ":="); idx != -1 {
			name = strings.TrimSpace(p[:idx])
			key = strings.TrimSpace(p[idx+1:])
		}
		if name != "" && key != "" {
			res[name] = key
		}
	}
	return res
}

func parseCSV(raw string) []string {
	var res []string
	for _, part := range strings.Split(raw, ",") {
		if s := strings.TrimSpace(part); s != "" {
			res = append(res, s)
		}
	}
	return res
}

// Authorize validates admin credentials, client IP allowlists, lockout, and records metrics.
// When authorized, it updates the request context with WithAuditAuthSource and returns (operator, true).
func (a *AdminAuth) Authorize(w http.ResponseWriter, r *http.Request) (string, bool) {
	if a == nil || (a.adminKey == "" && len(a.operators) == 0) {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusServiceUnavailable, httputil.UNAVAILABLE, "admin API is not configured")
		return "", false
	}

	clientIPStr := middleware.ExtractClientIP(r)
	clientIP := net.ParseIP(clientIPStr)

	// 1. CIDR allowlist enforcement
	if (len(a.allowedCIDRs) > 0 || a.internalOnly) && clientIP != nil {
		allowed := false
		for _, cidr := range a.allowedCIDRs {
			if cidr.Contains(clientIP) {
				allowed = true
				break
			}
		}
		if !allowed {
			metrics.RecordAdminAuthFailure("ip_not_allowed")
			slog.Warn("admin: rejected request from non-allowlisted IP", "ip", clientIPStr, "path", r.URL.Path)
			httputil.WriteErrorCtx(r.Context(), w, http.StatusForbidden, httputil.FORBIDDEN, "admin endpoints are restricted to internal networks")
			return "", false
		}
	}

	// 2. Anti-bruteforce lockout check
	if a.lockout != nil && a.lockout.isLocked(clientIPStr) {
		metrics.RecordAdminAuthFailure("rate_limited")
		slog.Warn("admin: rejected request from locked-out IP", "ip", clientIPStr, "path", r.URL.Path)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusTooManyRequests, httputil.RATE_LIMITED, "too many failed admin authentication attempts, try again later")
		return "", false
	}

	// 3. Extract provided admin key
	providedKey := r.Header.Get("X-Admin-Key")
	if providedKey == "" {
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			providedKey = strings.TrimSpace(auth[7:])
		}
	}

	if providedKey == "" {
		if a.lockout != nil {
			a.lockout.recordFailure(clientIPStr)
		}
		metrics.RecordAdminAuthFailure("missing_key")
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, httputil.UNAUTHORIZED, "invalid or missing admin key")
		return "", false
	}

	// 4. Verify against per-operator credentials or primary adminKey using constant-time comparison
	var matchedOperator string

	// Check per-operator keys first
	for op, key := range a.operators {
		if subtle.ConstantTimeCompare([]byte(key), []byte(providedKey)) == 1 {
			matchedOperator = op
			break
		}
	}

	// Check primary static admin key
	if matchedOperator == "" && a.adminKey != "" {
		if subtle.ConstantTimeCompare([]byte(a.adminKey), []byte(providedKey)) == 1 {
			// If request provided an operator hint header, use it; otherwise fallback to "admin"
			if hint := r.Header.Get("X-Admin-Operator"); hint != "" {
				matchedOperator = hint
			} else {
				matchedOperator = "admin"
			}
		}
	}

	if matchedOperator == "" {
		if a.lockout != nil {
			a.lockout.recordFailure(clientIPStr)
		}
		metrics.RecordAdminAuthFailure("invalid_key")
		slog.Warn("admin: authentication failed with invalid key", "ip", clientIPStr, "path", r.URL.Path)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, httputil.UNAUTHORIZED, "invalid or missing admin key")
		return "", false
	}

	// 5. Successful authentication
	if a.lockout != nil {
		a.lockout.recordSuccess(clientIPStr)
	}

	// Inject operator identity into context for audit logging
	ctx := middleware.WithAuditAuthSource(r.Context(), "admin:"+matchedOperator)
	*r = *r.WithContext(ctx)

	return matchedOperator, true
}
