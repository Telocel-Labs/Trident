package middleware_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Depo-dev/trident/services/api/middleware"
)

func hashKey(salt, key string) string {
	mac := hmac.New(sha256.New, []byte(salt))
	mac.Write([]byte(key))
	return hex.EncodeToString(mac.Sum(nil))
}

func okHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func TestAPIKey_validKeyPasses(t *testing.T) {
	const salt = "testsalt"
	const key = "my-secret-key"

	t.Setenv("API_KEY_SALT", salt)
	t.Setenv("API_KEY_HASHES", hashKey(salt, key))

	handler := middleware.APIKey(http.HandlerFunc(okHandler))

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.Header.Set("X-API-Key", key)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestAPIKey_missingHeader_returns401(t *testing.T) {
	t.Setenv("API_KEY_SALT", "testsalt")
	t.Setenv("API_KEY_HASHES", "somehash")

	handler := middleware.APIKey(http.HandlerFunc(okHandler))

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAPIKey_invalidKey_returns401(t *testing.T) {
	t.Setenv("API_KEY_SALT", "testsalt")
	t.Setenv("API_KEY_HASHES", hashKey("testsalt", "correct-key"))

	handler := middleware.APIKey(http.HandlerFunc(okHandler))

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.Header.Set("X-API-Key", "wrong-key")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAPIKey_healthSkipsAuth(t *testing.T) {
	t.Setenv("API_KEY_SALT", "testsalt")
	t.Setenv("API_KEY_HASHES", "") // no valid keys

	handler := middleware.APIKey(http.HandlerFunc(okHandler))

	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on /v1/health without key, got %d", rec.Code)
	}
}
