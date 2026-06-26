package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Depo-dev/trident/services/api/handlers"
)

func TestHealth_NilDB_Returns503(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	rr := httptest.NewRecorder()

	handlers.Health(nil, nil)(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503, got %d", rr.Code)
	}

	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected application/json, got %s", ct)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if body["code"] != "INTERNAL" {
		t.Errorf("want code=INTERNAL, got %v", body["code"])
	}
}
