// Package ws implements the WebSocket endpoint that fans out real-time
// Soroban events to connected browser/SDK clients.
//
// Library choice: github.com/gorilla/websocket — it is production-hardened,
// supports ping/pong framing control, and has a stable, well-documented API.
// golang.org/x/net/websocket is lower-level and lacks built-in ping/pong.
package ws

import (
	"log/slog"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

var upgrader = websocket.Upgrader{
	// Allow all origins — enforce CORS at the reverse-proxy layer.
	CheckOrigin: func(_ *http.Request) bool { return true },
}

// Hub tracks all active WebSocket connections.
type Hub struct {
	mu      sync.RWMutex
	clients map[*Client]struct{}
}

// NewHub creates an empty Hub.
func NewHub() *Hub {
	return &Hub{clients: make(map[*Client]struct{})}
}

// ActiveConnections returns the current number of connected clients.
func (h *Hub) ActiveConnections() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

func (h *Hub) register(c *Client) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	count := len(h.clients)
	h.mu.Unlock()
	slog.Info("ws: client connected", "contract_id", c.contractID, "connections", count)
}

func (h *Hub) unregister(c *Client) {
	h.mu.Lock()
	delete(h.clients, c)
	count := len(h.clients)
	h.mu.Unlock()
	slog.Info("ws: client disconnected", "contract_id", c.contractID, "connections", count)
}

// Handler returns an http.HandlerFunc for GET /ws that accepts
// contractId (required) and topic0 (optional) query params, upgrades
// the connection to WebSocket, and starts the Redis fan-out goroutine.
func (h *Hub) Handler(rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		contractID := r.URL.Query().Get("contractId")
		if contractID == "" {
			http.Error(w, "contractId query param is required", http.StatusBadRequest)
			return
		}
		topic0 := r.URL.Query().Get("topic0")

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			slog.Warn("ws: upgrade failed", "err", err)
			return
		}

		c := newClient(h, conn, contractID, topic0)
		go c.run(rdb)
	}
}
