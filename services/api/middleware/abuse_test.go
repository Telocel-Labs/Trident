package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Depo-dev/trident/services/api/internal/metrics"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// fakeSlider is a deterministic in-memory stand-in for the Redis sliding
// window, keyed exactly like redisSlider, so tests don't need a live Redis.
func fakeSlider(t *testing.T) func(ctx context.Context, key string, limit, windowMs int64) (bool, int64, error) {
	t.Helper()
	var mu sync.Mutex
	counts := map[string]int64{}
	return func(_ context.Context, key string, limit, _ int64) (bool, int64, error) {
		mu.Lock()
		defer mu.Unlock()
		counts[key]++
		if counts[key] > limit {
			return false, counts[key], nil
		}
		return true, counts[key], nil
	}
}

func TestPerIPRateLimit_ExceedingIPBlocked_OtherIPUnaffected(t *testing.T) {
	handler := PerIPRateLimit(PerIPRateLimitConfig{
		RPS:      2,
		Window:   time.Second,
		SliderFn: fakeSlider(t),
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	do := func(ip string) int {
		req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
		req.RemoteAddr = ip + ":12345"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	// IP A: first two requests allowed, third rejected.
	if code := do("1.2.3.4"); code != http.StatusOK {
		t.Fatalf("request 1 for IP A: expected 200, got %d", code)
	}
	if code := do("1.2.3.4"); code != http.StatusOK {
		t.Fatalf("request 2 for IP A: expected 200, got %d", code)
	}
	if code := do("1.2.3.4"); code != http.StatusTooManyRequests {
		t.Fatalf("request 3 for IP A: expected 429, got %d", code)
	}

	// IP B is a distinct bucket and is unaffected by IP A's limit.
	if code := do("5.6.7.8"); code != http.StatusOK {
		t.Fatalf("request 1 for IP B: expected 200, got %d", code)
	}
}

// TestPerIPRateLimit_RejectionRecordsPrometheusMetric verifies a 429 from the
// per-IP limiter increments trident_ratelimit_rejections_total{limiter="per_ip"}
// (issue #58).
func TestPerIPRateLimit_RejectionRecordsPrometheusMetric(t *testing.T) {
	handler := PerIPRateLimit(PerIPRateLimitConfig{
		RPS:      1,
		Window:   time.Second,
		SliderFn: fakeSlider(t),
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	before := testutil.ToFloat64(metrics.RateLimitRejectionsTotal.WithLabelValues("per_ip"))

	do := func() int {
		req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
		req.RemoteAddr = "8.8.8.8:1"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	do()         // allowed
	code := do() // rejected

	if code != http.StatusTooManyRequests {
		t.Fatalf("expected second request to be rejected, got %d", code)
	}
	if got := testutil.ToFloat64(metrics.RateLimitRejectionsTotal.WithLabelValues("per_ip")); got != before+1 {
		t.Errorf("per_ip rejections total: want %v, got %v", before+1, got)
	}
}

// TestGlobalConcurrencyLimit_RejectionRecordsPrometheusMetric verifies a shed
// request increments trident_ratelimit_rejections_total{limiter="global_concurrency"}.
func TestGlobalConcurrencyLimit_RejectionRecordsPrometheusMetric(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)

	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	})
	handler := GlobalConcurrencyLimit(1)(slow)

	before := testutil.ToFloat64(metrics.RateLimitRejectionsTotal.WithLabelValues("global_concurrency"))

	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			codes[i] = rec.Code
		}(i)
	}

	<-started
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := testutil.ToFloat64(metrics.RateLimitRejectionsTotal.WithLabelValues("global_concurrency")); got != before+1 {
		t.Errorf("global_concurrency rejections total: want %v, got %v", before+1, got)
	}
}

func TestPerIPRateLimit_NonPublicPath_Skipped(t *testing.T) {
	handler := PerIPRateLimit(PerIPRateLimitConfig{
		RPS:      0, // would reject request 1 if applied
		Window:   time.Second,
		SliderFn: fakeSlider(t),
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "1.2.3.4:1"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected /metrics to bypass the per-IP limiter, got %d", rec.Code)
	}
}

func TestTrustedClientIP_UntrustedByDefault(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.RemoteAddr = "9.9.9.9:5555"
	req.Header.Set("X-Forwarded-For", "1.1.1.1, 2.2.2.2")

	if ip := trustedClientIP(req, PerIPRateLimitConfig{TrustProxyHeaders: false}); ip != "9.9.9.9" {
		t.Fatalf("expected RemoteAddr host 9.9.9.9 when proxy headers are untrusted, got %q", ip)
	}
}

func TestTrustedClientIP_TrustsLastHopOnly(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.RemoteAddr = "9.9.9.9:5555"
	req.Header.Set("X-Forwarded-For", "1.1.1.1, 2.2.2.2")

	// Only the last entry (the one the trusted proxy itself appended) is used.
	if ip := trustedClientIP(req, PerIPRateLimitConfig{TrustProxyHeaders: true}); ip != "2.2.2.2" {
		t.Fatalf("expected last XFF hop 2.2.2.2, got %q", ip)
	}
}

func TestParseTrustedProxies(t *testing.T) {
	cidrs, err := ParseTrustedProxies("10.0.0.0/8, 192.168.1.1, private")
	if err != nil {
		t.Fatalf("unexpected error parsing trusted proxies: %v", err)
	}
	if len(cidrs) < 3 {
		t.Fatalf("expected at least 3 CIDRs, got %d", len(cidrs))
	}

	_, err = ParseTrustedProxies("not-a-valid-ip-or-cidr")
	if err == nil {
		t.Fatalf("expected error parsing invalid CIDR string")
	}
}

func TestTrustedClientIP_DirectUntrustedRemoteAddr_IgnoresHeaders(t *testing.T) {
	trusted, err := ParseTrustedProxies("10.0.0.0/8,172.16.0.0/12")
	if err != nil {
		t.Fatal(err)
	}
	cfg := PerIPRateLimitConfig{
		TrustProxyHeaders: true,
		TrustedProxies:    trusted,
	}

	// Caller connects directly from 198.51.100.5 (not in trusted proxies)
	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.RemoteAddr = "198.51.100.5:44321"
	req.Header.Set("X-Forwarded-For", "8.8.8.8, 1.1.1.1")
	req.Header.Set("Fly-Client-IP", "8.8.8.8")

	if ip := trustedClientIP(req, cfg); ip != "198.51.100.5" {
		t.Fatalf("expected direct caller IP 198.51.100.5 because RemoteAddr is untrusted, got %q", ip)
	}
}

func TestTrustedClientIP_RightToLeftWalk_SkipsTrustedHops(t *testing.T) {
	trusted, err := ParseTrustedProxies("10.0.0.0/8,172.16.0.0/12,192.168.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	cfg := PerIPRateLimitConfig{
		TrustProxyHeaders: true,
		TrustedProxies:    trusted,
	}

	// Request path: Client (203.0.113.19) -> Cloud LB (10.0.1.1) -> Nginx (10.0.2.2) -> Go API
	// RemoteAddr is Nginx (10.0.2.2:50000)
	// XFF header contains client IP, then LB IP appended by Nginx
	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.RemoteAddr = "10.0.2.2:50000"
	req.Header.Set("X-Forwarded-For", "203.0.113.19, 10.0.1.1")

	if ip := trustedClientIP(req, cfg); ip != "203.0.113.19" {
		t.Fatalf("expected client IP 203.0.113.19, got %q", ip)
	}
}

func TestTrustedClientIP_SpoofedXFFIgnored(t *testing.T) {
	trusted, err := ParseTrustedProxies("10.0.0.0/8,172.16.0.0/12")
	if err != nil {
		t.Fatal(err)
	}
	cfg := PerIPRateLimitConfig{
		TrustProxyHeaders: true,
		TrustedProxies:    trusted,
	}

	// Client (203.0.113.19) sends spoofed X-Forwarded-For: 1.1.1.1, 2.2.2.2
	// Cloud LB appends client IP: 1.1.1.1, 2.2.2.2, 203.0.113.19
	// Nginx appends LB IP: 1.1.1.1, 2.2.2.2, 203.0.113.19, 10.0.1.1
	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.RemoteAddr = "10.0.2.2:50000"
	req.Header.Set("X-Forwarded-For", "1.1.1.1, 2.2.2.2, 203.0.113.19, 10.0.1.1")

	if ip := trustedClientIP(req, cfg); ip != "203.0.113.19" {
		t.Fatalf("expected true client IP 203.0.113.19 ignoring spoofed hops, got %q", ip)
	}
}

func TestTrustedClientIP_FlyPlatform(t *testing.T) {
	cfg := PerIPRateLimitConfig{
		TrustProxyHeaders: true,
		Platform:          "fly",
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.RemoteAddr = "fdaa:0:1::2:12345"
	req.Header.Set("Fly-Client-IP", "198.51.100.99")
	req.Header.Set("X-Forwarded-For", "1.1.1.1") // Spoofed XFF should be ignored in favor of Fly-Client-IP

	if ip := trustedClientIP(req, cfg); ip != "198.51.100.99" {
		t.Fatalf("expected Fly-Client-IP 198.51.100.99, got %q", ip)
	}
}

func TestPerIPRateLimit_IndependentBudgetsBehindProxy(t *testing.T) {
	trusted, err := ParseTrustedProxies("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	handler := PerIPRateLimit(PerIPRateLimitConfig{
		RPS:               2,
		Window:            time.Second,
		TrustProxyHeaders: true,
		TrustedProxies:    trusted,
		SliderFn:          fakeSlider(t),
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	do := func(clientIP string, spoofedXFF string) int {
		req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
		// All requests arrive from the same nginx proxy pod IP
		req.RemoteAddr = "10.244.0.15:43210"
		if spoofedXFF != "" {
			req.Header.Set("X-Forwarded-For", spoofedXFF+", "+clientIP)
		} else {
			req.Header.Set("X-Forwarded-For", clientIP)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	// Client A: 2 requests allowed, 3rd blocked
	if code := do("203.0.113.10", ""); code != http.StatusOK {
		t.Fatalf("request 1 for client A: expected 200, got %d", code)
	}
	if code := do("203.0.113.10", ""); code != http.StatusOK {
		t.Fatalf("request 2 for client A: expected 200, got %d", code)
	}
	if code := do("203.0.113.10", ""); code != http.StatusTooManyRequests {
		t.Fatalf("request 3 for client A: expected 429, got %d", code)
	}

	// Client A tries to evade by injecting spoofed XFF prefix; still rejected because true IP is resolved
	if code := do("203.0.113.10", "1.1.1.1, 2.2.2.2"); code != http.StatusTooManyRequests {
		t.Fatalf("spoofed evasion request for client A: expected 429, got %d", code)
	}

	// Client B is a different client behind the same nginx pod; has independent budget
	if code := do("203.0.113.20", ""); code != http.StatusOK {
		t.Fatalf("request 1 for client B: expected 200, got %d", code)
	}
}

func TestGlobalConcurrencyLimit_ShedsLoadUnderConcurrency(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 10)

	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	})

	handler := GlobalConcurrencyLimit(2)(slow)

	var wg sync.WaitGroup
	codes := make([]int, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			codes[i] = rec.Code
		}(i)
	}

	// Wait for at most 2 to actually start executing (the ones that got past
	// the cap), then release them.
	for i := 0; i < 2; i++ {
		<-started
	}
	// Give the other two goroutines a moment to hit the cap and be rejected
	// before we release the in-flight ones.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	var okCount, shedCount int
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			okCount++
		case http.StatusServiceUnavailable:
			shedCount++
		default:
			t.Fatalf("unexpected status code %d", c)
		}
	}
	if okCount != 2 {
		t.Fatalf("expected exactly 2 requests to pass through the cap of 2, got %d", okCount)
	}
	if shedCount != 2 {
		t.Fatalf("expected exactly 2 requests to be shed, got %d", shedCount)
	}
}
