package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Depo-dev/trident/services/api/middleware"
)

func TestAdminAuth_MultiOperatorAuthentication(t *testing.T) {
	operators := map[string]string{
		"alice":   "key-alice-secret-token-123",
		"bob":     "key-bob-secret-token-456",
		"charlie": "key-charlie-secret-token-789",
	}

	auth := NewAdminAuth("master-admin-key", operators, nil, false)

	// Case 1: Alice's key resolves to operator "alice"
	req1 := httptest.NewRequest(http.MethodGet, "/v1/admin/db", nil)
	req1.Header.Set("X-Admin-Key", "key-alice-secret-token-123")
	w1 := httptest.NewRecorder()

	op1, ok1 := auth.Authorize(w1, req1)
	if !ok1 || op1 != "alice" {
		t.Fatalf("expected operator alice, got %q (ok=%v)", op1, ok1)
	}
	if w1.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w1.Code)
	}
	authSource1 := middleware.AuditAuthSourceFromContext(req1.Context())
	if authSource1 != "admin:alice" {
		t.Errorf("expected audit auth_source 'admin:alice', got %q", authSource1)
	}

	// Case 2: Bob's key resolves to operator "bob"
	req2 := httptest.NewRequest(http.MethodGet, "/v1/admin/db", nil)
	req2.Header.Set("X-Admin-Key", "key-bob-secret-token-456")
	w2 := httptest.NewRecorder()

	op2, ok2 := auth.Authorize(w2, req2)
	if !ok2 || op2 != "bob" {
		t.Fatalf("expected operator bob, got %q (ok=%v)", op2, ok2)
	}
	authSource2 := middleware.AuditAuthSourceFromContext(req2.Context())
	if authSource2 != "admin:bob" {
		t.Errorf("expected audit auth_source 'admin:bob', got %q", authSource2)
	}

	// Case 3: Master admin key with operator hint
	req3 := httptest.NewRequest(http.MethodGet, "/v1/admin/db", nil)
	req3.Header.Set("X-Admin-Key", "master-admin-key")
	req3.Header.Set("X-Admin-Operator", "sre-oncall")
	w3 := httptest.NewRecorder()

	op3, ok3 := auth.Authorize(w3, req3)
	if !ok3 || op3 != "sre-oncall" {
		t.Fatalf("expected operator sre-oncall, got %q (ok=%v)", op3, ok3)
	}
	authSource3 := middleware.AuditAuthSourceFromContext(req3.Context())
	if authSource3 != "admin:sre-oncall" {
		t.Errorf("expected audit auth_source 'admin:sre-oncall', got %q", authSource3)
	}

	// Case 4: Master admin key without hint defaults to "admin"
	req4 := httptest.NewRequest(http.MethodGet, "/v1/admin/db", nil)
	req4.Header.Set("X-Admin-Key", "master-admin-key")
	w4 := httptest.NewRecorder()

	op4, ok4 := auth.Authorize(w4, req4)
	if !ok4 || op4 != "admin" {
		t.Fatalf("expected operator admin, got %q (ok=%v)", op4, ok4)
	}
	authSource4 := middleware.AuditAuthSourceFromContext(req4.Context())
	if authSource4 != "admin:admin" {
		t.Errorf("expected audit auth_source 'admin:admin', got %q", authSource4)
	}

	// Case 5: Invalid key rejected with 401
	req5 := httptest.NewRequest(http.MethodGet, "/v1/admin/db", nil)
	req5.Header.Set("X-Admin-Key", "bogus-key")
	w5 := httptest.NewRecorder()

	op5, ok5 := auth.Authorize(w5, req5)
	if ok5 {
		t.Fatalf("expected rejection for bogus key, got operator %q", op5)
	}
	if w5.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", w5.Code)
	}

	// Case 6: Missing key rejected with 401
	req6 := httptest.NewRequest(http.MethodGet, "/v1/admin/db", nil)
	w6 := httptest.NewRecorder()

	op6, ok6 := auth.Authorize(w6, req6)
	if ok6 {
		t.Fatalf("expected rejection for missing key, got operator %q", op6)
	}
	if w6.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", w6.Code)
	}
}

