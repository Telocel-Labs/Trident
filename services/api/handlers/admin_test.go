package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Depo-dev/trident/services/api/handlers"
)

func okStats(context.Context) (*handlers.DBStats, error) {
	return &handlers.DBStats{
		Pools: []map[string]any{{"database": "trident", "cl_active": int64(3)}},
		Stats: []map[string]any{{"database": "trident", "total_query_count": int64(42)}},
	}, nil
}

func TestAdminDB_NotConfigured_Returns503(t *testing.T) {
	// No admin key set -> endpoint disabled.
	h := handlers.AdminDB(handlers.AdminConfig{StatsFunc: okStats})

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/db", nil)
	req.Header.Set("X-Admin-Key", "anything")
	rr := httptest.NewRecorder()
	h(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503, got %d", rr.Code)
	}
}

func TestAdminDB_MissingKey_Returns401(t *testing.T) {
	h := handlers.AdminDB(handlers.AdminConfig{AdminKey: "secret", StatsFunc: okStats})

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/db", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", rr.Code)
	}
}

func TestAdminDB_WrongKey_Returns401(t *testing.T) {
	h := handlers.AdminDB(handlers.AdminConfig{AdminKey: "secret", StatsFunc: okStats})

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/db", nil)
	req.Header.Set("X-Admin-Key", "wrong")
	rr := httptest.NewRecorder()
	h(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", rr.Code)
	}
}

func TestAdminDB_ValidKey_Returns200WithStats(t *testing.T) {
	h := handlers.AdminDB(handlers.AdminConfig{AdminKey: "secret", StatsFunc: okStats})

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/db", nil)
	req.Header.Set("X-Admin-Key", "secret")
	rr := httptest.NewRecorder()
	h(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}

	var body handlers.DBStats
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Pools) != 1 || len(body.Stats) != 1 {
		t.Errorf("want 1 pool and 1 stat row, got %d pools / %d stats", len(body.Pools), len(body.Stats))
	}
	if body.Pools[0]["database"] != "trident" {
		t.Errorf("want pools[0].database=trident, got %v", body.Pools[0]["database"])
	}
}

func TestAdminDB_StatsError_Returns502(t *testing.T) {
	failing := func(context.Context) (*handlers.DBStats, error) {
		return nil, errors.New("pgbouncer unreachable")
	}
	h := handlers.AdminDB(handlers.AdminConfig{AdminKey: "secret", StatsFunc: failing})

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/db", nil)
	req.Header.Set("X-Admin-Key", "secret")
	rr := httptest.NewRecorder()
	h(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Errorf("want 502, got %d", rr.Code)
	}
}
