package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler(called *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*called = true
		w.WriteHeader(http.StatusOK)
	})
}

func TestCORS(t *testing.T) {
	tests := []struct {
		name           string
		allowedOrigins []string
		method         string
		origin         string
		wantStatus     int
		wantOrigin     string
		wantMaxAge     string
		wantMethods    string
		wantHeaders    string
		wantNextCalled bool
	}{
		{
			name:           "wildcard: any origin reflected",
			allowedOrigins: nil,
			method:         http.MethodGet,
			origin:         "https://example.com",
			wantStatus:     http.StatusOK,
			wantOrigin:     "https://example.com",
			wantMaxAge:     "86400",
			wantMethods:    "GET, POST, PUT, PATCH, DELETE, OPTIONS",
			wantHeaders:    "Authorization, Content-Type, X-Request-ID",
			wantNextCalled: true,
		},
		{
			name:           "exact match: configured origin gets CORS headers",
			allowedOrigins: []string{"https://allowed.com"},
			method:         http.MethodGet,
			origin:         "https://allowed.com",
			wantStatus:     http.StatusOK,
			wantOrigin:     "https://allowed.com",
			wantMaxAge:     "86400",
			wantMethods:    "GET, POST, PUT, PATCH, DELETE, OPTIONS",
			wantHeaders:    "Authorization, Content-Type, X-Request-ID",
			wantNextCalled: true,
		},
		{
			name:           "origin rejection: unlisted origin gets 403",
			allowedOrigins: []string{"https://allowed.com"},
			method:         http.MethodGet,
			origin:         "https://evil.com",
			wantStatus:     http.StatusForbidden,
			wantOrigin:     "",
			wantNextCalled: false,
		},
		{
			name:           "OPTIONS preflight allowed: 204, CORS headers, next not called",
			allowedOrigins: nil,
			method:         http.MethodOptions,
			origin:         "https://example.com",
			wantStatus:     http.StatusNoContent,
			wantOrigin:     "https://example.com",
			wantMaxAge:     "86400",
			wantMethods:    "GET, POST, PUT, PATCH, DELETE, OPTIONS",
			wantHeaders:    "Authorization, Content-Type, X-Request-ID",
			wantNextCalled: false,
		},
		{
			name:           "OPTIONS preflight rejected: 403, next not called",
			allowedOrigins: []string{"https://allowed.com"},
			method:         http.MethodOptions,
			origin:         "https://evil.com",
			wantStatus:     http.StatusForbidden,
			wantOrigin:     "",
			wantNextCalled: false,
		},
		{
			name:           "no Origin header: passes through unchanged",
			allowedOrigins: []string{"https://allowed.com"},
			method:         http.MethodGet,
			origin:         "",
			wantStatus:     http.StatusOK,
			wantOrigin:     "",
			wantNextCalled: true,
		},
		{
			name:           "max-age header value equals 86400",
			allowedOrigins: nil,
			method:         http.MethodGet,
			origin:         "https://example.com",
			wantStatus:     http.StatusOK,
			wantMaxAge:     "86400",
			wantNextCalled: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nextCalled := false
			handler := CORS(tc.allowedOrigins)(okHandler(&nextCalled))

			req := httptest.NewRequest(tc.method, "/", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			if rr.Code != tc.wantStatus {
				t.Errorf("status: got %d, want %d", rr.Code, tc.wantStatus)
			}

			if tc.wantOrigin != "" {
				got := rr.Header().Get("Access-Control-Allow-Origin")
				if got != tc.wantOrigin {
					t.Errorf("Access-Control-Allow-Origin: got %q, want %q", got, tc.wantOrigin)
				}
				vary := rr.Header().Get("Vary")
				if vary != "Origin" {
					t.Errorf("Vary: got %q, want Origin", vary)
				}
			}

			if tc.wantMethods != "" {
				got := rr.Header().Get("Access-Control-Allow-Methods")
				if got != tc.wantMethods {
					t.Errorf("Access-Control-Allow-Methods: got %q, want %q", got, tc.wantMethods)
				}
			}
			if tc.wantHeaders != "" {
				got := rr.Header().Get("Access-Control-Allow-Headers")
				if got != tc.wantHeaders {
					t.Errorf("Access-Control-Allow-Headers: got %q, want %q", got, tc.wantHeaders)
				}
			}

			if tc.wantMaxAge != "" {
				got := rr.Header().Get("Access-Control-Max-Age")
				if got != tc.wantMaxAge {
					t.Errorf("Access-Control-Max-Age: got %q, want %q", got, tc.wantMaxAge)
				}
			}

			if nextCalled != tc.wantNextCalled {
				t.Errorf("next called: got %v, want %v", nextCalled, tc.wantNextCalled)
			}

			if tc.wantStatus == http.StatusForbidden {
				var body map[string]any
				if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
					t.Fatalf("failed to decode 403 body: %v", err)
				}
				errObj, ok := body["error"].(map[string]any)
				if !ok {
					t.Fatal("403 body missing 'error' object")
				}
				if errObj["code"] != "CORS_FORBIDDEN" {
					t.Errorf("error.code: got %v, want CORS_FORBIDDEN", errObj["code"])
				}
				if errObj["message"] != "origin not allowed" {
					t.Errorf("error.message: got %v, want 'origin not allowed'", errObj["message"])
				}
				ct := rr.Header().Get("Content-Type")
				if ct != "application/json" {
					t.Errorf("Content-Type: got %q, want application/json", ct)
				}
			}
		})
	}
}

func TestNewCORSFromEnv(t *testing.T) {
	t.Run("empty env = wildcard", func(t *testing.T) {
		t.Setenv("ALLOWED_ORIGINS", "")
		nextCalled := false
		handler := NewCORSFromEnv()(okHandler(&nextCalled))

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Origin", "https://anything.com")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("status: got %d, want 200", rr.Code)
		}
		if rr.Header().Get("Access-Control-Allow-Origin") != "https://anything.com" {
			t.Error("expected origin to be reflected in wildcard mode")
		}
	})

	t.Run("star env = wildcard", func(t *testing.T) {
		t.Setenv("ALLOWED_ORIGINS", "*")
		nextCalled := false
		handler := NewCORSFromEnv()(okHandler(&nextCalled))

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Origin", "https://anything.com")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("status: got %d, want 200", rr.Code)
		}
	})

	t.Run("comma-separated list parsed and trimmed", func(t *testing.T) {
		t.Setenv("ALLOWED_ORIGINS", " https://a.com , https://b.com ")
		nextCalled := false
		handler := NewCORSFromEnv()(okHandler(&nextCalled))

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Origin", "https://a.com")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("https://a.com should be allowed, got %d", rr.Code)
		}

		req2 := httptest.NewRequest(http.MethodGet, "/", nil)
		req2.Header.Set("Origin", "https://c.com")
		rr2 := httptest.NewRecorder()
		handler.ServeHTTP(rr2, req2)

		if rr2.Code != http.StatusForbidden {
			t.Errorf("https://c.com should be forbidden, got %d", rr2.Code)
		}
	})
}
