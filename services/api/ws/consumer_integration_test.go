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

// TestIntegration_MultiReplicaWebSocketFanout verifies issue #715:
// With multiple API replicas and one client on each replica, all clients receive
// every published event without load-balancing/splitting events across replicas.
func TestIntegration_MultiReplicaWebSocketFanout(t *testing.T) {
	const validContractID = "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer rdb.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Replica A: Hub, Consumer, Test Server
	hubA := ws.NewHub()
	go ws.StartConsumer(ctx, rdb, hubA)
	tsA := httptest.NewServer(ws.Handler(hubA))
	defer tsA.Close()

	// Replica B: Hub, Consumer, Test Server
	hubB := ws.NewHub()
	go ws.StartConsumer(ctx, rdb, hubB)
	tsB := httptest.NewServer(ws.Handler(hubB))
	defer tsB.Close()

	// Connect Client A to Replica A
	uA, err := url.Parse(tsA.URL)
	if err != nil {
		t.Fatalf("parse server url A: %v", err)
	}
	connA, err := net.Dial("tcp", uA.Host)
	if err != nil {
		t.Fatalf("dial server A: %v", err)
	}
	defer connA.Close()

	reqA := fmt.Sprintf("GET /ws?contractId=%s HTTP/1.1\r\n"+
		"Host: %s\r\n"+
		"Upgrade: websocket\r\n"+
		"Connection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n"+
		"Sec-WebSocket-Version: 13\r\n\r\n", validContractID, uA.Host)
	if _, err := connA.Write([]byte(reqA)); err != nil {
		t.Fatalf("write upgrade request A: %v", err)
	}
	readerA := bufio.NewReader(connA)
	respA, err := http.ReadResponse(readerA, nil)
	if err != nil || respA.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("client A handshake failed: %v, status: %v", err, respA.StatusCode)
	}

	// Connect Client B to Replica B
	uB, err := url.Parse(tsB.URL)
	if err != nil {
		t.Fatalf("parse server url B: %v", err)
	}
	connB, err := net.Dial("tcp", uB.Host)
	if err != nil {
		t.Fatalf("dial server B: %v", err)
	}
	defer connB.Close()

	reqB := fmt.Sprintf("GET /ws?contractId=%s HTTP/1.1\r\n"+
		"Host: %s\r\n"+
		"Upgrade: websocket\r\n"+
		"Connection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n"+
		"Sec-WebSocket-Version: 13\r\n\r\n", validContractID, uB.Host)
	if _, err := connB.Write([]byte(reqB)); err != nil {
		t.Fatalf("write upgrade request B: %v", err)
	}
	readerB := bufio.NewReader(connB)
	respB, err := http.ReadResponse(readerB, nil)
	if err != nil || respB.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("client B handshake failed: %v, status: %v", err, respB.StatusCode)
	}

	// Allow subscriptions and consumers to initialize
	time.Sleep(50 * time.Millisecond)

	// Publish multiple events to Redis Stream
	eventsToPublish := []struct {
		seq   string
		index string
		data  string
	}{
		{seq: "1001", index: "0", data: `{"msg":"first_event"}`},
		{seq: "1001", index: "1", data: `{"msg":"second_event"}`},
		{seq: "1002", index: "0", data: `{"msg":"third_event"}`},
		{seq: "1003", index: "0", data: `{"msg":"fourth_event"}`},
	}

	for _, evt := range eventsToPublish {
		if err := rdb.XAdd(ctx, &redis.XAddArgs{
			Stream: ws.StreamKey,
			Values: map[string]interface{}{
				"contract_id":      validContractID,
				"ledger_sequence":  evt.seq,
				"ledger_timestamp": "2026-10-19T11:00:00Z",
				"transaction_hash": "tx_hash_" + evt.seq,
				"event_index":      evt.index,
				"event_type":       "contract",
				"topics":          `["test"]`,
				"data":            evt.data,
				"event_id":         fmt.Sprintf("evt_%s_%s", evt.seq, evt.index),
			},
		}).Err(); err != nil {
			t.Fatalf("XAdd failed: %v", err)
		}
	}

	// Read and verify that BOTH clients receive EVERY event in order
	_ = connA.SetReadDeadline(time.Now().Add(5 * time.Second))
	_ = connB.SetReadDeadline(time.Now().Add(5 * time.Second))

	for i, want := range eventsToPublish {
		payloadA := readWSTextFrame(t, readerA)
		payloadB := readWSTextFrame(t, readerB)

		var recA, recB map[string]interface{}
		if err := json.Unmarshal(payloadA, &recA); err != nil {
			t.Fatalf("event %d unmarshal A failed: %v", i, err)
		}
		if err := json.Unmarshal(payloadB, &recB); err != nil {
			t.Fatalf("event %d unmarshal B failed: %v", i, err)
		}

		if recA["ledger_sequence"] != want.seq || recA["event_index"] != want.index || recA["data"] != want.data {
			t.Errorf("Client A event %d mismatch: got seq=%v index=%v data=%v, want seq=%s index=%s data=%s",
				i, recA["ledger_sequence"], recA["event_index"], recA["data"], want.seq, want.index, want.data)
		}
		if recB["ledger_sequence"] != want.seq || recB["event_index"] != want.index || recB["data"] != want.data {
			t.Errorf("Client B event %d mismatch: got seq=%v index=%v data=%v, want seq=%s index=%s data=%s",
				i, recB["ledger_sequence"], recB["event_index"], recB["data"], want.seq, want.index, want.data)
		}
	}
}
