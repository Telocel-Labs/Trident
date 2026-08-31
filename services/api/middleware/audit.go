package middleware

import (
	"context"
	"database/sql"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Depo-dev/trident/services/api/internal/httputil"
	"github.com/google/uuid"
)

type contextKey string

const (
	apiKeyIDKey contextKey = "audit_api_key_id"
	networkKey   contextKey = "audit_network"
)

// AuditEntry represents an audit log row recorded in the database.
type AuditEntry struct {
	APIKeyID   *uuid.UUID
	Endpoint   string
	Method     string
	IP         string
	StatusCode int
	DurationMs int64
	UserAgent  string
	RequestID  string
	Timestamp  time.Time
}

// AuditWriter asynchronously writes audit log entries to the database.
type AuditWriter struct {
	ch     chan AuditEntry
	pool   *sql.DB
	logger *slog.Logger
	wg     sync.WaitGroup
}

// NewAuditWriter creates an AuditWriter and starts its background flusher.
func NewAuditWriter(pool *sql.DB, logger *slog.Logger, bufferSize int) *AuditWriter
{
	aw := &AuditWriter{
		ch:     make(chan AuditEntry, bufferSize),
		pool:   pool,
		logger: logger,
	}
	aw.wg.Add(1)
	go aw.flushLoop()
	return aw
}

func (aw *AuditWriter) flushLoop() {
	defer aw.wg.Done()
	for entry := range aw.ch {
		if aw.pool == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, err := aw.pool.ExecContext(ctx,
			`INSERT INTO audit_log (api_key_id, endpoint, method, ip, status_code, duration_ms, user_agent, request_id, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			entry.APIKeyID, entry.Endpoint, entry.Method, entry.IP, entry.StatusCode,
			entry.DurationMs, entry.UserAgent, entry.RequestID, entry.Timestamp,
		)
		cancel()
		if err != nil {
			aw.logger.Error("failed to write audit log entry", "err", err, "request_id", entry.RequestID)
		}
	}
}

// Write non-blockingly enqueues an audit entry.
func (aw *AuditWriter) Write(entry AuditEntry) {
	select {
	case aw.ch <- entry:
	default:
		aw.logger.Warn("audit log channel full, dropping entry", "endpoint", entry.Endpoint, "request_id", entry.RequestID)
	}
}

// Close flushes and shuts down the audit writer.
func (aw *AuditWriter) Close() {
	close(aw.ch)
	aw.wg.Wait()
}

// auditResponseWriter wraps http.ResponseWriter to capture status code.
type auditResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *auditResponseWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

// AuditMiddleware records request telemetry into the audit log.
func AuditMiddleware(aw *AuditWriter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			arw := &auditResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}

			next.ServeHTTP(arw, r)

			duration := time.Since(start).Milliseconds()
			reqID := httputil.RequestIDFromContext(r.Context())

			aw.Write(AuditEntry{
				APIKeyID:   AuditAPIKeyIDFromContext(r.Context()),
				Endpoint:   r.URL.Path,
				Method:     r.Method,
				IP:         ExtractClientIP(r),
				StatusCode: arw.statusCode,
				DurationMs: duration,
				UserAgent:  r.UserAgent(),
				RequestID:  reqID,
				Timestamp:  start,
			})
		})
	}
}

func ExtractClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			ip := strings.TrimSpace(parts[0])
			if parsed := net.ParseIP(ip); parsed != nil {
				return ip
			}
		}
	}
	if xrip := r.Header.Get("X-Real-IP"); xrip != "" {
		return strings.TrimSpace(xrip)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func WithAuditAPIKeyID(ctx context.Context, id *uuid.UUID) context.Context {
	return context.WithValue(ctx, apiKeyIDKey, id)
}

func AuditAPIKeyIDFromContext(ctx context.Context) *uuid.UUID {
	val, _ := ctx.Value(apiKeyIDKey).(*uuid.UUID)
	return val
}

func WithAuditNetwork(ctx context.Context, network string) context.Context {
	return context.WithValue(ctx, networkKey, network)
}

func AuditNetworkFromContext(ctx context.Context) string {
	val, _ := ctx.Value(networkKey).(string)
	return val
}
