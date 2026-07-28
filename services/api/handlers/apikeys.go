package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Depo-dev/trident/services/api/internal/httputil"
	"github.com/Depo-dev/trident/services/api/middleware"
	"github.com/Depo-dev/trident/services/api/validation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// defaultRotationGracePeriod is used when a rotate request does not specify
// grace_period_seconds, and when APIKeyConfig.RotationGracePeriod is zero
// (issue #314). It gives callers a full day to swap over to the new key
// before the old one is lazily auto-revoked.
const defaultRotationGracePeriod = 24 * time.Hour

// APIKeyConfig wires the api-key handlers.
type APIKeyConfig struct {
	AdminKey string
	DB       *pgxpool.Pool
	// Redis is used for cache invalidation on key revocation.
	Redis *redis.Client
	// InvalidateTier evicts a key's cached rate-limit tier (by key hash) after
	// an admin tier change so the new limit applies promptly instead of after
	// the tier-cache TTL. Wired to middleware.TierCache.Invalidate; nil-safe.
	InvalidateTier func(keyHash string)
	// RotationGracePeriod is the default grace window a rotated-out key stays
	// valid for when a rotate request does not specify one. Defaults to
	// defaultRotationGracePeriod when zero.
	RotationGracePeriod time.Duration
}

// APIKeyResponse is returned for list/create/update/rotate operations.
// The Key field is only populated on creation and rotation and is never
// returned again afterwards.
type APIKeyResponse struct {
	ID            string  `json:"id"`
	KeyPrefix     string  `json:"key_prefix"`
	Key           *string `json:"key,omitempty"`
	Label         string  `json:"label"`
	Network       string  `json:"network"`
	RateLimitTier string  `json:"rate_limit_tier"`
	Scope         string  `json:"scope"`
	CreatedBy     *string `json:"created_by,omitempty"`
	LastUsedAt    *string `json:"last_used_at"`
	RequestCount  int64   `json:"request_count"`
	RevokedAt     *string `json:"revoked_at,omitempty"`
	ExpiresAt     *string `json:"expires_at,omitempty"`
	GraceUntil    *string `json:"grace_until,omitempty"`
	RotatedFrom   *string `json:"rotated_from,omitempty"`
	RotatedTo     *string `json:"rotated_to,omitempty"`
	CreatedAt     string  `json:"created_at"`
}

// RotateAPIKeyResponse is returned by POST /v1/api-keys/{id}/rotate.
type RotateAPIKeyResponse struct {
	RotatedFrom string         `json:"rotated_from"`
	GraceUntil  string         `json:"grace_until"`
	NewKey      APIKeyResponse `json:"new_key"`
}

type createKeyRequest struct {
	Label         string  `json:"label"`
	Network       string  `json:"network"`
	RateLimitTier string  `json:"rate_limit_tier"`
	CreatedBy     string  `json:"created_by"`
	Scope         string  `json:"scope"`
	ExpiresAt     *string `json:"expires_at"`
}

type updateKeyRequest struct {
	Label         *string `json:"label"`
	RateLimitTier *string `json:"rate_limit_tier"`
	Scope         *string `json:"scope"`
	// ExpiresAt updates the key's expiry when non-nil. A pointer to an empty
	// string clears the expiry (makes the key non-expiring); a pointer to an
	// RFC3339 timestamp sets it; nil leaves it unchanged.
	ExpiresAt *string `json:"expires_at"`
}

type rotateKeyRequest struct {
	// GracePeriodSeconds overrides how long the old key keeps authenticating
	// after rotation. Defaults to cfg.RotationGracePeriod (or
	// defaultRotationGracePeriod when that is zero) when omitted or <= 0.
	GracePeriodSeconds *int64 `json:"grace_period_seconds"`
}

// requireAdmin checks admin key and DB availability, writing an appropriate
// error response and returning false when the handler should abort.
func requireAdmin(cfg APIKeyConfig, w http.ResponseWriter, r *http.Request) bool {
	if cfg.AdminKey == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusForbidden, httputil.UNAUTHORIZED, "admin API key is not configured")
		return false
	}
	if !validAdminKey(cfg.AdminKey, r.Header.Get("X-Admin-Key")) {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, httputil.UNAUTHORIZED, "invalid or missing admin key")
		return false
	}
	if cfg.DB == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusServiceUnavailable, httputil.UNAVAILABLE, "database unavailable")
		return false
	}
	return true
}

