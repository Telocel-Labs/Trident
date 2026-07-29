// Package handlers contains the HTTP handler functions for the Trident REST API.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"

	"github.com/Depo-dev/trident/services/api/gen"
)

const healthCheckTimeout = 3 * time.Second

var (
	errNoDatabase   = errors.New("not configured")
	errNoRedis      = errors.New("not configured")
	errNoGRPCClient = errors.New("not configured")
)

// DBPool is the subset of pgxpool.Pool used across handlers.
// Ping is required so the health check can detect a broken pool that was
// created successfully but whose underlying connections have since died.
type DBPool interface {
	Ping(ctx context.Context) error
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// RedisPinger is the subset of *redis.Client used by the health check.
type RedisPinger interface {
	Ping(ctx context.Context) *redis.StatusCmd
}

// EventsLister is the subset of gen.EventsClient used by the health check.
type EventsLister interface {
	ListEvents(ctx context.Context, in *gen.ListEventsRequest, opts ...grpc.CallOption) (*gen.ListEventsResponse, error)
}

// LivenessResponse is the JSON body for GET /v1/health.
type LivenessResponse struct {
	Status string `json:"status"`
}

// Health handles GET /v1/health — a liveness check (issue #243).
//
// Deliberately cheap: no dependency calls (no DB/Redis/gRPC), just confirms
// the process is up and serving requests. This is what Kubernetes' liveness
// probe should hit — restarting the pod never fixes an unreachable external
// dependency, so liveness must not fail because Postgres/Redis/the gRPC
// backend is down. For that, see Ready (GET /v1/ready).
func Health() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, LivenessResponse{Status: "ok"})
	}
}

// ReadyChecks holds the per-dependency check results.
type ReadyChecks struct {
	Postgres string `json:"postgres"`
	Redis    string `json:"redis"`
	GRPCAPI  string `json:"grpc_api"`
}

// ReadyResponse is the JSON body for GET /v1/ready.
type ReadyResponse struct {
	Status     string      `json:"status"`
	IndexerLag *int64      `json:"indexer_lag"`
	Checks     ReadyChecks `json:"checks"`
}

// Ready handles GET /v1/ready — a readiness check (issue #243).
//
// Runs Postgres, Redis, and gRPC checks concurrently with a shared
// 3-second timeout. Returns 200 when all checks pass, 503 when any fail —
// this is what Kubernetes' readiness probe should hit, so a pod with a
// broken dependency is pulled out of the Service's endpoint rotation
// instead of continuing to receive traffic it can't serve. The indexer_lag
// field is populated from system_state when Postgres is healthy and the
// chain tip is available in the cache; null otherwise.
func Ready(db DBPool, redisClient RedisPinger, grpcClient EventsLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parentCtx := r.Context()

		var (
			wg         sync.WaitGroup
			pgErr      error
			lastLedger *int64
			redisErr   error
			grpcErr    error
		)

		wg.Add(3)

		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(parentCtx, healthCheckTimeout)
			defer cancel()
			lastLedger, pgErr = checkPostgres(ctx, db)
		}()

		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(parentCtx, healthCheckTimeout)
			defer cancel()
			redisErr = checkRedis(ctx, redisClient)
		}()

		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(parentCtx, healthCheckTimeout)
			defer cancel()
			grpcErr = checkGRPC(ctx, grpcClient)
		}()

		wg.Wait()

		resp := ReadyResponse{
			Checks: ReadyChecks{
				Postgres: resultString(pgErr),
				Redis:    resultString(redisErr),
				GRPCAPI:  resultString(grpcErr),
			},
		}

		if pgErr == nil && lastLedger != nil {
			if tip := globalChainTipCache.get(parentCtx); tip != nil {
				lag := *tip - *lastLedger
				resp.IndexerLag = &lag
			}
		}

		if pgErr != nil || redisErr != nil || grpcErr != nil {
			resp.Status = "degraded"
			writeJSON(w, http.StatusServiceUnavailable, resp)
			return
		}

		resp.Status = "ok"
		writeJSON(w, http.StatusOK, resp)
	}
}

func resultString(err error) string {
	if err == nil {
		return "ok"
	}
	return "error: " + err.Error()
}

// checkPostgres pings the pool then queries system_state for the indexer's
// last processed ledger. Returns (nil, errNoDatabase) when db is nil,
// (nil, err) on ping/query failure, (&ledger, nil) on success, or
// (nil, nil) when the system_state row doesn't exist yet (ErrNoRows).
func checkPostgres(ctx context.Context, db DBPool) (*int64, error) {
	if db == nil {
		return nil, errNoDatabase
	}
	// Ping first so a broken pool is detected even if QueryRow would succeed
	// by returning a cached-but-stale row.
	if err := db.Ping(ctx); err != nil {
		return nil, err
	}
	var lastLedger *int64
	row := db.QueryRow(ctx,
		`SELECT last_ledger_indexed FROM system_state WHERE key = 'latest_ledger_cursor'`,
	)
	if err := row.Scan(&lastLedger); err != nil && err != pgx.ErrNoRows {
		return nil, err
	}
	return lastLedger, nil
}

func checkRedis(ctx context.Context, redisClient RedisPinger) error {
	if redisClient == nil {
		return errNoRedis
	}
	return redisClient.Ping(ctx).Err()
}

func checkGRPC(ctx context.Context, client EventsLister) error {
	if client == nil {
		return errNoGRPCClient
	}
	_, err := client.ListEvents(ctx, &gen.ListEventsRequest{Limit: 0})
	return err
}
