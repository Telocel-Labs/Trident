package main

import (
	"context"
	"fmt"

	"github.com/Depo-dev/trident/services/api/handlers"
	"github.com/jackc/pgx/v5"
)

// PgBouncer compatibility notes (#256)
//
// Supported pooling mode: transaction pooling (pool_mode = transaction).
//
// The application pool (newDBPool in main.go) is configured with
// QueryExecModeSimpleProtocol, which disables server-side prepared statements.
// This is required for PgBouncer transaction pooling because:
//
//   - Extended query protocol prepared statements are session-scoped; in
//     transaction mode the backend connection changes between transactions and
//     a previously prepared statement is no longer available.
//   - Simple protocol sends each query as a one-shot Parse+Execute in the
//     same message, which is safe across connection hand-offs.
//
// Session-level state avoided:
//   - No SET LOCAL / SET SESSION variables outside of transactions.
//   - No advisory locks (pg_advisory_lock / pg_try_advisory_lock).
//   - No LISTEN / NOTIFY (WebSocket fan-out uses Redis Streams instead).
//   - No temp tables that persist beyond a single transaction.
//
// To enable PgBouncer, set the PGBOUNCER_ADMIN_URL environment variable to
// the admin console URL (e.g. postgresql://pgbouncer-admin@host:6432/pgbouncer).
// The app then exposes live SHOW POOLS / SHOW STATS data at GET /v1/db/stats.
//
// Validation: run load-tests/pgbouncer-validation.js against the stack while
// PgBouncer fronts the database to confirm no connection-exhaustion errors and
// p99 < 500 ms:
//
//   BASE_URL=http://localhost:3000 k6 run load-tests/pgbouncer-validation.js

// newPgbouncerStats returns a stats function that connects to the PgBouncer
// admin console at adminURL (the virtual "pgbouncer" database) and reads
// SHOW POOLS / SHOW STATS on demand.
//
// The PgBouncer admin console speaks only the simple query protocol, so the
// connection is forced into QueryExecModeSimpleProtocol — the same mode the
// application pool uses for transaction-mode compatibility. A fresh, short-lived
// connection is opened per request: admin calls are rare and this keeps no extra
// connection occupying a PgBouncer slot between calls.
func newPgbouncerStats(adminURL string) func(context.Context) (*handlers.DBStats, error) {
	return func(ctx context.Context) (*handlers.DBStats, error) {
		connConfig, err := pgx.ParseConfig(adminURL)
		if err != nil {
			return nil, fmt.Errorf("parse PGBOUNCER_ADMIN_URL: %w", err)
		}
		connConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

		conn, err := pgx.ConnectConfig(ctx, connConfig)
		if err != nil {
			return nil, fmt.Errorf("connect to pgbouncer admin: %w", err)
		}
		defer func() { _ = conn.Close(ctx) }()

		pools, err := queryShow(ctx, conn, "SHOW POOLS")
		if err != nil {
			return nil, fmt.Errorf("SHOW POOLS: %w", err)
		}
		stats, err := queryShow(ctx, conn, "SHOW STATS")
		if err != nil {
			return nil, fmt.Errorf("SHOW STATS: %w", err)
		}

		return &handlers.DBStats{Pools: pools, Stats: stats}, nil
	}
}

// queryShow runs a PgBouncer SHOW command and returns each row as a map of
// column name to value, preserving whatever columns the server reports.
func queryShow(ctx context.Context, conn *pgx.Conn, sql string) ([]map[string]any, error) {
	rows, err := conn.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fields := rows.FieldDescriptions()
	out := make([]map[string]any, 0)
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, err
		}
		row := make(map[string]any, len(fields))
		for i, f := range fields {
			row[f.Name] = values[i]
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
