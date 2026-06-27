package trident

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestQueryEvents(t *testing.T) {
	mockResponse := PaginatedEvents{
		Events: []*SorobanEvent{
			{
				ID:             "550e8400-e29b-41d4-a716-446655440000",
				ContractID:     "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM",
				LedgerSequence: 50000,
				EventType:      "contract",
				Data:           `{"amount":"100"}`,
			},
		},
		HasMore:    true,
		NextCursor: "next-page-cursor",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/events" {
			t.Errorf("expected path /v1/events, got %s", r.URL.Path)
		}
		if r.Header.Get("X-API-Key") != "test-key" {
			t.Errorf("expected X-API-Key test-key, got %s", r.Header.Get("X-API-Key"))
		}
		if r.URL.Query().Get("contractId") != "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM" {
			t.Errorf("expected contractId query param, got %s", r.URL.Query().Get("contractId"))
		}
		if r.URL.Query().Get("limit") != "50" {
			t.Errorf("expected limit query param 50, got %s", r.URL.Query().Get("limit"))
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(mockResponse)
	}))
	defer server.Close()

	client := NewClient(TridentClientConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
	})

	limit := 50
	res, err := client.QueryEvents(context.Background(), QueryEventsParams{
		ContractID: "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM",
		Limit:      limit,
	})
	if err != nil {
		t.Fatalf("QueryEvents failed: %v", err)
	}

	if len(res.Events) != 1 || res.Events[0].ID != mockResponse.Events[0].ID {
		t.Errorf("expected event %s, got %v", mockResponse.Events[0].ID, res.Events)
	}
	if !res.HasMore || res.NextCursor != "next-page-cursor" {
		t.Errorf("unexpected pagination metadata: %+v", res)
	}
}

func TestGetEventByID(t *testing.T) {
	mockResponse := struct {
		Event *SorobanEvent `json:"event"`
	}{
		Event: &SorobanEvent{
			ID:             "550e8400-e29b-41d4-a716-446655440000",
			ContractID:     "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM",
			LedgerSequence: 50000,
			EventType:      "contract",
			Data:           `{"amount":"100"}`,
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := "/v1/events/550e8400-e29b-41d4-a716-446655440000"
		if r.URL.Path != expectedPath {
			t.Errorf("expected path %s, got %s", expectedPath, r.URL.Path)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(mockResponse)
	}))
	defer server.Close()

	client := NewClient(TridentClientConfig{
		BaseURL: server.URL,
	})

	res, err := client.GetEventByID(context.Background(), "550e8400-e29b-41d4-a716-446655440000")
	if err != nil {
		t.Fatalf("GetEventByID failed: %v", err)
	}

	if res.ID != mockResponse.Event.ID || res.ContractID != mockResponse.Event.ContractID {
		t.Errorf("unexpected event returned: %+v", res)
	}
}

func TestSubscribeToContract(t *testing.T) {
	mockEvent := SorobanEvent{
		ID:             "test-uuid",
		ContractID:     "C123",
		LedgerSequence: 42,
		Data:           `{"foo":"bar"}`,
	}

	wsHandler := websocket.Server{
		Handler: func(ws *websocket.Conn) {
			defer ws.Close()

			// Check path and query
			req := ws.Request()
			if !strings.HasPrefix(req.URL.Path, "/ws") {
				t.Errorf("expected ws path, got %s", req.URL.Path)
			}
			if req.URL.Query().Get("contractId") != "C123" {
				t.Errorf("expected contractId C123, got %s", req.URL.Query().Get("contractId"))
			}

			// Send mock event
			payload, _ := json.Marshal(mockEvent)
			_, err := ws.Write(payload)
			if err != nil {
				t.Errorf("failed to write ws message: %v", err)
				return
			}

			// Wait a bit
			time.Sleep(100 * time.Millisecond)
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wsHandler.ServeHTTP(w, r)
	}))
	defer server.Close()

	client := NewClient(TridentClientConfig{
		BaseURL: server.URL,
	})

	sub, err := client.SubscribeToContract(context.Background(), SubscribeToContractParams{
		ContractID: "C123",
	})
	if err != nil {
		t.Fatalf("SubscribeToContract failed: %v", err)
	}
	defer sub.Unsubscribe()

	select {
	case ev := <-sub.Events:
		if ev.ID != mockEvent.ID || ev.ContractID != mockEvent.ContractID {
			t.Errorf("expected event %+v, got %+v", mockEvent, ev)
		}
	case err := <-sub.Errors:
		t.Errorf("received subscription error: %v", err)
	case <-time.After(2 * time.Second):
		t.Error("timeout waiting for live event")
	}
}
