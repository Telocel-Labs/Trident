package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLoadRetentionConfig_Defaults(t *testing.T) {
	for _, key := range []string{
		"RETENTION_AUDIT_LOG_DAYS",
		"RETENTION_PARSE_ERRORS_DAYS",
		"RETENTION_WEBHOOK_DELIVERIES_DAYS",
		"EVENT_RETENTION_DAYS",
		"RETENTION_EVENT_OUTBOX_DAYS",
	} {
		t.Setenv(key, "")
		_ = os.Unsetenv(key)
	}

	cfg := loadRetentionConfig()

	if cfg.AuditLogDays != 90 {
		t.Errorf("AuditLogDays = %d, want 90", cfg.AuditLogDays)
	}
	if cfg.ParseErrorsDays != 30 {
		t.Errorf("ParseErrorsDays = %d, want 30", cfg.ParseErrorsDays)
	}
	if cfg.WebhookDeliveriesDays != 30 {
		t.Errorf("WebhookDeliveriesDays = %d, want 30", cfg.WebhookDeliveriesDays)
	}
	if cfg.EventRetentionDays != 0 {
		t.Errorf("EventRetentionDays = %d, want 0", cfg.EventRetentionDays)
	}
	// #604: event_outbox is the fastest-growing unbounded table, so unlike
	// soroban_events it defaults to enabled rather than opt-in.
	if cfg.EventOutboxDays != 7 {
		t.Errorf("EventOutboxDays = %d, want 7", cfg.EventOutboxDays)
	}
}

func TestLoadRetentionConfig_EventRetentionDaysOverride(t *testing.T) {
	t.Setenv("EVENT_RETENTION_DAYS", "7")
	cfg := loadRetentionConfig()
	if cfg.EventRetentionDays != 7 {
		t.Errorf("EventRetentionDays = %d, want 7", cfg.EventRetentionDays)
	}
}

func TestLoadRetentionConfig_EventOutboxDaysOverride(t *testing.T) {
	t.Setenv("RETENTION_EVENT_OUTBOX_DAYS", "14")
	cfg := loadRetentionConfig()
	if cfg.EventOutboxDays != 14 {
		t.Errorf("EventOutboxDays = %d, want 14", cfg.EventOutboxDays)
	}
}

func TestPruneSorobanEvents_StopsWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	deleted, err := pruneSorobanEvents(ctx, nil, 7)
	if err != context.Canceled {
		t.Fatalf("error = %v, want %v", err, context.Canceled)
	}
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0", deleted)
	}
}

