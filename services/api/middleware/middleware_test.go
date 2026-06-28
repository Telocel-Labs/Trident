package middleware_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Depo-dev/trident/services/api/middleware"
)

const testSalt = "test-salt"

// computeHash mirrors the middleware's internal hashKey so tests stay in sync.
func computeHash(salt, key string) string {
	mac := hmac.New(sha256.New, []byte(salt))
	mac.Write([]byte(key))
	return hex.EncodeToString(mac.Sum(nil))
}

func validHashes(salt string, keys ...string) map[string]struct{} {
	raw := ""
	for i, k := range keys {
		if i > 0 {
			raw += ","
		}
		raw += computeHash(salt, k)
	}
	return middleware.ParseKeyHashes(raw)
}

var noop = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

// ---------------------------------------------------------------------------
// Auth middleware tests
// ---------------------------------------------------------------------------

func TestAuth_HealthSkipped(t *testing.T) {
	os.Setenv("API_KEY_SALT", testSalt)
	h := middleware.Auth(validHashes(testSalt, "k1"), noop)

	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("health should skip auth: want 200, got %d", rr.Code)
	}
}

func TestAuth_ValidKey(t *testing.T) {
	os.Setenv("API_KEY_SALT", testSalt)
	h := middleware.Auth(validHashes(testSalt, "good-key"), noop)

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.Header.Set("X-API-Key", "good-key")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("valid key: want 200, got %d", rr.Code)
	}
}

func TestAuth_MissingKey(t *testing.T) {
	os.Setenv("API_KEY_SALT", testSalt)
	h := middleware.Auth(validHashes(testSalt, "k1"), noop)

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("missing key: want 401, got %d", rr.Code)
	}
}

func TestAuth_InvalidKey(t *testing.T) {
	os.Setenv("API_KEY_SALT", testSalt)
	h := middleware.Auth(validHashes(testSalt, "good-key"), noop)

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.Header.Set("X-API-Key", "bad-key")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("invalid key: want 401, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Rate limit middleware tests
// ---------------------------------------------------------------------------

func TestRateLimit_ExceedBurst_Returns429(t *testing.T) {
	calls := 0
	sliderFn := func(_ context.Context, _ string, limit, _ int64) (bool, int64, error) {
		calls++
		if calls > 2 {
			return false, 0, nil
		}
		return true, limit - int64(calls), nil
	}
	cfg := middleware.RateLimitConfig{
		SliderFn: sliderFn,
		Tiers:    map[string]middleware.TierConfig{"free": {RPS: 2, Window: time.Second}},
	}
	h := middleware.TieredRateLimit(cfg)(noop)

	const key = "burst-test-key"
	got429 := false
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
		req.Header.Set("X-API-Key", key)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code == http.StatusTooManyRequests {
			got429 = true
			if rr.Header().Get("Retry-After") == "" {
				t.Error("429 response missing Retry-After header")
			}
			break
		}
	}
	if !got429 {
		t.Error("expected a 429 after exceeding burst, got none")
	}
}
