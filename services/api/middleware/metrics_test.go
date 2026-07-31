package middleware_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Depo-dev/trident/services/api/middleware"
)

func TestPrometheusHTTP_RecordsRequestCountAndLatency(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/widgets/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := middleware.PrometheusHTTP(mux)

	req := httptest.NewRequest(http.MethodGet, "/v1/widgets/abc-123", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}

	var buf bytes.Buffer
	middleware.WriteHTTPMetrics(&buf)
	body := buf.String()

	// The route *pattern* — not the raw path with its id value — must appear,
	// so per-request identifiers never blow up metric cardinality.
	if !strings.Contains(body, `route="GET /v1/widgets/{id}"`) {
		t.Errorf("expected metrics keyed by route pattern, got:\n%s", body)
	}
	if strings.Contains(body, "abc-123") {
		t.Errorf("raw path value must not appear in metrics output, got:\n%s", body)
	}
	if !strings.Contains(body, `trident_api_http_requests_total{method="GET",route="GET /v1/widgets/{id}",status="200"} 1`) {
		t.Errorf("expected a request_total sample of 1, got:\n%s", body)
	}
	if !strings.Contains(body, "trident_api_http_request_duration_seconds_bucket") {
		t.Errorf("expected latency histogram buckets, got:\n%s", body)
	}
}

func TestPrometheusHTTP_RecordsErrorStatus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/boom", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	handler := middleware.PrometheusHTTP(mux)
	req := httptest.NewRequest(http.MethodGet, "/v1/boom", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var buf bytes.Buffer
	middleware.WriteHTTPMetrics(&buf)
	body := buf.String()

	if !strings.Contains(body, `status="500"`) {
		t.Errorf("expected a status=500 sample, got:\n%s", body)
	}
}
