package main

import (
	"context"
	"fmt"

	"github.com/Depo-dev/trident/services/api/handlers"
	"github.com/jackc/pgx/v5"
)

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
		defer conn.Close(ctx)

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
