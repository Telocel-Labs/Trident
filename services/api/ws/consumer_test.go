package ws

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestConsumerGroupName(t *testing.T) {
	t.Run("default derives from consumerName", func(t *testing.T) {
		orig := os.Getenv("WS_CONSUMER_GROUP")
		_ = os.Unsetenv("WS_CONSUMER_GROUP")
		t.Cleanup(func() {
			if orig != "" {
				_ = os.Setenv("WS_CONSUMER_GROUP", orig)
			}
		})

		consumerName := "replica-1-pod-xyz"
		got := consumerGroupName(consumerName)
		want := "trident-api-replica-1-pod-xyz"
		if got != want {
			t.Fatalf("expected %q, got %q", want, got)
		}
	})

	t.Run("explicit WS_CONSUMER_GROUP overrides", func(t *testing.T) {
		t.Setenv("WS_CONSUMER_GROUP", "custom-group")
		got := consumerGroupName("replica-1-pod-xyz")
		if got != "custom-group" {
			t.Fatalf("expected custom-group, got %q", got)
		}
	})
}

func TestParseStreamEntry_CanonicalIndexerSchema(t *testing.T) {
	values := map[string]interface{}{
		"contract_id":      "CA7QYNF7SOWQ3GLR2BGMZEHXAVIRZA4KVWLTJJFC7MGXUA74P7UJVSGZ",
		"id":               "11111111-2222-3333-4444-555555555555",
		"network":          "local",
		"topics":           `["mint","GA1111","GB2222"]`,
		"data":             `{"amount":4200000}`,
		"ledger_sequence":  "12345",
		"transaction_hash": "tx_deadbeef",
		"event_index":      "1",
		"event_type":       "contract",
	}

	cid, payload, ok := parseStreamEntry(values)
	if !ok {
		t.Fatal("expected ok=true for canonical Indexer schema")
	}
	if cid != "CA7QYNF7SOWQ3GLR2BGMZEHXAVIRZA4KVWLTJJFC7MGXUA74P7UJVSGZ" {
		t.Fatalf("unexpected contractId: %s", cid)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("failed to unmarshal generated payload: %v", err)
	}

	if parsed["id"] != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("expected id preserved, got %v", parsed["id"])
	}
	if parsed["network"] != "local" {
		t.Errorf("expected network=local, got %v", parsed["network"])
	}
	if parsed["contractId"] != cid || parsed["contract_id"] != cid {
		t.Errorf("expected both camelCase and snake_case contract ID")
	}
	topics, ok := parsed["topics"].([]interface{})
	if !ok || len(topics) != 3 || topics[0] != "mint" {
		t.Errorf("topics not properly parsed: %v", parsed["topics"])
	}
}

func TestParseStreamEntry_LegacyWrapperSchema(t *testing.T) {
	values := map[string]interface{}{
		"data": `{"contract_id":"C_LEGACY","payload":{"hello":"world"}}`,
	}

	cid, payload, ok := parseStreamEntry(values)
	if !ok {
		t.Fatal("expected ok=true for legacy wrapper schema")
	}
	if cid != "C_LEGACY" {
		t.Fatalf("unexpected contractId: %s", cid)
	}
	if !strings.Contains(string(payload), "world") {
		t.Fatalf("unexpected payload: %s", string(payload))
	}
}
