package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"strings"
)

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

func hashKey(key string) string {
	salt := []byte(os.Getenv("API_KEY_SALT"))
	mac := hmac.New(sha256.New, salt)
	mac.Write([]byte(key))
	return hex.EncodeToString(mac.Sum(nil))
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

		if _, ok := validHashes[hashKey(key)]; !ok {
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

// Validator returns a closure that validates a raw API key against validHashes.
// When validHashes is empty all keys pass (auth disabled — suitable for local dev).
func Validator(validHashes map[string]struct{}) func(string) bool {
	return func(key string) bool {
		if len(validHashes) == 0 {
			return true
		}
		_, ok := validHashes[hashKey(key)]
		return ok
	}
}
