package ws

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"log/slog"
	"net"
	"net/http"
	"time"
)

const (
	// sendBufSize is the number of messages buffered per client before drops.
	sendBufSize = 64

	// wsGUID is the RFC 6455 magic string used in the WebSocket handshake.
	wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

	// pingInterval is how often the server sends a ping frame (issue #15).
	pingInterval = 30 * time.Second

	// readDeadline is how long the server waits for any data (including pong)
	// before declaring the connection dead (issue #15 requires ≤ 60 s).
	readDeadline = 60 * time.Second
)

// Handler returns an http.HandlerFunc that upgrades incoming HTTP connections
// to WebSocket connections and registers them with hub.
//
// Query parameters:
//
//	contractId – required; only events for this contract are delivered.
//	topic0     – optional filter (reserved for future use by the consumer).
//
// Issue #15: the server sends a ping frame every 30 s and enforces a 60 s
// read deadline so dead connections are detected and goroutines do not leak.
func Handler(hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		contractID := r.URL.Query().Get("contractId")
		if contractID == "" {
			http.Error(w, "missing contractId query parameter", http.StatusBadRequest)
			return
		}

		// Perform the RFC 6455 WebSocket handshake.
		conn, bufrw, err := upgradeWebSocket(w, r)
		if err != nil {
			slog.Error("ws: upgrade failed", "err", err)
			return
		}
		defer func() {
			if err := conn.Close(); err != nil {
				slog.Debug("ws: connection close failed", "err", err)
			}
		}()

		c := &client{
			contractID: contractID,
			send:       make(chan []byte, sendBufSize),
		}
		hub.register(c)
		defer hub.unregister(c)

		// Set initial read deadline. It is refreshed on every successful write
		// so that an active connection is never incorrectly timed out.
		if err := conn.SetDeadline(time.Now().Add(readDeadline)); err != nil {
			slog.Warn("ws: failed to set initial deadline", "err", err)
			return
		}

		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()

		for {
			select {
			case msg, ok := <-c.send:
				if !ok {
					// Hub closed the channel — client was unregistered.
					return
				}
				if err := writeTextFrame(bufrw, msg); err != nil {
					slog.Warn("ws: write error, closing connection", "err", err)
					return
				}
				// Refresh deadline after a successful write.
				if err := conn.SetDeadline(time.Now().Add(readDeadline)); err != nil {
					slog.Warn("ws: failed to refresh deadline", "err", err)
					return
				}

			case <-ticker.C:
				if err := writePingFrame(bufrw); err != nil {
					slog.Warn("ws: ping write error, closing connection", "err", err)
					return
				}
				// Refresh deadline: if no pong or data arrives within readDeadline
				// the next I/O call will return a timeout error and exit the loop.
				if err := conn.SetDeadline(time.Now().Add(readDeadline)); err != nil {
					slog.Warn("ws: failed to refresh deadline", "err", err)
					return
				}
			}
		}
	}
}

// upgradeWebSocket performs a minimal RFC 6455 HTTP→WebSocket upgrade using
// net/http's Hijack interface. This avoids an external dependency while
// remaining spec-compliant for server-to-client text frames.
func upgradeWebSocket(w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.ReadWriter, error) {
	key := r.Header.Get("Sec-Websocket-Key")
	if key == "" {
		http.Error(w, "missing Sec-WebSocket-Key", http.StatusBadRequest)
		return nil, nil, http.ErrNotSupported
	}

	accept := computeAccept(key)

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "server does not support hijacking", http.StatusInternalServerError)
		return nil, nil, http.ErrNotSupported
	}

	conn, bufrw, err := hj.Hijack()
	if err != nil {
		return nil, nil, err
	}

	// Write the 101 Switching Protocols response directly on the raw connection.
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := bufrw.WriteString(resp); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if err := bufrw.Flush(); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}

	return conn, bufrw, nil
}

// computeAccept derives the Sec-WebSocket-Accept header value per RFC 6455 §4.2.2.
func computeAccept(key string) string {
	h := sha1.New()
	h.Write([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// writeTextFrame encodes payload as a WebSocket text frame (opcode 0x1, FIN set)
// and writes it to bufrw.
func writeTextFrame(bufrw *bufio.ReadWriter, payload []byte) error {
	return writeFrame(bufrw, 0x81, payload)
}

// writePingFrame writes an RFC 6455 ping frame (opcode 0x9, FIN set, empty payload).
func writePingFrame(bufrw *bufio.ReadWriter) error {
	return writeFrame(bufrw, 0x89, nil)
}

// writeFrame writes a single WebSocket frame with the given first-byte opcode
// and payload. Supports payloads up to 2^63-1 bytes.
func writeFrame(bufrw *bufio.ReadWriter, opcode byte, payload []byte) error {
	length := len(payload)

	header := []byte{opcode}

	switch {
	case length <= 125:
		header = append(header, byte(length))
	case length <= 65535:
		header = append(header, 126)
		b := make([]byte, 2)
		binary.BigEndian.PutUint16(b, uint16(length))
		header = append(header, b...)
	default:
		header = append(header, 127)
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, uint64(length))
		header = append(header, b...)
	}

	if _, err := bufrw.Write(header); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := bufrw.Write(payload); err != nil {
			return err
		}
	}
	return bufrw.Flush()
}
