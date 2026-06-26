package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Depo-dev/trident/services/api/middleware"
)

func TestRequestIDMiddleware(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := r.Context().Value(middleware.RequestIDCtxKey).(string)
		if !ok || id == "" {
			t.Fatal("request ID not found in context")
		}
		w.WriteHeader(http.StatusOK)
	})

	wrapped := middleware.RequestID(handler)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()

	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	if rr.Header().Get(middleware.RequestIDHeader) == "" {
		t.Error("X-Request-ID header not set")
	}
}

func TestRequestIDMiddlewareUnique(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := middleware.RequestID(handler)

	ids := make(map[string]bool)
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rr := httptest.NewRecorder()
		wrapped.ServeHTTP(rr, req)
		id := rr.Header().Get(middleware.RequestIDHeader)
		if id == "" {
			t.Fatal("X-Request-ID header not set")
		}
		if ids[id] {
			t.Error("duplicate request ID generated")
		}
		ids[id] = true
	}
}

func TestStructuredLoggingMiddleware(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := middleware.StructuredLogging(handler)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()

	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestChainMiddleware(t *testing.T) {
	callOrder := []string{}

	middleware1 := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callOrder = append(callOrder, "mid1")
			next.ServeHTTP(w, r)
		})
	}

	middleware2 := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callOrder = append(callOrder, "mid2")
			next.ServeHTTP(w, r)
		})
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callOrder = append(callOrder, "handler")
		w.WriteHeader(http.StatusOK)
	})

	chained := middleware.Chain(handler, middleware1, middleware2)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()

	chained.ServeHTTP(rr, req)

	expected := []string{"mid2", "mid1", "handler"}
	for i, v := range expected {
		if i >= len(callOrder) || callOrder[i] != v {
			t.Errorf("call order mismatch: expected %v, got %v", expected, callOrder)
			break
		}
	}
}
