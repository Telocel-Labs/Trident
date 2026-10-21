package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Depo-dev/trident/services/api/internal/httputil"
	"github.com/Depo-dev/trident/services/api/internal/metrics"
	"github.com/redis/go-redis/v9"
)

// ---------------------------------------------------------------------------
// Rejection metrics, split by reason (issue #318). Follows the same
// package-level atomic-counter pattern as rlAllowed/rlRejected in
// ratelimit.go, exposed to callers (handlers.MetricsHandler) via
// AbuseMetrics, the same way RateLimitMetrics() is.
// ---------------------------------------------------------------------------

var (
	perIPAllowed, perIPRejected   atomic.Int64
	globalAllowed, globalRejected atomic.Int64
)

// AbuseMetrics returns rejection counters for the per-IP limiter and the
// global concurrency cap, since startup. Per-key counters are already
// exposed by RateLimitMetrics().
func AbuseMetrics() (perIPAllowedN, perIPRejectedN, globalAllowedN, globalRejectedN int64) {
	return perIPAllowed.Load(), perIPRejected.Load(), globalAllowed.Load(), globalRejected.Load()
}

// ---------------------------------------------------------------------------
// Per-IP rate limit (pre-auth)
// ---------------------------------------------------------------------------

// PerIPRateLimitConfig configures PerIPRateLimit.
type PerIPRateLimitConfig struct {
	Redis *redis.Client
	// RPS and Window define the sliding-window limit applied per client IP.
	RPS    int
	Window time.Duration
	// TrustProxyHeaders, when true, resolves the client IP from proxy headers
	// instead of r.RemoteAddr. See the trust-assumption comment on trustedClientIP
	// below before enabling this in an environment where untrusted clients can
	// reach the API directly.
	TrustProxyHeaders bool
	// TrustedProxies is an optional list of trusted proxy CIDRs (issue #779).
	// When walking X-Forwarded-For right-to-left, hops falling inside these
	// networks are skipped as intermediate proxies until the first untrusted (client) IP is reached.
	// Furthermore, if r.RemoteAddr does not match one of these CIDRs, incoming
	// proxy headers are ignored to prevent spoofing by direct callers.
	TrustedProxies []*net.IPNet
	// Platform specifies deployment platform (e.g. "fly").
	// When "fly", Fly-Client-IP header is preferred.
	Platform string
	// TrustedHeader optionally overrides the proxy header to read (defaults to "X-Forwarded-For",
	// or "Fly-Client-IP" on Fly).
	TrustedHeader string
	// SliderFn overrides the Redis sliding window. Set in tests. Defaults to
	// the same redisSlider helper TieredRateLimit uses.
	SliderFn func(ctx context.Context, key string, limit, windowMs int64) (allowed bool, count int64, err error)
}

var privateCIDRBlocks = []string{
	"127.0.0.0/8",    // IPv4 Loopback
	"10.0.0.0/8",     // RFC 1918
	"172.16.0.0/12",  // RFC 1918
	"192.168.0.0/16", // RFC 1918
	"169.254.0.0/16", // IPv4 Link-local
	"::1/128",        // IPv6 Loopback
	"fc00::/7",       // RFC 4193 Unique Local Address
	"fe80::/10",      // IPv6 Link-local
}

// ParseTrustedProxies parses a comma-separated list of CIDR strings or individual IPs
// into a slice of *net.IPNet. It supports the keyword "private" which expands
// to RFC 1918 and RFC 4193 private ranges plus loopback and link-local.
func ParseTrustedProxies(spec string) ([]*net.IPNet, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, nil
	}
	var networks []*net.IPNet
	entries := strings.Split(spec, ",")
	for _, raw := range entries {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if strings.EqualFold(entry, "private") {
			for _, cidr := range privateCIDRBlocks {
				_, netBlock, err := net.ParseCIDR(cidr)
				if err == nil {
					networks = append(networks, netBlock)
				}
			}
			continue
		}
		if !strings.Contains(entry, "/") {
			ip := net.ParseIP(entry)
			if ip == nil {
				return nil, fmt.Errorf("invalid IP or CIDR in trusted proxies: %q", entry)
			}
			if ip.To4() != nil {
				entry += "/32"
			} else {
				entry += "/128"
			}
		}
		_, netBlock, err := net.ParseCIDR(entry)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR in trusted proxies %q: %w", entry, err)
		}
		networks = append(networks, netBlock)
	}
	return networks, nil
}

