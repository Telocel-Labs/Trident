package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Depo-dev/trident/services/api/cursor"
	"github.com/Depo-dev/trident/services/api/gen"
	"github.com/Depo-dev/trident/services/api/handlers"
)

// MockEventsClient is a mock implementation of gen.EventsClient
type MockEventsClient struct {
	ListEventsFunc func(context.Context, *gen.ListEventsRequest) (*gen.ListEventsResponse, error)
	GetEventFunc   func(context.Context, *gen.GetEventRequest) (*gen.Event, error)
}

func (m *MockEventsClient) ListEvents(ctx context.Context, req *gen.ListEventsRequest) (*gen.ListEventsResponse, error) {
	if m.ListEventsFunc != nil {
		return m.ListEventsFunc(ctx, req)
	}
	return &gen.ListEventsResponse{}, nil
}

func (m *MockEventsClient) GetEvent(ctx context.Context, req *gen.GetEventRequest) (*gen.Event, error) {
	if m.GetEventFunc != nil {
		return m.GetEventFunc(ctx, req)
	}
	return &gen.Event{}, nil
}

func (m *MockEventsClient) StreamEvents(ctx context.Context, req *gen.StreamEventsRequest) (gen.Events_StreamEventsClient, error) {
	return nil, nil
}

func TestListEvents_NoParams_Returns200(t *testing.T) {
	mock := &MockEventsClient{
		ListEventsFunc: func(ctx context.Context, req *gen.ListEventsRequest) (*gen.ListEventsResponse, error) {
			return &gen.ListEventsResponse{
				Events:    []*gen.Event{},
				NextCursor: "",
				HasMore:   false,
			}, nil
		},
	}
	handlers.SetEventsClient(mock)

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	rr := httptest.NewRecorder()

	handlers.ListEvents(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rr.Code)
	}
}

func TestListEvents_InvalidLimit_Returns400(t *testing.T) {
	handlers.SetEventsClient(&MockEventsClient{})

	req := httptest.NewRequest(http.MethodGet, "/v1/events?limit=999", nil)
	rr := httptest.NewRecorder()

	handlers.ListEvents(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}

	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected application/json, got %s", ct)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["code"] != "INVALID_ARGUMENT" {
		t.Errorf("want code=INVALID_ARGUMENT, got %v", body["code"])
	}
	if body["error"] == nil {
		t.Error("error message should be present")
	}
}

func TestListEvents_InvalidContractID_Returns400(t *testing.T) {
	handlers.SetEventsClient(&MockEventsClient{})

	req := httptest.NewRequest(http.MethodGet, "/v1/events?contractId=not-a-contract", nil)
	rr := httptest.NewRecorder()

	handlers.ListEvents(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
}

func TestListEvents_LedgerRangeInverted_Returns400(t *testing.T) {
	handlers.SetEventsClient(&MockEventsClient{})

	req := httptest.NewRequest(http.MethodGet, "/v1/events?ledgerFrom=500&ledgerTo=100", nil)
	rr := httptest.NewRecorder()

	handlers.ListEvents(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
}

func TestListEvents_ValidParams_CallsGRPC(t *testing.T) {
	called := false
	mock := &MockEventsClient{
		ListEventsFunc: func(ctx context.Context, req *gen.ListEventsRequest) (*gen.ListEventsResponse, error) {
			called = true
			if req.ContractId != "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4" {
				t.Errorf("want contractId passed through, got %s", req.ContractId)
			}
			if req.Limit != 10 {
				t.Errorf("want limit=10, got %d", req.Limit)
			}
			return &gen.ListEventsResponse{
				Events: []*gen.Event{
					{
						Id:              "550e8400-e29b-41d4-a716-446655440000",
						ContractId:      "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4",
						LedgerSequence:  100,
						LedgerTimestamp: "2024-01-01T00:00:00Z",
						Data:            "{}",
					},
				},
				NextCursor: "",
				HasMore:    false,
			}, nil
		},
	}
	handlers.SetEventsClient(mock)

	req := httptest.NewRequest(http.MethodGet, "/v1/events?limit=10&contractId=CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4", nil)
	rr := httptest.NewRecorder()

	handlers.ListEvents(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rr.Code)
	}
	if !called {
		t.Error("gRPC ListEvents was not called")
	}
}

func TestListEvents_WithCursor_DecodesProperly(t *testing.T) {
	cursorValue := cursor.Encode("ledger:42")
	mock := &MockEventsClient{
		ListEventsFunc: func(ctx context.Context, req *gen.ListEventsRequest) (*gen.ListEventsResponse, error) {
			if req.Cursor != "ledger:42" {
				t.Errorf("want decoded cursor 'ledger:42', got %s", req.Cursor)
			}
			return &gen.ListEventsResponse{}, nil
		},
	}
	handlers.SetEventsClient(mock)

	req := httptest.NewRequest(http.MethodGet, "/v1/events?cursor="+cursorValue, nil)
	rr := httptest.NewRecorder()

	handlers.ListEvents(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rr.Code)
	}
}

func TestGetEvent_ValidUUID_Returns200(t *testing.T) {
	mock := &MockEventsClient{
		GetEventFunc: func(ctx context.Context, req *gen.GetEventRequest) (*gen.Event, error) {
			if req.Id != "550e8400-e29b-41d4-a716-446655440000" {
				t.Errorf("want id passed through")
			}
			return &gen.Event{
				Id:              "550e8400-e29b-41d4-a716-446655440000",
				ContractId:      "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4",
				LedgerSequence:  100,
				LedgerTimestamp: "2024-01-01T00:00:00Z",
				Data:            "{}",
			}, nil
		},
	}
	handlers.SetEventsClient(mock)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/events/{id}", handlers.GetEvent)

	req := httptest.NewRequest(http.MethodGet, "/v1/events/550e8400-e29b-41d4-a716-446655440000", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rr.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["event"] == nil {
		t.Error("event not returned in response")
	}
}

func TestGetEvent_InvalidUUID_Returns400(t *testing.T) {
	handlers.SetEventsClient(&MockEventsClient{})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/events/{id}", handlers.GetEvent)

	req := httptest.NewRequest(http.MethodGet, "/v1/events/not-a-uuid", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}

	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected application/json, got %s", ct)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["code"] != "INVALID_ARGUMENT" {
		t.Errorf("want code=INVALID_ARGUMENT, got %v", body["code"])
	}
}

func TestListEvents_ValidCursor_ReturnsNextCursor(t *testing.T) {
	mock := &MockEventsClient{
		ListEventsFunc: func(ctx context.Context, req *gen.ListEventsRequest) (*gen.ListEventsResponse, error) {
			return &gen.ListEventsResponse{
				Events:     []*gen.Event{},
				NextCursor: "ledger:100",
				HasMore:    true,
			}, nil
		},
	}
	handlers.SetEventsClient(mock)

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
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
	handlers.SetEventsClient(&MockEventsClient{})

	req := httptest.NewRequest(http.MethodGet, "/v1/events?cursor=!!!notbase64!!!", nil)
	rr := httptest.NewRecorder()

	handlers.ListEvents(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}

	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected application/json, got %s", ct)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["code"] != "INVALID_ARGUMENT" {
		t.Errorf("want code=INVALID_ARGUMENT, got %v", body["code"])
	}
}
