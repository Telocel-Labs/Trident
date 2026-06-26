package middleware

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
)

const (
	corsAllowMethods = "GET, POST, PUT, PATCH, DELETE, OPTIONS"
	corsAllowHeaders = "Authorization, Content-Type, X-Request-ID"
	corsMaxAge       = "86400"
)

func CORS(allowedOrigins []string) func(http.Handler) http.Handler {
	wildcard := len(allowedOrigins) == 0

	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[o] = struct{}{}
	}

	isAllowed := func(origin string) bool {
		if wildcard {
			return true
		}
		_, ok := allowed[origin]
		return ok
	}

	setCORSHeaders := func(w http.ResponseWriter, origin string) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", corsAllowMethods)
		w.Header().Set("Access-Control-Allow-Headers", corsAllowHeaders)
		w.Header().Set("Access-Control-Max-Age", corsMaxAge)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			if !isAllowed(origin) {
				writeJSON(w, http.StatusForbidden, map[string]any{
					"error": map[string]any{
						"code":    "CORS_FORBIDDEN",
						"message": "origin not allowed",
					},
				})
				return
			}

			setCORSHeaders(w, origin)

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func NewCORSFromEnv() func(http.Handler) http.Handler {
	raw := os.Getenv("ALLOWED_ORIGINS")
	if raw == "" || raw == "*" {
		return CORS(nil)
	}

	parts := strings.Split(raw, ",")
	origins := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			origins = append(origins, t)
		}
	}
	return CORS(origins)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
