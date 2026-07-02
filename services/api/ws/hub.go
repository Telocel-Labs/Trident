// Package ws implements a WebSocket hub that fans out contract events from
// Redis Streams to connected browser/client subscribers.
package ws

import (
	"log/slog"
	"sync"
)

// subscriber is the registration interface shared by REST WebSocket clients
// and GraphQL subscription channels.
type subscriber interface {
	getContractID() string
	trySend(msg []byte) bool // false = dropped (slow consumer)
	shutdown()               // called by Hub.unregister to signal cleanup
}

// client is a REST WebSocket subscriber.
type client struct {
	contractID string
	send       chan []byte
}

func (c *client) getContractID() string { return c.contractID }

func (c *client) trySend(msg []byte) bool {
	select {
	case c.send <- msg:
		return true
	default:
		return false
	}
}

func (c *client) shutdown() { close(c.send) }

// Hub manages all active subscribers and routes broadcast messages to
// the correct subscribers based on contractId.
type Hub struct {
	mu      sync.RWMutex
	clients map[subscriber]struct{}
}

// NewHub constructs a Hub ready to use.
func NewHub() *Hub {
	return &Hub{
		clients: make(map[subscriber]struct{}),
	}
}

// register adds s to the hub's active set. Safe to call concurrently.
func (h *Hub) register(s subscriber) {
	h.mu.Lock()
	h.clients[s] = struct{}{}
	h.mu.Unlock()
	slog.Debug("ws: client registered", "contractId", s.getContractID())
}

// unregister removes s from the hub and calls shutdown so its goroutine
// can exit cleanly.
func (h *Hub) unregister(s subscriber) {
	h.mu.Lock()
	if _, ok := h.clients[s]; ok {
		delete(h.clients, s)
		s.shutdown()
	}
	h.mu.Unlock()
	slog.Debug("ws: client unregistered", "contractId", s.getContractID())
}

// Broadcast delivers msg to every subscriber watching contractID.
// It is safe to call from any goroutine. Slow consumers are dropped.
func (h *Hub) Broadcast(contractID string, msg []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for s := range h.clients {
		if s.getContractID() != contractID {
			continue
		}
		if !s.trySend(msg) {
			slog.Warn("ws: dropping message for slow client", "contractId", contractID)
		}
	}
}

// ClientCount returns the number of currently active WebSocket clients.
// Safe to call from any goroutine.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
