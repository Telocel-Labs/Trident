package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"strings"
)

// APIKey validates X-API-Key for protected API and WebSocket routes.
// GET /v1/health remains public for infrastructure health checks.
func APIKey(next http.Handler) http.Handler {
	salt := []byte(os.Getenv("API_KEY_SALT"))
	validHashes := make(map[string]struct{})
	for _, hash := range strings.Split(os.Getenv("API_KEY_HASHES"), ",") {
		hash = strings.TrimSpace(hash)
		if hash != "" {
			validHashes[hash] = struct{}{}
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/health" {
			next.ServeHTTP(w, r)
			return
		}

		if !strings.HasPrefix(r.URL.Path, "/v1/") && r.URL.Path != "/ws" {
			next.ServeHTTP(w, r)
			return
		}

		key := r.Header.Get("X-API-Key")
		if key == "" {
			http.Error(w, "missing X-API-Key header", http.StatusUnauthorized)
			return
		}

		if _, ok := validHashes[hmacSHA256Hex(salt, key)]; !ok {
			http.Error(w, "invalid API key", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func hmacSHA256Hex(salt []byte, key string) string {
	mac := hmac.New(sha256.New, salt)
	_, _ = mac.Write([]byte(key))
	return hex.EncodeToString(mac.Sum(nil))
}
