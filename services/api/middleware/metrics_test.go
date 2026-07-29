package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Depo-dev/trident/services/api/internal/metrics"
	"github.com/Depo-dev/trident/services/api/middleware"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMetrics_RecordsCountAndStatusForMatchedRoute(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/events/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	h := middleware.NewMetrics(mux)(mux)

	before := testutil.ToFloat64(metrics.HTTPRequestsTotal.WithLabelValues("GET", "GET /v1/events/{id}", "201"))

	req := httptest.NewRequest(http.MethodGet, "/v1/events/abc", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rr.Code)
	}

	after := testutil.ToFloat64(metrics.HTTPRequestsTotal.WithLabelValues("GET", "GET /v1/events/{id}", "201"))
	if after != before+1 {
		t.Errorf("expected trident_http_requests_total to increment by 1, before=%v after=%v", before, after)
	}
}

func TestMetrics_ExcludesLegacyMetricsRoute(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := middleware.NewMetrics(mux)(mux)

	before := testutil.ToFloat64(metrics.HTTPRequestsTotal.WithLabelValues("GET", "GET /metrics", "200"))

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}

	after := testutil.ToFloat64(metrics.HTTPRequestsTotal.WithLabelValues("GET", "GET /metrics", "200"))
	if after != before {
		t.Errorf("expected /metrics to be excluded from duration tracking, before=%v after=%v", before, after)
	}
}

func TestMetrics_CapturesRejectionFromOuterMiddleware(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	rejecter := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	h := middleware.NewMetrics(mux)(rejecter)

	before := testutil.ToFloat64(metrics.HTTPRequestsTotal.WithLabelValues("GET", "GET /v1/events", "429"))

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d", rr.Code)
	}

	after := testutil.ToFloat64(metrics.HTTPRequestsTotal.WithLabelValues("GET", "GET /v1/events", "429"))
	if after != before+1 {
		t.Errorf("expected the route pattern to still resolve for a request rejected by an outer middleware, before=%v after=%v", before, after)
	}
}
