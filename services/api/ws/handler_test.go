package ws

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// clientMaskFrame encodes a client-to-server RFC 6455 frame with masking.
func clientMaskFrame(opcode byte, payload []byte) []byte {
	length := len(payload)
	header := []byte{0x80 | (opcode & 0x0F)} // FIN + opcode

	// Set mask bit (0x80) on length byte per RFC 6455 §5.1
	if length <= 125 {
		header = append(header, 0x80|byte(length))
	} else if length <= 65535 {
		header = append(header, 0x80|126)
		b := make([]byte, 2)
		binary.BigEndian.PutUint16(b, uint16(length))
		header = append(header, b...)
	} else {
		header = append(header, 0x80|127)
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, uint64(length))
		header = append(header, b...)
	}

	maskKey := []byte{0x12, 0x34, 0x56, 0x78}
	header = append(header, maskKey...)

	maskedPayload := make([]byte, length)
	for i, b := range payload {
		maskedPayload[i] = b ^ maskKey[i%4]
	}

	return append(header, maskedPayload...)
}

// readServerFrame reads an unmasked RFC 6455 frame sent from server to client.
func readServerFrame(r io.Reader) (payload []byte, opcode byte, err error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, 0, err
	}
	opcode = header[0] & 0x0F
	masked := header[1]&0x80 != 0
	payloadLen := int64(header[1] & 0x7F)

	switch payloadLen {
	case 126:
		b := make([]byte, 2)
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, 0, err
		}
		payloadLen = int64(binary.BigEndian.Uint16(b))
	case 127:
		b := make([]byte, 8)
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, 0, err
		}
		payloadLen = int64(binary.BigEndian.Uint64(b))
	}

	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(r, maskKey[:]); err != nil {
			return nil, 0, err
		}
	}

	payload = make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, 0, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return payload, opcode, nil
}

// dialWS connects to the given httptest.Server and performs RFC 6455 handshake.
func dialWS(t *testing.T, s *httptest.Server, path string) net.Conn {
	t.Helper()
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}

	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	req := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"

	if _, err := conn.Write([]byte(req)); err != nil {
		_ = conn.Close()
		t.Fatalf("write handshake: %v", err)
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		_ = conn.Close()
		t.Fatalf("read handshake: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = conn.Close()
		t.Fatalf("expected 101, got %d", resp.StatusCode)
	}

	return conn
}

