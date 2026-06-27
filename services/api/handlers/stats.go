package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/Depo-dev/trident/services/api/internal/httputil"
	"github.com/Depo-dev/trident/services/api/validation"
	"github.com/redis/go-redis/v9"
)

// ContractStats represents a single contract's activity metrics
type ContractStats struct {
	ContractID       string    `json:"contract_id"`
	EventCount       int64     `json:"event_count"`
	LastSeenLedger   int64     `json:"last_seen_ledger"`
	LastSeenAt       string    `json:"last_seen_at"`
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
func ContractsStats(db *sql.DB, rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if db == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, httputil.INTERNAL, "database unavailable")
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
			httputil.WriteError(w, http.StatusBadRequest, httputil.INVALID_ARGUMENT, verr.Message)
			return
		}

		// Build cache key
		cacheKey := cacheKeyContractsStats(params.Network, params.FromLedger, params.ToLedger, params.Limit)

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
			httputil.WriteError(w, http.StatusInternalServerError, httputil.INTERNAL, "failed to fetch statistics")
			return
		}

		// Get the latest ledger for the response metadata if to_ledger was not explicitly set
		toLedger := params.ToLedger
		if q.Get("to_ledger") == "" {
			latestLedger, err := getLatestLedger(ctx, db)
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
			httputil.WriteError(w, http.StatusInternalServerError, httputil.INTERNAL, "internal error")
			return
		}

		// Cache in Redis for 60 seconds (best-effort; ignore errors)
		_ = rdb.SetEX(r.Context(), cacheKey, body, 60*time.Second).Err()

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("X-Cache", "MISS")
		_, _ = w.Write(body)
	}
}

// queryContractStats executes the aggregation query against the database.
// Requires compound index (contract_id, ledger_sequence DESC) from #61.
func queryContractStats(ctx context.Context, db *sql.DB, params *validation.QueryStatsParams) ([]*ContractStats, error) {
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

	rows, err := db.QueryContext(ctx, query, params.Network, params.FromLedgerPtr, params.ToLedgerPtr, params.Limit)
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

// getLatestLedger queries the database for the highest indexed ledger sequence.
func getLatestLedger(ctx context.Context, db *sql.DB) (int64, error) {
	var latest int64
	err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(ledger_sequence), 0) FROM soroban_events").Scan(&latest)
	return latest, err
}

// cacheKeyContractsStats builds the Redis cache key for contract stats.
func cacheKeyContractsStats(network string, fromLedger, toLedger, limit int64) string {
	return "stats:contracts:" + network + ":" + strconv.FormatInt(fromLedger, 10) + ":" + strconv.FormatInt(toLedger, 10) + ":" + strconv.FormatInt(limit, 10)
}