// validScope reports whether s is a recognized key scope.
func validScope(s string) bool {
	return s == middleware.ScopeRead || s == middleware.ScopeAdmin
}

// parseExpiresAt validates and parses an optional RFC3339 expiry timestamp.
// An empty string is treated as "no expiry" (returns nil, nil).
func parseExpiresAt(raw string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// evictAuthCache removes a key's cached auth entry (by key hash) so a
// revocation, expiry change, or scope change takes effect on the next
// request instead of waiting for the auth cache's TTL (issue #314, following
// the same immediate-invalidation pattern as InvalidateTier from #229).
func evictAuthCache(ctx context.Context, redisClient *redis.Client, keyHash string) {
	if redisClient == nil {
		return
	}
	redisClient.Del(ctx, fmt.Sprintf("apiauth:%s", keyHash))
}

// CreateAPIKey handles POST /v1/api-keys (admin-only).
//
// Generates a key: "trident_" + 32 random hex bytes. Only the SHA-256 hash is
// stored. The plaintext key is returned exactly once in the response.
func CreateAPIKey(cfg APIKeyConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(cfg, w, r) {
			return
		}

		var req createKeyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, httputil.INVALID_ARGUMENT, "invalid JSON body")
			return
		}
		if req.Network == "" {
			req.Network = "mainnet"
		}
		if req.RateLimitTier == "" {
			req.RateLimitTier = "standard"
		}
		// New keys default to the least-privileged scope (issue #314) unless
		// the caller (who must already hold the admin key to reach this
		// endpoint) explicitly requests admin scope for the new key.
		if req.Scope == "" {
			req.Scope = middleware.ScopeRead
		}
		if !validScope(req.Scope) {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, httputil.INVALID_ARGUMENT, "scope must be 'read' or 'admin'")
			return
		}

		var expiresAt *time.Time
		if req.ExpiresAt != nil {
			t, err := parseExpiresAt(*req.ExpiresAt)
			if err != nil {
				httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, httputil.INVALID_ARGUMENT, "expires_at must be an RFC3339 timestamp")
				return
			}
			if t != nil && !t.After(time.Now()) {
				httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, httputil.INVALID_ARGUMENT, "expires_at must be in the future")
				return
			}
			expiresAt = t
		}

		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, httputil.INTERNAL, "failed to generate key")
			return
		}
		plaintext := "trident_" + hex.EncodeToString(raw)
		hash := sha256hex(plaintext)
		prefix := plaintext[:16]

		var createdBy *string
		if req.CreatedBy != "" {
			createdBy = &req.CreatedBy
		}

		var id string
		var createdAt time.Time
		err := cfg.DB.QueryRow(r.Context(),
			`INSERT INTO api_keys (key_hash, key_prefix, label, network, rate_limit_tier, created_by, scope, expires_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			 RETURNING id, created_at`,
			hash, prefix, req.Label, req.Network, req.RateLimitTier, createdBy, req.Scope, expiresAt,
		).Scan(&id, &createdAt)
		if err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, httputil.INTERNAL, "failed to create api key")
			return
		}

		ts := createdAt.UTC().Format(time.RFC3339)
		resp := APIKeyResponse{
			ID:            id,
			KeyPrefix:     prefix,
			Key:           &plaintext,
			Label:         req.Label,
			Network:       req.Network,
			RateLimitTier: req.RateLimitTier,
			Scope:         req.Scope,
			CreatedBy:     createdBy,
			CreatedAt:     ts,
		}
		if expiresAt != nil {
			s := expiresAt.UTC().Format(time.RFC3339)
			resp.ExpiresAt = &s
		}
		writeJSON(w, http.StatusCreated, resp)
	}
}

