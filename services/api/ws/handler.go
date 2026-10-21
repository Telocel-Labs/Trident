package ws

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Depo-dev/trident/services/api/internal/httputil"
	"github.com/Depo-dev/trident/services/api/validation"
)

const (
	// sendBufSize is the number of messages buffered per client before drops.
	sendBufSize = 64

	// wsGUID is the RFC 6455 magic string used in the WebSocket handshake.
	wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

	// pingInterval is how often the server sends a ping frame (issue #15).
	pingInterval = 30 * time.Second

	// readDeadline is how long the server waits for any data (including pong)
	// before declaring the connection dead (issue #15, #806 requires ≤ 60 s).
	readDeadline = 60 * time.Second

	// writeDeadline bounds a single frame write. A stuck TCP socket (full
	// kernel send buffer because the peer stopped reading) must not block
	// this connection's goroutine forever (issue #224).
	writeDeadline = 10 * time.Second

	// closeStatusPolicyViolation is the RFC 6455 close code sent to a slow
	// consumer disconnected under the fill policy (issue #224).
	closeStatusPolicyViolation = 1008
)

// Handler returns an http.HandlerFunc that upgrades incoming HTTP connections
// to WebSocket connections and registers them with hub.
//
// Query parameters:
//
//	contractId – required; only events for this contract are delivered.
//	topic0     – optional filter (reserved for future use by the consumer).
//
// Issue #15 & #806: the server sends a ping frame every 30 s and enforces a 60 s
// read deadline. Client frames (ping, pong, close, unsolicited data) are actively
// read by a dedicated reader goroutine so:
//   - Client pings receive RFC 6455 compliant pongs with identical payload;
//   - Client pongs refresh read deadlines so dead/idle clients are reaped;
//   - Client close frames are answered to complete the RFC 6455 close handshake;
//   - Socket receive buffers never overflow with unread client frames.
func Handler(hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// Pre-handshake failures use the same INVALID_ARGUMENT envelope as the
		// REST endpoints rather than a plain-text body (issue #222).
		if verr := validation.RejectUnknownParams(q, "contractId", "topic0"); verr != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, httputil.INVALID_ARGUMENT, verr.Message)
			return
		}

		contractID := q.Get("contractId")
		if verr := validation.ValidateRequiredContractID("contractId", contractID); verr != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, httputil.INVALID_ARGUMENT, verr.Message)
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
			closeSlow:  make(chan struct{}),
		}
		hub.register(c)
		defer hub.unregister(c)

		writer := newWSWriter(conn, bufrw)

		// Set initial read deadline.
		if err := conn.SetReadDeadline(time.Now().Add(readDeadline)); err != nil {
			slog.Warn("ws: failed to set initial read deadline", "err", err)
			return
		}

		done := make(chan struct{})
		var once sync.Once
		closeDone := func() {
			once.Do(func() {
				close(done)
			})
		}

		// Read loop: continuously reads client frames (RFC 6455 RFC §5).
		go func() {
			defer closeDone()
			for {
				payload, opcode, err := readClientFrame(bufrw.Reader)
				if err != nil {
					return
				}

				// Any valid incoming frame proves peer liveness: refresh read deadline.
				_ = conn.SetReadDeadline(time.Now().Add(readDeadline))

				switch opcode {
				case 0x8: // Close frame
					var code uint16 = 1000
					if len(payload) >= 2 {
						code = binary.BigEndian.Uint16(payload[:2])
					}
					// Respond with close frame to complete the close handshake (RFC 6455 §5.5.1).
					_ = writer.writeClose(code, "")
					return
				case 0x9: // Ping frame -> respond with pong containing the exact payload (RFC 6455 §5.5.3).
					_ = writer.writePong(payload)
				case 0xA: // Pong frame -> keep-alive acknowledged; deadline refreshed above.
				default:
					// Any other frames (text/binary) are discarded to avoid receive buffer saturation.
				}
			}
		}()

		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				// Connection terminated by client close, network failure, or read deadline expiration.
				return

			case msg, ok := <-c.send:
				if !ok {
					// Hub closed the channel — client was unregistered.
					return
				}
				if err := writer.writeText(msg); err != nil {
					slog.Warn("ws: write error, closing connection", "err", err)
					return
				}

			case <-ticker.C:
				if err := writer.writePing(); err != nil {
					slog.Warn("ws: ping write error, closing connection", "err", err)
					return
				}

			case <-c.closeSlow:
				// Fill-policy disconnect (issue #224): tell the client why before dropping connection.
				_ = writer.writeClose(closeStatusPolicyViolation, "slow consumer: buffer exceeded")
				return
			}
		}
	}
}

// upgradeWebSocket performs a minimal RFC 6455 HTTP→WebSocket upgrade using
// net/http's Hijack interface.
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
