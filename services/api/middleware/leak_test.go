package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestNoRawKeyLeakage exercises the real request path (NewDBAuth followed by
// AuditMiddleware, mirroring how main.go wires them) with a known, distinctive
// X-API-Key value and asserts that value never appears in:
//   - the structured logger output the middleware stack writes to,
//   - the JSON error body returned to the client,
//   - the fields captured on the audit log entry that gets queued for
//     persistence.
//
// This guards against the raw secret leaking into logs, error responses, or
// the audit trail — only its SHA-256/HMAC-SHA256 hash (or the opaque
// api_key_id derived from it) should ever be observable outside the request.
func TestNoRawKeyLeakage(t *testing.T) {
	const rawKey = "trident_super-secret-raw-key-should-never-appear-anywhere"

	// Use the legacy env-var fallback path (step 3 of NewDBAuth) since it's
	// the one that historically did a hash-map lookup on a hashed key; DB and
	// Redis are both nil so lookups 1 and 2 are skipped entirely.
	_ = os.Setenv("API_KEY_SALT", "leak-test-salt")
	// Intentionally do NOT include rawKey's hash, so the request is rejected
	// with 401 — the failure path is exactly where a naive implementation
	// might be tempted to log the offending raw key for debugging.
	_ = os.Setenv("API_KEY_HASHES", hmacKeyHash("some-other-key"))

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	entries := make(chan AuditEntry, 10)
	aw := &AuditWriter{
		ch:     entries,
		pool:   nil,
		logger: logger,
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	handler := AuditMiddleware(aw)(NewDBAuth(DBAuthConfig{})(inner))

	req := httptest.NewRequest(http.MethodGet, "/v1/events/stream", nil)
	req.Header.Set("X-API-Key", rawKey)
	req.Header.Set("User-Agent", "leak-test-agent")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unrecognized key, got %d", rec.Code)
	}

	body := rec.Body.String()
	if strings.Contains(body, rawKey) {
		t.Errorf("raw API key leaked into error response body: %q", body)
	}

	if strings.Contains(logBuf.String(), rawKey) {
		t.Errorf("raw API key leaked into logger output: %q", logBuf.String())
	}

	select {
	case entry := <-entries:
		if strings.Contains(entry.Endpoint, rawKey) ||
			strings.Contains(entry.Method, rawKey) ||
			strings.Contains(entry.IP, rawKey) ||
			strings.Contains(entry.UserAgent, rawKey) ||
			strings.Contains(entry.RequestID, rawKey) ||
			strings.Contains(entry.Network, rawKey) {
			t.Errorf("raw API key leaked into an audit log entry field: %+v", entry)
		}
	default:
		t.Fatal("expected an audit entry to have been queued")
	}
}
