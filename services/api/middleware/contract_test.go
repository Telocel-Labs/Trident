package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Depo-dev/trident/services/api/internal/contracttest"
)

// validEventListBody is a minimal EventListResponse-shaped body (issue
// #242), used by contract tests that exercise rate-limit middleware in
// isolation — the middleware under test doesn't care what the wrapped
// handler returns, but the contract test validates the full response
// against api/openapi.yaml's GET /v1/events schema, so the body must be
// shaped correctly too.
const validEventListBody = `{"events":[],"has_more":false,"next_cursor":null}`

// withDevServer rewrites req's URL to match api/openapi.yaml's declared
// "http://localhost:3000" dev server — the gorillamux contract-test router
// matches routes against declared servers, but httptest.NewRequest builds a
// relative-only URL that doesn't match any of them.
func withDevServer(req *http.Request) *http.Request {
	req.URL.Scheme = "http"
	req.URL.Host = "localhost:3000"
	req.Host = "localhost:3000"
	return req
}

func eventListStub() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(validEventListBody))
	})
}

// TestContract_TieredRateLimit_Success200 verifies a rate-limit-allowed
// response through TieredRateLimit conforms to GET /v1/events's documented
// 200 response — the X-RateLimit-* headers declared in api/openapi.yaml are
// marked required, so a middleware regression that stops setting one of
// them fails this test (issue #242).
func TestContract_TieredRateLimit_Success200(t *testing.T) {
	resetCounters()
	doc := contracttest.LoadSpec(t)
	router := contracttest.NewRouter(t, doc)

	cfg := RateLimitConfig{SliderFn: alwaysAllow, Tiers: testTiers()}
	mw := TieredRateLimit(cfg)(eventListStub())

	req := withDevServer(apiKeyReq("contract-test-key"))
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	contracttest.ValidateResponse(t, router, req, rr.Code, rr.Header(), rr.Body.Bytes())
}

// TestContract_TieredRateLimit_429 verifies a rejected request through
// TieredRateLimit conforms to GET /v1/events's documented 429
// (RateLimitExceeded) response — X-RateLimit-* and Retry-After headers,
// plus the ErrorResponse body shape (issue #242).
func TestContract_TieredRateLimit_429(t *testing.T) {
	resetCounters()
	doc := contracttest.LoadSpec(t)
	router := contracttest.NewRouter(t, doc)

	cfg := RateLimitConfig{SliderFn: alwaysReject, Tiers: testTiers()}
	mw := TieredRateLimit(cfg)(eventListStub())

	req := withDevServer(apiKeyReq("contract-test-key"))
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d", rr.Code)
	}
	contracttest.ValidateResponse(t, router, req, rr.Code, rr.Header(), rr.Body.Bytes())
}

// TestContract_GlobalConcurrencyLimit_503 verifies a load-shed request
// conforms to GET /v1/events's documented 503 (ServiceUnavailable)
// response, including the Retry-After header that's only present in the
// load-shedding case (issue #242). Chains GlobalConcurrencyLimit outside
// TieredRateLimit, mirroring main.go's real middleware order, so the
// successful path also carries the X-RateLimit-* headers the 200 response
// requires.
func TestContract_GlobalConcurrencyLimit_503(t *testing.T) {
	resetCounters()
	doc := contracttest.LoadSpec(t)
	router := contracttest.NewRouter(t, doc)

	rlCfg := RateLimitConfig{SliderFn: alwaysAllow, Tiers: testTiers()}
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		eventListStub().ServeHTTP(w, r)
	})
	mw := GlobalConcurrencyLimit(1)(TieredRateLimit(rlCfg)(slow))

	var wg sync.WaitGroup
	results := make([]*httptest.ResponseRecorder, 2)
	reqs := make([]*http.Request, 2)
	for i := 0; i < 2; i++ {
		reqs[i] = withDevServer(apiKeyReq("contract-test-key"))
		results[i] = httptest.NewRecorder()
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mw.ServeHTTP(results[i], reqs[i])
		}(i)
	}

	<-started
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	var okIdx, shedIdx = -1, -1
	for i, rr := range results {
		switch rr.Code {
		case http.StatusOK:
			okIdx = i
		case http.StatusServiceUnavailable:
			shedIdx = i
		}
	}
	if okIdx == -1 || shedIdx == -1 {
		t.Fatalf("want one 200 and one 503, got %d and %d", results[0].Code, results[1].Code)
	}

	contracttest.ValidateResponse(t, router, reqs[okIdx], results[okIdx].Code, results[okIdx].Header(), results[okIdx].Body.Bytes())
	contracttest.ValidateResponse(t, router, reqs[shedIdx], results[shedIdx].Code, results[shedIdx].Header(), results[shedIdx].Body.Bytes())

	if got := results[shedIdx].Header().Get("Retry-After"); got == "" {
		t.Error("shed response missing Retry-After header")
	}
}
