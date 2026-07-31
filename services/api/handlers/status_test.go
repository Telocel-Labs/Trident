package handlers_test
package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Depo-dev/trident/services/api/handlers"
)

func TestInternalStatus_NotConfigured_Returns401(t *testing.T) {
	t.Setenv("INTERNAL_API_KEY", "")

	req := httptest.NewRequest(http.MethodGet, "/internal/status", nil)
	rr := httptest.NewRecorder()
	handlers.InternalStatus()(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401 when INTERNAL_API_KEY unset, got %d", rr.Code)
	}
}

func TestInternalStatus_MissingKey_Returns401(t *testing.T) {
	t.Setenv("INTERNAL_API_KEY", "internal-secret")

	req := httptest.NewRequest(http.MethodGet, "/internal/status", nil)
	rr := httptest.NewRecorder()
	handlers.InternalStatus()(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401 for missing X-Internal-Key, got %d", rr.Code)
	}
}

func TestInternalStatus_WrongKey_Returns401(t *testing.T) {
	t.Setenv("INTERNAL_API_KEY", "internal-secret")

	req := httptest.NewRequest(http.MethodGet, "/internal/status", nil)
	req.Header.Set("X-Internal-Key", "wrong-key")
	rr := httptest.NewRecorder()
	handlers.InternalStatus()(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("want 401 for wrong X-Internal-Key, got %d", rr.Code)
	}
}

func TestInternalStatus_ValidKey_Returns200(t *testing.T) {
	t.Setenv("INTERNAL_API_KEY", "internal-secret")

	req := httptest.NewRequest(http.MethodGet, "/internal/status", nil)
	req.Header.Set("X-Internal-Key", "internal-secret")
	rr := httptest.NewRecorder()
	handlers.InternalStatus()(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("want 200 for valid X-Internal-Key, got %d", rr.Code)
	}
}

// TestInternalStatus_NoRawKeyLeakage sends a distinctive, known
// X-Internal-Key value (both a wrong one on a 401 response and the correct
// one on a 200 response) and asserts the raw key string never appears
// anywhere in the response body. The internal key is only ever compared via
// validAdminKey's constant-time check and never echoed, logged, or embedded
// in an error message, so this should hold for both outcomes.
func TestInternalStatus_NoRawKeyLeakage(t *testing.T) {
	const rawInternalKey = "trident-internal-status-super-secret-value"
	t.Setenv("INTERNAL_API_KEY", rawInternalKey)

	t.Run("wrong key on 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/internal/status", nil)
		req.Header.Set("X-Internal-Key", "definitely-not-the-right-key")
		rr := httptest.NewRecorder()
		handlers.InternalStatus()(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d", rr.Code)
		}
		if strings.Contains(rr.Body.String(), rawInternalKey) {
			t.Errorf("raw internal key leaked into 401 response body: %q", rr.Body.String())
		}
	})

	t.Run("correct key on 200", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/internal/status", nil)
		req.Header.Set("X-Internal-Key", rawInternalKey)
		rr := httptest.NewRecorder()
		handlers.InternalStatus()(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("want 200, got %d", rr.Code)
		}
		if strings.Contains(rr.Body.String(), rawInternalKey) {
			t.Errorf("raw internal key leaked into 200 response body: %q", rr.Body.String())
		}
	})
	"testing"
)

// These tests exercise the X-Internal-Key check on GET /internal/status
// directly (package handlers, not handlers_test) since statusDeps is
// unexported. statusDeps is left nil throughout, which is fine: the auth
// check runs before any dependency is touched.

func TestInternalStatus_CorrectKey_Returns200(t *testing.T) {
	t.Setenv("INTERNAL_API_KEY", "correct-horse-battery-staple")

	req := httptest.NewRequest(http.MethodGet, "/internal/status", nil)
	req.Header.Set("X-Internal-Key", "correct-horse-battery-staple")
	rr := httptest.NewRecorder()

	InternalStatus()(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestInternalStatus_WrongKey_Returns401(t *testing.T) {
	t.Setenv("INTERNAL_API_KEY", "correct-horse-battery-staple")

	req := httptest.NewRequest(http.MethodGet, "/internal/status", nil)
	req.Header.Set("X-Internal-Key", "wrong-key")
	rr := httptest.NewRecorder()

	InternalStatus()(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestInternalStatus_MissingHeader_Returns401(t *testing.T) {
	t.Setenv("INTERNAL_API_KEY", "correct-horse-battery-staple")

	req := httptest.NewRequest(http.MethodGet, "/internal/status", nil)
	rr := httptest.NewRecorder()

	InternalStatus()(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestInternalStatus_UnsetKey_FailsClosed(t *testing.T) {
	// INTERNAL_API_KEY unset entirely -> every request must be rejected,
	// even one that (implausibly) sends an empty X-Internal-Key.
	t.Setenv("INTERNAL_API_KEY", "")

	req := httptest.NewRequest(http.MethodGet, "/internal/status", nil)
	// Deliberately do not set X-Internal-Key at all.
	rr := httptest.NewRecorder()

	InternalStatus()(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 (fail closed), got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestInternalStatus_UnsetKey_EmptyProvidedKey_StillRejected(t *testing.T) {
	// Guards against a regression where an empty expected key and an empty
	// provided key would compare equal and incorrectly authenticate.
	t.Setenv("INTERNAL_API_KEY", "")

	req := httptest.NewRequest(http.MethodGet, "/internal/status", nil)
	req.Header.Set("X-Internal-Key", "")
	rr := httptest.NewRecorder()

	InternalStatus()(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 (fail closed), got %d: %s", rr.Code, rr.Body.String())
	}
}
