package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Depo-dev/trident/services/api/handlers"
	"github.com/Depo-dev/trident/services/api/middleware"
)

func TestStreamMissingContractID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/events/stream", nil)
	rec := httptest.NewRecorder()

	handlers.Stream(nil)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var body struct {
		Error struct {
			Field string `json:"field"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Field != "contractId" {
		t.Fatalf("error field: got %q, want contractId", body.Error.Field)
	}
}

func TestStreamRequiresAPIKey(t *testing.T) {
	t.Setenv("API_KEY_SALT", "test-salt")
	t.Setenv("API_KEY_HASHES", "not-the-request-key")

	handler := middleware.APIKey(handlers.Stream(nil))
	req := httptest.NewRequest(http.MethodGet, "/v1/events/stream?contractId=CTEST", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}
