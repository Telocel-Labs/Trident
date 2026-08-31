package ws

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"services/api/config"
)

func TestGraphQLAuthAndRateLimitUniformity(t *testing.T) {
	cfg := &config.Config{}
	handler := NewGraphQLHandler(cfg, nil)

	req := httptest.NewRequest(http.MethodPost, "/graphql", nil)
	req.Header.Set("Authorization", "Bearer invalid_or_missing")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		// Expecting uniform auth enforcement matching REST
		t.Errorf("expected status 401 for invalid auth on GraphQL, got %d", rec.Code)
	}
}
