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
	"google.golang.org/grpc"
)

// MockEventsClient is a mock implementation of gen.EventsClient
type MockEventsClient struct {
	ListEventsFunc func(context.Context, *gen.ListEventsRequest) (*gen.ListEventsResponse, error)
	GetEventFunc   func(context.Context, *gen.GetEventRequest) (*gen.Event, error)
}

func (m *MockEventsClient) ListEvents(ctx context.Context, req *gen.ListEventsRequest, opts ...grpc.CallOption) (*gen.ListEventsResponse, error) {
	if m.ListEventsFunc != nil {
		return m.ListEventsFunc(ctx, req)
	}
	return &gen.ListEventsResponse{}, nil
}

func (m *MockEventsClient) GetEvent(ctx context.Context, req *gen.GetEventRequest, opts ...grpc.CallOption) (*gen.Event, error) {
	if m.GetEventFunc != nil {
		return m.GetEventFunc(ctx, req)
	}
	return &gen.Event{}, nil
}

func (m *MockEventsClient) StreamEvents(ctx context.Context, req *gen.StreamEventsRequest, opts ...grpc.CallOption) (gen.Events_StreamEventsClient, error) {
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

// TestListEvents_HasMoreField verifies the response always contains has_more.
func TestListEvents_HasMoreField_PresentInResponse(t *testing.T) {
	mock := &MockEventsClient{
		ListEventsFunc: func(ctx context.Context, req *gen.ListEventsRequest) (*gen.ListEventsResponse, error) {
			return &gen.ListEventsResponse{Events: []*gen.Event{}, HasMore: false, NextCursor: ""}, nil
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
	if _, ok := body["has_more"]; !ok {
		t.Error("response must contain has_more field")
	}
}

// TestListEvents_EmptyPage_HasMoreFalseAndNullCursor checks the last-page contract.
// gRPC returning HasMore=false and empty NextCursor → has_more=false, next_cursor=null.
func TestListEvents_EmptyPage_HasMoreFalseAndNullCursor(t *testing.T) {
	mock := &MockEventsClient{
		ListEventsFunc: func(ctx context.Context, req *gen.ListEventsRequest) (*gen.ListEventsResponse, error) {
			return &gen.ListEventsResponse{Events: []*gen.Event{}, HasMore: false, NextCursor: ""}, nil
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
	hasMore, ok := body["has_more"].(bool)
	if !ok {
		t.Fatalf("has_more must be a boolean, got %T", body["has_more"])
	}
	if hasMore {
		t.Error("has_more must be false when no events returned")
	}
	if body["next_cursor"] != nil {
		t.Errorf("next_cursor must be null on last page, got %v", body["next_cursor"])
	}
}

// TestListEvents_ValidCursor_Returns200WithNullCursor verifies that when gRPC
// returns HasMore=false, next_cursor is null even if the request included a cursor.
func TestListEvents_ValidCursor_Returns200WithNullCursor(t *testing.T) {
	mock := &MockEventsClient{
		ListEventsFunc: func(ctx context.Context, req *gen.ListEventsRequest) (*gen.ListEventsResponse, error) {
			return &gen.ListEventsResponse{Events: []*gen.Event{}, HasMore: false, NextCursor: ""}, nil
		},
	}
	handlers.SetEventsClient(mock)

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
	if body["next_cursor"] != nil {
		t.Errorf("want next_cursor=null, got %v", body["next_cursor"])
	}
	hasMore, _ := body["has_more"].(bool)
	if hasMore {
		t.Error("has_more must be false when gRPC returns HasMore=false")
	}
}

// TestListEvents_HasMore_NonNullCursor verifies has_more=true yields a non-null next_cursor.
func TestListEvents_HasMore_NonNullCursor(t *testing.T) {
	mock := &MockEventsClient{
		ListEventsFunc: func(ctx context.Context, req *gen.ListEventsRequest) (*gen.ListEventsResponse, error) {
			return &gen.ListEventsResponse{
				Events:     []*gen.Event{},
				HasMore:    true,
				NextCursor: "ledger:100",
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
	hasMore, _ := body["has_more"].(bool)
	if !hasMore {
		t.Error("has_more must be true when gRPC returns HasMore=true")
	}
	if body["next_cursor"] == nil {
		t.Error("next_cursor must be non-null when has_more=true")
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
