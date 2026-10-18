package ws_test

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Depo-dev/trident/services/api/ws"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// readWSTextFrame reads one RFC 6455 unmasked or masked text frame from r.
func readWSTextFrame(t *testing.T, r *bufio.Reader) []byte {
	t.Helper()
	for {
		header := make([]byte, 2)
		if _, err := io.ReadFull(r, header); err != nil {
			t.Fatalf("read frame header: %v", err)
		}
		opcode := header[0] & 0x0F
		isMasked := (header[1] & 0x80) != 0
		payloadLen := int(header[1] & 0x7F)

		if payloadLen == 126 {
			b := make([]byte, 2)
			if _, err := io.ReadFull(r, b); err != nil {
				t.Fatalf("read extended length: %v", err)
			}
			payloadLen = int(binary.BigEndian.Uint16(b))
		} else if payloadLen == 127 {
			b := make([]byte, 8)
			if _, err := io.ReadFull(r, b); err != nil {
				t.Fatalf("read 64-bit length: %v", err)
			}
			payloadLen = int(binary.BigEndian.Uint64(b))
		}

		var maskKey [4]byte
		if isMasked {
			if _, err := io.ReadFull(r, maskKey[:]); err != nil {
				t.Fatalf("read mask key: %v", err)
			}
		}

		payload := make([]byte, payloadLen)
		if _, err := io.ReadFull(r, payload); err != nil {
			t.Fatalf("read payload: %v", err)
		}

		if isMasked {
			for i := 0; i < payloadLen; i++ {
				payload[i] ^= maskKey[i%4]
			}
		}

		// Opcode 0x1 is text frame
		if opcode == 0x1 {
			return payload
		}
		// Skip pings (0x9), pongs (0xA), etc.
	}
}

// TestRedisStreamSchemaContract verifies that Go consumer field names match
// the Rust producer constants in crates/common/src/types.rs (Issue #711).
func TestRedisStreamSchemaContract(t *testing.T) {
	// Verify Go constants match expected wire names
	expected := map[string]string{
		ws.StreamFieldContractID:      "contract_id",
		ws.StreamFieldLedgerSequence:  "ledger_sequence",
		ws.StreamFieldLedgerTimestamp: "ledger_timestamp",
		ws.StreamFieldTransactionHash: "transaction_hash",
		ws.StreamFieldEventIndex:      "event_index",
		ws.StreamFieldEventType:       "event_type",
		ws.StreamFieldTopics:          "topics",
		ws.StreamFieldData:            "data",
		ws.StreamFieldEventID:         "event_id",
	}

	for field, want := range expected {
		if field != want {
			t.Errorf("field constant mismatch: got %q, want %q", field, want)
		}
	}

	// Read Rust types.rs to ensure Rust constants match Go constants
	typesPath := filepath.Join("..", "..", "..", "crates", "common", "src", "types.rs")
	content, err := os.ReadFile(typesPath)
	if err != nil {
		t.Fatalf("failed to read Rust types.rs: %v", err)
	}
	rustSrc := string(content)

	for _, field := range ws.StreamAllFields {
		// e.g. pub const CONTRACT_ID: &str = "contract_id";
		needle := fmt.Sprintf(`= "%s";`, field)
		if !strings.Contains(rustSrc, needle) {
			t.Errorf("Rust redis_stream_fields in types.rs missing constant with value %q", field)
		}
	}
}

// TestIntegration_IndexerPublishedEventDeliveredToWebSocketClient verifies the full
// pipeline (Issue #711):
// 1. A simulated indexer publisher writes flat fields to trident:events.
// 2. The real Go StartConsumer reads the message via consumer group.
// 3. The real ws.Handler upgrades a client WebSocket connection on /ws?contractId=...
// 4. The WebSocket client receives the published event payload.
func TestIntegration_IndexerPublishedEventDeliveredToWebSocketClient(t *testing.T) {
	const validContractID = "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer rdb.Close()

	hub := ws.NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start Go consumer group reader
	go ws.StartConsumer(ctx, rdb, hub)

	// Set up HTTP test server with ws.Handler
	ts := httptest.NewServer(ws.Handler(hub))
	defer ts.Close()

	// Connect WebSocket client to /ws?contractId=...
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}

	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatalf("dial server: %v", err)
	}
	defer conn.Close()

	// Perform WebSocket handshake
	req := fmt.Sprintf("GET /ws?contractId=%s HTTP/1.1\r\n"+
		"Host: %s\r\n"+
		"Upgrade: websocket\r\n"+
		"Connection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n"+
		"Sec-WebSocket-Version: 13\r\n\r\n", validContractID, u.Host)

	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write upgrade request: %v", err)
	}

	reader := bufio.NewReader(conn)
	// Read HTTP response headers
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("read upgrade response: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("expected status 101 Switching Protocols, got %d", resp.StatusCode)
	}

	// Ensure consumer and client registration are ready
	time.Sleep(50 * time.Millisecond)

	// Publish flat fields exactly as published by crates/indexer/src/redis_stream/mod.rs
	publishedValues := map[string]interface{}{
		ws.StreamFieldContractID:      validContractID,
		ws.StreamFieldLedgerSequence:  "12345",
		ws.StreamFieldLedgerTimestamp: "2026-10-18T22:30:00Z",
		ws.StreamFieldTransactionHash: "tx_hash_abc",
		ws.StreamFieldEventIndex:      "1",
		ws.StreamFieldEventType:       "contract",
		ws.StreamFieldTopics:          `["transfer","alice","bob"]`,
		ws.StreamFieldData:            `{"amount":"1000"}`,
		ws.StreamFieldEventID:         "evt_uuid_1",
	}

	if err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: ws.StreamKey,
		Values: publishedValues,
	}).Err(); err != nil {
		t.Fatalf("XAdd failed: %v", err)
	}

	// Read WebSocket message delivered to client
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	payload := readWSTextFrame(t, reader)

	var received map[string]interface{}
	if err := json.Unmarshal(payload, &received); err != nil {
		t.Fatalf("unmarshal received WebSocket payload: %v, raw: %s", err, string(payload))
	}

	if received["contract_id"] != validContractID {
		t.Errorf("contract_id: got %v, want %s", received["contract_id"], validContractID)
	}
	if received["ledger_sequence"] != "12345" {
		t.Errorf("ledger_sequence: got %v, want 12345", received["ledger_sequence"])
	}
	if received["transaction_hash"] != "tx_hash_abc" {
		t.Errorf("transaction_hash: got %v, want tx_hash_abc", received["transaction_hash"])
	}
	if received["event_type"] != "contract" {
		t.Errorf("event_type: got %v, want contract", received["event_type"])
	}
	if received["data"] != `{"amount":"1000"}` {
		t.Errorf("data: got %v, want {\"amount\":\"1000\"}", received["data"])
	}
	if received["topics"] != `["transfer","alice","bob"]` {
		t.Errorf("topics: got %v, want [\"transfer\",\"alice\",\"bob\"]", received["topics"])
	}
}