// ListAPIKeys handles GET /v1/api-keys (admin-only).
//
// Returns all keys with key_prefix, last_used_at, and request_count.
// The full plaintext key and hash are never returned.
func ListAPIKeys(cfg APIKeyConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(cfg, w, r) {
			return
		}

		rows, err := cfg.DB.Query(r.Context(),
			`SELECT id, key_prefix, label, network, rate_limit_tier, created_by,
			        last_used_at, request_count, revoked_at, created_at,
			        scope, expires_at, grace_until, rotated_from, rotated_to
			 FROM api_keys
			 ORDER BY created_at DESC`,
		)
		if err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, httputil.INTERNAL, "failed to list api keys")
			return
		}
		defer rows.Close()

		keys := []APIKeyResponse{}
		for rows.Next() {
			var k APIKeyResponse
			var lastUsedAt, revokedAt, expiresAt, graceUntil *time.Time
			var createdAt time.Time
			var rotatedFrom, rotatedTo *string
			if err := rows.Scan(&k.ID, &k.KeyPrefix, &k.Label, &k.Network,
				&k.RateLimitTier, &k.CreatedBy, &lastUsedAt, &k.RequestCount,
				&revokedAt, &createdAt, &k.Scope, &expiresAt, &graceUntil,
				&rotatedFrom, &rotatedTo); err != nil {
				httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, httputil.INTERNAL, "scan error")
				return
			}
			k.CreatedAt = createdAt.UTC().Format(time.RFC3339)
			if lastUsedAt != nil {
				s := lastUsedAt.UTC().Format(time.RFC3339)
				k.LastUsedAt = &s
			}
			if revokedAt != nil {
				s := revokedAt.UTC().Format(time.RFC3339)
				k.RevokedAt = &s
			}
			if expiresAt != nil {
				s := expiresAt.UTC().Format(time.RFC3339)
				k.ExpiresAt = &s
			}
			if graceUntil != nil {
				s := graceUntil.UTC().Format(time.RFC3339)
				k.GraceUntil = &s
			}
			k.RotatedFrom = rotatedFrom
			k.RotatedTo = rotatedTo
			keys = append(keys, k)
		}
		if rows.Err() != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, httputil.INTERNAL, "query error")
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"api_keys": keys})
	}
}

// UpdateAPIKey handles PATCH /v1/api-keys/{id} (admin-only).
//
// Allows updating the label, rate_limit_tier, scope, or expiry of an active
// key.
func UpdateAPIKey(cfg APIKeyConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(cfg, w, r) {
			return
		}

		// Path ids go through the shared UUID validator so a malformed id is a
		// clear INVALID_ARGUMENT instead of a database-shaped failure (#222).
		id := r.PathValue("id")
		if verr := validation.ValidateUUID("id", id); verr != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, httputil.INVALID_ARGUMENT, verr.Message)
			return
		}

		var req updateKeyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, httputil.INVALID_ARGUMENT, "invalid JSON body")
			return
		}
		if req.Label == nil && req.RateLimitTier == nil && req.Scope == nil && req.ExpiresAt == nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, httputil.INVALID_ARGUMENT, "at least one of label, rate_limit_tier, scope, or expires_at is required")
			return
		}
		if req.Scope != nil && !validScope(*req.Scope) {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, httputil.INVALID_ARGUMENT, "scope must be 'read' or 'admin'")
			return
		}

		// expiresAtSet distinguishes "clear the expiry" (empty string) from
		// "leave it unchanged" (field omitted) since both must pass through
		// COALESCE-free SQL to support explicitly nulling the column.
		var expiresAtSet bool
		var expiresAt *time.Time
		if req.ExpiresAt != nil {
			expiresAtSet = true
			t, err := parseExpiresAt(*req.ExpiresAt)
			if err != nil {
				httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, httputil.INVALID_ARGUMENT, "expires_at must be an RFC3339 timestamp or an empty string to clear it")
				return
			}
			if t != nil && !t.After(time.Now()) {
				httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, httputil.INVALID_ARGUMENT, "expires_at must be in the future")
				return
			}
			expiresAt = t
		}

		var k APIKeyResponse
		var lastUsedAt, respExpiresAt, respGraceUntil *time.Time
		var createdAt time.Time
		var keyHash string
		var rotatedFrom, rotatedTo *string
		err := cfg.DB.QueryRow(r.Context(),
			`UPDATE api_keys
			 SET label           = COALESCE($2, label),
			     rate_limit_tier = COALESCE($3, rate_limit_tier),
			     scope           = COALESCE($4, scope),
			     expires_at      = CASE WHEN $5 THEN $6 ELSE expires_at END
			 WHERE id = $1 AND revoked_at IS NULL
			 RETURNING id, key_prefix, label, network, rate_limit_tier,
			           last_used_at, request_count, created_at, key_hash,
			           scope, expires_at, grace_until, rotated_from, rotated_to`,
			id, req.Label, req.RateLimitTier, req.Scope, expiresAtSet, expiresAt,
		).Scan(&k.ID, &k.KeyPrefix, &k.Label, &k.Network, &k.RateLimitTier,
			&lastUsedAt, &k.RequestCount, &createdAt, &keyHash,
			&k.Scope, &respExpiresAt, &respGraceUntil, &rotatedFrom, &rotatedTo)
		if err == pgx.ErrNoRows {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, httputil.NOT_FOUND, "api key not found")
			return
		}
		if err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, httputil.INTERNAL, "failed to update api key")
			return
		}

		// A tier change must evict the cached tier so the new limit applies on
		// the next request rather than after the tier-cache TTL (issue #229).
		if req.RateLimitTier != nil && cfg.InvalidateTier != nil {
			cfg.InvalidateTier(keyHash)
		}
		// A scope or expiry change must evict the auth cache immediately too
		// (issue #314), following the same pattern: otherwise a previously
		// cached scope/expiry could remain honored for up to authCacheTTL
		// after the admin change.
		if req.Scope != nil || expiresAtSet {
			evictAuthCache(r.Context(), cfg.Redis, keyHash)
		}

		k.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		if lastUsedAt != nil {
			s := lastUsedAt.UTC().Format(time.RFC3339)
			k.LastUsedAt = &s
		}
		if respExpiresAt != nil {
			s := respExpiresAt.UTC().Format(time.RFC3339)
			k.ExpiresAt = &s
		}
		if respGraceUntil != nil {
			s := respGraceUntil.UTC().Format(time.RFC3339)
			k.GraceUntil = &s
		}
		k.RotatedFrom = rotatedFrom
		k.RotatedTo = rotatedTo
		writeJSON(w, http.StatusOK, k)
	}
}

