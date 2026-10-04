package trident

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Depo-dev/trident/sdk/go/openapi"
)

func TestCreateWebhook(t *testing.T) {
	mockResponse := openapi.WebhookCreateResponse{
		ID:         "wh_1",
		ContractID: "CAAAA",
		Network:    "testnet",
		Secret:     "whsec_abc123",
		TargetURL:  "https://example.com/hook",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/webhooks" {
			t.Errorf("expected path /v1/webhooks, got %s", r.URL.Path)
		}
		var body openapi.WebhookCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body.ContractID != "CAAAA" {
			t.Errorf("expected contractId CAAAA, got %s", body.ContractID)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(mockResponse)
	}))
	defer server.Close()

	client := NewClient(TridentClientConfig{BaseURL: server.URL, APIKey: "test-key"})

	res, err := client.CreateWebhook(context.Background(), openapi.WebhookCreateRequest{
		ContractID: "CAAAA",
		TargetURL:  "https://example.com/hook",
	})
	if err != nil {
		t.Fatalf("CreateWebhook failed: %v", err)
	}
	if res.ID != "wh_1" || res.Secret != "whsec_abc123" {
		t.Errorf("unexpected response: %+v", res)
	}
}

func TestListWebhooks(t *testing.T) {
	mockResponse := openapi.ListWebhooksResponse{
		Webhooks: []openapi.WebhookSubscription{
			{ID: "wh_1", ContractID: "CAAAA", Network: "testnet", TargetURL: "https://example.com/hook"},
		},
		HasMore:    false,
		NextCursor: "",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/webhooks" {
			t.Errorf("expected path /v1/webhooks, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("limit") != "10" {
			t.Errorf("expected limit=10, got %s", r.URL.Query().Get("limit"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(mockResponse)
	}))
	defer server.Close()

	client := NewClient(TridentClientConfig{BaseURL: server.URL, APIKey: "test-key"})

	res, err := client.ListWebhooks(context.Background(), 10, "")
	if err != nil {
		t.Fatalf("ListWebhooks failed: %v", err)
	}
	if len(res.Webhooks) != 1 || res.Webhooks[0].ID != "wh_1" {
		t.Errorf("unexpected response: %+v", res)
	}
	// The list response never carries the signing secret.
	if res.Webhooks[0].Secret != nil {
		t.Errorf("expected no secret in list response, got %v", *res.Webhooks[0].Secret)
	}
}

func TestDeleteWebhook(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if r.URL.Path != "/v1/webhooks/wh_1" {
			t.Errorf("expected path /v1/webhooks/wh_1, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(TridentClientConfig{BaseURL: server.URL, APIKey: "test-key"})

	if err := client.DeleteWebhook(context.Background(), "wh_1"); err != nil {
		t.Fatalf("DeleteWebhook failed: %v", err)
	}
}

func TestDeleteWebhookNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"code": "NOT_FOUND", "message": "webhook not found"},
		})
	}))
	defer server.Close()

	client := NewClient(TridentClientConfig{BaseURL: server.URL, APIKey: "test-key"})

	err := client.DeleteWebhook(context.Background(), "does-not-exist")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	apiErr, ok := err.(*TridentApiError)
	if !ok {
		t.Fatalf("expected *TridentApiError, got %T: %v", err, err)
	}
	if apiErr.Status != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", apiErr.Status)
	}
}

func TestPauseAndResumeWebhook(t *testing.T) {
	var sawPause, sawResume bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("expected PATCH, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		switch r.URL.Path {
		case "/v1/webhooks/wh_1/pause":
			sawPause = true
			_ = json.NewEncoder(w).Encode(openapi.WebhookStatusResponse{Status: openapi.Paused})
		case "/v1/webhooks/wh_1/resume":
			sawResume = true
			_ = json.NewEncoder(w).Encode(openapi.WebhookStatusResponse{Status: openapi.Resumed})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(TridentClientConfig{BaseURL: server.URL, APIKey: "test-key"})

	paused, err := client.PauseWebhook(context.Background(), "wh_1")
	if err != nil {
		t.Fatalf("PauseWebhook failed: %v", err)
	}
	if paused.Status != openapi.Paused {
		t.Errorf("expected paused status, got %v", paused.Status)
	}

	resumed, err := client.ResumeWebhook(context.Background(), "wh_1")
	if err != nil {
		t.Fatalf("ResumeWebhook failed: %v", err)
	}
	if resumed.Status != openapi.Resumed {
		t.Errorf("expected resumed status, got %v", resumed.Status)
	}

	if !sawPause || !sawResume {
		t.Errorf("expected both pause and resume requests, sawPause=%v sawResume=%v", sawPause, sawResume)
	}
}

func TestRotateWebhookSecret(t *testing.T) {
	mockResponse := openapi.WebhookRotateSecretResponse{
		ID:             "wh_1",
		Secret:         "whsec_new",
		PreviousSecret: "whsec_old",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/webhooks/wh_1/rotate-secret" {
			t.Errorf("expected path /v1/webhooks/wh_1/rotate-secret, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(mockResponse)
	}))
	defer server.Close()

	client := NewClient(TridentClientConfig{BaseURL: server.URL, APIKey: "test-key"})

	res, err := client.RotateWebhookSecret(context.Background(), "wh_1")
	if err != nil {
		t.Fatalf("RotateWebhookSecret failed: %v", err)
	}
	if res.Secret != "whsec_new" || res.PreviousSecret != "whsec_old" {
		t.Errorf("unexpected response: %+v", res)
	}
}
