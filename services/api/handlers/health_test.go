package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Depo-dev/trident/services/api/gen"
	"github.com/Depo-dev/trident/services/api/handlers"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeHealthDB struct {
	pingErr    error
	lastLedger *int64
	rowErr     error
}

func (db fakeHealthDB) Ping(context.Context) error {
	return db.pingErr
}

func (db fakeHealthDB) QueryRow(context.Context, string, ...any) pgx.Row {
	return fakeHealthRow{lastLedger: db.lastLedger, err: db.rowErr}
}

func (db fakeHealthDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}

type fakeHealthRow struct {
	lastLedger *int64
	err        error
}

func (r fakeHealthRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) == 0 || r.lastLedger == nil {
		return nil
	}
	target, ok := dest[0].(**int64)
	if !ok {
		return nil
	}
	value := *r.lastLedger
	*target = &value
	return nil
}

type fakeRedisPinger struct {
	err error
}

func (p fakeRedisPinger) Ping(ctx context.Context) *redis.StatusCmd {
	cmd := redis.NewStatusCmd(ctx)
	if p.err != nil {
		cmd.SetErr(p.err)
		return cmd
	}
	cmd.SetVal("PONG")
	return cmd
}

type fakeHealthEventsClient struct {
	err error
}

func (c fakeHealthEventsClient) ListEvents(ctx context.Context, in *gen.ListEventsRequest, opts ...grpc.CallOption) (*gen.ListEventsResponse, error) {
	if c.err != nil {
		return nil, c.err
	}
	return &gen.ListEventsResponse{}, nil
}

func TestHealthHandler_TableDriven(t *testing.T) {
	dbErr := errors.New("database unavailable")
	redisErr := errors.New("redis unavailable")
	grpcErr := status.Error(codes.Unavailable, "grpc unavailable")

	tests := []struct {
		name       string
		db         handlers.DBPool
		redis      handlers.RedisPinger
		grpc       handlers.EventsLister
		wantStatus int
		wantBody   handlers.HealthResponse
		check      func(t *testing.T, body handlers.HealthResponse)
	}{
		{
			name:       "all dependencies reachable",
			db:         fakeHealthDB{},
			redis:      fakeRedisPinger{},
			grpc:       fakeHealthEventsClient{},
			wantStatus: http.StatusOK,
			wantBody: handlers.HealthResponse{
				Status: "ok",
				Checks: handlers.HealthChecks{
					Postgres: "ok",
					Redis:    "ok",
					GRPCAPI:  "ok",
				},
			},
		},
		{
			name:       "db unreachable returns degraded 503",
			db:         fakeHealthDB{pingErr: dbErr},
			redis:      fakeRedisPinger{},
			grpc:       fakeHealthEventsClient{},
			wantStatus: http.StatusServiceUnavailable,
			check: func(t *testing.T, body handlers.HealthResponse) {
				if body.Status != "degraded" {
					t.Fatalf("status: got %q, want degraded", body.Status)
				}
				if !strings.Contains(body.Checks.Postgres, dbErr.Error()) {
					t.Fatalf("postgres check: got %q, want db error", body.Checks.Postgres)
				}
			},
		},
		{
			name:       "grpc unreachable reflected in checks",
			db:         fakeHealthDB{},
			redis:      fakeRedisPinger{},
			grpc:       fakeHealthEventsClient{err: grpcErr},
			wantStatus: http.StatusServiceUnavailable,
			check: func(t *testing.T, body handlers.HealthResponse) {
				if body.Status != "degraded" {
					t.Fatalf("status: got %q, want degraded", body.Status)
				}
				if !strings.Contains(body.Checks.GRPCAPI, "Unavailable") {
					t.Fatalf("grpc_api check: got %q, want Unavailable", body.Checks.GRPCAPI)
				}
			},
		},
		{
			name:       "redis unreachable reflected in checks",
			db:         fakeHealthDB{},
			redis:      fakeRedisPinger{err: redisErr},
			grpc:       fakeHealthEventsClient{},
			wantStatus: http.StatusServiceUnavailable,
			check: func(t *testing.T, body handlers.HealthResponse) {
				if body.Status != "degraded" {
					t.Fatalf("status: got %q, want degraded", body.Status)
				}
				if !strings.Contains(body.Checks.Redis, redisErr.Error()) {
					t.Fatalf("redis check: got %q, want redis error", body.Checks.Redis)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
			rr := httptest.NewRecorder()

			handlers.Health(tt.db, tt.redis, tt.grpc)(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("status: got %d, want %d; body: %s", rr.Code, tt.wantStatus, rr.Body.String())
			}

			var body handlers.HealthResponse
			if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if tt.wantBody.Status != "" {
				if body.Status != tt.wantBody.Status {
					t.Fatalf("body.status: got %q, want %q", body.Status, tt.wantBody.Status)
				}
				if body.Checks != tt.wantBody.Checks {
					t.Fatalf("checks: got %+v, want %+v", body.Checks, tt.wantBody.Checks)
				}
			}
			if tt.check != nil {
				tt.check(t, body)
			}
		})
	}
}
