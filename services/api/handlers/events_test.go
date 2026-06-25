package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Depo-dev/trident/services/api/cursor"
	"github.com/Depo-dev/trident/services/api/handlers"
)

func TestListEvents_NoParams_Returns200(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	rr := httptest.NewRecorder()

	handlers.ListEvents(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rr.Code)
	}
}

func TestListEvents_InvalidLimit_Returns400(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/events?limit=999", nil)
	rr := httptest.NewRecorder()

	handlers.ListEvents(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object in body")
	}
	if errObj["field"] != "limit" {
		t.Errorf("want field=limit, got %v", errObj["field"])
	}
}

func TestListEvents_InvalidContractID_Returns400(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/events?contractId=not-a-contract", nil)
	rr := httptest.NewRecorder()

	handlers.ListEvents(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
}

func TestListEvents_LedgerRangeInverted_Returns400(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/events?ledgerFrom=500&ledgerTo=100", nil)
	rr := httptest.NewRecorder()

	handlers.ListEvents(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
}

func TestGetEvent_ValidUUID_Returns200(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/events/{id}", handlers.GetEvent)

	req := httptest.NewRequest(http.MethodGet, "/v1/events/550e8400-e29b-41d4-a716-446655440000", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rr.Code)
	}
}

func TestGetEvent_InvalidUUID_Returns400(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/events/{id}", handlers.GetEvent)

	req := httptest.NewRequest(http.MethodGet, "/v1/events/not-a-uuid", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object in body")
	}
	if errObj["field"] != "id" {
		t.Errorf("want field=id, got %v", errObj["field"])
	}
}

func TestListEvents_ValidCursor_Returns200WithNextCursor(t *testing.T) {
	opaque := cursor.Encode("ledger:42")
	req := httptest.NewRequest(http.MethodGet, "/v1/events?cursor="+opaque, nil)
	rr := httptest.NewRecorder()

	handlers.ListEvents(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	nc, ok := body["next_cursor"].(string)
	if !ok || nc == "" {
		t.Errorf("want non-empty next_cursor in response, got %v", body["next_cursor"])
	}
}

func TestListEvents_InvalidCursor_Returns400(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/events?cursor=!!!notbase64!!!", nil)
	rr := httptest.NewRecorder()

	handlers.ListEvents(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object in body")
	}
	if errObj["field"] != "cursor" {
		t.Errorf("want field=cursor, got %v", errObj["field"])
	}
}