func isIPInCIDRs(ip net.IP, cidrs []*net.IPNet) bool {
	if ip == nil {
		return false
	}
	for _, cidr := range cidrs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// trustedClientIP resolves the client IP to rate-limit on (issues #318, #779).
//
// Trust assumption:
// In direct/unproxied setups, r.RemoteAddr is used so client headers cannot spoof buckets.
// When behind proxies (Helm with cloud LB + Nginx, Fly.io, etc.), r.RemoteAddr points to
// the immediate proxy node (e.g. nginx pod or Fly proxy), collapsing all clients into
// a single bucket unless proxy headers are inspected.
//
// When cfg.TrustProxyHeaders is enabled:
//  1. If cfg.TrustedProxies is non-empty, r.RemoteAddr must match a trusted proxy CIDR;
//     if it does not (e.g. an attacker connecting directly bypassing proxies), headers
//     are untrusted and r.RemoteAddr is returned.
//  2. On Fly.io (Platform=="fly" or TrustedHeader=="Fly-Client-IP"), Fly-Client-IP is
//     consulted first as Fly's edge securely guarantees this header.
//  3. When walking X-Forwarded-For, entries are inspected right-to-left. Any hops matching
//     cfg.TrustedProxies are skipped as intermediate proxy hops (e.g. cloud load balancer,
//     ingress controller, nginx edge) until the first non-trusted IP is reached, which represents
//     the true client IP. Any earlier spoofed hops injected by the client are ignored.
func trustedClientIP(r *http.Request, cfg PerIPRateLimitConfig) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	host = strings.TrimSpace(host)

	if !cfg.TrustProxyHeaders {
		return host
	}

	remoteIP := net.ParseIP(host)
	if len(cfg.TrustedProxies) > 0 {
		if remoteIP == nil || !isIPInCIDRs(remoteIP, cfg.TrustedProxies) {
			return host
		}
	}

	isFly := strings.EqualFold(cfg.Platform, "fly") || strings.EqualFold(cfg.Platform, "fly.io")
	headerName := cfg.TrustedHeader
	if headerName == "" && isFly {
		headerName = "Fly-Client-IP"
	}

	if strings.EqualFold(headerName, "Fly-Client-IP") || isFly {
		if flyIP := strings.TrimSpace(r.Header.Get("Fly-Client-IP")); flyIP != "" {
			if h, _, err := net.SplitHostPort(flyIP); err == nil {
				flyIP = h
			}
			if parsed := net.ParseIP(flyIP); parsed != nil {
				return parsed.String()
			}
		}
		if strings.EqualFold(headerName, "Fly-Client-IP") && !isFly {
			return host
		}
	}

	if headerName == "" || strings.EqualFold(headerName, "X-Forwarded-For") {
		headerName = "X-Forwarded-For"
	}

	rawHeader := r.Header.Get(headerName)
	if rawHeader == "" {
		return host
	}

	parts := strings.Split(rawHeader, ",")
	if len(cfg.TrustedProxies) == 0 {
		last := strings.TrimSpace(parts[len(parts)-1])
		if last != "" {
			if h, _, err := net.SplitHostPort(last); err == nil {
				return h
			}
			return last
		}
		return host
	}

	var leftmostValid string
	for i := len(parts) - 1; i >= 0; i-- {
		hop := strings.TrimSpace(parts[i])
		if hop == "" {
			continue
		}
		hopHost := hop
		if h, _, err := net.SplitHostPort(hop); err == nil {
			hopHost = h
		}
		parsed := net.ParseIP(hopHost)
		if parsed == nil {
			continue
		}
		leftmostValid = parsed.String()
		if !isIPInCIDRs(parsed, cfg.TrustedProxies) {
			return parsed.String()
		}
	}

	if leftmostValid != "" {
		return leftmostValid
	}
	return host
}

// publicPathPrefixes lists the path prefixes PerIPRateLimit applies to. It
// intentionally does NOT cover /internal or /metrics, which are not exposed
// publicly (see docs/threat-model.md).
var publicPathPrefixes = []string{
	"/v1/",
	"/ws",
	"/graphql",
}

func isPublicPath(path string) bool {
	for _, p := range publicPathPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// PerIPRateLimit enforces a coarse, pre-auth sliding-window rate limit keyed
// on the resolved client IP (issue #318). It runs BEFORE middleware.NewDBAuth
// in the chain (see main.go) so a single source flooding the auth path itself
// — before any per-key limit from TieredRateLimit ever engages — is
// contained. Only applies to public paths (isPublicPath); operational
// endpoints like /metrics and /internal/status are unaffected.
//
// Fail-open on Redis errors, matching TieredRateLimit's policy: availability
// over strict enforcement during a Redis outage.
func PerIPRateLimit(cfg PerIPRateLimitConfig) func(http.Handler) http.Handler {
	slide := cfg.SliderFn
	if slide == nil {
		if cfg.Redis != nil {
			slide = redisSlider(cfg.Redis)
		} else {
			slide = func(_ context.Context, _ string, _, _ int64) (bool, int64, error) {
				return true, 0, nil
			}
		}
	}
	rps := cfg.RPS
	if rps <= 0 {
		rps = 1
	}
	window := cfg.Window
	if window <= 0 {
		window = time.Second
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isPublicPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			ip := trustedClientIP(r, cfg)
			redisKey := fmt.Sprintf("ratelimit:ip:%s", ip)
			windowMs := int64(window / time.Millisecond)

			allowed, _, err := slide(r.Context(), redisKey, int64(rps), windowMs)
			if err != nil {
				slog.Warn("per-IP rate limit check failed; failing open", "err", err)
				perIPAllowed.Add(1)
				next.ServeHTTP(w, r)
				return
			}

			if !allowed {
				perIPRejected.Add(1)
				metrics.RateLimitRejectionsTotal.WithLabelValues("per_ip").Inc()
				retryAfter := int64(window.Seconds())
				if retryAfter < 1 {
					retryAfter = 1
				}
				w.Header().Set("Retry-After", strconv.FormatInt(retryAfter, 10))
				httputil.WriteErrorCtx(r.Context(), w, http.StatusTooManyRequests, httputil.RATE_LIMITED, "too many requests from this IP")
				return
			}

			perIPAllowed.Add(1)
			next.ServeHTTP(w, r)
		})
	}
}

