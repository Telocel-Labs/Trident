package middleware

import (
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type rateLimiter struct {
	rps   int
	burst int
	mu    sync.Mutex
	m     map[string]*limiterEntry
}

type limiterEntry struct {
	lim    *rate.Limiter
	lastAt time.Time
}

// RateLimit returns middleware that enforces a token-bucket rate limit per API key.
// Rates are read from RATE_LIMIT_RPS (default 100) and RATE_LIMIT_BURST (default 200)
// at the time RateLimit is called. Keys idle for 5 minutes are evicted.
func RateLimit(next http.Handler) http.Handler {
	rl := &rateLimiter{
		rps:   envInt("RATE_LIMIT_RPS", 100),
		burst: envInt("RATE_LIMIT_BURST", 200),
		m:     map[string]*limiterEntry{},
	}
	go rl.cleanup()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-API-Key")
		if key == "" {
			// Auth middleware will reject keyless requests with 401; pass through.
			next.ServeHTTP(w, r)
			return
		}

		if !rl.get(key).Allow() {
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (rl *rateLimiter) get(key string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	e, ok := rl.m[key]
	if !ok {
		e = &limiterEntry{lim: rate.NewLimiter(rate.Limit(rl.rps), rl.burst)}
		rl.m[key] = e
	}
	e.lastAt = time.Now()
	return e.lim
}

func (rl *rateLimiter) cleanup() {
	for range time.Tick(5 * time.Minute) {
		rl.mu.Lock()
		for k, e := range rl.m {
			if time.Since(e.lastAt) > 5*time.Minute {
				delete(rl.m, k)
			}
		}
		rl.mu.Unlock()
	}
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}
