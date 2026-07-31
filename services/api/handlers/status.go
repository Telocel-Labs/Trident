package handlers

import (
	"context"
	"crypto/subtle"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/Depo-dev/trident/services/api/internal/httputil"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

var processStartTime = time.Now()

// StatusResponse is the JSON response for GET /internal/status.
type StatusResponse struct {
	IndexerLagLedgers int64 `json:"indexer_lag_ledgers"`
	ActiveWSClients   int   `json:"active_ws_clients"`
	RedisStreamDepth  int64 `json:"redis_stream_depth"`
	DBPoolOpenConns   int32 `json:"db_pool_open_conns"`
	ParseErrors24h    int64 `json:"parse_errors_24h"`
	UptimeSeconds     int64 `json:"uptime_seconds"`
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
//
// This endpoint is internal-only: it must never be reachable from outside the
// cluster/VPC. Defense in depth is layered on top of this handler's own auth
// check — see docker/nginx/nginx.conf and helm/trident/templates/ingress.yaml,
// which both explicitly deny /internal/ before it ever reaches this handler.
func InternalStatus() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Fail closed: an unset/empty INTERNAL_API_KEY must reject every
		// request, never be treated as "auth disabled". Do not change this
		// to skip the check when the env var is empty.
		expectedKey := os.Getenv("INTERNAL_API_KEY")
		if expectedKey == "" {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, httputil.UNAUTHORIZED, "INTERNAL_API_KEY not configured")
			return
		}
		providedKey := r.Header.Get("X-Internal-Key")
		// Constant-time comparison so response timing can't be used to learn
		// the key byte-by-byte. ConstantTimeCompare requires equal-length
		// inputs to be meaningful; a length mismatch is itself decisive (and
		// not secret), so short-circuit it before the constant-time check.
		if len(providedKey) != len(expectedKey) ||
			subtle.ConstantTimeCompare([]byte(providedKey), []byte(expectedKey)) != 1 {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, httputil.UNAUTHORIZED, "invalid X-Internal-Key")
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
