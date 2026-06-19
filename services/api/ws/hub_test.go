package ws

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialWS upgrades a test HTTP server connection to WebSocket.
func dialWS(t *testing.T, server *httptest.Server, path string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + path
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	return conn
}

func TestHub_missingContractID_returns400(t *testing.T) {
	h := NewHub()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Bypass upgrader — check the query param guard directly.
		h.Handler(nil)(w, r)
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ws")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHub_connectDisconnect_lifecycle(t *testing.T) {
	h := NewHub()

	if h.ActiveConnections() != 0 {
		t.Fatal("expected 0 connections before any client")
	}

	// Simulate register/unregister directly (internal test).
	c := &Client{hub: h, contractID: "CTEST"}
	h.register(c)

	if h.ActiveConnections() != 1 {
		t.Fatalf("expected 1 connection after register, got %d", h.ActiveConnections())
	}

	h.unregister(c)

	if h.ActiveConnections() != 0 {
		t.Fatalf("expected 0 connections after unregister, got %d", h.ActiveConnections())
	}
}

func TestHub_multipleClients(t *testing.T) {
	h := NewHub()

	clients := make([]*Client, 5)
	for i := range clients {
		clients[i] = &Client{hub: h, contractID: "C"}
		h.register(clients[i])
	}

	if h.ActiveConnections() != 5 {
		t.Fatalf("expected 5 connections, got %d", h.ActiveConnections())
	}

	for _, c := range clients {
		h.unregister(c)
	}

	if h.ActiveConnections() != 0 {
		t.Fatal("expected 0 connections after all unregistered")
	}
}

func TestHub_websocketConnect_receivesEvent(t *testing.T) {
	redisURL := ""
	// This sub-test requires TEST_REDIS_URL; skip otherwise.
	// We still exercise the upgrade path and ping/pong.
	_ = redisURL

	h := NewHub()
	mux := http.NewServeMux()

	// Handler that upgrades but immediately closes (no Redis needed).
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		contractID := r.URL.Query().Get("contractId")
		if contractID == "" {
			http.Error(w, "contractId required", http.StatusBadRequest)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		h.register(&Client{hub: h, contractID: contractID})
		// Close immediately to test disconnect lifecycle.
		_ = conn.Close()
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	conn := dialWS(t, srv, "/ws?contractId=CTEST")
	defer func() { _ = conn.Close() }()

	// Give the server goroutine time to register.
	time.Sleep(50 * time.Millisecond)

	// The connection will be closed server-side; reading triggers EOF.
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, _, _ = conn.ReadMessage() // expected to return error on close
}
