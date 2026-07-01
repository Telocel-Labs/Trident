package middleware

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

// DBAuthConfig configures the database-backed authentication middleware.
type DBAuthConfig struct {
	// DB is used for API key lookups. When nil, only env-var auth is attempted.
	DB interface {
		QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	}
	// Redis is used for caching successful lookups (5 min TTL). Optional.
	Redis *redis.Client
}

const authCacheTTL = 5 * time.Minute

// ParseKeyHashes parses a comma-separated list of HMAC-SHA256 hex digests
// (as stored in API_KEY_HASHES) into a set for O(1) lookup.
func ParseKeyHashes(raw string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, h := range strings.Split(raw, ",") {
		h = strings.TrimSpace(h)
		if h != "" {
			out[h] = struct{}{}
		}
	}
	return out
}

// hmacKeyHash computes HMAC-SHA256 of key using API_KEY_SALT — used for the
// legacy API_KEY_HASHES env-var authentication path.
func hmacKeyHash(key string) string {
	salt := []byte(os.Getenv("API_KEY_SALT"))
	mac := hmac.New(sha256.New, salt)
	mac.Write([]byte(key))
	return hex.EncodeToString(mac.Sum(nil))
}

// hashKey is kept for backward compatibility with the TieredRateLimit
// middleware which references it by name.
func hashKey(key string) string {
	return hmacKeyHash(key)
}

// sha256KeyHash computes a plain SHA-256 hash of key — matches the hash stored
// in the api_keys table by handlers.sha256hex.
func sha256KeyHash(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

func authRedisCacheKey(hash string) string {
	return fmt.Sprintf("apiauth:%s", hash)
}

// NewDBAuth returns an authentication middleware that:
//  1. Looks up the hashed API key in Redis cache (5 min TTL).
//  2. Falls back to the api_keys database table (active keys only).
//  3. Falls back to legacy HMAC-SHA256 env-var authentication (API_KEY_HASHES).
//
// On success, api_key_id and network are attached to the request context.
// Unauthenticated requests receive 401 unless the path is excluded.
func NewDBAuth(cfg DBAuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Public paths — skip auth entirely.
			path := r.URL.Path
			if path == "/v1/health" || path == "/metrics" {
				next.ServeHTTP(w, r)
				return
			}
			if !strings.HasPrefix(path, "/v1/") && path != "/ws" {
				next.ServeHTTP(w, r)
				return
			}

			key := r.Header.Get("X-API-Key")
			if key == "" {
				http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
				return
			}

			// ── 1. Redis cache ──────────────────────────────────────────────
			dbHash := sha256KeyHash(key)
			if cfg.Redis != nil {
				if cached, err := cfg.Redis.Get(r.Context(), authRedisCacheKey(dbHash)).Result(); err == nil {
					// Cached value format: "<uuid>:<network>"
					parts := strings.SplitN(cached, ":", 2)
					ctx := context.WithValue(r.Context(), contextKeyAPIKeyID, parts[0])
					if len(parts) == 2 {
						ctx = context.WithValue(ctx, contextKeyNetwork, parts[1])
					}
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}

			// ── 2. Database lookup ──────────────────────────────────────────
			if cfg.DB != nil {
				var id, network string
				err := cfg.DB.QueryRow(r.Context(),
					`SELECT id, network FROM api_keys WHERE key_hash = $1 AND revoked_at IS NULL`,
					dbHash,
				).Scan(&id, &network)
				if err == nil {
					// Populate Redis cache so the next request is O(1).
					if cfg.Redis != nil {
						cfg.Redis.Set(r.Context(), authRedisCacheKey(dbHash),
							id+":"+network, authCacheTTL)
					}
					ctx := context.WithValue(r.Context(), contextKeyAPIKeyID, id)
					ctx = context.WithValue(ctx, contextKeyNetwork, network)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}

			// ── 3. Legacy env-var fallback (API_KEY_HASHES) ────────────────
			validHashes := ParseKeyHashes(os.Getenv("API_KEY_HASHES"))
			if len(validHashes) > 0 {
				if _, ok := validHashes[hmacKeyHash(key)]; ok {
					next.ServeHTTP(w, r)
					return
				}
			}

			http.Error(w, `{"error":"Unauthorized"}`, http.StatusUnauthorized)
		})
	}
}

// Validator returns a func(string) bool that checks whether the HMAC-SHA256
// of the provided key is in the given valid hashes set. Used by the GraphQL
// WebSocket handler which needs a standalone key-check function.
func Validator(hashes map[string]struct{}) func(string) bool {
	return func(key string) bool {
		_, ok := hashes[hmacKeyHash(key)]
		return ok
	}
}

// Auth validates X-API-Key for protected API and WebSocket routes.
// GET /v1/health remains public. validHashes is the pre-parsed set of
// accepted HMAC-SHA256 hex digests. When validHashes is empty all requests
// pass through (auth is disabled — suitable for local development).
func Auth(validHashes map[string]struct{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/health" {
			next.ServeHTTP(w, r)
			return
		}

		if len(validHashes) == 0 {
			next.ServeHTTP(w, r)
			return
		}

		if !strings.HasPrefix(r.URL.Path, "/v1/") && r.URL.Path != "/ws" {
			next.ServeHTTP(w, r)
			return
		}

		key := r.Header.Get("X-API-Key")
		if key == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		if _, ok := validHashes[hmacKeyHash(key)]; !ok {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// APIKey is a convenience wrapper around Auth that reads API_KEY_HASHES from
// the environment on each call.
func APIKey(next http.Handler) http.Handler {
	return Auth(ParseKeyHashes(os.Getenv("API_KEY_HASHES")), next)
}
