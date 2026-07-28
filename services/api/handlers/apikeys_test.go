package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Depo-dev/trident/services/api/handlers"
	"github.com/Depo-dev/trident/services/api/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
)

// --- Gating tests: no DB required, since requireAdmin rejects before any
// query runs. ---

func TestCreateAPIKey_NotConfigured_Returns403(t *testing.T) {
	h := handlers.CreateAPIKey(handlers.APIKeyConfig{})

	req := httptest.NewRequest(http.MethodPost, "/v1/api-keys", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	h(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d", rr.Code)
	}
}

func TestCreateAPIKey_WrongAdminKey_Returns401(t *testing.T) {
	h := handlers.CreateAPIKey(handlers.APIKeyConfig{AdminKey: "secret"})

	req := httptest.NewRequest(http.MethodPost, "/v1/api-keys", strings.NewReader(`{}`))
	req.Header.Set("X-Admin-Key", "wrong")
	rr := httptest.NewRecorder()
	h(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", rr.Code)
	}
}

func TestRotateAPIKey_WrongAdminKey_Returns401(t *testing.T) {
	h := handlers.RotateAPIKey(handlers.APIKeyConfig{AdminKey: "secret"})

	req := httptest.NewRequest(http.MethodPost, "/v1/api-keys/00000000-0000-0000-0000-000000000000/rotate", nil)
	req.Header.Set("X-Admin-Key", "wrong")
	rr := httptest.NewRecorder()
	h(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", rr.Code)
	}
}

func TestUpdateAPIKey_WrongAdminKey_Returns401(t *testing.T) {
	h := handlers.UpdateAPIKey(handlers.APIKeyConfig{AdminKey: "secret"})

	req := httptest.NewRequest(http.MethodPatch, "/v1/api-keys/00000000-0000-0000-0000-000000000000", strings.NewReader(`{}`))
	req.Header.Set("X-Admin-Key", "wrong")
	rr := httptest.NewRecorder()
	h(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", rr.Code)
	}
}

// --- Full lifecycle integration tests: opt-in, skipped unless
// TEST_DATABASE_URL is set (matches TestContractStatsRollup_MatchesLiveAggregation
// in stats_test.go), since the `go` CI job does not run a Postgres service.
// ---

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestAPIKeyLifecycle_CreateScopeAndExpiry exercises CreateAPIKey end to end:
// scope defaults to "read" when omitted, an explicit "admin" scope is
// honored, and expires_at round-trips through ListAPIKeys.
func TestAPIKeyLifecycle_CreateScopeAndExpiry(t *testing.T) {
	pool := testPool(t)
	const adminKey = "test-admin-key"
	cfg := handlers.APIKeyConfig{AdminKey: adminKey, DB: pool}

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM api_keys WHERE label LIKE 'lifecycle-test-%'")
	})

	// Default scope -> read.
	createReq := httptest.NewRequest(http.MethodPost, "/v1/api-keys",
		strings.NewReader(`{"label":"lifecycle-test-default"}`))
	createReq.Header.Set("X-Admin-Key", adminKey)
	rr := httptest.NewRecorder()
	handlers.CreateAPIKey(cfg)(rr, createReq)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create (default scope): want 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var defaultResp handlers.APIKeyResponse
	if err := json.NewDecoder(rr.Body).Decode(&defaultResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if defaultResp.Scope != middleware.ScopeRead {
		t.Errorf("want default scope %q, got %q", middleware.ScopeRead, defaultResp.Scope)
	}

	// Explicit admin scope + future expiry.
	expiry := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)
	createReq2 := httptest.NewRequest(http.MethodPost, "/v1/api-keys",
		strings.NewReader(fmt.Sprintf(`{"label":"lifecycle-test-admin","scope":"admin","expires_at":%q}`, expiry)))
	createReq2.Header.Set("X-Admin-Key", adminKey)
	rr2 := httptest.NewRecorder()
	handlers.CreateAPIKey(cfg)(rr2, createReq2)
	if rr2.Code != http.StatusCreated {
		t.Fatalf("create (admin scope): want 201, got %d: %s", rr2.Code, rr2.Body.String())
	}
	var adminResp handlers.APIKeyResponse
	if err := json.NewDecoder(rr2.Body).Decode(&adminResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if adminResp.Scope != middleware.ScopeAdmin {
		t.Errorf("want scope %q, got %q", middleware.ScopeAdmin, adminResp.Scope)
	}
	if adminResp.ExpiresAt == nil {
		t.Fatal("want expires_at set in create response")
	}

	// Reject invalid scope.
	badReq := httptest.NewRequest(http.MethodPost, "/v1/api-keys",
		strings.NewReader(`{"label":"lifecycle-test-bad","scope":"superuser"}`))
	badReq.Header.Set("X-Admin-Key", adminKey)
	rrBad := httptest.NewRecorder()
	handlers.CreateAPIKey(cfg)(rrBad, badReq)
	if rrBad.Code != http.StatusBadRequest {
		t.Errorf("invalid scope: want 400, got %d", rrBad.Code)
	}
}

