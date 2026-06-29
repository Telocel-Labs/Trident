package ws

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	gqlProtocol     = "graphql-transport-ws"
	gqlInitTimeout  = 10 * time.Second
	gqlPingInterval = 30 * time.Second
	gqlMaxFrameSize = 1 << 20 // 1 MiB per frame
)

// gqlMessage is a graphql-transport-ws protocol envelope.
type gqlMessage struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// gqlSub is a GraphQL subscription registered with the Hub.
type gqlSub struct {
	cid  string
	send chan []byte
}

func (s *gqlSub) getContractID() string { return s.cid }
func (s *gqlSub) trySend(msg []byte) bool {
	select {
	case s.send <- msg:
		return true
	default:
		return false
	}
}
func (s *gqlSub) shutdown() { close(s.send) }

// gqlConn serialises writes to a hijacked WebSocket connection.
type gqlConn struct {
	bufrw *bufio.ReadWriter
	mu    sync.Mutex
}

func (c *gqlConn) write(msg gqlMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return writeTextFrame(c.bufrw, data)
}

func (c *gqlConn) writePong(payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return writeFrame(c.bufrw, 0x8A, payload) // FIN + opcode 0xA (pong)
}

// GraphQLHandler returns an http.HandlerFunc that serves the
// graphql-transport-ws protocol for real-time contract event subscriptions.
// The handler shares the same Hub as the REST WebSocket endpoint — only one
// Redis XREADGROUP reader runs per process, regardless of how many connections
// are open.
//
// Authentication happens in the connection_init phase via an Authorization
// field in the payload. Invalid or missing keys receive connection_error and
// the connection is closed.
func GraphQLHandler(hub *Hub, validateKey func(string) bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "WebSocket upgrade required", http.StatusUpgradeRequired)
			return
		}
		if !strings.Contains(r.Header.Get("Sec-Websocket-Protocol"), gqlProtocol) {
			http.Error(w, "graphql-transport-ws protocol required", http.StatusBadRequest)
			return
		}

		conn, bufrw, err := upgradeGQLWebSocket(w, r)
		if err != nil {
			slog.Error("graphql: WebSocket upgrade failed", "err", err)
			return
		}

		serveGQL(conn, bufrw, hub, validateKey)
	}
}

