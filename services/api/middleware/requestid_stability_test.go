package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Depo-dev/trident/services/api/internal/httputil"
	"github.com/Depo-dev/trident/services/api/middleware"
)

// TestRequestID_StableAcrossFullMiddlewareChain asserts that the request ID
// is stable and accessible across the full standard middleware chain.
func TestRequestID_StableAcrossFullMiddlewareChain(t *testing.T) {
	const incoming = "stable-chain-id-777"

	var capturedIDs []string

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedIDs = append(capturedIDs, httputil.RequestIDFromContext(r.Context()))
		w.WriteHeader(http.StatusOK)
	})

	// Simulate full production middleware chain
	// Order: RequestID -> StructuredLogging -> Audit -> Handler
	aw := middleware.NewAuditWriter(nil, middleware.NewTestLoggerForTest(), 10)
	defer aw.Close()

	chain := middleware.Chain(
		inner,
		middleware.RequestID,
		middleware.StructuredLogging,
		middleware.AuditMiddleware(aw),
	)

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.Header.Set(reqIDHeader, incoming)
	rec := httptest.NewRecorder()

	chain.ServeHTTP(rec, req);

	if got := rec.Header().Get(reqIDHeader); got != incoming {
		Fatalf(t, "response header request id = %q, want %q", got, incoming)
	}
	if len(capturedIDs) != 1 || capturedIDs[0] != incoming {
		Fatalf(t, "handler context request id = %v, want [%q]", capturedIDs, incoming)
	}
}

func Fatalf(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Fatalf(format, args...)
}