// TestAPIKeyLifecycle_RotateGraceWindow exercises rotation end to end against
// the real auth path: the old key authenticates while grace_until is in the
// future, and is rejected (via the lazy_revoke CTE) once grace_until has
// elapsed.
func TestAPIKeyLifecycle_RotateGraceWindow(t *testing.T) {
	pool := testPool(t)
	const adminKey = "test-admin-key"
	cfg := handlers.APIKeyConfig{AdminKey: adminKey, DB: pool}

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM api_keys WHERE label = 'lifecycle-test-rotate'")
	})

	createReq := httptest.NewRequest(http.MethodPost, "/v1/api-keys",
		strings.NewReader(`{"label":"lifecycle-test-rotate","scope":"admin"}`))
	createReq.Header.Set("X-Admin-Key", adminKey)
	rr := httptest.NewRecorder()
	handlers.CreateAPIKey(cfg)(rr, createReq)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: want 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var created handlers.APIKeyResponse
	if err := json.NewDecoder(rr.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	oldKey := *created.Key

	rotateReq := httptest.NewRequest(http.MethodPost, "/v1/api-keys/"+created.ID+"/rotate",
		strings.NewReader(`{"grace_period_seconds": 3600}`))
	rotateReq.SetPathValue("id", created.ID)
	rotateReq.Header.Set("X-Admin-Key", adminKey)
	rrRotate := httptest.NewRecorder()
	handlers.RotateAPIKey(cfg)(rrRotate, rotateReq)
	if rrRotate.Code != http.StatusCreated {
		t.Fatalf("rotate: want 201, got %d: %s", rrRotate.Code, rrRotate.Body.String())
	}

	authHandler := middleware.NewDBAuth(middleware.DBAuthConfig{DB: pool})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }),
	)

	// Old key still authenticates: grace_until is an hour out.
	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.Header.Set("X-API-Key", oldKey)
	recStillValid := httptest.NewRecorder()
	authHandler.ServeHTTP(recStillValid, req)
	if recStillValid.Code != http.StatusNoContent {
		t.Fatalf("old key during grace window: want 204, got %d", recStillValid.Code)
	}

	// Force the grace window into the past, as if it had elapsed.
	if _, err := pool.Exec(context.Background(),
		`UPDATE api_keys SET grace_until = NOW() - INTERVAL '1 second' WHERE id = $1`, created.ID); err != nil {
		t.Fatalf("force grace expiry: %v", err)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req2.Header.Set("X-API-Key", oldKey)
	recExpired := httptest.NewRecorder()
	authHandler.ServeHTTP(recExpired, req2)
	if recExpired.Code != http.StatusUnauthorized {
		t.Fatalf("old key after grace window: want 401, got %d", recExpired.Code)
	}

	// The lazy_revoke CTE should have set revoked_at deterministically.
	var revoked bool
	if err := pool.QueryRow(context.Background(),
		`SELECT revoked_at IS NOT NULL FROM api_keys WHERE id = $1`, created.ID).Scan(&revoked); err != nil {
		t.Fatalf("check revoked_at: %v", err)
	}
	if !revoked {
		t.Error("want old key auto-revoked (revoked_at set) after grace window elapsed")
	}
}