// DeleteAPIKey handles DELETE /v1/api-keys/{id} (admin-only).
//
// Soft-deletes the key by setting revoked_at. The key is immediately removed
// from the Redis auth cache so revocation takes effect on the next request
// without waiting for TTL expiry.
func DeleteAPIKey(cfg APIKeyConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(cfg, w, r) {
			return
		}

		// Path ids go through the shared UUID validator so a malformed id is a
		// clear INVALID_ARGUMENT instead of a database-shaped failure (#222).
		id := r.PathValue("id")
		if verr := validation.ValidateUUID("id", id); verr != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, httputil.INVALID_ARGUMENT, verr.Message)
			return
		}

		var keyHash string
		err := cfg.DB.QueryRow(r.Context(),
			`UPDATE api_keys
			 SET revoked_at = NOW()
			 WHERE id = $1 AND revoked_at IS NULL
			 RETURNING key_hash`,
			id,
		).Scan(&keyHash)
		if err == pgx.ErrNoRows {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, httputil.NOT_FOUND, "api key not found")
			return
		}
		if err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, httputil.INTERNAL, "failed to revoke api key")
			return
		}

		// Immediately evict the revoked key from Redis so auth rejects it
		// on the next request rather than waiting for the 5-minute TTL.
		evictAuthCache(r.Context(), cfg.Redis, keyHash)

		w.WriteHeader(http.StatusNoContent)
	}
}