// serveGQL runs the graphql-transport-ws protocol loop on an already-upgraded
// connection. Extracted for testability.
func serveGQL(conn net.Conn, bufrw *bufio.ReadWriter, hub *Hub, validateKey func(string) bool) {
	gc := &gqlConn{bufrw: bufrw}

	type frameMsg struct {
		data   []byte
		opcode byte
		err    error
	}
	reads := make(chan frameMsg)
	done := make(chan struct{})

	// conn.Close() runs last so the read goroutine unblocks via I/O error.
	defer conn.Close()
	// Closing done signals the read goroutine to exit via select.
	defer close(done)

	go func() {
		for {
			data, opcode, err := readClientFrame(bufrw.Reader)
			select {
			case reads <- frameMsg{data, opcode, err}:
				if err != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()

	subs := make(map[string]*gqlSub)
	// Unregister all active subscriptions first so forwarding goroutines
	// receive the channel-close signal before the connection is torn down.
	defer func() {
		for _, sub := range subs {
			hub.unregister(sub)
		}
	}()

	initTimer := time.NewTimer(gqlInitTimeout)
	defer initTimer.Stop()
	pingTicker := time.NewTicker(gqlPingInterval)
	defer pingTicker.Stop()

	authenticated := false

	for {
		select {
		case <-initTimer.C:
			// Timer fired: close if client never authenticated.
			if !authenticated {
				_ = gc.write(gqlMessage{Type: "connection_error"})
				return
			}
			// Rare race: timer channel arrived after Stop() returned false.

		case <-pingTicker.C:
			if err := gc.write(gqlMessage{Type: "ping"}); err != nil {
				return
			}

		case fm := <-reads:
			if fm.err != nil {
				return
			}

			switch fm.opcode {
			case 0x8: // WebSocket close
				return
			case 0x9: // WebSocket ping → pong
				_ = gc.writePong(fm.data)
				continue
			case 0xA: // WebSocket pong — ignore
				continue
			}

			if fm.opcode != 0x1 && fm.opcode != 0x2 {
				continue // skip non-text/binary frames
			}

			var msg gqlMessage
			if err := json.Unmarshal(fm.data, &msg); err != nil {
				slog.Debug("graphql: malformed message", "err", err)
				return
			}

			switch msg.Type {
			case "connection_init":
				key := gqlExtractKey(msg.Payload)
				if !validateKey(key) {
					_ = gc.write(gqlMessage{Type: "connection_error"})
					return
				}
				authenticated = true
				initTimer.Stop()
				if err := gc.write(gqlMessage{Type: "connection_ack"}); err != nil {
					return
				}

			case "subscribe":
				if !authenticated {
					_ = gc.write(gqlMessage{Type: "connection_error"})
					return
				}
				contractID, topic0, err := gqlParseSubscribe(msg.Payload)
				if err != nil {
					_ = gc.write(gqlErrorMsg(msg.ID, err.Error()))
					continue
				}
				if _, exists := subs[msg.ID]; exists {
					_ = gc.write(gqlErrorMsg(msg.ID, "subscription id already in use"))
					continue
				}
				sub := &gqlSub{cid: contractID, send: make(chan []byte, sendBufSize)}
				hub.register(sub)
				subs[msg.ID] = sub
				go gqlForward(sub, msg.ID, topic0, gc)

			case "complete":
				if sub, ok := subs[msg.ID]; ok {
					hub.unregister(sub)
					delete(subs, msg.ID)
				}

			case "ping":
				_ = gc.write(gqlMessage{Type: "pong", Payload: msg.Payload})

			case "pong":
				// keep-alive acknowledgement — no-op
			}
		}
	}
}

// gqlForward reads events from sub and writes graphql-transport-ws next
// messages to gc. It exits when sub.send is closed (on hub unregister) or
// when a write fails (connection dead).
func gqlForward(sub *gqlSub, opID, topic0 string, gc *gqlConn) {
	for raw := range sub.send {
		if topic0 != "" && !gqlMatchesTopic0(raw, topic0) {
			continue
		}
		if err := gc.write(gqlNextMsg(opID, raw)); err != nil {
			return
		}
	}
}

// upgradeGQLWebSocket performs the RFC 6455 handshake and negotiates the
// graphql-transport-ws subprotocol in the 101 response.
func upgradeGQLWebSocket(w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.ReadWriter, error) {
	key := r.Header.Get("Sec-Websocket-Key")
	if key == "" {
		http.Error(w, "missing Sec-WebSocket-Key", http.StatusBadRequest)
		return nil, nil, http.ErrNotSupported
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return nil, nil, http.ErrNotSupported
	}

	conn, bufrw, err := hj.Hijack()
	if err != nil {
		return nil, nil, err
	}

	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + computeAccept(key) + "\r\n" +
		"Sec-WebSocket-Protocol: " + gqlProtocol + "\r\n\r\n"
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

// readClientFrame reads one RFC 6455 frame from a client.
// Client→server frames are always masked per spec.
func readClientFrame(r *bufio.Reader) ([]byte, byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, 0, err
	}

	opcode := header[0] & 0x0F
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

	if payloadLen > gqlMaxFrameSize {
		return nil, 0, fmt.Errorf("frame payload too large: %d bytes", payloadLen)
	}

	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(r, maskKey[:]); err != nil {
			return nil, 0, err
		}
	}

	payload := make([]byte, payloadLen)
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

var (
	reContractID = regexp.MustCompile(`contractId\s*:\s*"([^"]+)"`)
	reTopic0     = regexp.MustCompile(`topic0\s*:\s*"([^"]+)"`)
)

// gqlParseSubscribe extracts contractId and topic0 from a subscribe payload.
// Variables take precedence over inline query arguments.
func gqlParseSubscribe(payload json.RawMessage) (contractID, topic0 string, err error) {
	if payload == nil {
		return "", "", fmt.Errorf("missing subscribe payload")
	}
	var p struct {
		Query     string                 `json:"query"`
		Variables map[string]interface{} `json:"variables"`
	}
	if jsonErr := json.Unmarshal(payload, &p); jsonErr != nil {
		return "", "", fmt.Errorf("invalid subscribe payload")
	}

	if p.Variables != nil {
		if v, ok := p.Variables["contractId"].(string); ok {
			contractID = v
		}
		if v, ok := p.Variables["topic0"].(string); ok {
			topic0 = v
		}
	}

	if contractID == "" {
		if m := reContractID.FindStringSubmatch(p.Query); len(m) == 2 {
			contractID = m[1]
		}
	}
	if topic0 == "" {
		if m := reTopic0.FindStringSubmatch(p.Query); len(m) == 2 {
			topic0 = m[1]
		}
	}

	if contractID == "" {
		return "", "", fmt.Errorf("contractId is required")
	}
	return contractID, topic0, nil
}

// gqlExtractKey extracts an API key from a connection_init payload.
// Accepts {"Authorization":"tk_..."} or {"Authorization":"Bearer tk_..."}.
func gqlExtractKey(payload json.RawMessage) string {
	if payload == nil {
		return ""
	}
	var p struct {
		Authorization string `json:"Authorization"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return ""
	}
	return strings.TrimPrefix(p.Authorization, "Bearer ")
}

// gqlNextMsg wraps raw event bytes in a graphql-transport-ws next envelope.
// Snake-case payload fields are remapped to the camelCase GraphQL schema names.
func gqlNextMsg(opID string, raw []byte) gqlMessage {
	var src map[string]interface{}
	if err := json.Unmarshal(raw, &src); err != nil {
		src = map[string]interface{}{}
	}

	ev := make(map[string]interface{}, 10)
	pick := func(dst, key string) {
		if v, ok := src[key]; ok {
			ev[dst] = v
		}
	}
	pick("id", "id")
	pick("contractId", "contract_id")
	pick("ledgerSequence", "ledger_sequence")
	pick("ledgerTimestamp", "ledger_timestamp")
	pick("txHash", "transaction_hash")
	pick("eventIndex", "event_index")
	pick("eventType", "event_type")
	pick("topics", "topics")
	pick("data", "data")
	pick("createdAt", "created_at")

	inner, _ := json.Marshal(map[string]interface{}{"contractEvents": ev})
	return gqlMessage{
		Type:    "next",
		ID:      opID,
		Payload: json.RawMessage(`{"data":` + string(inner) + `}`),
	}
}

// gqlErrorMsg builds an error message for a subscription operation.
func gqlErrorMsg(id, message string) gqlMessage {
	errs, _ := json.Marshal([]map[string]interface{}{{"message": message}})
	return gqlMessage{Type: "error", ID: id, Payload: json.RawMessage(errs)}
}

// gqlMatchesTopic0 reports whether the first topic in a raw event payload
// equals topic0. Handles both JSON-array and JSON-string-encoded-array formats.
func gqlMatchesTopic0(payload []byte, topic0 string) bool {
	var ev struct {
		Topics interface{} `json:"topics"`
	}
	if err := json.Unmarshal(payload, &ev); err != nil {
		return false
	}
	switch t := ev.Topics.(type) {
	case []interface{}:
		return len(t) > 0 && fmt.Sprint(t[0]) == topic0
	case string:
		var arr []string
		if err := json.Unmarshal([]byte(t), &arr); err != nil {
			return false
		}
		return len(arr) > 0 && arr[0] == topic0
	}
	return false
}
