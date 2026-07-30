package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInternalStatus_NotConfigured_Returns401(t *testing.T) {
	t.Setenv("INTERNAL_API_KEY", "")

	req := httptest.NewRequest(http.MethodGet, "/internal/status", nil)
	rr := httptest.NewRecorder()
	InternalStatus()(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401 when INTERNAL_API_KEY unset, got %d", rr.Code)
	}
}

func TestInternalStatus_MissingKey_Returns401(t *testing.T) {
	t.Setenv("INTERNAL_API_KEY", "internal-secret")

	req := httptest.NewRequest(http.MethodGet, "/internal/status", nil)
	rr := httptest.NewRecorder()
	InternalStatus()(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401 for missing X-Internal-Key, got %d", rr.Code)
	}
}

func TestInternalStatus_WrongKey_Returns401(t *testing.T) {
	t.Setenv("INTERNAL_API_KEY", "internal-secret")

	req := httptest.NewRequest(http.MethodGet, "/internal/status", nil)
	req.Header.Set("X-Internal-Key", "wrong-key")
	rr := httptest.NewRecorder()
	InternalStatus()(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401 for wrong X-Internal-Key, got %d", rr.Code)
	}
}

func TestInternalStatus_ValidKey_Returns200(t *testing.T) {
	t.Setenv("INTERNAL_API_KEY", "internal-secret")

	req := httptest.NewRequest(http.MethodGet, "/internal/status", nil)
	req.Header.Set("X-Internal-Key", "internal-secret")
	rr := httptest.NewRecorder()
	InternalStatus()(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("want 200 for valid X-Internal-Key, got %d", rr.Code)
	}
}

func TestInternalStatus_NoRawKeyLeakage(t *testing.T) {
	const rawInternalKey = "trident-internal-status-super-secret-value"
	t.Setenv("INTERNAL_API_KEY", rawInternalKey)

	tests := []struct {
		name        string
		providedKey string
		wantStatus  int
	}{
		{name: "wrong key", providedKey: "definitely-not-the-right-key", wantStatus: http.StatusUnauthorized},
		{name: "correct key", providedKey: rawInternalKey, wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/internal/status", nil)
			req.Header.Set("X-Internal-Key", tt.providedKey)
			rr := httptest.NewRecorder()
			InternalStatus()(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("want %d, got %d", tt.wantStatus, rr.Code)
			}
			if strings.Contains(rr.Body.String(), rawInternalKey) {
				t.Errorf("raw internal key leaked into response body: %q", rr.Body.String())
			}
		})
	}
}

func TestInternalStatus_UnsetKey_EmptyProvidedKey_StillRejected(t *testing.T) {
	t.Setenv("INTERNAL_API_KEY", "")

	req := httptest.NewRequest(http.MethodGet, "/internal/status", nil)
	req.Header.Set("X-Internal-Key", "")
	rr := httptest.NewRecorder()
	InternalStatus()(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 when both configured and provided key are empty, got %d: %s", rr.Code, rr.Body.String())
	}
}
