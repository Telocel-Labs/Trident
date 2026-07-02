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
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
)

const auditCleanupInterval = 6 * time.Hour

const defaultDBPoolSize = 5

func initTracer(ctx context.Context) func() {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		return func() {}
	}

	samplingRatio := 0.1
	if r := os.Getenv("OTEL_SAMPLING_RATIO"); r != "" {
		if f, err := strconv.ParseFloat(r, 64); err == nil {
			samplingRatio = f
		}
	}

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		slog.Warn("failed to create OTLP trace exporter", "err", err)
		return func() {}
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName("trident-go-api")),
	)
	if err != nil {
		slog.Warn("failed to create OTel resource", "err", err)
		res = resource.Default()
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(samplingRatio)),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	return func() { _ = tp.Shutdown(context.Background()) }
}

func main() {
	shutdownTracer := initTracer(context.Background())
	defer shutdownTracer()

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

	// Start API-key usage tracker (issue #139). Flushes request_count /
	// last_used_at to postgres in batches every 5s so auth never blocks.
	var usageTrack chan<- string
	var usageStop func()
	if pool != nil {
		usageTrack, usageStop = handlers.NewAPIKeyUsageTracker(pool, 5*time.Second)
		defer usageStop()
	}

	// Start async audit log writer (issue #162). Batches entries every 500ms
	// and inserts them in bulk — zero latency added to the request path.
	var auditWriter *middleware.AuditWriter
	if pool != nil {
		auditWriter = middleware.NewAuditWriter(
			pool, slog.Default(), 500*time.Millisecond, 100, 10000,
		)
		defer auditWriter.Close()
		// Background cleanup: delete audit log entries older than 90 days.
		go runAuditCleanup(ctx, pool)
	}

	adminCfg := handlers.AdminConfig{
		AdminKey: os.Getenv("ADMIN_API_KEY"),
		DB:       pool,
	}
	if adminURL := os.Getenv("PGBOUNCER_ADMIN_URL"); adminURL != "" {
		adminCfg.StatsFunc = newPgbouncerStats(adminURL)
	}

	apiKeyCfg := handlers.APIKeyConfig{
		AdminKey: os.Getenv("ADMIN_API_KEY"),
		DB:       pool,
		Redis:    redisClient,
	}

	webhookDB, err := newDB()
	if err != nil {
		slog.Warn("database unavailable for webhook handlers", "err", err)
	} else {
		defer webhookDB.Close()
	}
	startWebhookWorker(ctx, webhookDB, redisClient)
	startWebhookCleanupJob(ctx, webhookDB)

	// Configure internal status handler with dependencies.
	handlers.SetInternalStatusDeps(pool, redisClient, hub)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", handlers.Health(healthDB, redisClient, grpcClient))
	mux.HandleFunc("GET /v1/events", handlers.ListEvents)
	mux.HandleFunc("POST /v1/events/batch", handlers.BatchGetEvents)
	mux.HandleFunc("GET /v1/events/{id}", handlers.GetEvent)
	mux.HandleFunc("GET /v1/events/stream", handlers.Stream(redisClient))
	mux.HandleFunc("GET /v1/admin/db", handlers.AdminDB(adminCfg))
	mux.HandleFunc("GET /v1/admin/keys/{id}/usage", handlers.AdminKeyUsage(adminCfg))
	// API key management (admin-only via X-Admin-Key header)
	mux.HandleFunc("POST /v1/api-keys", handlers.CreateAPIKey(apiKeyCfg))
	mux.HandleFunc("GET /v1/api-keys", handlers.ListAPIKeys(apiKeyCfg))
	mux.HandleFunc("PATCH /v1/api-keys/{id}", handlers.UpdateAPIKey(apiKeyCfg))
	mux.HandleFunc("DELETE /v1/api-keys/{id}", handlers.DeleteAPIKey(apiKeyCfg))
	mux.HandleFunc("GET /v1/stats/indexer", handlers.IndexerStats(healthDB))
	mux.HandleFunc("GET /v1/stats/contracts", handlers.ContractsStats(pool, redisClient))
	mux.HandleFunc("GET /v1/webhooks", listWebhooksHandler(webhookDB))
	mux.HandleFunc("POST /v1/webhooks", createWebhookHandler(webhookDB))
	mux.HandleFunc("DELETE /v1/webhooks/{id}", deleteWebhookHandler(webhookDB))
	mux.HandleFunc("PATCH /v1/webhooks/{id}/pause", pauseWebhookHandler(webhookDB))
	mux.HandleFunc("PATCH /v1/webhooks/{id}/resume", resumeWebhookHandler(webhookDB))
	mux.HandleFunc("GET /v1/webhooks/{id}/deliveries", deliveriesWebhookHandler(webhookDB))
	mux.HandleFunc("GET /metrics", handlers.MetricsHandler())
	mux.HandleFunc("GET /internal/status", handlers.InternalStatus())
	mux.Handle("/ws", middleware.WSConnectionLimit(ws.Handler(hub)))
	keyValidator := middleware.Validator(middleware.ParseKeyHashes(os.Getenv("API_KEY_HASHES")))
	mux.Handle("/graphql", middleware.WSConnectionLimit(ws.GraphQLHandler(hub, keyValidator)))

	_ = usageTrack // passed to middleware in future; declared for shutdown ordering

	var rlDB middleware.TierDB
	if pool != nil {
		rlDB = pool
	}
	rlCfg := middleware.RateLimitConfig{Redis: redisClient, DB: rlDB}

	// DB-backed auth middleware with Redis caching and env-var fallback.
	var authDB middleware.DBAuthConfig
	if pool != nil {
		authDB.DB = pool
	}
	authDB.Redis = redisClient

	handler := middleware.Chain(mux, middleware.StructuredLogging, middleware.RequestID)
	handler = middleware.TieredRateLimit(rlCfg)(handler)
	if auditWriter != nil {
		handler = middleware.AuditMiddleware(auditWriter)(handler)
	}
	handler = middleware.NewDBAuth(authDB)(handler)
	handler = middleware.NewCORSFromEnv()(middleware.NewTimeoutFromEnv()(handler))

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

func runAuditCleanup(ctx context.Context, pool *pgxpool.Pool) {
	ticker := time.NewTicker(auditCleanupInterval)
	defer ticker.Stop()

	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		for {
			tag, err := pool.Exec(cleanupCtx,
				`DELETE FROM audit_log WHERE ts < NOW() - INTERVAL '90 days' AND ctid IN (
					SELECT ctid FROM audit_log WHERE ts < NOW() - INTERVAL '90 days' LIMIT 1000
				)`,
			)
			if err != nil {
				slog.Warn("audit cleanup failed", "err", err)
				return
			}
			if tag.RowsAffected() == 0 {
				return
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			cleanup()
			return
		case <-ticker.C:
			cleanup()
		}
	}
}
