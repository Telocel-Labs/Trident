package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Depo-dev/trident/services/api/gen"
	"github.com/Depo-dev/trident/services/api/handlers"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	uuid1 = "550e8400-e29b-41d4-a716-446655440001"
	uuid2 = "550e8400-e29b-41d4-a716-446655440002"
	uuid3 = "550e8400-e29b-41d4-a716-446655440003"
)

func fakeEvent(id string) *gen.Event {
	return &gen.Event{
		Id:              id,
		ContractId:      "CABC123",
		LedgerSequence:  42,
		LedgerTimestamp: "2026-01-01T00:00:00Z",
		TransactionHash: "deadbeef",
		EventIndex:      0,
		EventType:       "contract",
		Topics:          []string{"transfer"},
		Data:            `{}`,
		CreatedAt:       "2026-01-01T00:00:01Z",
	}
}

func batchPost(body any) *http.Request {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "/v1/events/batch", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestBatchGetEvents_AllFound(t *testing.T) {
	handlers.SetEventsClient(&MockEventsClient{
		GetEventFunc: func(_ context.Context, req *gen.GetEventRequest) (*gen.Event, error) {
			return fakeEvent(req.Id), nil
		},
	})

	w := httptest.NewRecorder()
	handlers.BatchGetEvents(w, batchPost(map[string]any{"ids": []string{uuid1, uuid2}}))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp handlers.BatchEventsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Events) != 2 {
		t.Errorf("expected 2 events, got %d", len(resp.Events))
	}
	if len(resp.Missing) != 0 {
		t.Errorf("expected 0 missing, got %v", resp.Missing)
	}
}

func TestBatchGetEvents_SomeMissing(t *testing.T) {
	handlers.SetEventsClient(&MockEventsClient{
		GetEventFunc: func(_ context.Context, req *gen.GetEventRequest) (*gen.Event, error) {
			if req.Id == uuid1 {
				return fakeEvent(uuid1), nil
			}
			return nil, status.Error(codes.NotFound, "not found")
		},
	})

	w := httptest.NewRecorder()
	handlers.BatchGetEvents(w, batchPost(map[string]any{"ids": []string{uuid1, uuid2}}))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp handlers.BatchEventsResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Events) != 1 {
		t.Errorf("expected 1 event, got %d", len(resp.Events))
	}
	if len(resp.Missing) != 1 || resp.Missing[0] != uuid2 {
		t.Errorf("expected [%s] in missing, got %v", uuid2, resp.Missing)
	}
}

func TestBatchGetEvents_LimitExceeded(t *testing.T) {
	handlers.SetEventsClient(&MockEventsClient{})

	ids := make([]string, 101)
	for i := range ids {
		ids[i] = uuid1
	}
	w := httptest.NewRecorder()
	handlers.BatchGetEvents(w, batchPost(map[string]any{"ids": ids}))

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "100") {
		t.Errorf("expected limit message, got: %s", w.Body.String())
	}
}

func TestBatchGetEvents_InvalidUUID(t *testing.T) {
	handlers.SetEventsClient(&MockEventsClient{})

	w := httptest.NewRecorder()
	handlers.BatchGetEvents(w, batchPost(map[string]any{"ids": []string{"not-a-uuid", uuid1}}))

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	var body map[string]any
	_ = json.NewDecoder(w.Body).Decode(&body)
	if _, ok := body["invalid_ids"]; !ok {
		t.Errorf("expected invalid_ids in response, got: %v", body)
	}
}

func TestBatchGetEvents_InvalidJSON(t *testing.T) {
	handlers.SetEventsClient(&MockEventsClient{})

	r := httptest.NewRequest(http.MethodPost, "/v1/events/batch", strings.NewReader("{bad json"))
	w := httptest.NewRecorder()
	handlers.BatchGetEvents(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestBatchGetEvents_EmptyMissingIsArray(t *testing.T) {
	handlers.SetEventsClient(&MockEventsClient{
		GetEventFunc: func(_ context.Context, req *gen.GetEventRequest) (*gen.Event, error) {
			return fakeEvent(req.Id), nil
		},
	})

	w := httptest.NewRecorder()
	handlers.BatchGetEvents(w, batchPost(map[string]any{"ids": []string{uuid1}}))

	body := w.Body.String()
	if !strings.Contains(body, `"missing":[]`) {
		t.Errorf("missing should be empty array, got: %s", body)
	}
}
