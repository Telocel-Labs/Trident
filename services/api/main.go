package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"syscall"
	"time"

	"github.com/Depo-dev/trident/services/api/grpc"
	"github.com/Depo-dev/trident/services/api/handlers"
	"github.com/Depo-dev/trident/services/api/internal/metrics"
	"github.com/Depo-dev/trident/services/api/internal/profiling"
	"github.com/Depo-dev/trident/services/api/internal/sorobanrpc"
	"github.com/Depo-dev/trident/services/api/middleware"
	"github.com/Depo-dev/trident/services/api/ws"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	"semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
)

const contractStatsRollupRefreshInterval = 60 * time.Second
const dbPoolMetricsPollInterval = 15 * time.Second

const (
	usageRollupInterval        = 5 * time.Minute
	usageRollupLookback        = 48 * time.Hour
	usageRollupRetention       = 400 * 24 * time.Hour
	usageRollupCleanupInterval = 24 * time.Hour
)

const defaultDBPoolSize = 5

const (
	defaultDBPoolMinConns              = 0
	defaultDBPoolMaxConnLifetimeMS     = 1_800_000 // 30 min
	defaultDBPoolMaxConnIdleTimeMS     = 600_000   // 10 min
	defaultDBPoolHealthCheckPeriodMS   = 30_000    // 30 sec
	defaultDBPoolConnTimeoutMS         = 5_000     // 5 sec
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	ctx := context.Background()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://trident:password@localhost:5432/trident"
	}

	poolConfig, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		logger.Error("failed to parse database url", "error", err)
		os.Exit(1)
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		logger.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Event retention job setup
	retentionDaysStr := os.Getenv("EVENT_RETENTION_DAYS")
	if retentionDaysStr != "" {
		if days, err := strconv.Atoi(retentionDaysStr); err == nil && days > 0 {
			go func() {
				ticker := time.NewTicker(1 * time.Hour)
				defer ticker.Stop()
				for range ticker.C {
					cutoff := time.Now().AddDate(0, 0, -days)
					partitionName := fmt.Sprintf("soroban_events_%s", cutoff.Format("20060102"))
					// Detach and drop partition
					detachQuery := fmt.Sprintf("ALTER TABLE soroban_events DETACH PARTITION %s", partitionName)
					_, err := pool.Exec(ctx, detachQuery)
					if err == nil {
						dropQuery := fmt.Sprintf("DROP TABLE IF EXISTS %s", partitionName)
						_, _ = pool.Exec(ctx, dropQuery)
					}
				}
			}()
		}
	}

	logger.Info("starting trident api service")
	select {}
}
