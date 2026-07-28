package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Depo-dev/trident/services/api/middleware"
	"github.com/jackc/pgx/v5"
)

// fakeRow is a minimal pgx.Row implementation so tests can control what a
// QueryRow().Scan() call observes, without a live Postgres connection.
type fakeRow struct {
	scan func(dest ...any) error
}

func (r fakeRow) Scan(dest ...any) error { return r.scan(dest...) }

// fakeDB implements the small QueryRow-only interface middleware.DBAuthConfig
// requires, letting each test control the row (or ErrNoRows) returned for the
// combined lazy-revoke-and-select query in NewDBAuth.
type fakeDB struct {
	queryRow func(ctx context.Context, sql string, args ...any) pgx.Row
}

func (f fakeDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return f.queryRow(ctx, sql, args...)
}

// newScanRow builds a fakeRow that scans a successful auth lookup result into
// whatever destination NewDBAuth passes (id, network, scope, expires_at,
// grace_until, in that order).
func newScanRow(id, network, scope string, expiresAt, graceUntil *time.Time) fakeRow {
	return fakeRow{scan: func(dest ...any) error {
		*(dest[0].(*string)) = id
		*(dest[1].(*string)) = network
		*(dest[2].(*string)) = scope
		*(dest[3].(**time.Time)) = expiresAt
		*(dest[4].(**time.Time)) = graceUntil
		return nil
	}}
}

func noRowsRow() fakeRow {
	return fakeRow{scan: func(dest ...any) error { return pgx.ErrNoRows }}
}

// finalHandler records whether it was reached and echoes the request's scope
// so tests can assert on both authentication and scope propagation.
func finalHandler(reached *bool, gotScope *string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		if gotScope != nil {
			*gotScope = middleware.ScopeFromContext(r.Context())
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func TestNewDBAuth_RotatedKey_ValidDuringGrace_RejectedAfterGraceExpires(t *testing.T) {
	// Simulates the two states a rotated-out key passes through: while
	// grace_until is still in the future the row is returned (still valid);
	// once grace_until has elapsed, the auth query's own WHERE clause would
	// exclude the row (and the lazy_revoke CTE sets revoked_at), which we
	// model here as ErrNoRows.
	future := time.Now().Add(1 * time.Hour)

	// --- During grace window: request succeeds. ---
	db := fakeDB{queryRow: func(ctx context.Context, sql string, args ...any) pgx.Row {
		return newScanRow("key-id-1", "mainnet", middleware.ScopeAdmin, nil, &future)
	}}
	handler := middleware.NewDBAuth(middleware.DBAuthConfig{DB: db})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }),
	)

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.Header.Set("X-API-Key", "trident_old_key_still_in_grace")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("during grace window: want 204, got %d", rec.Code)
	}

	// --- After grace window elapses: the DB query no longer returns a row. ---
	dbExpired := fakeDB{queryRow: func(ctx context.Context, sql string, args ...any) pgx.Row {
		return noRowsRow()
	}}
	handlerExpired := middleware.NewDBAuth(middleware.DBAuthConfig{DB: dbExpired})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }),
	)

	req2 := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req2.Header.Set("X-API-Key", "trident_old_key_still_in_grace")
	rec2 := httptest.NewRecorder()
	handlerExpired.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("after grace window: want 401, got %d", rec2.Code)
	}
}

func TestNewDBAuth_ExpiredKey_Rejected(t *testing.T) {
	// An expires_at in the past means the auth query's
	// "AND (expires_at IS NULL OR expires_at > NOW())" clause excludes the
	// row, which we model here as ErrNoRows (the query itself enforces this
	// server-side; the fake stands in for that enforcement).
	db := fakeDB{queryRow: func(ctx context.Context, sql string, args ...any) pgx.Row {
		return noRowsRow()
	}}
	handler := middleware.NewDBAuth(middleware.DBAuthConfig{DB: db})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }),
	)

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.Header.Set("X-API-Key", "trident_expired_key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired key: want 401, got %d", rec.Code)
	}
}

func TestNewDBAuth_ScopeEnforcement_ReadRejectedAdminAllowed(t *testing.T) {
	tests := []struct {
		name       string
		scope      string
		wantStatus int
	}{
		{name: "read-scoped key on admin route", scope: middleware.ScopeRead, wantStatus: http.StatusForbidden},
		{name: "admin-scoped key on admin route", scope: middleware.ScopeAdmin, wantStatus: http.StatusNoContent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := fakeDB{queryRow: func(ctx context.Context, sql string, args ...any) pgx.Row {
				return newScanRow("key-id-2", "mainnet", tt.scope, nil, nil)
			}}

			var reached bool
			var gotScope string
			// Mirrors the production layering: NewDBAuth (outer) attaches
			// scope to the context, RequireScope (inner, wrapping the
			// specific route) enforces it — same as main.go's
			// requireAdminScope wrapping the webhook mutating routes.
			protected := middleware.RequireScope(middleware.ScopeAdmin)(finalHandler(&reached, &gotScope))
			handler := middleware.NewDBAuth(middleware.DBAuthConfig{DB: db})(protected)

			req := httptest.NewRequest(http.MethodPost, "/v1/webhooks", nil)
			req.Header.Set("X-API-Key", "trident_scoped_key")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("want %d, got %d", tt.wantStatus, rec.Code)
			}
			if tt.wantStatus == http.StatusNoContent {
				if !reached {
					t.Fatal("expected final handler to be reached")
				}
				if gotScope != tt.scope {
					t.Fatalf("want scope %q propagated, got %q", tt.scope, gotScope)
				}
			} else if reached {
				t.Fatal("final handler must not be reached when scope is insufficient")
			}
		})
	}
}

func TestNewDBAuth_LegacyEnvVarFallback_TreatedAsAdminScope(t *testing.T) {
	// Legacy API_KEY_HASHES keys predate scoping and must keep working on
	// admin-scoped routes (issue #314's backward-compatibility requirement).
	t.Setenv("API_KEY_SALT", "salt")
	t.Setenv("API_KEY_HASHES", hashKey("salt", "legacy-key"))

	db := fakeDB{queryRow: func(ctx context.Context, sql string, args ...any) pgx.Row {
		return noRowsRow()
	}}

	var reached bool
	var gotScope string
	protected := middleware.RequireScope(middleware.ScopeAdmin)(finalHandler(&reached, &gotScope))
	handler := middleware.NewDBAuth(middleware.DBAuthConfig{DB: db})(protected)

	req := httptest.NewRequest(http.MethodPost, "/v1/webhooks", nil)
	req.Header.Set("X-API-Key", "legacy-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	if gotScope != middleware.ScopeAdmin {
		t.Fatalf("want legacy key treated as admin scope, got %q", gotScope)
	}
}
