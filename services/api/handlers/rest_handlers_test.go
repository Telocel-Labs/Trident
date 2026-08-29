package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Depo-dev/trident/services/api/cursor"
	"github.com/Depo-dev/trident/services/api/gen"
	"github.com/Depo-dev/trident/services/api/handlers"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	restContractID = "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4"
	restEventID    = "550e8400-e29b-41d4-a716-446655440000"
)

func TestRESTListEventsHandler_TableDriven(t *testing.T) {
	tests := []struct {
		name       string
		target     string
		client     *MockEventsClient
		wantStatus int
		check      func(t *testing.T, rr *httptest.ResponseRecorder)
	}{
		{
			name:   "valid request with contract filter forwards to grpc",
			target: "/v1/events?contractId=" + restContractID,
			client: &MockEventsClient{
				ListEventsFunc: func(ctx context.Context, req *gen.ListEventsRequest) (*gen.ListEventsResponse, error) {
					if req.ContractId != restContractID {
						t.Fatalf("contract_id forwarded to grpc: got %q, want %q", req.ContractId, restContractID)
					}
					return &gen.ListEventsResponse{
						Events: []*gen.Event{restEvent(restEventID)},
					}, nil
				},
			},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, rr *httptest.ResponseRecorder) {
				var body handlers.ListEventsResponse
				decodeJSON(t, rr, &body)
				if len(body.Events) != 1 {
					t.Fatalf("events length: got %d, want 1", len(body.Events))
				}
				if body.Events[0].ID != restEventID {
					t.Fatalf("event id: got %q, want %q", body.Events[0].ID, restEventID)
				}
			},
		},
		{
			name:   "request without filters returns empty page",
			target: "/v1/events",
			client: &MockEventsClient{
				ListEventsFunc: func(ctx context.Context, req *gen.ListEventsRequest) (*gen.ListEventsResponse, error) {
					if req.ContractId != "" || req.LedgerFrom != 0 || req.LedgerTo != 0 {
						t.Fatalf("unexpected filters forwarded: %+v", req)
					}
					return &gen.ListEventsResponse{Events: []*gen.Event{}}, nil
				},
			},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, rr *httptest.ResponseRecorder) {
				var body handlers.ListEventsResponse
				decodeJSON(t, rr, &body)
				if len(body.Events) != 0 {
					t.Fatalf("events length: got %d, want 0", len(body.Events))
				}
			},
		},
		{
			name:       "invalid ledgerFrom non integer returns structured 400",
			target:     "/v1/events?ledgerFrom=abc",
			client:     &MockEventsClient{},
			wantStatus: http.StatusBadRequest,
			check:      expectErrorCode("INVALID_ARGUMENT", "ledgerFrom"),
		},
		{
			name:       "invalid ledgerTo negative returns structured 400",
			target:     "/v1/events?ledgerTo=-1",
			client:     &MockEventsClient{},
			wantStatus: http.StatusBadRequest,
			check:      expectErrorCode("INVALID_ARGUMENT", "ledgerTo"),
		},
		{
			name:       "invalid ledger range returns structured 400",
			target:     "/v1/events?ledgerFrom=20&ledgerTo=10",
			client:     &MockEventsClient{},
			wantStatus: http.StatusBadRequest,
			check:      expectErrorCode("INVALID_ARGUMENT", "ledgerTo"),
		},
		{
			name:       "legacy from_ledger query name is rejected",
			target:     "/v1/events?from_ledger=1",
			client:     &MockEventsClient{},
			wantStatus: http.StatusBadRequest,
			check:      expectErrorCode("INVALID_ARGUMENT", "from_ledger"),
		},
		{
			name:   "grpc unavailable returns structured 503",
			target: "/v1/events",
			client: &MockEventsClient{
				ListEventsFunc: func(ctx context.Context, req *gen.ListEventsRequest) (*gen.ListEventsResponse, error) {
					return nil, status.Error(codes.Unavailable, "connection refused")
				},
			},
			wantStatus: http.StatusServiceUnavailable,
			check:      expectErrorCode("UNAVAILABLE", "failed to fetch events"),
		},
		{
			name:   "grpc deadline exceeded returns structured 504",
			target: "/v1/events",
			client: &MockEventsClient{
				ListEventsFunc: func(ctx context.Context, req *gen.ListEventsRequest) (*gen.ListEventsResponse, error) {
					return nil, status.Error(codes.DeadlineExceeded, "deadline exceeded")
				},
			},
			wantStatus: http.StatusGatewayTimeout,
			check:      expectErrorCode("UNAVAILABLE", "failed to fetch events"),
		},
		{
			name:   "pagination cursor is decoded and response cursor is encoded",
			target: "/v1/events?cursor=" + cursor.Encode("ledger:42"),
			client: &MockEventsClient{
				ListEventsFunc: func(ctx context.Context, req *gen.ListEventsRequest) (*gen.ListEventsResponse, error) {
					if req.Cursor != "ledger:42" {
						t.Fatalf("cursor forwarded to grpc: got %q, want ledger:42", req.Cursor)
					}
					return &gen.ListEventsResponse{
						Events:     []*gen.Event{},
						HasMore:    true,
						NextCursor: "ledger:84",
					}, nil
				},
			},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, rr *httptest.ResponseRecorder) {
				var body handlers.ListEventsResponse
				decodeJSON(t, rr, &body)
				if body.NextCursor == nil {
					t.Fatal("next_cursor: got nil, want encoded cursor")
				}
				decoded, err := cursor.Decode(*body.NextCursor)
				if err != nil {
					t.Fatalf("decode next_cursor: %v", err)
				}
				if decoded != "ledger:84" {
					t.Fatalf("next_cursor decoded: got %q, want ledger:84", decoded)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handlers.SetEventsClient(tt.client)

			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			rr := httptest.NewRecorder()
			handlers.ListEvents(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("status: got %d, want %d; body: %s", rr.Code, tt.wantStatus, rr.Body.String())
			}
			if tt.check != nil {
				tt.check(t, rr)
			}
		})
	}
}