func TestAdminAuth_CIDRAllowlist(t *testing.T) {
	allowedCIDRs := []string{"10.0.0.0/8", "192.168.1.0/24"}
	auth := NewAdminAuth("secret-admin-key", nil, allowedCIDRs, false)

	// Allowed IP (192.168.1.42)
	reqGood := httptest.NewRequest(http.MethodGet, "/v1/admin/db", nil)
	reqGood.RemoteAddr = "192.168.1.42:12345"
	reqGood.Header.Set("X-Admin-Key", "secret-admin-key")
	wGood := httptest.NewRecorder()

	_, okGood := auth.Authorize(wGood, reqGood)
	if !okGood {
		t.Fatalf("expected request from allowlisted IP to pass, failed with status %d", wGood.Code)
	}

	// Disallowed public IP (203.0.113.195)
	reqBad := httptest.NewRequest(http.MethodGet, "/v1/admin/db", nil)
	reqBad.RemoteAddr = "203.0.113.195:54321"
	reqBad.Header.Set("X-Admin-Key", "secret-admin-key")
	wBad := httptest.NewRecorder()

	_, okBad := auth.Authorize(wBad, reqBad)
	if okBad {
		t.Fatalf("expected request from public IP to be blocked")
	}
	if wBad.Code != http.StatusForbidden {
		t.Fatalf("expected status 403 Forbidden, got %d", wBad.Code)
	}
}

func TestAdminAuth_BruteforceLockout(t *testing.T) {
	auth := NewAdminAuth("secret-admin-key", nil, nil, false)
	clientIP := "198.51.100.22:9876"

	// 5 failed attempts
	for i := 1; i <= 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/admin/db", nil)
		req.RemoteAddr = clientIP
		req.Header.Set("X-Admin-Key", "wrong-password")
		w := httptest.NewRecorder()

		_, ok := auth.Authorize(w, req)
		if ok {
			t.Fatalf("attempt %d should fail", i)
		}
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d expected 401, got %d", i, w.Code)
		}
	}

	// 6th attempt: even with CORRECT key, should be locked out with 429
	reqLock := httptest.NewRequest(http.MethodGet, "/v1/admin/db", nil)
	reqLock.RemoteAddr = clientIP
	reqLock.Header.Set("X-Admin-Key", "secret-admin-key")
	wLock := httptest.NewRecorder()

	_, okLock := auth.Authorize(wLock, reqLock)
	if okLock {
		t.Fatalf("expected locked out request to be rejected")
	}
	if wLock.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests, got %d", wLock.Code)
	}
}

func TestAdminAuth_UnconfiguredReturns503(t *testing.T) {
	auth := NewAdminAuth("", nil, nil, false)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/db", nil)
	w := httptest.NewRecorder()

	_, ok := auth.Authorize(w, req)
	if ok {
		t.Fatalf("expected unconfigured auth to fail")
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestAdminAuth_ParseOperators(t *testing.T) {
	// JSON format
	jsonInput := `{"alice": "key1", "bob": "key2"}`
	resJSON := parseOperators(jsonInput)
	if len(resJSON) != 2 || resJSON["alice"] != "key1" || resJSON["bob"] != "key2" {
		t.Errorf("failed to parse JSON operators: %v", resJSON)
	}

	// CSV format
	csvInput := "alice:key1,bob=key2, charlie : key3 "
	resCSV := parseOperators(csvInput)
	if len(resCSV) != 3 || resCSV["alice"] != "key1" || resCSV["bob"] != "key2" || resCSV["charlie"] != "key3" {
		t.Errorf("failed to parse CSV operators: %v", resCSV)
	}
}

func TestAdminDB_WithAdminAuth(t *testing.T) {
	auth := NewAdminAuth("admin-secret", map[string]string{"carol": "carol-key"}, nil, false)
	cfg := AdminConfig{
		Auth: auth,
		StatsFunc: func(ctx context.Context) (*DBStats, error) {
			return &DBStats{Pools: []map[string]any{}, Stats: []map[string]any{}}, nil
		},
	}

	handler := AdminDB(cfg)

	// Valid Carol key
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/db", nil)
	req.Header.Set("X-Admin-Key", "carol-key")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	authSource := middleware.AuditAuthSourceFromContext(req.Context())
	if authSource != "admin:carol" {
		t.Errorf("expected authSource admin:carol, got %q", authSource)
	}
}
