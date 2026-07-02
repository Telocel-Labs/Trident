package handlers

import (
	"context"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

var processStartTime = time.Now()

// StatusResponse is the JSON response for GET /internal/status.
type StatusResponse struct {
	IndexerLagLedgers  int64 `json:"indexer_lag_ledgers"`
	ActiveWSClients    int   `json:"active_ws_clients"`
	RedisStreamDepth   int64 `json:"redis_stream_depth"`
	DBPoolOpenConns    int32 `json:"db_pool_open_conns"`
	ParseErrors24h     int64 `json:"parse_errors_24h"`
	UptimeSeconds      int64 `json:"uptime_seconds"`
}

// internalStatusDeps wraps dependencies for the status handler.
type internalStatusDeps struct {
	mu    sync.RWMutex
	db    *pgxpool.Pool
	redis *redis.Client
	hub   HubConn
}

type HubConn interface {
	ClientCount() int
}

var statusDeps *internalStatusDeps

// SetInternalStatusDeps configures the status handler's dependencies.
// Must be called before requests arrive.
func SetInternalStatusDeps(db *pgxpool.Pool, redis *redis.Client, hub HubConn) {
	statusDeps = &internalStatusDeps{
		db:    db,
		redis: redis,
		hub:   hub,
	}
}

// InternalStatus handles GET /internal/status.
// Requires X-Internal-Key header matching INTERNAL_API_KEY env var.
// Returns 401 if missing or wrong, 200 with diagnostics on success.
func InternalStatus() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Check X-Internal-Key header.
		expectedKey := os.Getenv("INTERNAL_API_KEY")
		if expectedKey == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "INTERNAL_API_KEY not configured"})
			return
		}
		providedKey := r.Header.Get("X-Internal-Key")
		if providedKey != expectedKey {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid X-Internal-Key"})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		resp := StatusResponse{
			UptimeSeconds: int64(time.Since(processStartTime).Seconds()),
		}

		if statusDeps != nil {
			statusDeps.mu.RLock()
			defer statusDeps.mu.RUnlock()

			// Indexer lag
			if statusDeps.db != nil {
				var lastLedger int64
				row := statusDeps.db.QueryRow(ctx,
					`SELECT COALESCE(CAST(value AS BIGINT), 0) FROM system_state WHERE key = 'latest_ledger_cursor'`,
				)
				_ = row.Scan(&lastLedger)
				if tip := globalChainTipCache.get(ctx); tip != nil {
					resp.IndexerLagLedgers = *tip - lastLedger
				}
			}

			// WebSocket clients
			if statusDeps.hub != nil {
				resp.ActiveWSClients = statusDeps.hub.ClientCount()
			}

			// Redis stream depth
			if statusDeps.redis != nil {
				len := statusDeps.redis.XLen(ctx, "trident:events").Val()
				resp.RedisStreamDepth = len
			}

			// DB pool connections
			if statusDeps.db != nil {
				resp.DBPoolOpenConns = statusDeps.db.Stat().TotalConns()
			}

			// Parse errors in last 24 hours
			if statusDeps.db != nil {
				var count int64
				row := statusDeps.db.QueryRow(ctx,
					`SELECT COUNT(*) FROM parse_errors WHERE occurred_at > NOW() - INTERVAL '24 hours'`,
				)
				_ = row.Scan(&count)
				resp.ParseErrors24h = count
			}
		}

		writeJSON(w, http.StatusOK, resp)
	}
}
