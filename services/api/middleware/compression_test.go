package middleware_test

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Depo-dev/trident/services/api/middleware"
)

func TestCompression_GzipJSONWhenNegotiated(t *testing.T) {
	body := strings.Repeat(`{"id":"evt_123","contract_id":"CABC","event_type":"contract","data":"AAAA"}`, 40)
	h := middleware.Compression(middleware.CompressionConfig{MinSize: 64})(jsonHandler(body))

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if got := rr.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding: got %q, want gzip", got)
	}
	if vary := rr.Header().Get("Vary"); !strings.Contains(vary, "Accept-Encoding") {
		t.Fatalf("Vary: got %q, want Accept-Encoding", vary)
	}

	zr, err := gzip.NewReader(rr.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer func() { _ = zr.Close() }()
	decoded, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read gzip: %v", err)
	}
	if string(decoded) != body {
		t.Fatalf("decoded body mismatch")
	}
}

func TestCompression_DeflateJSONWhenNegotiated(t *testing.T) {
	body := strings.Repeat(`{"id":"evt_123","contract_id":"CABC","event_type":"contract","data":"AAAA"}`, 40)
	h := middleware.Compression(middleware.CompressionConfig{MinSize: 64})(jsonHandler(body))

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.Header.Set("Accept-Encoding", "deflate")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if got := rr.Header().Get("Content-Encoding"); got != "deflate" {
		t.Fatalf("Content-Encoding: got %q, want deflate", got)
	}

	zr, err := zlib.NewReader(rr.Body)
	if err != nil {
		t.Fatalf("deflate reader: %v", err)
	}
	defer func() { _ = zr.Close() }()
	decoded, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read deflate: %v", err)
	}
	if string(decoded) != body {
		t.Fatalf("decoded body mismatch")
	}
}

func TestCompression_RespectsQValuesAndWildcard(t *testing.T) {
	body := strings.Repeat(`{"id":"evt_123","contract_id":"CABC","event_type":"contract","data":"AAAA"}`, 40)
	h := middleware.Compression(middleware.CompressionConfig{MinSize: 64})(jsonHandler(body))

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.Header.Set("Accept-Encoding", "gzip;q=0, *;q=1")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if got := rr.Header().Get("Content-Encoding"); got != "deflate" {
		t.Fatalf("Content-Encoding: got %q, want deflate", got)
	}
}

func TestCompression_SkipsSmallOrNonJSONResponses(t *testing.T) {
	t.Run("small JSON", func(t *testing.T) {
		h := middleware.Compression(middleware.CompressionConfig{MinSize: 1024})(jsonHandler(`{"ok":true}`))
		req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)

		if got := rr.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("Content-Encoding: got %q, want empty", got)
		}
		if got := rr.Body.String(); got != `{"ok":true}` {
			t.Fatalf("body: got %q", got)
		}
	})

	t.Run("non JSON", func(t *testing.T) {
		h := middleware.Compression(middleware.CompressionConfig{MinSize: 1})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(strings.Repeat("hello ", 100)))
		}))
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)

		if got := rr.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("Content-Encoding: got %q, want empty", got)
		}
	})
}

func TestCompression_ExcludedStreamKeepsFlusherAndBodyUncompressed(t *testing.T) {
	h := middleware.Compression(middleware.CompressionConfig{
		MinSize:      1,
		ExcludePaths: []string{"/v1/events/stream"},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Fatalf("stream writer lost http.Flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {}\n\n"))
		w.(http.Flusher).Flush()
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/events/stream", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if got := rr.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding: got %q, want empty", got)
	}
	if got := rr.Body.String(); got != "data: {}\n\n" {
		t.Fatalf("body: got %q", got)
	}
}

func TestCompressionRepresentativePayloadReduction(t *testing.T) {
	payload := representativeEventsPayload(t)
	h := middleware.Compression(middleware.CompressionConfig{MinSize: 1024})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	rawSize := len(payload)
	gzipSize := rr.Body.Len()
	reduction := 100 * (1 - float64(gzipSize)/float64(rawSize))
	t.Logf("representative events payload: raw=%d bytes gzip=%d bytes reduction=%.1f%%", rawSize, gzipSize, reduction)
	if reduction < 70 {
		t.Fatalf("gzip reduction %.1f%% below expected floor", reduction)
	}
}

func BenchmarkCompressionRepresentativeEvents(b *testing.B) {
	payload := representativeEventsPayload(b)
	h := middleware.Compression(middleware.CompressionConfig{MinSize: 1024})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.Header.Set("Accept-Encoding", "gzip")

	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
	}
}

func representativeEventsPayload(tb testing.TB) []byte {
	tb.Helper()

	type event struct {
		ID              string   `json:"id"`
		ContractID      string   `json:"contract_id"`
		LedgerSequence  uint64   `json:"ledger_sequence"`
		LedgerTimestamp string   `json:"ledger_timestamp"`
		TransactionHash string   `json:"transaction_hash"`
		EventIndex      uint32   `json:"event_index"`
		EventType       string   `json:"event_type"`
		Topics          []string `json:"topics"`
		Data            string   `json:"data"`
		CreatedAt       string   `json:"created_at"`
	}
	payload := struct {
		Events     []event `json:"events"`
		HasMore    bool    `json:"has_more"`
		NextCursor string  `json:"next_cursor"`
	}{Events: make([]event, 100), HasMore: true, NextCursor: "eyJsZWRnZXIiOjEyMzQ1Njc4OX0"}

	for i := range payload.Events {
		payload.Events[i] = event{
			ID:              "018f6e1c-7b2b-7d0a-9c3f-000000000001",
			ContractID:      "CCONTRACTIDEXAMPLE000000000000000000000000000000000000000001",
			LedgerSequence:  uint64(500000 + i),
			LedgerTimestamp: "2026-07-27T08:30:00Z",
			TransactionHash: "d8b04e8d7c0f4c93a2b6a7a8d8f930beefcafe1234567890abcdef1234567890",
			EventIndex:      uint32(i % 12),
			EventType:       "contract",
			Topics: []string{
				"AAAADAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
				"AAAAEAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			},
			Data:      "AAAAAgAAAAl0cmFuc2ZlcgAAAAAAABQAAAABAAAAAQAAAAEAAAAA" + strings.Repeat("A", 80),
			CreatedAt: "2026-07-27T08:30:01Z",
		}
	}

	buf := bytes.Buffer{}
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		tb.Fatalf("encode representative events: %v", err)
	}
	return buf.Bytes()
}

func jsonHandler(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
}
