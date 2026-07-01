package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVerifyWebhookSignature(t *testing.T) {
	body := `{"hello":"world"}`
	secret := "super-secret"
	signature := "sha256=" + signWebhookBody(body, secret)

	if !verifyWebhookSignature(body, signature, secret) {
		t.Fatalf("expected signature verification to succeed")
	}

	if verifyWebhookSignature(body, "sha256=deadbeef", secret) {
		t.Fatalf("expected signature verification to fail for a mismatched signature")
	}
}

func TestDeliverWebhookSendsSignedPayload(t *testing.T) {
	var gotBody []byte
	var gotSignature string
	var gotContentType string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		gotSignature = r.Header.Get("X-Trident-Signature")
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	subscription := webhookSubscription{
		ID:        "sub-123",
		Secret:    "super-secret",
		TargetURL: server.URL,
	}
	event := webhookEvent{
		ID:              "evt-123",
		ContractID:      "C123",
		LedgerSequence:  55001,
		Topic0:          "transfer",
		Data:            map[string]any{"amount": "100"},
		TransactionHash: "abc123",
		Network:         "testnet",
	}

	if err := deliverWebhook(context.Background(), subscription, event); err != nil {
		t.Fatalf("deliverWebhook returned error: %v", err)
	}

	if gotContentType != "application/json" {
		t.Fatalf("expected application/json content type, got %q", gotContentType)
	}
	if gotSignature == "" {
		t.Fatalf("expected X-Trident-Signature header to be sent")
	}
	if gotSignature != "sha256="+signWebhookBody(string(gotBody), subscription.Secret) {
		t.Fatalf("unexpected signature header: %q", gotSignature)
	}

	var payload map[string]any
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("payload was not valid JSON: %v", err)
	}
	if payload["webhook_id"] != subscription.ID {
		t.Fatalf("expected webhook_id to be set")
	}
	if payload["event"].(map[string]any)["id"] != event.ID {
		t.Fatalf("expected event id to be included in payload")
	}
}