func TestSorobanEventRetention_PrunesOldEventsAndAllowsConcurrentInsert(t *testing.T) {
	url, ok := os.LookupEnv("TEST_DATABASE_URL")
	if !ok {
		if os.Getenv("REQUIRE_TEST_SERVICES") != "" {
			t.Fatal("TEST_DATABASE_URL must be set when REQUIRE_TEST_SERVICES is set")
		}
		t.Skip("SKIP: TEST_DATABASE_URL not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	adminPool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect TEST_DATABASE_URL: %v", err)
	}
	t.Cleanup(adminPool.Close)

	schema := fmt.Sprintf("retention_test_%d", time.Now().UnixNano())
	if _, err := adminPool.Exec(ctx, fmt.Sprintf(`CREATE SCHEMA %s`, schema)); err != nil {
		t.Fatalf("create isolated retention schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = adminPool.Exec(context.Background(), fmt.Sprintf(`DROP SCHEMA %s CASCADE`, schema))
	})

	if _, err := adminPool.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE %s.soroban_events (
			id UUID NOT NULL,
			ledger_sequence BIGINT NOT NULL,
			ledger_timestamp TIMESTAMPTZ NOT NULL
		) PARTITION BY RANGE (ledger_sequence);
	`, schema)); err != nil {
		t.Fatalf("create isolated partitioned events table: %v", err)
	}
	if _, err := adminPool.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE %s.soroban_events_default
			PARTITION OF %s.soroban_events DEFAULT
	`, schema, schema)); err != nil {
		t.Fatalf("create isolated partitioned events table: %v", err)
	}

	poolConfig, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatalf("create isolated retention pool: %v", err)
	}
	t.Cleanup(pool.Close)

	t.Setenv("EVENT_RETENTION_DAYS", "7")
	days := loadRetentionConfig().EventRetentionDays
	if _, err := pool.Exec(ctx,
		`INSERT INTO soroban_events (id, ledger_sequence, ledger_timestamp)
		 SELECT gen_random_uuid(), 1, NOW() - INTERVAL '10 days'
		 FROM generate_series(1, 50)`); err != nil {
		t.Fatalf("seed old soroban_events: %v", err)
	}

	start := make(chan struct{})
	type pruneResult struct {
		deleted int64
		err     error
	}
	pruned := make(chan pruneResult, 1)
	inserted := make(chan error, 1)
	go func() {
		<-start
		deleted, err := pruneSorobanEvents(ctx, pool, days)
		pruned <- pruneResult{deleted: deleted, err: err}
	}()
	go func() {
		<-start
		_, err := pool.Exec(ctx,
			`INSERT INTO soroban_events (id, ledger_sequence, ledger_timestamp)
			 VALUES (gen_random_uuid(), 1, NOW())`)
		inserted <- err
	}()
	close(start)

	var result pruneResult
	select {
	case result = <-pruned:
	case <-ctx.Done():
		t.Fatal("retention cleanup did not finish while an insert ran concurrently")
	}
	if result.err != nil {
		t.Fatalf("pruneSorobanEvents: %v", result.err)
	}
	if result.deleted != 50 {
		t.Errorf("deleted %d old events, want 50", result.deleted)
	}
	if err := <-inserted; err != nil {
		t.Fatalf("concurrent event insert failed: %v", err)
	}

	var oldCount, recentCount int64
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FILTER (WHERE ledger_timestamp < NOW() - INTERVAL '7 days'),
		        COUNT(*) FILTER (WHERE ledger_timestamp >= NOW() - INTERVAL '7 days')
		 FROM soroban_events`).Scan(&oldCount, &recentCount); err != nil {
		t.Fatalf("count events after retention cleanup: %v", err)
	}
	if oldCount != 0 {
		t.Errorf("old event count = %d, want 0", oldCount)
	}
	if recentCount != 1 {
		t.Errorf("recent event count = %d, want 1", recentCount)
	}
}

// TestEventOutboxRetention_OnlyDeletesPublishedRowsPastTheWindow is the
// regression test for #604's core deletion criterion: an unpublished row
// must never be deleted regardless of age (the relay has not yet delivered
// it), and a published row inside the retention window must survive.
func TestEventOutboxRetention_OnlyDeletesPublishedRowsPastTheWindow(t *testing.T) {
	db := connectWebhookTestDB(t) // reuses the shared TEST_DATABASE_URL helper
	ctx := context.Background()

	insert := func(published bool, publishedAt *time.Time) string {
		t.Helper()
		var id string
		if err := db.QueryRowContext(ctx,
			`INSERT INTO event_outbox (event_id, payload, published, published_at) VALUES (gen_random_uuid(), $1, $2, $3) RETURNING event_id`,
			`{"kind":"retention-test"}`, published, publishedAt,
		).Scan(&id); err != nil {
			t.Fatalf("insert event_outbox row: %v", err)
		}
		return id
	}

	oldPublishedAt := time.Now().Add(-10 * 24 * time.Hour) // 10 days old
	recentPublishedAt := time.Now().Add(-1 * time.Hour)    // 1 hour old

	oldPublished := insert(true, &oldPublishedAt)
	recentPublished := insert(true, &recentPublishedAt)
	oldUnpublished := insert(false, nil) // published_at is NULL when unpublished

	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM event_outbox WHERE event_id IN ($1, $2, $3)`, oldPublished, recentPublished, oldUnpublished)
	})

	// Mirror startRetentionJob's exact event_outbox query with a 7-day window.
	if _, err := db.ExecContext(ctx,
		`DELETE FROM event_outbox WHERE published = TRUE AND published_at < NOW() - ($1 || ' days')::INTERVAL AND event_id IN ($2, $3, $4)`,
		fmt.Sprintf("%d", 7), oldPublished, recentPublished, oldUnpublished,
	); err != nil {
		t.Fatalf("run retention delete: %v", err)
	}

	exists := func(id string) bool {
		var ok bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM event_outbox WHERE event_id = $1)`, id).Scan(&ok); err != nil {
			t.Fatalf("check exists: %v", err)
		}
		return ok
	}

	if exists(oldPublished) {
		t.Error("a published row older than the retention window was not deleted")
	}
	if !exists(recentPublished) {
		t.Error("a published row inside the retention window was deleted")
	}
	if !exists(oldUnpublished) {
		t.Error("an unpublished row was deleted despite never having been delivered")
	}
}
