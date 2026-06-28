package middleware

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

type mockTierDB struct {
	tier string
	err  error
}

func (m *mockTierDB) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return &mockTierRow{tier: m.tier, err: m.err}
}

type mockTierRow struct {
	tier string
	err  error
}

func (r *mockTierRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*dest[0].(*string) = r.tier
	return nil
}

func noop() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func apiKeyReq(key string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	if key != "" {
		r.Header.Set("X-API-Key", key)
	}
	return r
}

func resetCounters() {
	rlAllowed.Store(0)
	rlRejected.Store(0)
}

// alwaysAllow is a SliderFn that always allows with count=1.
func alwaysAllow(_ context.Context, _ string, _ int64, _ int64) (bool, int64, error) {
	return true, 1, nil
}

// alwaysReject is a SliderFn that always rejects at the limit.
func alwaysReject(_ context.Context, _ string, limit int64, _ int64) (bool, int64, error) {
	return false, limit, nil
}

func testTiers() map[string]TierConfig {
	return map[string]TierConfig{
		"free":     {RPS: 5, Window: time.Second},
		"pro":      {RPS: 50, Window: time.Second},
		"internal": {RPS: 500, Window: time.Second},
	}
}

// ---------------------------------------------------------------------------
// TieredRateLimit tests
// ---------------------------------------------------------------------------

func TestTieredRateLimit_NoAPIKey_Passthrough(t *testing.T) {
	resetCounters()
	mw := TieredRateLimit(RateLimitConfig{Tiers: testTiers()})(noop())
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, apiKeyReq(""))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if rec.Header().Get("X-RateLimit-Limit") != "" {
		t.Error("rate limit headers must not be set when no API key present")
	}
}

func TestTieredRateLimit_NilRedis_FailOpen(t *testing.T) {
	resetCounters()
	cfg := RateLimitConfig{Redis: nil, DB: nil, Tiers: testTiers()}
	mw := TieredRateLimit(cfg)(noop())
	for i := 0; i < 200; i++ {
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, apiKeyReq("any-key"))
		if rec.Code != http.StatusOK {
			t.Fatalf("nil Redis must fail open: want 200, got %d (i=%d)", rec.Code, i)
		}
	}
}

func TestTieredRateLimit_Allows_SetsHeaders(t *testing.T) {
	resetCounters()
	cfg := RateLimitConfig{SliderFn: alwaysAllow, Tiers: testTiers()}
	mw := TieredRateLimit(cfg)(noop())
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, apiKeyReq("my-key"))

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("X-RateLimit-Limit"); got != "5" {
		t.Errorf("X-RateLimit-Limit: want 5, got %q", got)
	}
	if rec.Header().Get("X-RateLimit-Remaining") == "" {
		t.Error("X-RateLimit-Remaining must be set")
	}
	if rec.Header().Get("X-RateLimit-Reset") == "" {
		t.Error("X-RateLimit-Reset must be set")
	}
	if rec.Header().Get("Retry-After") != "" {
		t.Error("Retry-After must not be set on 200")
	}
}

func TestTieredRateLimit_Rejects_Returns429WithHeaders(t *testing.T) {
	resetCounters()
	cfg := RateLimitConfig{SliderFn: alwaysReject, Tiers: testTiers()}
	mw := TieredRateLimit(cfg)(noop())
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, apiKeyReq("key"))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("Retry-After must be set on 429")
	}
	if got := rec.Header().Get("X-RateLimit-Limit"); got != "5" {
		t.Errorf("X-RateLimit-Limit: want 5, got %q", got)
	}
	if got := rec.Header().Get("X-RateLimit-Remaining"); got != "0" {
		t.Errorf("X-RateLimit-Remaining: want 0, got %q", got)
	}
	if rec.Header().Get("X-RateLimit-Reset") == "" {
		t.Error("X-RateLimit-Reset must be set on 429")
	}
}

