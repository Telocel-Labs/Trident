package middleware

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Depo-dev/trident/services/api/internal/metrics"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestAuditWriter_RetryOnDBOutage_NoEntriesLost verifies issue #800:
// A DB outage shorter than the retry budget loses no audit entries,
// and flush failure metrics count each failed attempt.
func TestAuditWriter_RetryOnDBOutage_NoEntriesLost(t *testing.T) {
	beforeFailures := testutil.ToFloat64(metrics.AuditLogFlushFailuresTotal)
	beforeDropped := testutil.ToFloat64(metrics.AuditLogDroppedTotal)

	var attempts int32
	var receivedMu sync.Mutex
	var received []AuditEntry

	aw := &AuditWriter{
		ch:     make(chan AuditEntry, 100),
		logger: newTestLogger(),
	}

	// Fail the first 2 attempts, succeed on the 3rd attempt
	aw.batchInserter = func(ctx context.Context, batch []AuditEntry) error {
		n := atomic.AddInt32(&attempts, 1)
		if n <= 2 {
			return errors.New("simulated transient db connection error")
		}
		receivedMu.Lock()
		defer receivedMu.Unlock()
		received = append(received, batch...)
		return nil
	}

	aw.wg.Add(1)
	go aw.run(20*time.Millisecond, 10)

	// Write 5 entries
	for i := 0; i < 5; i++ {
		aw.Write(AuditEntry{
			Endpoint:   "/v1/events",
			Method:     "GET",
			StatusCode: 200,
		})
	}

	// Wait enough time for at least 3 flush ticks
	time.Sleep(150 * time.Millisecond)
	aw.Close()

	receivedMu.Lock()
	gotCount := len(received)
	receivedMu.Unlock()

	if gotCount != 5 {
		t.Fatalf("expected 5 entries written after retry, got %d", gotCount)
	}

	deltaFailures := testutil.ToFloat64(metrics.AuditLogFlushFailuresTotal) - beforeFailures
	if deltaFailures < 2 {
		t.Errorf("expected at least 2 flush failures counted, got %v", deltaFailures)
	}

	deltaDropped := testutil.ToFloat64(metrics.AuditLogDroppedTotal) - beforeDropped
	if deltaDropped != 0 {
		t.Errorf("expected 0 entries dropped during transient outage, got %v", deltaDropped)
	}
}

// TestAuditWriter_DroppedMetricsOnChannelFull verifies drop metrics when channel capacity is exceeded.
func TestAuditWriter_DroppedMetricsOnChannelFull(t *testing.T) {
	beforeDropped := testutil.ToFloat64(metrics.AuditLogDroppedTotal)

	aw := &AuditWriter{
		ch:     make(chan AuditEntry, 1),
		logger: newTestLogger(),
	}

	// Put 1 entry into channel to fill it
	aw.Write(AuditEntry{Endpoint: "/fits"})

	// These two should be dropped and count towards AuditLogDroppedTotal
	aw.Write(AuditEntry{Endpoint: "/dropped-1"})
	aw.Write(AuditEntry{Endpoint: "/dropped-2"})

	delta := testutil.ToFloat64(metrics.AuditLogDroppedTotal) - beforeDropped
	if delta != 2 {
		t.Fatalf("expected drop count delta = 2, got %v", delta)
	}

	aw.Close()
}
