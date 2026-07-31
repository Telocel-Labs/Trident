package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// echoReadHandler reads the whole body (as a real JSON-decoding handler
// would) and reports whether the read failed with the MaxBytesReader error.
func echoReadHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
			if IsBodyTooLarge(err) {
				WriteBodyTooLarge(w, r)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func TestBodySizeLimit_WithinLimit_PassesThrough(t *testing.T) {
	handler := BodySizeLimit(1024, 0)(echoReadHandler())

	body := `{"ids":["a","b"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/events/other", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestBodySizeLimit_Oversized_Returns413(t *testing.T) {
	handler := BodySizeLimit(16, 0)(echoReadHandler())

	big := bytes.Repeat([]byte("a"), 1024)
	body := `{"ids":["` + string(big) + `"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/events/other", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}

	var envelope map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("failed to decode error envelope: %v", err)
	}
	errObj, ok := envelope["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object, got %v", envelope)
	}
	if errObj["code"] != "PAYLOAD_TOO_LARGE" {
		t.Fatalf("expected code PAYLOAD_TOO_LARGE, got %v", errObj["code"])
	}
}

func TestBodySizeLimit_GetRequest_Unaffected(t *testing.T) {
	handler := BodySizeLimit(1, 0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for GET (no body limit applied), got %d", rec.Code)
	}
}

func TestBodySizeLimit_BatchPath_UsesBatchLimit(t *testing.T) {
	// Generic limit is tiny, but the batch-specific limit is generous enough
	// for this body — proves the per-path override takes effect.
	handler := BodySizeLimit(4, 1024)(echoReadHandler())

	body := `{"ids":["11111111-1111-1111-1111-111111111111"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/events/batch", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 under the batch limit, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMaxBodyBytesFromEnv_Defaults(t *testing.T) {
	limit, batchLimit := MaxBodyBytesFromEnv()
	if limit != defaultMaxBodyBytes {
		t.Fatalf("expected default limit %d, got %d", defaultMaxBodyBytes, limit)
	}
	if batchLimit != defaultMaxBatchBodyBytes {
		t.Fatalf("expected default batch limit %d, got %d", defaultMaxBatchBodyBytes, batchLimit)
	}
}