func TestTieredRateLimit_FailOpen_OnSliderError(t *testing.T) {
	resetCounters()
	errSlider := func(_ context.Context, _ string, _, _ int64) (bool, int64, error) {
		return false, 0, fmt.Errorf("redis: connection refused")
	}
	cfg := RateLimitConfig{SliderFn: errSlider, Tiers: testTiers()}
	mw := TieredRateLimit(cfg)(noop())
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, apiKeyReq("key"))
	if rec.Code != http.StatusOK {
		t.Fatalf("slider error must fail open: want 200, got %d", rec.Code)
	}
}

func TestTieredRateLimit_TierFromDB_UsesCorrectLimit(t *testing.T) {
	resetCounters()
	db := &mockTierDB{tier: "internal"}
	var capturedLimit int64
	captureSlider := func(_ context.Context, _ string, limit, _ int64) (bool, int64, error) {
		capturedLimit = limit
		return true, 1, nil
	}
	cfg := RateLimitConfig{
		DB:       db,
		SliderFn: captureSlider,
		Tiers: map[string]TierConfig{
			"free":     {RPS: 5, Window: time.Second},
			"internal": {RPS: 500, Window: time.Second},
		},
	}
	mw := TieredRateLimit(cfg)(noop())
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, apiKeyReq("internal-key"))
	if capturedLimit != 500 {
		t.Errorf("internal tier: want limit=500, got %d", capturedLimit)
	}
}

func TestTieredRateLimit_DBError_DefaultsToFree(t *testing.T) {
	resetCounters()
	db := &mockTierDB{err: fmt.Errorf("db down")}
	var capturedLimit int64
	captureSlider := func(_ context.Context, _ string, limit, _ int64) (bool, int64, error) {
		capturedLimit = limit
		return true, 1, nil
	}
	cfg := RateLimitConfig{
		DB:       db,
		SliderFn: captureSlider,
		Tiers: map[string]TierConfig{
			"free": {RPS: 10, Window: time.Second},
			"pro":  {RPS: 100, Window: time.Second},
		},
	}
	mw := TieredRateLimit(cfg)(noop())
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, apiKeyReq("unknown-key"))
	if capturedLimit != 10 {
		t.Errorf("DB error should default to free (10), got %d", capturedLimit)
	}
}

func TestTieredRateLimit_Counters(t *testing.T) {
	resetCounters()
	call := 0
	var mu sync.Mutex
	countingSlider := func(_ context.Context, _ string, limit, _ int64) (bool, int64, error) {
		mu.Lock()
		defer mu.Unlock()
		call++
		if call <= 3 {
			return true, int64(call), nil
		}
		return false, limit, nil
	}
	cfg := RateLimitConfig{SliderFn: countingSlider, Tiers: testTiers()}
	mw := TieredRateLimit(cfg)(noop())

	for i := 0; i < 5; i++ {
		mw.ServeHTTP(httptest.NewRecorder(), apiKeyReq("ctr-key"))
	}

	allowed, rejected := RateLimitMetrics()
	if allowed != 3 {
		t.Errorf("allowed counter: want 3, got %d", allowed)
	}
	if rejected != 2 {
		t.Errorf("rejected counter: want 2, got %d", rejected)
	}
}

// ---------------------------------------------------------------------------
// WSConnectionLimit tests
// ---------------------------------------------------------------------------

func TestWSConnectionLimit_UnderLimit_Passes(t *testing.T) {
	wsConns.Store(0)
	defer wsConns.Store(0)
	mw := WSConnectionLimit(noop())
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ws", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("under limit: want 200, got %d", rec.Code)
	}
}

func TestWSConnectionLimit_OverLimit_Returns429(t *testing.T) {
	wsConns.Store(1000) // already AT the limit
	defer wsConns.Store(0)
	mw := WSConnectionLimit(noop())
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ws", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over limit: want 429, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// RateLimitMetrics helper
// ---------------------------------------------------------------------------

func TestRateLimitMetrics_InitiallyZero(t *testing.T) {
	resetCounters()
	allowed, rejected := RateLimitMetrics()
	if allowed != 0 || rejected != 0 {
		t.Errorf("want (0,0), got (%d,%d)", allowed, rejected)
	}
}