// NewPerIPRateLimitFromEnv constructs PerIPRateLimit from env vars, following
// the TieredRateLimit / NewTimeoutFromEnv pattern:
//
//   - PER_IP_RATE_LIMIT_RPS (default 20)
//   - PER_IP_RATE_LIMIT_WINDOW_MS (default 1000)
//   - TRUSTED_PROXY_ENABLED ("true" to trust proxy headers)
//   - TRUSTED_PROXY_PLATFORM ("fly" to trust Fly-Client-IP)
//   - TRUSTED_PROXY_HEADER (override header, e.g. "Fly-Client-IP")
//   - TRUSTED_PROXIES / TRUSTED_PROXY_CIDRS (comma-separated CIDRs/IPs, or "private")
func NewPerIPRateLimitFromEnv(redisClient *redis.Client) func(http.Handler) http.Handler {
	rps := envInt("PER_IP_RATE_LIMIT_RPS", 20)
	windowMs := envInt("PER_IP_RATE_LIMIT_WINDOW_MS", 1000)
	platform := strings.ToLower(strings.TrimSpace(os.Getenv("TRUSTED_PROXY_PLATFORM")))
	headerOverride := strings.TrimSpace(os.Getenv("TRUSTED_PROXY_HEADER"))

	trustedProxyStr := os.Getenv("TRUSTED_PROXY_ENABLED")
	trustProxy := trustedProxyStr == "true" || trustedProxyStr == "1"
	if platform != "" && trustedProxyStr == "" {
		trustProxy = true
	}

	proxiesSpec := os.Getenv("TRUSTED_PROXIES")
	if proxiesSpec == "" {
		proxiesSpec = os.Getenv("TRUSTED_PROXY_CIDRS")
	}
	if proxiesSpec != "" && trustedProxyStr == "" {
		trustProxy = true
	}

	trustedCIDRs, err := ParseTrustedProxies(proxiesSpec)
	if err != nil {
		slog.Warn("failed to parse TRUSTED_PROXIES; proxy headers will not filter by CIDR", "err", err, "spec", proxiesSpec)
	}

	return PerIPRateLimit(PerIPRateLimitConfig{
		Redis:             redisClient,
		RPS:               rps,
		Window:            time.Duration(windowMs) * time.Millisecond,
		TrustProxyHeaders: trustProxy,
		TrustedProxies:    trustedCIDRs,
		Platform:          platform,
		TrustedHeader:     headerOverride,
	})
}

// ---------------------------------------------------------------------------
// Global concurrency / burst cap
// ---------------------------------------------------------------------------

var inFlight atomic.Int64

// InFlightRequests returns the current number of requests being served by
// GlobalConcurrencyLimit's handler, for observability.
func InFlightRequests() int64 { return inFlight.Load() }

// GlobalConcurrencyLimit sheds load once more than maxInFlight requests are
// concurrently in-flight, returning 503 with Retry-After rather than letting
// the process degrade under an attack or traffic spike (issue #318). It is
// deliberately simple — a single atomic counter, no queueing — and is meant
// to run as early/outermost in the middleware chain as possible, before any
// other work (auth lookups, rate-limit Redis calls, etc.) happens for a
// request that is going to be shed anyway.
func GlobalConcurrencyLimit(maxInFlight int) func(http.Handler) http.Handler {
	if maxInFlight <= 0 {
		maxInFlight = 1
	}
	limit := int64(maxInFlight)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := inFlight.Add(1)
			defer inFlight.Add(-1)

			if n > limit {
				globalRejected.Add(1)
				metrics.RateLimitRejectionsTotal.WithLabelValues("global_concurrency").Inc()
				w.Header().Set("Retry-After", "1")
				httputil.WriteErrorCtx(r.Context(), w, http.StatusServiceUnavailable, httputil.UNAVAILABLE, "server is shedding load; try again shortly")
				return
			}

			globalAllowed.Add(1)
			next.ServeHTTP(w, r)
		})
	}
}

// NewGlobalConcurrencyLimitFromEnv reads MAX_IN_FLIGHT_REQUESTS (default
// 500) and returns configured GlobalConcurrencyLimit middleware.
func NewGlobalConcurrencyLimitFromEnv() func(http.Handler) http.Handler {
	return GlobalConcurrencyLimit(envInt("MAX_IN_FLIGHT_REQUESTS", 500))
}