// RotateAPIKey handles POST /v1/api-keys/{id}/rotate (admin-only).
//
// Creates a brand-new key (new random secret, new row) that inherits the old
// key's label, network, rate_limit_tier, and scope, linked to the old row via
// rotated_from/rotated_to. The old row is kept — not deleted — so audit
// history and any existing audit_log foreign keys stay intact, and it
// continues to authenticate for a grace window (default
// defaultRotationGracePeriod, overridable per-request or via
// cfg.RotationGracePeriod) so callers have time to switch over.
//
// After the grace window elapses, the old key is auto-revoked lazily inside
// the auth path itself (see middleware.NewDBAuth's lazy_revoke CTE) rather
// than by a background job, so retirement is deterministic. This handler
// does not need to evict the old key's Redis cache entry at rotation time:
// NewDBAuth caps any cached entry's TTL at min(authCacheTTL,
// time_until(grace_until)), so nothing cached can outlive the DB-enforced
// grace boundary either way.
func RotateAPIKey(cfg APIKeyConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(cfg, w, r) {
			return
		}

		id := r.PathValue("id")
		if verr := validation.ValidateUUID("id", id); verr != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, httputil.INVALID_ARGUMENT, verr.Message)
			return
		}

		var req rotateKeyRequest
		// The rotate request body is optional (grace period may be omitted
		// entirely), so only reject genuinely malformed JSON — an empty body
		// is fine.
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, httputil.INVALID_ARGUMENT, "invalid JSON body")
			return
		}

		grace := cfg.RotationGracePeriod
		if grace <= 0 {
			grace = defaultRotationGracePeriod
		}
		if req.GracePeriodSeconds != nil && *req.GracePeriodSeconds > 0 {
			grace = time.Duration(*req.GracePeriodSeconds) * time.Second
		}

		tx, err := cfg.DB.Begin(r.Context())
		if err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, httputil.INTERNAL, "failed to start rotation")
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()

		var label, network, rateLimitTier, scope string
		var createdBy *string
		err = tx.QueryRow(r.Context(),
			`SELECT label, network, rate_limit_tier, scope, created_by
			 FROM api_keys
			 WHERE id = $1 AND revoked_at IS NULL AND rotated_to IS NULL
			 FOR UPDATE`,
			id,
		).Scan(&label, &network, &rateLimitTier, &scope, &createdBy)
		if err == pgx.ErrNoRows {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, httputil.NOT_FOUND, "api key not found, already revoked, or already rotated")
			return
		}
		if err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, httputil.INTERNAL, "failed to read api key")
			return
		}

		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, httputil.INTERNAL, "failed to generate key")
			return
		}
		plaintext := "trident_" + hex.EncodeToString(raw)
		newHash := sha256hex(plaintext)
		newPrefix := plaintext[:16]

		var newID string
		var newCreatedAt time.Time
		err = tx.QueryRow(r.Context(),
			`INSERT INTO api_keys (key_hash, key_prefix, label, network, rate_limit_tier, created_by, scope, rotated_from)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			 RETURNING id, created_at`,
			newHash, newPrefix, label, network, rateLimitTier, createdBy, scope, id,
		).Scan(&newID, &newCreatedAt)
		if err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, httputil.INTERNAL, "failed to create rotated key")
			return
		}

		graceUntil := time.Now().Add(grace)
		if _, err := tx.Exec(r.Context(),
			`UPDATE api_keys SET grace_until = $2, rotated_to = $3 WHERE id = $1`,
			id, graceUntil, newID,
		); err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, httputil.INTERNAL, "failed to link rotated key")
			return
		}

		if err := tx.Commit(r.Context()); err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, httputil.INTERNAL, "failed to commit rotation")
			return
		}

		graceUntilStr := graceUntil.UTC().Format(time.RFC3339)
		newKeyResp := APIKeyResponse{
			ID:            newID,
			KeyPrefix:     newPrefix,
			Key:           &plaintext,
			Label:         label,
			Network:       network,
			RateLimitTier: rateLimitTier,
			Scope:         scope,
			CreatedBy:     createdBy,
			CreatedAt:     newCreatedAt.UTC().Format(time.RFC3339),
			RotatedFrom:   &id,
		}
		writeJSON(w, http.StatusCreated, RotateAPIKeyResponse{
			RotatedFrom: id,
			GraceUntil:  graceUntilStr,
			NewKey:      newKeyResp,
		})
	}
}

func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h)
}

// NewAPIKeyUsageTracker returns a channel-based background aggregator for
// issue #139. The caller should send a key UUID on the channel after every
// successful auth. The aggregator batches pending updates and flushes them to
// postgres every flushInterval (typically 5s). Call stop() on shutdown to
// drain the channel before exit.
func NewAPIKeyUsageTracker(db *pgxpool.Pool, flushInterval time.Duration) (track chan<- string, stop func()) {
	ch := make(chan string, 4096)

	go func() {
		ticker := time.NewTicker(flushInterval)
		defer ticker.Stop()

		pending := map[string]int64{}

		flush := func() {
			if len(pending) == 0 {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			for id, count := range pending {
				if _, err := db.Exec(ctx,
					`UPDATE api_keys
					 SET request_count = request_count + $1,
					     last_used_at  = NOW()
					 WHERE id = $2`,
					count, id,
				); err != nil {
					// Log but don't crash — usage tracking is non-critical.
					_ = err
				}
			}
			pending = map[string]int64{}
		}

		for {
			select {
			case id, ok := <-ch:
				if !ok {
					flush()
					return
				}
				pending[id]++
			case <-ticker.C:
				flush()
			}
		}
	}()

	return ch, func() { close(ch) }
}