func TestRESTGetEventHandler_TableDriven(t *testing.T) {
	tests := []struct {
		name       string
		eventID    string
		client     *MockEventsClient
		wantStatus int
		check      func(t *testing.T, rr *httptest.ResponseRecorder)
	}{
		{
			name:    "valid uuid exists",
			eventID: restEventID,
			client: &MockEventsClient{
				GetEventFunc: func(ctx context.Context, req *gen.GetEventRequest) (*gen.Event, error) {
					if req.Id != restEventID {
						t.Fatalf("event id forwarded to grpc: got %q, want %q", req.Id, restEventID)
					}
					return restEvent(restEventID), nil
				},
			},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, rr *httptest.ResponseRecorder) {
				var body struct {
					Event *handlers.EventJSON `json:"event"`
				}
				decodeJSON(t, rr, &body)
				if body.Event == nil {
					t.Fatal("event: got nil")
				}
				if body.Event.ID != restEventID {
					t.Fatalf("event id: got %q, want %q", body.Event.ID, restEventID)
				}
			},
		},
		{
			name:    "valid uuid not found",
			eventID: restEventID,
			client: &MockEventsClient{
				GetEventFunc: func(ctx context.Context, req *gen.GetEventRequest) (*gen.Event, error) {
					return nil, status.Error(codes.NotFound, "event not found")
				},
			},
			wantStatus: http.StatusNotFound,
			check:      expectErrorCode("NOT_FOUND", "event not found"),
		},
		{
			name:       "malformed uuid",
			eventID:    "not-a-uuid",
			client:     &MockEventsClient{},
			wantStatus: http.StatusBadRequest,
			check:      expectErrorCode("INVALID_ARGUMENT", "id"),
		},
		{
			name:    "grpc unavailable",
			eventID: restEventID,
			client: &MockEventsClient{
				GetEventFunc: func(ctx context.Context, req *gen.GetEventRequest) (*gen.Event, error) {
					return nil, status.Error(codes.Unavailable, "connection refused")
				},
			},
			wantStatus: http.StatusServiceUnavailable,
			check:      expectErrorCode("UNAVAILABLE", "failed to fetch event"),
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/events/{id}", handlers.GetEvent)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handlers.SetEventsClient(tt.client)

			req := httptest.NewRequest(http.MethodGet, "/v1/events/"+tt.eventID, nil)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("status: got %d, want %d; body: %s", rr.Code, tt.wantStatus, rr.Body.String())
			}
			if tt.check != nil {
				tt.check(t, rr)
			}
		})
	}
}

func restEvent(id string) *gen.Event {
	return &gen.Event{
		Id:              id,
		ContractId:      restContractID,
		LedgerSequence:  100,
		LedgerTimestamp: "2026-07-30T08:00:00Z",
		TransactionHash: "d8b04e8d7c0f4c93a2b6a7a8d8f930beefcafe1234567890abcdef1234567890",
		EventIndex:      1,
		EventType:       "contract",
		Topics:          []string{"transfer"},
		Data:            `{"amount":"100"}`,
		CreatedAt:       "2026-07-30T08:00:01Z",
	}
}

func expectErrorCode(code, messageContains string) func(t *testing.T, rr *httptest.ResponseRecorder) {
	return func(t *testing.T, rr *httptest.ResponseRecorder) {
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		decodeJSON(t, rr, &body)
		if body.Error.Code != code {
			t.Fatalf("error.code: got %q, want %q; body: %s", body.Error.Code, code, rr.Body.String())
		}
		if messageContains != "" && !strings.Contains(body.Error.Message, messageContains) {
			t.Fatalf("error.message: got %q, want containing %q", body.Error.Message, messageContains)
		}
	}
}

func decodeJSON(t *testing.T, rr *httptest.ResponseRecorder, dest any) {
	t.Helper()
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type: got %q, want application/json", ct)
	}
	if err := json.NewDecoder(rr.Body).Decode(dest); err != nil {
		t.Fatalf("decode json: %v; body: %s", err, rr.Body.String())
	}
}
