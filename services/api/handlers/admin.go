package handlers

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/google/uuid"
)

// adminStatsTimeout bounds how long the admin endpoint waits on PgBouncer.
const adminStatsTimeout = 5 * time.Second

// DBStats is the PgBouncer pooler snapshot returned by GET /v1/admin/db.
//
// Pools and Stats hold the raw rows from `SHOW POOLS` and `SHOW STATS`, each as
// an ordered list of column-name to value maps. Keeping the rows verbatim means
// the response stays faithful to whatever columns the running PgBouncer version
// reports, without this code having to track schema changes between versions.
type DBStats struct {
	Pools []map[string]any `json:"pools"`
	Stats []map[string]any `json:"stats"`
}

// AdminConfig wires up the admin DB endpoint.
//
// AdminKey is the shared secret the caller must present in the X-Admin-Key
// header. StatsFunc fetches a live PgBouncer snapshot. If AdminKey is empty or
// StatsFunc is nil the endpoint is considered disabled and returns 503, so an
// operator can leave it off simply by not setting ADMIN_API_KEY.
type AdminConfig struct {
	AdminKey  string
	StatsFunc func(ctx context.Context) (*DBStats, error)
	DB        *pgxpool.Pool // for audit log queries
}

// AdminDB handles GET /v1/admin/db.
//
// It returns PgBouncer pool utilisation and cumulative stats (SHOW POOLS /
// SHOW STATS) for capacity planning (issue #87). The caller must present a
// valid admin key in the X-Admin-Key header.
func AdminDB(cfg AdminConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cfg.AdminKey == "" || cfg.StatsFunc == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("admin DB endpoint is not configured"))
			return
		}

		if !validAdminKey(cfg.AdminKey, r.Header.Get("X-Admin-Key")) {
			writeJSON(w, http.StatusUnauthorized, errorBody("invalid or missing admin key"))
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), adminStatsTimeout)
		defer cancel()

		stats, err := cfg.StatsFunc(ctx)
		if err != nil {
			// The PgBouncer admin console is the upstream here, so a failure to
			// reach it is a bad-gateway condition rather than our own error.
			writeJSON(w, http.StatusBadGateway, errorBody("could not read PgBouncer stats"))
			return
		}

		writeJSON(w, http.StatusOK, stats)
	}
}

// validAdminKey reports whether provided matches expected, using a constant-time
// comparison so the endpoint does not leak the key length or content via timing.
func validAdminKey(expected, provided string) bool {
	if provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}

func errorBody(message string) map[string]any {
	return map[string]any{"error": map[string]string{"message": message}}
}

// AdminKeyUsageResponse is the response for GET /v1/admin/keys/:id/usage.
type AdminKeyUsageResponse struct {
	APIKeyID           string                `json:"api_key_id"`
	From               string                `json:"from"`
	To                 string                `json:"to"`
	TotalRequests      int64                 `json:"total_requests"`
	SuccessfulRequests int64                 `json:"successful_requests"`
	ByEndpoint         []AdminEndpointUsage  `json:"by_endpoint"`
}

type AdminEndpointUsage struct {
	Endpoint        string  `json:"endpoint"`
	Requests        int64   `json:"requests"`
	AvgDurationMs   float64 `json:"avg_duration_ms"`
}

// AdminKeyUsage handles GET /v1/admin/keys/:id/usage.
//
// Returns aggregated usage statistics for an API key from the audit log.
// Query params: from (RFC3339, required), to (RFC3339, required).
func AdminKeyUsage(cfg AdminConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cfg.AdminKey == "" || cfg.DB == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("admin usage endpoint is not configured"))
			return
		}

		if !validAdminKey(cfg.AdminKey, r.Header.Get("X-Admin-Key")) {
			writeJSON(w, http.StatusUnauthorized, errorBody("invalid or missing admin key"))
			return
		}

		keyIDStr := r.PathValue("id")
		if keyIDStr == "" {
			writeJSON(w, http.StatusBadRequest, errorBody("missing api key id"))
			return
		}
		keyID, err := uuid.Parse(keyIDStr)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid api key id"))
			return
		}

		fromStr := r.URL.Query().Get("from")
		toStr := r.URL.Query().Get("to")
		if fromStr == "" || toStr == "" {
			writeJSON(w, http.StatusBadRequest, errorBody("from and to query parameters are required (RFC3339)"))
			return
		}

		from, err := time.Parse(time.RFC3339, fromStr)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid from timestamp format, use RFC3339"))
			return
		}
		to, err := time.Parse(time.RFC3339, toStr)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid to timestamp format, use RFC3339"))
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), adminStatsTimeout)
		defer cancel()

		// Total requests
		var totalReqs int64
		err = cfg.DB.QueryRow(ctx,
			`SELECT COUNT(*) FROM audit_log WHERE api_key_id = $1 AND ts >= $2 AND ts < $3`,
			keyID, from, to,
		).Scan(&totalReqs)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody("failed to query total requests"))
			return
		}

		// Successful requests (2xx)
		var successReqs int64
		err = cfg.DB.QueryRow(ctx,
			`SELECT COUNT(*) FROM audit_log WHERE api_key_id = $1 AND ts >= $2 AND ts < $3 AND status_code < 400`,
			keyID, from, to,
		).Scan(&successReqs)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody("failed to query successful requests"))
			return
		}

		// By endpoint
		rows, err := cfg.DB.Query(ctx,
			`SELECT endpoint, COUNT(*) as req_count, AVG(duration_ms)::float8 as avg_duration
			 FROM audit_log
			 WHERE api_key_id = $1 AND ts >= $2 AND ts < $3
			 GROUP BY endpoint
			 ORDER BY req_count DESC`,
			keyID, from, to,
		)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody("failed to query endpoint breakdown"))
			return
		}
		defer rows.Close()

		var byEndpoint []AdminEndpointUsage
		for rows.Next() {
			var eu AdminEndpointUsage
			if err := rows.Scan(&eu.Endpoint, &eu.Requests, &eu.AvgDurationMs); err != nil {
				writeJSON(w, http.StatusInternalServerError, errorBody("scan error"))
				return
			}
			byEndpoint = append(byEndpoint, eu)
		}

		resp := AdminKeyUsageResponse{
			APIKeyID:           keyIDStr,
			From:               fromStr,
			To:                 toStr,
			TotalRequests:      totalReqs,
			SuccessfulRequests: successReqs,
			ByEndpoint:         byEndpoint,
		}

		writeJSON(w, http.StatusOK, resp)
	}
}
