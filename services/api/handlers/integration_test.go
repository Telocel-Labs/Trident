package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Depo-dev/trident/services/api/gen"
	"github.com/Depo-dev/trident/services/api/handlers"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Integration test: List events with valid parameters
func TestIntegration_ListEventsWithFilters(t *testing.T) {
	mock := &MockEventsClient{
		ListEventsFunc: func(ctx context.Context, req *gen.ListEventsRequest) (*gen.ListEventsResponse, error) {
			// Verify all filters were passed through correctly
			if req.ContractId == "" {
				t.Error("contractId filter missing")
			}
			if req.Limit != 25 {
				t.Errorf("limit mismatch: expected 25, got %d", req.Limit)
			}

			return &gen.ListEventsResponse{
				Events: []*gen.Event{
					{
						Id:              "550e8400-e29b-41d4-a716-446655440001",
						ContractId:      req.ContractId,
						LedgerSequence:  1000,
						LedgerTimestamp: "2024-01-01T00:00:00Z",
						TransactionHash: "abcd1234",
						EventIndex:      0,
						EventType:       "contract",
						Topics:          []string{"transfer", "GACCOUNT1"},
						Data:            `{"amount":"100"}`,
						CreatedAt:       "2024-01-01T00:00:01Z",
					},
					{
						Id:              "550e8400-e29b-41d4-a716-446655440002",
						ContractId:      req.ContractId,
						LedgerSequence:  1001,
						LedgerTimestamp: "2024-01-01T00:01:00Z",
						TransactionHash: "efgh5678",
						EventIndex:      1,
						EventType:       "contract",
						Topics:          []string{"transfer", "GACCOUNT2"},
						Data:            `{"amount":"200"}`,
						CreatedAt:       "2024-01-01T00:01:01Z",
					},
				},
				NextCursor: "ledger:1002",
				HasMore:    true,
			}, nil
		},
	}
	handlers.SetEventsClient(mock)

	url := "/v1/events?contractId=CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4&limit=25&ledgerFrom=1000&ledgerTo=2000"
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rr := httptest.NewRecorder()

	handlers.ListEvents(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	events, ok := resp["events"].([]any)
	if !ok || len(events) != 2 {
		t.Errorf("expected 2 events, got %d", len(events))
	}

	nextCursor, ok := resp["next_cursor"].(string)
	if !ok || nextCursor == "" {
		t.Error("next_cursor should be encoded in response")
	}
}

// Integration test: Get single event
func TestIntegration_GetEventByID(t *testing.T) {
	eventID := "550e8400-e29b-41d4-a716-446655440000"
	mock := &MockEventsClient{
		GetEventFunc: func(ctx context.Context, req *gen.GetEventRequest) (*gen.Event, error) {
			if req.Id != eventID {
				t.Errorf("expected ID %s, got %s", eventID, req.Id)
			}
			return &gen.Event{
				Id:              eventID,
				ContractId:      "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4",
				LedgerSequence:  100,
				LedgerTimestamp: "2024-01-01T00:00:00Z",
				TransactionHash: "abc123",
				EventIndex:      0,
				EventType:       "contract",
				Topics:          []string{"MyTopic"},
				Data:            `{"key":"value"}`,
				CreatedAt:       "2024-01-01T00:00:01Z",
			}, nil
		},
	}
	handlers.SetEventsClient(mock)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/events/{id}", handlers.GetEvent)

	req := httptest.NewRequest(http.MethodGet, "/v1/events/"+eventID, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	event, ok := resp["event"].(map[string]any)
	if !ok {
		t.Fatal("event should be in response")
	}

	if event["id"] != eventID {
		t.Errorf("event ID mismatch")
	}
}

// Integration test: Get event not found
func TestIntegration_GetEventNotFound(t *testing.T) {
	mock := &MockEventsClient{
		GetEventFunc: func(ctx context.Context, req *gen.GetEventRequest) (*gen.Event, error) {
			return nil, status.Error(codes.NotFound, "event not found")
		},
	}
	handlers.SetEventsClient(mock)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/events/{id}", handlers.GetEvent)

	req := httptest.NewRequest(http.MethodGet, "/v1/events/550e8400-e29b-41d4-a716-446655440000", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

// Integration test: gRPC backend unavailable
func TestIntegration_GRPCBackendUnavailable(t *testing.T) {
	handlers.SetEventsClient(nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	rr := httptest.NewRecorder()

	handlers.ListEvents(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rr.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if body["code"] != "INTERNAL" {
		t.Errorf("expected INTERNAL, got %v", body["code"])
	}
}

// Integration test: gRPC NotFound mapped to 404
func TestIntegration_GRPCNotFoundMappedTo404(t *testing.T) {
	mock := &MockEventsClient{
		GetEventFunc: func(ctx context.Context, req *gen.GetEventRequest) (*gen.Event, error) {
			return nil, status.Error(codes.NotFound, "event not found")
		},
	}
	handlers.SetEventsClient(mock)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/events/{id}", handlers.GetEvent)

	req := httptest.NewRequest(http.MethodGet, "/v1/events/550e8400-e29b-41d4-a716-446655440000", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if body["code"] != "NOT_FOUND" {
		t.Errorf("expected NOT_FOUND, got %v", body["code"])
	}
}
