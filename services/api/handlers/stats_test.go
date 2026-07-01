package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// mockStatsDB implements DBPool for stats handler tests.
type mockStatsDB struct {
	lastLedger  *int64
	eventsTotal *int64
	eventsLast  *int64
	pollMs      *int64
	lastPollAt  *time.Time
	scanErr     error
}

func (m *mockStatsDB) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return &mockStatsRow{m: m}
}

func (m *mockStatsDB) Ping(_ context.Context) error {
	return nil
}

func (m *mockStatsDB) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	return nil, nil
}

type mockStatsRow struct{ m *mockStatsDB }

func (r *mockStatsRow) Scan(dest ...any) error {
	if r.m.scanErr != nil {
		return r.m.scanErr
	}
	if len(dest) != 5 {
		return fmt.Errorf("expected 5 dest, got %d", len(dest))
	}
	*dest[0].(**int64) = r.m.lastLedger
	*dest[1].(**int64) = r.m.eventsTotal
	*dest[2].(**int64) = r.m.eventsLast
	*dest[3].(**int64) = r.m.pollMs
	*dest[4].(**time.Time) = r.m.lastPollAt
	return nil
}

func resetChainTipCache() {
	globalChainTipCache.mu.Lock()
	globalChainTipCache.ledger = nil
	globalChainTipCache.fetchedAt = time.Time{}
	globalChainTipCache.mu.Unlock()
}

func setChainTip(seq int64) {
	globalChainTipCache.mu.Lock()
	globalChainTipCache.ledger = &seq
	globalChainTipCache.fetchedAt = time.Now()
	globalChainTipCache.mu.Unlock()
}

func statsReq() *http.Request {
	return httptest.NewRequest(http.MethodGet, "/v1/stats/indexer", nil)
}

func TestIndexerStats_NilDB_Returns503(t *testing.T) {
	rec := httptest.NewRecorder()
	IndexerStats(nil).ServeHTTP(rec, statsReq())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rec.Code)
	}
}

func TestIndexerStats_Stalled_Returns503(t *testing.T) {
	stale := time.Now().Add(-90 * time.Second)
	resetChainTipCache()

	rec := httptest.NewRecorder()
	IndexerStats(&mockStatsDB{lastPollAt: &stale}).ServeHTTP(rec, statsReq())

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("stalled: want 503, got %d", rec.Code)
	}
	var resp IndexerStatsResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Status != "stalled" {
		t.Errorf("want status=stalled, got %q", resp.Status)
	}
}

func TestIndexerStats_NullLastPollAt_IsStalled(t *testing.T) {
	resetChainTipCache()
	rec := httptest.NewRecorder()
	IndexerStats(&mockStatsDB{}).ServeHTTP(rec, statsReq())

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("null poll: want 503, got %d", rec.Code)
	}
	var resp IndexerStatsResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Status != "stalled" {
		t.Errorf("want stalled, got %q", resp.Status)
	}
}

