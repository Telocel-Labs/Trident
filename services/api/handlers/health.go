// Package handlers contains the HTTP handler functions for the Trident REST API.
package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/Depo-dev/trident/services/api/internal/httputil"
	"github.com/jackc/pgx/v5"
)

const healthStalenessThreshold = 60 * time.Second

// DBPool is the minimal query surface the health check needs. Both *pgx.Conn
// and *pgxpool.Pool satisfy it, so handlers stay agnostic to how connections
// are pooled (the production server uses a *pgxpool.Pool behind PgBouncer).
type DBPool interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// HealthRow holds the columns we read from system_state for the health check.
type HealthRow struct {
	LastLedgerIndexed sql.NullInt64
	LastPollAt        sql.NullTime
}

// HealthResponse is the JSON body returned by GET /v1/health.
type HealthResponse struct {
	Status  string `json:"status"`
	Indexer struct {
		LastLedgerIndexed *int64  `json:"last_ledger_indexed"`
		LastPollAt        *string `json:"last_poll_at"`
	} `json:"indexer"`
}

// Health handles GET /v1/health.
//
// Acceptance criteria (issue #62):
//   - Returns the indexer's last_ledger_indexed and last_poll_at fields from system_state.
//   - Returns HTTP 503 with status "degraded" when last_poll_at is NULL or older than 60 s.
//   - Returns HTTP 200 with status "ok" otherwise.
//
// db may be nil when DATABASE_URL is not configured; the endpoint returns 503 in that case.
func Health(db DBPool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if db == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, httputil.INTERNAL, "database connection unavailable")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		var lastLedger *int64
		var lastPollAt *time.Time

		row := db.QueryRow(ctx,
			`SELECT last_ledger_indexed, last_poll_at
			   FROM system_state
			  WHERE key = 'latest_ledger_cursor'`,
		)

		// Scan into nullable pointers using pgx semantics.
		err := row.Scan(&lastLedger, &lastPollAt)
		if err != nil && err != pgx.ErrNoRows {
			httputil.WriteError(w, http.StatusServiceUnavailable, httputil.INTERNAL, "database query failed")
			return
		}

		var resp HealthResponse

		if lastLedger != nil {
			resp.Indexer.LastLedgerIndexed = lastLedger
		}
		if lastPollAt != nil {
			s := lastPollAt.UTC().Format(time.RFC3339)
			resp.Indexer.LastPollAt = &s
		}

		// Degraded if last_poll_at is null or older than 60 seconds.
		if lastPollAt == nil || time.Since(*lastPollAt) > healthStalenessThreshold {
			resp.Status = "degraded"
			writeJSON(w, http.StatusServiceUnavailable, resp)
			return
		}

		resp.Status = "ok"
		writeJSON(w, http.StatusOK, resp)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
