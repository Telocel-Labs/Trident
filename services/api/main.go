package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Depo-dev/trident/services/api/grpc"
	"github.com/Depo-dev/trident/services/api/handlers"
	"github.com/Depo-dev/trident/services/api/middleware"
	"github.com/Depo-dev/trident/services/api/ws"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}

	// ---------------------------------------------------------------------------
	// gRPC client connection
	// ---------------------------------------------------------------------------
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

	// ---------------------------------------------------------------------------
	// Postgres connection (health endpoint)
	// ---------------------------------------------------------------------------
	var dbConn *pgx.Conn
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		conn, err := pgx.Connect(ctx, dsn)
		cancel()
		if err != nil {
			slog.Warn("could not connect to database; health endpoint will return 503", "err", err)
		} else {
			dbConn = conn
			defer conn.Close(context.Background())
		}
	} else {
		slog.Warn("DATABASE_URL not set; health endpoint will return 503")
	}

	// ---------------------------------------------------------------------------
	// Redis client
	// ---------------------------------------------------------------------------
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

	// ---------------------------------------------------------------------------
	// WebSocket hub + Redis Streams consumer
	// ---------------------------------------------------------------------------
	hub := ws.NewHub()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go ws.StartConsumer(ctx, redisClient, hub)

	// ---------------------------------------------------------------------------
	// HTTP router with middleware
	// ---------------------------------------------------------------------------
	mux := http.NewServeMux()

	// GET /v1/health — indexer liveness (issue #62)
	mux.HandleFunc("GET /v1/health", handlers.Health(dbConn))

	// GET /v1/events — validated, cursor-paginated event listing (issues #42, #44)
	mux.HandleFunc("GET /v1/events", handlers.ListEvents)

	// GET /v1/events/{id} — single event by UUID v4 (issue #42)
	mux.HandleFunc("GET /v1/events/{id}", handlers.GetEvent)

	// WebSocket: /ws — real-time event subscription endpoint (issue #15)
	mux.HandleFunc("/ws", ws.Handler(hub))

	// Apply middleware chain: request ID → structured logging
	handler := middleware.Chain(
		mux,
		middleware.StructuredLogging,
		middleware.RequestID,
	)

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
