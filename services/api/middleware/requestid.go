package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/Depo-dev/trident/services/api/internal/httputil"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const RequestIDHeader = "X-Request-ID"

// RequestID middleware ensures every request has a unique request ID, attached to
// the context, logged, added to the OTel span, and echoed in the response header.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get(RequestIDHeader)
		if reqID == "" || strings.ContainsAny(reqID, " \t\n\r") || len(reqID) > 128 {
			reqID = uuid.New().String()
		}

		ctx := httputil.ContextWithRequestID(r.Context(), reqID)

		// Attach as OTel span attribute
		span := trace.SpanFromContext(ctx)
		if span.IsRecording() {
			span.SetAttributes(attribute.String("trident.request_id", reqID))
		}

		w.Header().Set(RequestIDHeader, reqID)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
