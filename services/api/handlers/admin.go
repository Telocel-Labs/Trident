package handlers

import (
	"context"
	"crypto/subtle"
	"net/http"
	"time"
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
