package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// newUnconnectedPool builds a pool that never successfully connects (bogus
// port) but is otherwise fully constructed, so Stat() is safe to call
// without a live database — pgxpool dials lazily/in the background and
// Stat() reflects whatever state exists at call time.
func newUnconnectedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig("postgres://user:pass@127.0.0.1:1/testdb")
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestPollDBPool_ReportsStatImmediately verifies PollDBPool populates the
// gauges from an initial report before the first tick (issue #238).
func TestPollDBPool_ReportsStatImmediately(t *testing.T) {
	pool := newUnconnectedPool(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		PollDBPool(ctx, pool, time.Hour) // long interval — only the immediate report matters here
		close(done)
	}()

	// Give the immediate report a moment to run.
	time.Sleep(50 * time.Millisecond)

	if got := testutil.ToFloat64(DBPoolMaxConns); got != 4 {
		t.Errorf("trident_db_pool_max_conns: want 4, got %v", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("PollDBPool did not return after context cancellation")
	}
}

// TestPollDBPool_StopsOnContextCancel verifies the polling loop exits
// promptly when ctx is done, rather than leaking a goroutine.
func TestPollDBPool_StopsOnContextCancel(t *testing.T) {
	pool := newUnconnectedPool(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		PollDBPool(ctx, pool, 10*time.Millisecond)
		close(done)
	}()

	// Let a few ticks happen, then cancel.
	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("PollDBPool did not stop after context cancellation")
	}
}
