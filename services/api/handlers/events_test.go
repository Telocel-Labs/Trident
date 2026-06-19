package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	trident "github.com/Depo-dev/trident/services/api/internal/proto"
	"github.com/Depo-dev/trident/services/api/handlers"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// mockEventsClient satisfies trident.EventsClient for unit tests.
type mockEventsClient struct {
	listResp *trident.ListEventsResponse
	listErr  error
	getResp  *trident.Event
	getErr   error
}

func (m *mockEventsClient) ListEvents(_ context.Context, _ *trident.ListEventsRequest, _ ...grpc.CallOption) (*trident.ListEventsResponse, error) {
	return m.listResp, m.listErr
}

func (m *mockEventsClient) GetEvent(_ context.Context, _ *trident.GetEventRequest, _ ...grpc.CallOption) (*trident.Event, error) {
	return m.getResp, m.getErr
}

func (m *mockEventsClient) StreamEvents(_ context.Context, _ *trident.StreamEventsRequest, _ ...grpc.CallOption) (trident.Events_StreamEventsClient, error) {
	return nil, status.Error(codes.Unimplemented, "not needed in tests")
}

func TestHealth(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	rec := httptest.NewRecorder()

	handlers.Health(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}

	if body["status"] != "ok" {
		t.Errorf("expected status=ok, got %q", body["status"])
	}
}

func TestListEvents_returnsEvents(t *testing.T) {
	mock := &mockEventsClient{
		listResp: &trident.ListEventsResponse{
			Events: []*trident.Event{
				{Id: "abc", ContractId: "CTEST", EventType: "contract"},
			},
			HasMore: false,
		},
	}
	h := handlers.NewEventsHandler(mock)

	req := httptest.NewRequest(http.MethodGet, "/v1/events?contractId=CTEST", nil)
	rec := httptest.NewRecorder()

	h.ListEvents(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestListEvents_grpcError_returns500(t *testing.T) {
	mock := &mockEventsClient{
		listErr: status.Error(codes.Internal, "db error"),
	}
	h := handlers.NewEventsHandler(mock)

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	rec := httptest.NewRecorder()

	h.ListEvents(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestGetEvent_found(t *testing.T) {
	mock := &mockEventsClient{
		getResp: &trident.Event{Id: "abc123", ContractId: "CTEST"},
	}
	h := handlers.NewEventsHandler(mock)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/events/{id}", h.GetEvent)

	req := httptest.NewRequest(http.MethodGet, "/v1/events/abc123", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestGetEvent_notFound(t *testing.T) {
	mock := &mockEventsClient{
		getErr: status.Error(codes.NotFound, "event not found"),
	}
	h := handlers.NewEventsHandler(mock)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/events/{id}", h.GetEvent)

	req := httptest.NewRequest(http.MethodGet, "/v1/events/unknown", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}
