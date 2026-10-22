package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebhookStreamMaxLen_ConfigurationAndDefault(t *testing.T) {
	// Default
	_ = os.Unsetenv("REDIS_STREAM_MAXLEN")
	if got := getRedisStreamMaxLen(); got != defaultRedisStreamMaxLen {
		t.Errorf("expected default %d, got %d", defaultRedisStreamMaxLen, got)
	}

	// Valid override
	_ = os.Setenv("REDIS_STREAM_MAXLEN", "75000")
	defer func() { _ = os.Unsetenv("REDIS_STREAM_MAXLEN") }()
	if got := getRedisStreamMaxLen(); got != 75000 {
		t.Errorf("expected 75000, got %d", got)
	}

	// Invalid value falls back
	_ = os.Setenv("REDIS_STREAM_MAXLEN", "not-a-number")
	if got := getRedisStreamMaxLen(); got != defaultRedisStreamMaxLen {
		t.Errorf("expected fallback %d, got %d", defaultRedisStreamMaxLen, got)
	}
}

func TestWebhookWorkerCheckpoint_Serialization(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	cp := webhookWorkerCheckpoint{
		CreatedAt: now,
		ID:        "00000000-0000-0000-0000-000000000042",
	}

	data, err := json.Marshal(cp)
	if err != nil {
		t.Fatalf("marshal checkpoint: %v", err)
	}

	var decoded webhookWorkerCheckpoint
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal checkpoint: %v", err)
	}

	if decoded.ID != cp.ID || !decoded.CreatedAt.Equal(cp.CreatedAt) {
		t.Errorf("expected %+v, got %+v", cp, decoded)
	}
}

func TestCatchupWebhookEventsFromPostgres_NilDBSafe(t *testing.T) {
	count, err := catchupWebhookEventsFromPostgres(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0, got %d", count)
	}
}

func TestCatchupWebhookEventsFromPostgres_LiveDBCatchup(t *testing.T) {
	db := connectWebhookTestDB(t)
	allowLoopbackWebhookTargets(t)

	var receivedCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	ctx := context.Background()
	suffix := time.Now().UnixNano()
	contractID := fmt.Sprintf("CCATCHUPTEST%d", suffix%1000000)

	// 1. Insert API key and subscription
	var apiKeyID, subID string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO api_keys (key_hash, key_prefix, label) VALUES ($1, $2, $3) RETURNING id`,
		fmt.Sprintf("catchup-hash-%d", suffix), "catchup-pfx", "catchup-test",
	).Scan(&apiKeyID); err != nil {
		t.Fatalf("insert api key: %v", err)
	}

	if err := db.QueryRowContext(ctx,
		`INSERT INTO webhook_subscriptions (api_key_id, contract_id, target_url, secret, network)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		apiKeyID, contractID, server.URL, "secret123", "testnet",
	).Scan(&subID); err != nil {
		t.Fatalf("insert subscription: %v", err)
	}

	// Initial checkpoint before our test events
	t0 := time.Now().Add(-10 * time.Second).UTC()
	_ = saveWebhookWorkerCheckpoint(ctx, db, webhookWorkerCheckpoint{
		CreatedAt: t0,
		ID:        "00000000-0000-0000-0000-000000000000",
	})

	// 2. Insert 3 soroban_events while worker was supposedly stopped
	var eventIDs []string
	for i := 1; i <= 3; i++ {
		var eid string
		if err := db.QueryRowContext(ctx,
			`INSERT INTO soroban_events (contract_id, ledger_sequence, ledger_timestamp, transaction_hash, event_index, event_type, network, topics, data, created_at)
			 VALUES ($1, $2, NOW(), $3, $4, 'contract', 'testnet', '[]'::jsonb, '{}'::jsonb, NOW()) RETURNING id`,
			contractID, int64(100+i), fmt.Sprintf("tx-%d-%d", suffix, i), i,
		).Scan(&eid); err != nil {
			t.Fatalf("insert soroban event %d: %v", i, err)
		}
		eventIDs = append(eventIDs, eid)
	}

	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM webhook_deliveries WHERE subscription_id = $1`, subID)
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM webhook_subscriptions WHERE id = $1`, subID)
		for _, eid := range eventIDs {
			_, _ = db.ExecContext(cleanupCtx, `DELETE FROM soroban_events WHERE id = $1`, eid)
		}
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM api_keys WHERE id = $1`, apiKeyID)
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM system_state WHERE key = $1`, webhookCheckpointKey)
	})

	// 3. Run catchup - simulates worker restart after downtime
	delivered, err := catchupWebhookEventsFromPostgres(ctx, db, nil)
	if err != nil {
		t.Fatalf("catchupWebhookEventsFromPostgres failed: %v", err)
	}
	if delivered < 3 {
		t.Fatalf("expected at least 3 events caught up, got %d", delivered)
	}

	// 4. Verify receiver got requests
	if receivedCount.Load() < 3 {
		t.Fatalf("expected receiver to receive at least 3 deliveries, got %d", receivedCount.Load())
	}

	// 5. Subsequent run should deliver 0 (no duplicate delivery)
	subsequent, err := catchupWebhookEventsFromPostgres(ctx, db, nil)
	if err != nil {
		t.Fatalf("second catchup failed: %v", err)
	}
	if subsequent != 0 {
		t.Errorf("expected 0 subsequent deliveries, got %d", subsequent)
	}
}
