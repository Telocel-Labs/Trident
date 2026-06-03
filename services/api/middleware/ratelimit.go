package middleware

import (
	"net/http"
	"os"
	"strconv"
	"sync"

	"golang.org/x/time/rate"
)

type rateLimitStore struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	rps      rate.Limit
	burst    int
}

func newRateLimitStore(rps rate.Limit, burst int) *rateLimitStore {
	return &rateLimitStore{
		limiters: make(map[string]*rate.Limiter),
		rps:      rps,
		burst:    burst,
	}
}

func (s *rateLimitStore) get(key string) *rate.Limiter {
	s.mu.Lock()
	defer s.mu.Unlock()

	if lim, ok := s.limiters[key]; ok {
		return lim
	}

	lim := rate.NewLimiter(s.rps, s.burst)
	s.limiters[key] = lim
	return lim
}

// RateLimit returns an HTTP middleware that enforces a per-API-key token bucket
// limit.  Exceeding the limit returns 429 with a Retry-After: 1 header.
//
// Default limits (100 req/s, burst 200) are overridden by RATE_LIMIT_RPS and
// RATE_LIMIT_BURST env vars.
func RateLimit(next http.Handler) http.Handler {
	rps := rate.Limit(100)
	burst := 200

	if v := os.Getenv("RATE_LIMIT_RPS"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			rps = rate.Limit(n)
		}
	}
	if v := os.Getenv("RATE_LIMIT_BURST"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			burst = n
		}
	}

	store := newRateLimitStore(rps, burst)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-API-Key")
		if key == "" {
			// No key: the auth middleware will handle this; pass through here.
			next.ServeHTTP(w, r)
			return
		}

		if !store.get(key).Allow() {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}
