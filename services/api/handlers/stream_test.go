package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != "INVALID_ARGUMENT" {
		t.Fatalf("error code: got %q, want INVALID_ARGUMENT", body.Error.Code)
	}
	if !strings.Contains(body.Error.Message, "contractId") {
		t.Fatalf("error message: got %q, want it to mention contractId", body.Error.Message)
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
