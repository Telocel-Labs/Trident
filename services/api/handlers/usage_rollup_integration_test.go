package handlers_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Depo-dev/trident/services/api/handlers"
	"github.com/jackc/pgx/v5/pgxpool"
)

// connectRealTestDB connects to TEST_DATABASE_URL, the same convention the
// Rust crates use for DB-backed integration tests (see
// crates/indexer/src/db/mod.rs): skip when unset, but hard-fail instead of
// silently skipping when REQUIRE_TEST_SERVICES is set, so a misconfigured CI
// job can't go green without actually running this test.
func connectRealTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url, ok := os.LookupEnv("TEST_DATABASE_URL")
	if !ok {
		if os.Getenv("REQUIRE_TEST_SERVICES") != "" {
			t.Fatal("TEST_DATABASE_URL must be set when REQUIRE_TEST_SERVICES is set")
		}
		t.Skip("SKIP: TEST_DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect TEST_DATABASE_URL: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestRollupUsage_MatchesRawAuditCounts is the acceptance-criteria test:
// after RollupUsage runs, usage_rollup's per-day totals for a key must equal
// what you'd get by counting the raw audit_log rows directly.
func TestRollupUsage_MatchesRawAuditCounts(t *testing.T) {
	pool := connectRealTestDB(t)
	ctx := context.Background()

	var keyID string
	err := pool.QueryRow(ctx,
		`INSERT INTO api_keys (key_hash, key_prefix, label) VALUES ($1, $2, $3) RETURNING id`,
		fmt.Sprintf("test-usage-rollup-hash-%d", time.Now().UnixNano()),
		"test-prefix",
		"usage-rollup-test",
	).Scan(&keyID)
	if err != nil {
		t.Fatalf("insert test api key: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM usage_rollup WHERE api_key_id = $1`, keyID)
		_, _ = pool.Exec(ctx, `DELETE FROM audit_log WHERE api_key_id = $1`, keyID)
		_, _ = pool.Exec(ctx, `DELETE FROM api_keys WHERE id = $1`, keyID)
	})

	// A fixed day well in the past so it can never collide with rows other
	// tests or production traffic might be writing "now".
	day := time.Date(2020, 1, 15, 0, 0, 0, 0, time.UTC)
	statuses := []int{200, 200, 404, 500}
	for i, status := range statuses {
		_, err := pool.Exec(ctx,
			`INSERT INTO audit_log (api_key_id, endpoint, method, status_code, duration_ms, request_id, ts)
			 VALUES ($1, '/v1/events', 'GET', $2, $3, $4, $5)`,
			keyID, status, 10*(i+1), fmt.Sprintf("usage-rollup-test-req-%d", i), day.Add(time.Duration(i)*time.Hour),
		)
		if err != nil {
			t.Fatalf("insert audit_log row %d: %v", i, err)
		}
	}

	if err := handlers.RollupUsage(ctx, pool, day.Add(-time.Hour)); err != nil {
		t.Fatalf("RollupUsage failed: %v", err)
	}

	var rawCount, rawErrors int64
	err = pool.QueryRow(ctx,
		`SELECT COUNT(*), COUNT(*) FILTER (WHERE status_code >= 400) FROM audit_log WHERE api_key_id = $1`,
		keyID,
	).Scan(&rawCount, &rawErrors)
	if err != nil {
		t.Fatalf("query raw audit_log counts: %v", err)
	}
	if rawCount != int64(len(statuses)) {
		t.Fatalf("test setup sanity check failed: expected %d raw rows, got %d", len(statuses), rawCount)
	}

	var rollupCount, rollupErrors int64
	err = pool.QueryRow(ctx,
		`SELECT request_count, error_count FROM usage_rollup WHERE api_key_id = $1 AND period_start = $2`,
		keyID, day,
	).Scan(&rollupCount, &rollupErrors)
	if err != nil {
		t.Fatalf("query usage_rollup: %v", err)
	}

	if rollupCount != rawCount {
		t.Errorf("usage_rollup.request_count = %d, want %d (raw audit_log count)", rollupCount, rawCount)
	}
	if rollupErrors != rawErrors {
		t.Errorf("usage_rollup.error_count = %d, want %d (raw audit_log error count)", rollupErrors, rawErrors)
	}

	// Re-running RollupUsage must be idempotent: totals stay the same rather
	// than double-counting.
	if err := handlers.RollupUsage(ctx, pool, day.Add(-time.Hour)); err != nil {
		t.Fatalf("second RollupUsage call failed: %v", err)
	}
	var rollupCount2 int64
	err = pool.QueryRow(ctx,
		`SELECT request_count FROM usage_rollup WHERE api_key_id = $1 AND period_start = $2`,
		keyID, day,
	).Scan(&rollupCount2)
	if err != nil {
		t.Fatalf("query usage_rollup after second run: %v", err)
	}
	if rollupCount2 != rawCount {
		t.Errorf("usage_rollup.request_count after re-run = %d, want %d (idempotent upsert)", rollupCount2, rawCount)
	}
}
