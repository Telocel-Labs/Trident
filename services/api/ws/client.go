package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

const (
	pingInterval = 30 * time.Second
	pongWait     = 60 * time.Second
	writeWait    = 10 * time.Second
)

// Client represents a single WebSocket connection subscribed to a contract.
type Client struct {
	hub        *Hub
	conn       *websocket.Conn
	contractID string
	topic0     string
}

func newClient(hub *Hub, conn *websocket.Conn, contractID, topic0 string) *Client {
	return &Client{hub: hub, conn: conn, contractID: contractID, topic0: topic0}
}

// run registers the client with the hub, starts the Redis reader, and
// cleans up on disconnect.  Must be called in its own goroutine.
func (c *Client) run(rdb *redis.Client) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c.hub.register(c)
	defer c.hub.unregister(c)
	defer func() { _ = c.conn.Close() }()

	// Pong handler resets the read deadline so the connection stays alive.
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	if err := c.conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
		return
	}

	// Drain any client-sent messages (we don't expect any, but we must read
	// to receive pong frames on the same connection).
	go func() {
		for {
			if _, _, err := c.conn.ReadMessage(); err != nil {
				cancel()
				return
			}
		}
	}()

	go c.pingLoop(ctx)
	c.redisReadLoop(ctx, rdb)
}

func (c *Client) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
				return
			}
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *Client) redisReadLoop(ctx context.Context, rdb *redis.Client) {
	lastID := "$"

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		streams, err := rdb.XRead(ctx, &redis.XReadArgs{
			Streams: []string{"trident:events", lastID},
			Count:   100,
			Block:   5 * time.Second,
		}).Result()

		if err != nil {
			if ctx.Err() != nil || err == redis.Nil {
				return
			}
			slog.Warn("ws: redis xread error", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}

		for _, stream := range streams {
			for _, msg := range stream.Messages {
				lastID = msg.ID

				contractID, _ := msg.Values["contract_id"].(string)
				if contractID != c.contractID {
					continue
				}

				if c.topic0 != "" {
					topicsRaw, _ := msg.Values["topics"].(string)
					var topics []string
					if err := json.Unmarshal([]byte(topicsRaw), &topics); err != nil || len(topics) == 0 || topics[0] != c.topic0 {
						continue
					}
				}

				if err := c.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
					return
				}
				if err := c.conn.WriteJSON(msg.Values); err != nil {
					return
				}
			}
		}
	}
}