func TestWSHandler_ClientPingRespondsWithPong(t *testing.T) {
	hub := NewHub()
	server := httptest.NewServer(Handler(hub))
	defer server.Close()

	conn := dialWS(t, server, "/ws?contractId=CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	defer func() { _ = conn.Close() }()

	pingPayload := []byte("ping-test-payload-123")
	frame := clientMaskFrame(0x9, pingPayload) // 0x9 = Ping

	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("failed to write ping frame: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	payload, opcode, err := readServerFrame(conn)
	if err != nil {
		t.Fatalf("failed to read frame from server: %v", err)
	}

	if opcode != 0xA { // 0xA = Pong
		t.Fatalf("expected pong opcode 0xA, got 0x%X", opcode)
	}
	if string(payload) != string(pingPayload) {
		t.Fatalf("expected pong payload %q, got %q", string(pingPayload), string(payload))
	}
}

func TestWSHandler_ClientCloseCompletesHandshake(t *testing.T) {
	hub := NewHub()
	server := httptest.NewServer(Handler(hub))
	defer server.Close()

	conn := dialWS(t, server, "/ws?contractId=CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	defer func() { _ = conn.Close() }()

	closePayload := make([]byte, 2)
	binary.BigEndian.PutUint16(closePayload, 1000)
	frame := clientMaskFrame(0x8, closePayload) // 0x8 = Close

	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("failed to write close frame: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	payload, opcode, err := readServerFrame(conn)
	if err != nil {
		t.Fatalf("failed to read close response from server: %v", err)
	}

	if opcode != 0x8 {
		t.Fatalf("expected close frame 0x8, got 0x%X", opcode)
	}
	if len(payload) >= 2 {
		code := binary.BigEndian.Uint16(payload[:2])
		if code != 1000 {
			t.Fatalf("expected close code 1000, got %d", code)
		}
	}
}

func TestWSHandler_UnmaskedClientFrameRejected(t *testing.T) {
	hub := NewHub()
	server := httptest.NewServer(Handler(hub))
	defer server.Close()

	conn := dialWS(t, server, "/ws?contractId=CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	defer func() { _ = conn.Close() }()

	// Unmasked frame (violates RFC 6455 §5.1)
	unmasked := []byte{0x81, 0x05, 'h', 'e', 'l', 'l', 'o'}
	if _, err := conn.Write(unmasked); err != nil {
		t.Fatalf("write unmasked frame: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 128)
	_, err := conn.Read(buf)
	// Server must close connection on unmasked client frame
	if err == nil {
		// Read until EOF
		_, err = conn.Read(buf)
	}
	if err == nil {
		t.Fatalf("expected connection to be closed by server on unmasked frame")
	}
}

func TestWSHandler_BroadcastToConnectedClient(t *testing.T) {
	hub := NewHub()
	server := httptest.NewServer(Handler(hub))
	defer server.Close()

	contractID := "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	conn := dialWS(t, server, "/ws?contractId="+contractID)
	defer func() { _ = conn.Close() }()

	// Give registration a moment to settle
	time.Sleep(20 * time.Millisecond)

	msg := []byte(`{"event":"test-broadcast"}`)
	hub.Broadcast(contractID, msg)

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	payload, opcode, err := readServerFrame(conn)
	if err != nil {
		t.Fatalf("failed to read broadcast frame: %v", err)
	}
	if opcode != 0x1 { // Text frame
		t.Fatalf("expected text opcode 0x1, got 0x%X", opcode)
	}
	if string(payload) != string(msg) {
		t.Fatalf("expected broadcast payload %q, got %q", string(msg), string(payload))
	}
}

func TestWSFrame_ParsingFuzzAndEdgeCases(t *testing.T) {
	// 1. Control frame larger than 125 bytes must fail
	largeControl := []byte{0x89, 0x80 | 126, 0x00, 0x80} // 128 bytes ping
	br := bufio.NewReader(bytes.NewReader(largeControl))
	_, _, err := readClientFrame(br)
	if err != errControlFrameTooLarge {
		t.Fatalf("expected errControlFrameTooLarge, got %v", err)
	}

	// 2. Fragmented control frame must fail (FIN=0)
	fragmentedPing := []byte{0x09, 0x80 | 0x00, 0x01, 0x02, 0x03, 0x04}
	br = bufio.NewReader(bytes.NewReader(fragmentedPing))
	_, _, err = readClientFrame(br)
	if err != errFragmentedControl {
		t.Fatalf("expected errFragmentedControl, got %v", err)
	}

	// 3. Random truncated bytes must not panic
	for i := 0; i < 50; i++ {
		b := make([]byte, i)
		_, _ = rand.Read(b)
		br = bufio.NewReader(bytes.NewReader(b))
		_, _, _ = readClientFrame(br)
	}
}

func TestWSHandler_ClientPongAcknowledged(t *testing.T) {
	hub := NewHub()
	server := httptest.NewServer(Handler(hub))
	defer server.Close()

	conn := dialWS(t, server, "/ws?contractId=CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	defer func() { _ = conn.Close() }()

	// Send an unsolicited Pong (allowed per RFC 6455 §5.5.3 as unidirectional heartbeat)
	pongFrame := clientMaskFrame(0xA, []byte("heartbeat-pong"))
	if _, err := conn.Write(pongFrame); err != nil {
		t.Fatalf("write pong: %v", err)
	}

	// Wait briefly and verify connection remains open and active
	time.Sleep(50 * time.Millisecond)
	msg := []byte(`{"status":"alive"}`)
	hub.Broadcast("CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", msg)

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	payload, opcode, err := readServerFrame(conn)
	if err != nil {
		t.Fatalf("connection died after pong: %v", err)
	}
	if opcode != 0x1 || string(payload) != string(msg) {
		t.Fatalf("unexpected message after pong: %s", string(payload))
	}
}
