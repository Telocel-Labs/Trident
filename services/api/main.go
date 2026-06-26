package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/Depo-dev/trident/services/api/grpc"
	"github.com/Depo-dev/trident/services/api/handlers"
	"github.com/Depo-dev/trident/services/api/middleware"
	"github.com/Depo-dev/trident/services/api/ws"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const defaultDBPoolSize = 5

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}

	grpcAddr := os.Getenv("GRPC_ADDR")
	if grpcAddr == "" {
		grpcAddr = "localhost:5000"
	}
	grpcClient, err := grpc.NewClient(context.Background(), grpcAddr)
	if err != nil {
		slog.Error("failed to connect to gRPC backend", "err", err)
		os.Exit(1)
	}
	defer grpcClient.Close()
	handlers.SetEventsClient(grpcClient)

	var pool *pgxpool.Pool
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		p, err := newDBPool(ctx, dsn, dbPoolSizeFromEnv())
		cancel()
		if err != nil {
			slog.Warn("could not connect to database; DB-backed endpoints will return 503", "err", err)
		} else {
			pool = p
			defer pool.Close()
		}
	} else {
		slog.Warn("DATABASE_URL not set; DB-backed endpoints will return 503")
	}

	var healthDB handlers.DBPool
	if pool != nil {
		healthDB = pool
	}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379"
	}
	redisOpts, err := redis.ParseURL(redisURL)
	if err != nil {
		slog.Error("invalid REDIS_URL", "err", err)
		os.Exit(1)
	}
	redisClient := redis.NewClient(redisOpts)
	defer redisClient.Close()

	hub := ws.NewHub()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go ws.StartConsumer(ctx, redisClient, hub)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", handlers.Health(healthDB))
	mux.HandleFunc("GET /v1/events", handlers.ListEvents)
	mux.HandleFunc("GET /v1/events/{id}", handlers.GetEvent)
	mux.HandleFunc("GET /v1/events/stream", handlers.Stream(redisClient))
	mux.HandleFunc("GET /v1/admin/db", handlers.AdminDB(adminConfig()))
	mux.HandleFunc("/ws", ws.Handler(hub))

	handler := middleware.Chain(mux, middleware.StructuredLogging, middleware.RequestID)

	server := &http.Server{
		Addr:         fmt.Sprintf(":%s", port),
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	go func() {
		slog.Info("Trident API server listening", "port", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "err", err)
	}
}

func newDBPool(ctx context.Context, dsn string, poolSize int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.MaxConns = poolSize
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

func dbPoolSizeFromEnv() int32 {
	if raw := os.Getenv("GO_API_DB_POOL_SIZE"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return int32(n)
		}
		slog.Warn("invalid GO_API_DB_POOL_SIZE; using default", "value", raw, "default", defaultDBPoolSize)
	}
	return defaultDBPoolSize
}

func adminConfig() handlers.AdminConfig {
	cfg := handlers.AdminConfig{AdminKey: os.Getenv("ADMIN_API_KEY")}
	if adminURL := os.Getenv("PGBOUNCER_ADMIN_URL"); adminURL != "" {
		cfg.StatsFunc = newPgbouncerStats(adminURL)
	}
	return cfg
}
