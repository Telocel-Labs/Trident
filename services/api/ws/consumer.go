package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// StreamKey is the Redis Stream the indexer publishes events to. Exported
// so other consumers of the same stream (e.g. middleware.StartCacheInvalidator,
// issue #221) have one source of truth for the name rather than a second
// copy of the literal that could drift from this one.
const StreamKey = "trident:events"

// streamKey is a private alias for StreamKey, kept so this file's many
// internal references don't all need renaming.
const streamKey = StreamKey

// streamEntry is the JSON structure expected in each Redis Stream message value.
type streamEntry struct {
	ContractID string          `json:"contract_id"`
	Payload    json.RawMessage `json:"payload"`
}

func redisValString(val any) string {
	switch v := val.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return ""
	}
}

// parseStreamMessage extracts contract_id and constructs the JSON broadcast payload
// from a Redis Stream message's Values map.
// It supports both the flat fields published by crates/indexer/src/redis_stream/mod.rs
// (contract_id, ledger_sequence, topics, data, etc.) and legacy JSON envelopes in "data".
func parseStreamMessage(values map[string]any) (contractID string, payload []byte, ok bool) {
	cid := redisValString(values["contract_id"])
	if cid != "" {
		eventMap := make(map[string]any, len(values))
		for k, v := range values {
			eventMap[k] = redisValString(v)
		}
		b, err := json.Marshal(eventMap)
		if err != nil {
			return "", nil, false
		}
		return cid, b, true
	}

	if raw, exists := values["data"]; exists {
		rawStr := redisValString(raw)
		if rawStr != "" {
			var entry streamEntry
			if err := json.Unmarshal([]byte(rawStr), &entry); err == nil && entry.ContractID != "" {
				p := entry.Payload
				if len(p) == 0 {
					p = json.RawMessage(rawStr)
				}
				return entry.ContractID, p, true
			}
		}
	}

	return "", nil, false
}

// StartConsumer begins a per-replica Redis Stream reader using XREAD.
//
// Fan-out architecture (issue #715):
// WebSocket event delivery requires fan-out to every connected client across all API
// replicas. In a multi-replica deployment, Redis consumer groups (XREADGROUP) deliver
// each stream entry to only ONE consumer in the group, which caused replicas to see
// roughly 1/N of events and deprived clients on replica A from seeing events handed
// to replica B.
//
// By reading via XREAD with a per-replica / per-process last-id, every replica independently
// receives all events published to the stream and broadcasts them to its locally connected
// WebSocket clients. Resume semantics start at the stream tail ($ or latest entry ID),
// and monotonically advance with each processed message. Group semantics remain reserved
// for work-distribution workloads like webhooks (services/api/webhooks.go).
func StartConsumer(ctx context.Context, rdb *redis.Client, hub *Hub) {
	if rdb == nil {
		slog.Warn("ws: Redis client is nil, consumer cannot start")
		return
	}

	// Start reading from the tail of the stream ($).
	lastID := "$"

	// If entries already exist, initialize from the latest entry ID.
	if msgs, err := rdb.XRevRangeN(ctx, streamKey, "+", "-", 1).Result(); err == nil && len(msgs) > 0 {
		lastID = msgs[0].ID
	}

	slog.Info("ws: Redis Streams fan-out consumer starting (per-replica XREAD)", "stream", streamKey, "startID", lastID)

	for {
		select {
		case <-ctx.Done():
			slog.Info("ws: Redis Streams consumer stopped")
			return
		default:
		}

		entries, err := rdb.XRead(ctx, &redis.XReadArgs{
			Streams: []string{streamKey, lastID},
			Count:   100,
			Block:   5000 * time.Millisecond,
		}).Result()

		if err != nil {
			if ctx.Err() != nil {
				slog.Info("ws: Redis Streams consumer stopped")
				return
			}
			if err == redis.Nil {
				continue
			}
			slog.Error("ws: XRead error", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(1 * time.Second):
			}
			continue
		}

		for _, stream := range entries {
			for _, msg := range stream.Messages {
				lastID = msg.ID

				cid, payload, valid := parseStreamMessage(msg.Values)
				if !valid || cid == "" {
					slog.Warn("ws: stream entry missing contract_id or malformed", "id", msg.ID)
					continue
				}

				hub.Broadcast(cid, payload)
			}
		}
	}
}
