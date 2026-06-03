package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"strings"
)

// APIKey returns an HTTP middleware that validates the X-API-Key header on all
// /v1/* and /ws routes.  GET /v1/health is exempt.
//
// The incoming key is HMAC-SHA256'd with API_KEY_SALT and compared against the
// comma-separated list of pre-hashed keys in API_KEY_HASHES.  Returns 401 if
// the header is missing or the key is unrecognised.
func APIKey(next http.Handler) http.Handler {
	salt := []byte(os.Getenv("API_KEY_SALT"))
	rawHashes := os.Getenv("API_KEY_HASHES")

	validHashes := make(map[string]struct{})
	for _, h := range strings.Split(rawHashes, ",") {
		h = strings.TrimSpace(h)
		if h != "" {
			validHashes[h] = struct{}{}
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Health check is always public.
		if r.Method == http.MethodGet && r.URL.Path == "/v1/health" {
			next.ServeHTTP(w, r)
			return
		}

		// Only guard /v1/* and /ws paths.
		if !strings.HasPrefix(r.URL.Path, "/v1/") && r.URL.Path != "/ws" {
			next.ServeHTTP(w, r)
			return
		}

		key := r.Header.Get("X-API-Key")
		if key == "" {
			http.Error(w, "missing X-API-Key header", http.StatusUnauthorized)
			return
		}

		hashed := hmacSHA256Hex(salt, key)
		if _, ok := validHashes[hashed]; !ok {
			http.Error(w, "invalid API key", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func hmacSHA256Hex(salt []byte, key string) string {
	mac := hmac.New(sha256.New, salt)
	mac.Write([]byte(key))
	return hex.EncodeToString(mac.Sum(nil))
}
