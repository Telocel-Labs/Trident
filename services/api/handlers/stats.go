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

	"github.com/Depo-dev/trident/services/api/validation"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
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


// ---------------------------------------------------------------------------
// Contract Analytics Endpoint
// ---------------------------------------------------------------------------

// ContractStats represents a single contract's activity metrics
type ContractStats struct {
	ContractID     string `json:"contract_id"`
	EventCount     int64  `json:"event_count"`
	LastSeenLedger int64  `json:"last_seen_ledger"`
	LastSeenAt     string `json:"last_seen_at"`
}

// ContractsStatsResponse is the JSON response for GET /v1/stats/contracts
type ContractsStatsResponse struct {
	Contracts  []*ContractStats `json:"contracts"`
	FromLedger int64            `json:"from_ledger"`
	ToLedger   int64            `json:"to_ledger"`
	Network    string           `json:"network"`
	GeneratedAt string          `json:"generated_at"`
}

// ContractsStats handles GET /v1/stats/contracts (analytics endpoint).
//
// Query parameters:
//   - from_ledger (optional): lower bound, inclusive. Default: 0 (all time)
//   - to_ledger (optional): upper bound, inclusive. Default: latest indexed ledger
//   - network (optional): "testnet" or "mainnet". Default: "testnet"
//   - limit (optional): 1-100, number of contracts to return. Default: 50
//
// Response is cached in Redis for 60 seconds using key:
// stats:contracts:{network}:{from}:{to}:{limit}
//
// Returns results ordered by event_count DESC (highest volume first).
func ContractsStats(db DBPool, rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if db == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "database unavailable"})
			return
		}

		q := r.URL.Query()

		// Validate and parse query parameters
		params, verr := validation.ValidateQueryStats(
			q.Get("from_ledger"),
			q.Get("to_ledger"),
			q.Get("network"),
			q.Get("limit"),
		)
		if verr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": verr.Message})
			return
		}

		// Build cache key
		cacheKey := fmt.Sprintf("stats:contracts:%s:%d:%d:%d", params.Network, params.FromLedger, params.ToLedger, params.Limit)

		// Try Redis cache first
		if cached, err := rdb.Get(r.Context(), cacheKey).Result(); err == nil {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "public, max-age=60")
			w.Header().Set("X-Cache", "HIT")
			_, _ = w.Write([]byte(cached))
			return
		} else if err != redis.Nil {
			// Log Redis error but continue (cache is best-effort)
			slog.ErrorContext(r.Context(), "redis cache get failed", "err", err)
		}

		// Query the database
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		stats, err := queryContractStats(ctx, db, params)
		if err != nil {
			slog.ErrorContext(r.Context(), "database query failed", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to fetch statistics"})
			return
		}

		// Get the latest ledger for the response metadata if to_ledger was not explicitly set
		toLedger := params.ToLedger
		if q.Get("to_ledger") == "" {
			latestLedger, err := getLatestIndexedLedger(ctx, db)
			if err != nil {
				slog.ErrorContext(r.Context(), "failed to get latest ledger", "err", err)
				// Continue anyway; use 0 as fallback
				latestLedger = 0
			}
			toLedger = latestLedger
		}

		response := &ContractsStatsResponse{
			Contracts:   stats,
			FromLedger:  params.FromLedger,
			ToLedger:    toLedger,
			Network:     params.Network,
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		}

		// Marshal to JSON for caching and response
		body, err := json.Marshal(response)
		if err != nil {
			slog.ErrorContext(r.Context(), "json marshal failed", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal error"})
			return
		}

		// Cache in Redis for 60 seconds (best-effort; ignore errors)
		_ = rdb.Set(r.Context(), cacheKey, body, 60*time.Second).Err()

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("X-Cache", "MISS")
		_, _ = w.Write(body)
	}
}

// queryContractStats executes the aggregation query against the database.
// Requires compound index (contract_id, ledger_sequence DESC) from #61.
func queryContractStats(ctx context.Context, db DBPool, params *validation.QueryStatsParams) ([]*ContractStats, error) {
	query := `
	SELECT
		contract_id,
		COUNT(*) AS event_count,
		MAX(ledger_sequence) AS last_seen_ledger,
		MAX(ledger_timestamp) AS last_seen_at
	FROM soroban_events
	WHERE
		network = $1
		AND ($2::BIGINT IS NULL OR ledger_sequence >= $2)
		AND ($3::BIGINT IS NULL OR ledger_sequence <= $3)
	GROUP BY contract_id
	ORDER BY event_count DESC
	LIMIT $4
	`

	rows, err := db.Query(ctx, query, params.Network, params.FromLedgerPtr, params.ToLedgerPtr, params.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stats []*ContractStats
	for rows.Next() {
		var cs ContractStats
		var lastSeenAt time.Time

		err := rows.Scan(
			&cs.ContractID,
			&cs.EventCount,
			&cs.LastSeenLedger,
			&lastSeenAt,
		)
		if err != nil {
			return nil, err
		}

		cs.LastSeenAt = lastSeenAt.UTC().Format(time.RFC3339)
		stats = append(stats, &cs)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	return stats, nil
}

// getLatestIndexedLedger queries the database for the highest indexed ledger sequence.
func getLatestIndexedLedger(ctx context.Context, db DBPool) (int64, error) {
	var latest int64
	err := db.QueryRow(ctx, "SELECT COALESCE(MAX(ledger_sequence), 0) FROM soroban_events").Scan(&latest)
	return latest, err
}