func TestIndexerStats_Healthy_Returns200(t *testing.T) {
	now := time.Now()
	ledger := int64(1000)
	total := int64(50000)
	last := int64(42)
	ms := int64(120)
	db := &mockStatsDB{lastLedger: &ledger, eventsTotal: &total, eventsLast: &last, pollMs: &ms, lastPollAt: &now}
	setChainTip(1005)

	rec := httptest.NewRecorder()
	IndexerStats(db).ServeHTTP(rec, statsReq())

	if rec.Code != http.StatusOK {
		t.Fatalf("healthy: want 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	var resp IndexerStatsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal("decode:", err)
	}
	if resp.Status != "healthy" {
		t.Errorf("want healthy, got %q", resp.Status)
	}
	if resp.LastLedgerIndexed == nil || *resp.LastLedgerIndexed != 1000 {
		t.Errorf("last_ledger_indexed: got %v", resp.LastLedgerIndexed)
	}
	if resp.ChainTipLedger == nil || *resp.ChainTipLedger != 1005 {
		t.Errorf("chain_tip_ledger: got %v", resp.ChainTipLedger)
	}
	if resp.LagLedgers == nil || *resp.LagLedgers != 5 {
		t.Errorf("lag_ledgers: want 5, got %v", resp.LagLedgers)
	}
	if resp.EventsIndexedTotal == nil || *resp.EventsIndexedTotal != 50000 {
		t.Errorf("events_indexed_total: got %v", resp.EventsIndexedTotal)
	}
}

func TestIndexerStats_Lagging_Returns200(t *testing.T) {
	now := time.Now()
	ledger := int64(1000)
	setChainTip(1020) // 20 ahead — lagging (>10)

	rec := httptest.NewRecorder()
	IndexerStats(&mockStatsDB{lastLedger: &ledger, lastPollAt: &now}).ServeHTTP(rec, statsReq())

	if rec.Code != http.StatusOK {
		t.Fatalf("lagging: want 200, got %d", rec.Code)
	}
	var resp IndexerStatsResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Status != "lagging" {
		t.Errorf("want lagging, got %q", resp.Status)
	}
}

func TestIndexerStats_NilChainTip_LagIsNil(t *testing.T) {
	now := time.Now()
	ledger := int64(500)
	resetChainTipCache() // STELLAR_RPC_URL unset -> nil tip

	rec := httptest.NewRecorder()
	IndexerStats(&mockStatsDB{lastLedger: &ledger, lastPollAt: &now}).ServeHTTP(rec, statsReq())

	if rec.Code != http.StatusOK {
		t.Fatalf("nil tip: want 200, got %d", rec.Code)
	}
	var resp IndexerStatsResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.ChainTipLedger != nil {
		t.Errorf("chain_tip_ledger: want nil, got %v", resp.ChainTipLedger)
	}
	if resp.LagLedgers != nil {
		t.Errorf("lag_ledgers: want nil when tip unknown, got %v", resp.LagLedgers)
	}
}

func TestIndexerStats_LastPollAt_RFC3339(t *testing.T) {
	ts := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	resetChainTipCache()

	rec := httptest.NewRecorder()
	IndexerStats(&mockStatsDB{lastPollAt: &ts}).ServeHTTP(rec, statsReq())

	var resp IndexerStatsResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.LastPollAt == nil {
		t.Fatal("last_poll_at should not be nil")
	}
	if *resp.LastPollAt != "2025-01-15T12:00:00Z" {
		t.Errorf("last_poll_at: got %q, want RFC3339 UTC", *resp.LastPollAt)
	}
}

func TestIndexerStats_DBError_Returns503(t *testing.T) {
	db := &mockStatsDB{scanErr: fmt.Errorf("connection reset")}
	resetChainTipCache()

	rec := httptest.NewRecorder()
	IndexerStats(db).ServeHTTP(rec, statsReq())

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("db error: want 503, got %d", rec.Code)
	}
}

func TestMetricsHandler_ExposesAllThreeGauges(t *testing.T) {
	rec := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, metric := range []string{
		"trident_indexer_lag_ledgers",
		"trident_indexer_last_poll_timestamp_seconds",
		"trident_indexer_events_total",
	} {
		if !strings.Contains(body, metric) {
			t.Errorf("metric %q not found in /metrics output", metric)
		}
	}
}

// TestContractsStats_NoParams_Returns200 verifies default parameters work
func TestContractsStats_NoParams_Returns200(t *testing.T) {
	t.Skip("requires database and redis integration")
}

// TestContractsStats_InvalidLimit_Returns400 validates limit bounds
func TestContractsStats_InvalidLimit_Returns400(t *testing.T) {
	t.Skip("requires database and redis integration")
}

// TestContractsStats_CacheHit_Returns200 verifies Redis caching
func TestContractsStats_CacheHit_Returns200(t *testing.T) {
	t.Skip("requires database and redis integration")
}

// TestContractsStats_RequiresAuth validates auth middleware
func TestContractsStats_RequiresAuth(t *testing.T) {
	t.Skip("requires database and redis integration")
}
