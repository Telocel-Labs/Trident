package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---------------------------------------------------------------------------
// Lightweight Prometheus gauge registry (no external dependency)
// ---------------------------------------------------------------------------

type atomicGauge struct {
	bits atomic.Uint64 // stores float64 bits
}

func (g *atomicGauge) Set(v float64) {
	g.bits.Store(math.Float64bits(v))
}

func (g *atomicGauge) Get() float64 {
	return math.Float64frombits(g.bits.Load())
}

var (
	metricLagLedgers        atomicGauge
	metricLastPollTimestamp atomicGauge
	metricEventsTotal       atomicGauge
)

// MetricsHandler exposes the three Prometheus gauges in text format.
// Mount at GET /metrics (or /v1/metrics).
func MetricsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprintf(w, "# HELP trident_indexer_lag_ledgers Number of ledgers the indexer is behind the chain tip.\n")
		fmt.Fprintf(w, "# TYPE trident_indexer_lag_ledgers gauge\n")
		fmt.Fprintf(w, "trident_indexer_lag_ledgers %g\n", metricLagLedgers.Get())
		fmt.Fprintf(w, "# HELP trident_indexer_last_poll_timestamp_seconds Unix timestamp of the last successful indexer poll.\n")
		fmt.Fprintf(w, "# TYPE trident_indexer_last_poll_timestamp_seconds gauge\n")
		fmt.Fprintf(w, "trident_indexer_last_poll_timestamp_seconds %g\n", metricLastPollTimestamp.Get())
		fmt.Fprintf(w, "# HELP trident_indexer_events_total Cumulative events indexed.\n")
		fmt.Fprintf(w, "# TYPE trident_indexer_events_total gauge\n")
		fmt.Fprintf(w, "trident_indexer_events_total %g\n", metricEventsTotal.Get())
	}
}

// ---------------------------------------------------------------------------
// Chain-tip cache (10-second TTL, 2-second RPC timeout, null on failure)
// ---------------------------------------------------------------------------

type chainTipCache struct {
	mu        sync.Mutex
	ledger    *int64
	fetchedAt time.Time
}

var globalChainTipCache chainTipCache

func (c *chainTipCache) get(ctx context.Context) *int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ledger != nil && time.Since(c.fetchedAt) < 10*time.Second {
		return c.ledger
	}

	rpcURL := os.Getenv("STELLAR_RPC_URL")
	if rpcURL == "" {
		return nil
	}

	fetchCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	ledger := fetchLatestLedger(fetchCtx, rpcURL)
	if ledger != nil {
		c.ledger = ledger
		c.fetchedAt = time.Now()
	}
	return ledger
}

func fetchLatestLedger(ctx context.Context, rpcURL string) *int64 {
	body := bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"getLatestLedger"}`))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL, body)
	if err != nil {
		slog.Debug("stats: build RPC request", "err", err)
		return nil
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Debug("stats: RPC call failed", "err", err)
		return nil
	}
	defer resp.Body.Close()

	var result struct {
		Result struct {
			Sequence int64 `json:"sequence"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		slog.Debug("stats: RPC decode failed", "err", err)
		return nil
	}
	if result.Result.Sequence == 0 {
		return nil
	}
	seq := result.Result.Sequence
	return &seq
}

// ---------------------------------------------------------------------------
// DB query
// ---------------------------------------------------------------------------

type indexerStatsRow struct {
	lastLedgerIndexed  *int64
	eventsIndexedTotal *int64
	eventsLastPoll     *int64
	pollDurationMs     *int64
	lastPollAt         *time.Time
}

func queryIndexerStats(ctx context.Context, db DBPool) (indexerStatsRow, error) {
	var row indexerStatsRow
	err := db.QueryRow(ctx,
		`SELECT last_ledger_indexed,
		        events_indexed_total,
		        events_in_last_poll,
		        poll_duration_ms,
		        last_poll_at
		   FROM system_state
		  WHERE key = 'latest_ledger_cursor'`,
	).Scan(
		&row.lastLedgerIndexed,
		&row.eventsIndexedTotal,
		&row.eventsLastPoll,
		&row.pollDurationMs,
		&row.lastPollAt,
	)
	if err != nil && err != pgx.ErrNoRows {
		return row, err
	}
	return row, nil
}

// ---------------------------------------------------------------------------
// Status logic
// ---------------------------------------------------------------------------

func indexerStatus(lastPollAt *time.Time, lagLedgers *int64) string {
	if lastPollAt == nil || time.Since(*lastPollAt) > 60*time.Second {
		return "stalled"
	}
	if lagLedgers != nil && *lagLedgers > 10 {
		return "lagging"
	}
	return "healthy"
}

// ---------------------------------------------------------------------------
// Response
// ---------------------------------------------------------------------------

// IndexerStatsResponse is the JSON body for GET /v1/stats/indexer.
type IndexerStatsResponse struct {
	LastLedgerIndexed  *int64  `json:"last_ledger_indexed"`
	ChainTipLedger     *int64  `json:"chain_tip_ledger"`
	LagLedgers         *int64  `json:"lag_ledgers"`
	EventsIndexedTotal *int64  `json:"events_indexed_total"`
	EventsLastPoll     *int64  `json:"events_last_poll"`
	AvgPollDurationMs  *int64  `json:"avg_poll_duration_ms"`
	LastPollAt         *string `json:"last_poll_at"`
	Status             string  `json:"status"`
	Network            string  `json:"network"`
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// IndexerStats handles GET /v1/stats/indexer.
//
// Returns real-time indexer health and throughput metrics sourced from
// system_state. Chain tip is fetched from STELLAR_RPC_URL with a 2-second
// timeout and cached for 10 seconds; it is null on RPC failure. HTTP 503 is
// returned only when status == "stalled". No API key is required.
func IndexerStats(db DBPool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if db == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "database unavailable"})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		stats, err := queryIndexerStats(ctx, db)
		if err != nil {
			slog.Error("stats: DB query failed", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "database query failed"})
			return
		}

		chainTip := globalChainTipCache.get(r.Context())

		var lagLedgers *int64
		if chainTip != nil && stats.lastLedgerIndexed != nil {
			lag := *chainTip - *stats.lastLedgerIndexed
			lagLedgers = &lag
		}

		status := indexerStatus(stats.lastPollAt, lagLedgers)

		// Update Prometheus gauges with latest observed values.
		if lagLedgers != nil {
			metricLagLedgers.Set(float64(*lagLedgers))
		}
		if stats.lastPollAt != nil {
			metricLastPollTimestamp.Set(float64(stats.lastPollAt.Unix()))
		}
		if stats.eventsIndexedTotal != nil {
			metricEventsTotal.Set(float64(*stats.eventsIndexedTotal))
		}

		var lastPollAtStr *string
		if stats.lastPollAt != nil {
			s := stats.lastPollAt.UTC().Format(time.RFC3339)
			lastPollAtStr = &s
		}

		resp := IndexerStatsResponse{
			LastLedgerIndexed:  stats.lastLedgerIndexed,
			ChainTipLedger:     chainTip,
			LagLedgers:         lagLedgers,
			EventsIndexedTotal: stats.eventsIndexedTotal,
			EventsLastPoll:     stats.eventsLastPoll,
			AvgPollDurationMs:  stats.pollDurationMs,
			LastPollAt:         lastPollAtStr,
			Status:             status,
			Network:            os.Getenv("NETWORK"),
		}

		httpStatus := http.StatusOK
		if status == "stalled" {
			httpStatus = http.StatusServiceUnavailable
		}
		writeJSON(w, httpStatus, resp)
	}
}