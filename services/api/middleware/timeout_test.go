package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTimeout_FastHandler(t *testing.T) {
	handler := Timeout(100*time.Millisecond, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/fast", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestTimeout_SlowHandler_Returns408(t *testing.T) {
	handler := Timeout(50*time.Millisecond, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(200 * time.Millisecond):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
		}
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/slow", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestTimeout {
		t.Fatalf("expected 408, got %d", rec.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object, got %v", body)
	}
	if errObj["code"] != "TIMEOUT" {
		t.Errorf("expected code TIMEOUT, got %v", errObj["code"])
	}
	if errObj["message"] != "request timed out" {
		t.Errorf("expected message 'request timed out', got %v", errObj["message"])
	}
}

func TestTimeout_WebSocketPathExcluded(t *testing.T) {
	called := false
	handler := Timeout(50*time.Millisecond, []string{"/ws"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("handler was not called")
	}
	if rec.Code != http.StatusSwitchingProtocols {
		t.Fatalf("expected 101, got %d", rec.Code)
	}
}

func TestTimeout_SSEPathExcluded(t *testing.T) {
	called := false
	handler := Timeout(50*time.Millisecond, []string{"/ws", "/v1/events/stream"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/events/stream", nil)
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("handler was not called")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestTimeout_DoubleWriteProtection(t *testing.T) {
	handler := Timeout(50*time.Millisecond, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		select {
		case <-time.After(200 * time.Millisecond):
		case <-r.Context().Done():
		}
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/partial", nil)

	// Should not panic and should keep the first status code written
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic during double-write: %v", r)
		}
	}()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 (first write wins), got %d", rec.Code)
	}
}

func TestTimeout_CustomEnvValue(t *testing.T) {
	t.Setenv("REQUEST_TIMEOUT_MS", "50")

	mw := NewTimeoutFromEnv()
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(200 * time.Millisecond):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
		}
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/custom", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestTimeout {
		t.Fatalf("expected 408 with 50ms timeout, got %d", rec.Code)
	}
}

func TestTimeout_DefaultEnvFallback(t *testing.T) {
	t.Setenv("REQUEST_TIMEOUT_MS", "not-a-number")

	mw := NewTimeoutFromEnv()
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/default", nil)
	handler.ServeHTTP(rec, req)

	// Fast handler should complete within the 30s default
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with default timeout, got %d", rec.Code)
	}
}

func TestTimeout_ContentTypeJSON(t *testing.T) {
	handler := Timeout(50*time.Millisecond, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(200 * time.Millisecond):
		case <-r.Context().Done():
		}
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/check-ct", nil)
	handler.ServeHTTP(rec, req)

	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected application/json content-type, got %q", ct)
	}
}
