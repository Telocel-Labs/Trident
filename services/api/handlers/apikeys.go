package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// APIKeyConfig wires the api-key handlers.
type APIKeyConfig struct {
	AdminKey string
	DB       *pgxpool.Pool
	// Redis is used for cache invalidation on key revocation.
	Redis *redis.Client
}

// APIKeyResponse is returned for list/create operations.
// The Key field is only populated on creation and is never returned again.
type APIKeyResponse struct {
	ID            string  `json:"id"`
	KeyPrefix     string  `json:"key_prefix"`
	Key           *string `json:"key,omitempty"`
	Label         string  `json:"label"`
	Network       string  `json:"network"`
	RateLimitTier string  `json:"rate_limit_tier"`
	CreatedBy     *string `json:"created_by,omitempty"`
	LastUsedAt    *string `json:"last_used_at"`
	RequestCount  int64   `json:"request_count"`
	RevokedAt     *string `json:"revoked_at,omitempty"`
	CreatedAt     string  `json:"created_at"`
}

type createKeyRequest struct {
	Label         string `json:"label"`
	Network       string `json:"network"`
	RateLimitTier string `json:"rate_limit_tier"`
	CreatedBy     string `json:"created_by"`
}

type updateKeyRequest struct {
	Label         *string `json:"label"`
	RateLimitTier *string `json:"rate_limit_tier"`
}

// requireAdmin checks admin key and DB availability, writing an appropriate
// error response and returning false when the handler should abort.
func requireAdmin(cfg APIKeyConfig, w http.ResponseWriter, r *http.Request) bool {
	if cfg.AdminKey == "" {
		writeJSON(w, http.StatusForbidden, errorBody("admin API key is not configured"))
		return false
	}
	if !validAdminKey(cfg.AdminKey, r.Header.Get("X-Admin-Key")) {
		writeJSON(w, http.StatusUnauthorized, errorBody("invalid or missing admin key"))
		return false
	}
	if cfg.DB == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("database unavailable"))
		return false
	}
	return true
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
			writeJSON(w, http.StatusBadRequest, errorBody("invalid JSON body"))
			return
		}
		if req.Network == "" {
			req.Network = "mainnet"
		}
		if req.RateLimitTier == "" {
			req.RateLimitTier = "standard"
		}

		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody("failed to generate key"))
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
			`INSERT INTO api_keys (key_hash, key_prefix, label, network, rate_limit_tier, created_by)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 RETURNING id, created_at`,
			hash, prefix, req.Label, req.Network, req.RateLimitTier, createdBy,
		).Scan(&id, &createdAt)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody("failed to create api key"))
			return
		}

		ts := createdAt.UTC().Format(time.RFC3339)
		writeJSON(w, http.StatusCreated, APIKeyResponse{
			ID:            id,
			KeyPrefix:     prefix,
			Key:           &plaintext,
			Label:         req.Label,
			Network:       req.Network,
			RateLimitTier: req.RateLimitTier,
			CreatedBy:     createdBy,
			CreatedAt:     ts,
		})
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
			        last_used_at, request_count, revoked_at, created_at
			 FROM api_keys
			 ORDER BY created_at DESC`,
		)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody("failed to list api keys"))
			return
		}
		defer rows.Close()

		keys := []APIKeyResponse{}
		for rows.Next() {
			var k APIKeyResponse
			var lastUsedAt, revokedAt *time.Time
			var createdAt time.Time
			if err := rows.Scan(&k.ID, &k.KeyPrefix, &k.Label, &k.Network,
				&k.RateLimitTier, &k.CreatedBy, &lastUsedAt, &k.RequestCount,
				&revokedAt, &createdAt); err != nil {
				writeJSON(w, http.StatusInternalServerError, errorBody("scan error"))
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
			keys = append(keys, k)
		}
		if rows.Err() != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody("query error"))
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"api_keys": keys})
	}
}

// UpdateAPIKey handles PATCH /v1/api-keys/{id} (admin-only).
//
// Allows updating the label or rate_limit_tier of an active key.
func UpdateAPIKey(cfg APIKeyConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(cfg, w, r) {
			return
		}

		id := r.PathValue("id")
		if id == "" {
			writeJSON(w, http.StatusBadRequest, errorBody("missing api key id"))
			return
		}

		var req updateKeyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid JSON body"))
			return
		}
		if req.Label == nil && req.RateLimitTier == nil {
			writeJSON(w, http.StatusBadRequest, errorBody("at least one of label or rate_limit_tier is required"))
			return
		}

		var k APIKeyResponse
		var lastUsedAt *time.Time
		var createdAt time.Time
		err := cfg.DB.QueryRow(r.Context(),
			`UPDATE api_keys
			 SET label           = COALESCE($2, label),
			     rate_limit_tier = COALESCE($3, rate_limit_tier)
			 WHERE id = $1 AND revoked_at IS NULL
			 RETURNING id, key_prefix, label, network, rate_limit_tier,
			           last_used_at, request_count, created_at`,
			id, req.Label, req.RateLimitTier,
		).Scan(&k.ID, &k.KeyPrefix, &k.Label, &k.Network, &k.RateLimitTier,
			&lastUsedAt, &k.RequestCount, &createdAt)
		if err == pgx.ErrNoRows {
			writeJSON(w, http.StatusNotFound, errorBody("api key not found"))
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody("failed to update api key"))
			return
		}

		k.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		if lastUsedAt != nil {
			s := lastUsedAt.UTC().Format(time.RFC3339)
			k.LastUsedAt = &s
		}
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

		id := r.PathValue("id")
		if id == "" {
			writeJSON(w, http.StatusBadRequest, errorBody("missing api key id"))
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
			writeJSON(w, http.StatusNotFound, errorBody("api key not found"))
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody("failed to revoke api key"))
			return
		}

		// Immediately evict the revoked key from Redis so auth rejects it
		// on the next request rather than waiting for the 5-minute TTL.
		if cfg.Redis != nil {
			cfg.Redis.Del(r.Context(), fmt.Sprintf("apiauth:%s", keyHash))
		}

		w.WriteHeader(http.StatusNoContent)
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
