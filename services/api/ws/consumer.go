package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// StreamKey is the Redis Stream the indexer publishes events to. Exported
// so other consumers of the same stream (e.g. middleware.StartCacheInvalidator,
// issue #221) have one source of truth for the name rather than a second
// copy of the literal that could drift from this one.
const StreamKey = "trident:events"

const groupName = "trident-api"

// streamKey is a private alias for StreamKey, kept so this file's many
// internal references don't all need renaming.
const streamKey = StreamKey

// Redis Stream field names published by the indexer (crates/indexer/src/redis_stream/mod.rs).
// Defined here and checked by contract tests against Rust constants to prevent schema drift.
const (
	StreamFieldContractID      = "contract_id"
	StreamFieldLedgerSequence  = "ledger_sequence"
	StreamFieldLedgerTimestamp = "ledger_timestamp"
	StreamFieldTransactionHash = "transaction_hash"
	StreamFieldEventIndex      = "event_index"
	StreamFieldEventType       = "event_type"
	StreamFieldTopics          = "topics"
	StreamFieldData            = "data"
	StreamFieldEventID         = "event_id"
)

// StreamRequiredFields contains the fields that must be present for a standard published event.
var StreamRequiredFields = []string{
	StreamFieldContractID,
	StreamFieldLedgerSequence,
	StreamFieldLedgerTimestamp,
	StreamFieldTransactionHash,
	StreamFieldEventIndex,
	StreamFieldEventType,
	StreamFieldTopics,
	StreamFieldData,
}

// StreamAllFields contains all known fields that may appear on the event stream.
var StreamAllFields = []string{
	StreamFieldContractID,
	StreamFieldLedgerSequence,
	StreamFieldLedgerTimestamp,
	StreamFieldTransactionHash,
	StreamFieldEventIndex,
	StreamFieldEventType,
	StreamFieldTopics,
	StreamFieldData,
	StreamFieldEventID,
}

// streamEntry is the legacy JSON structure expected when "data" held an envelope.
// Retained for backward compatibility if old messages exist in Redis.
type streamEntry struct {
	ContractID string          `json:"contract_id"`
	Payload    json.RawMessage `json:"payload"`
}

// redisValString converts a Redis message value to string.
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
//
// In Trident's indexer, events are published as flat stream fields:
// contract_id, ledger_sequence, ledger_timestamp, transaction_hash, event_index,
// event_type, topics, data, and optionally event_id.
//
// If a message was published in legacy format (where values["data"] contains JSON
// with {"contract_id": ..., "payload": ...}), it falls back to parsing that format.
func parseStreamMessage(values map[string]any) (contractID string, payload []byte, ok bool) {
	cid := redisValString(values[StreamFieldContractID])
	if cid != "" {
		// Indexer flat schema format.
		// Build the broadcast event payload expected by WebSocket subscribers & SDKs.
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

	// Legacy / nested fallback: check if "data" holds a JSON envelope with contract_id.
	if raw, exists := values[StreamFieldData]; exists {
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

// StartConsumer begins a Redis Streams consumer group reader. It creates the
// consumer group idempotently, uses a unique consumer name per process, and
// reads messages using XREADGROUP. Each message is acknowledged (XACK) after
// successful broadcast to the hub. PEL recovery via XAUTOCLAIM runs on startup
// and every 30s.
func StartConsumer(ctx context.Context, rdb *redis.Client, hub *Hub) {
	// Consumer name: prefer HOSTNAME, fall back to UUID if unavailable.
	consumerName, _ := os.Hostname()
	if consumerName == "" {
		consumerName = uuid.NewString()
	}

	slog.Info("ws: Redis Streams consumer starting (consumer group)", "stream", streamKey, "group", groupName, "consumer", consumerName)

	// Create consumer group idempotently: MKSTREAM ensures stream exists.
	if err := rdb.XGroupCreateMkStream(ctx, streamKey, groupName, "$").Err(); err != nil {
		// BUSYGROUP means group already exists — ignore.
		if strings.Contains(err.Error(), "BUSYGROUP") {
			slog.Info("ws: consumer group already exists, continuing")
		} else {
			slog.Error("ws: failed to create consumer group", "err", err)
			// Not fatal — continue and attempt to consume; downstream errors will surface.
		}
	}

	// Run PEL recovery (XAUTOCLAIM) once at startup.
	recoverPending(ctx, rdb, hub, consumerName)

	// Periodic PEL recovery every 30 seconds in background.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				recoverPending(ctx, rdb, hub, consumerName)
			case <-ctx.Done():
				slog.Info("ws: PEL recovery goroutine stopping")
				return
			}
		}
	}()

	for {
		// Block until messages are available or the context is cancelled.
		entries, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    groupName,
			Consumer: consumerName,
			Streams:  []string{streamKey, ">"},
			Count:    100,
			Block:    5000 * time.Millisecond,
		}).Result()

		if err != nil {
			if ctx.Err() != nil {
				// Context cancelled — clean shutdown.
				slog.Info("ws: Redis Streams consumer stopped")
				return
			}
			slog.Error("ws: XReadGroup error", "err", err)
			// Back off briefly on transient errors to avoid a tight error loop.
			select {
			case <-ctx.Done():
				return
			case <-time.After(1 * time.Second):
			}
			continue
		}

		for _, stream := range entries {
			for _, msg := range stream.Messages {
				cid, payload, valid := parseStreamMessage(msg.Values)
				if !valid || cid == "" {
					slog.Warn("ws: stream entry missing contract_id or malformed", "id", msg.ID)
					if err := rdb.XAck(ctx, streamKey, groupName, msg.ID).Err(); err != nil {
						slog.Warn("ws: failed to XACK malformed message", "id", msg.ID, "err", err)
					}
					continue
				}

				hub.Broadcast(cid, payload)

				// Acknowledge the message after successful broadcast.
				if err := rdb.XAck(ctx, streamKey, groupName, msg.ID).Err(); err != nil {
					slog.Warn("ws: failed to XACK message", "id", msg.ID, "err", err)
				}
			}
		}
	}
}

// recoverPending claims pending messages older than 30s using XAUTOCLAIM and
// reprocesses them locally so no messages stay stuck in the PEL.
func recoverPending(ctx context.Context, rdb *redis.Client, hub *Hub, consumerName string) {
	ctx2, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	minIdle := 30 * time.Second
	messages, _, err := rdb.XAutoClaim(ctx2, &redis.XAutoClaimArgs{
		Stream:   streamKey,
		Group:    groupName,
		Consumer: consumerName,
		MinIdle:  minIdle,
		Start:    "0-0",
	}).Result()
	if err != nil {
		// If no entries, Redis may return empty result — treat as non-fatal.
		if err != redis.Nil {
			slog.Warn("ws: XAUTOCLAIM failed", "err", err)
		}
		return
	}

	for _, msg := range messages {
		cid, payload, valid := parseStreamMessage(msg.Values)
		if !valid || cid == "" {
			slog.Warn("ws: recovered entry missing contract_id or malformed", "id", msg.ID)
			if err := rdb.XAck(ctx2, streamKey, groupName, msg.ID).Err(); err != nil {
				slog.Warn("ws: failed to XACK recovered malformed message", "id", msg.ID, "err", err)
			}
			continue
		}

		hub.Broadcast(cid, payload)
		if err := rdb.XAck(ctx2, streamKey, groupName, msg.ID).Err(); err != nil {
			slog.Warn("ws: failed to XACK recovered message", "id", msg.ID, "err", err)
		}
	}
}
