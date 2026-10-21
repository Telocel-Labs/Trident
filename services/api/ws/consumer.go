package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
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

// DefaultGroupName is the base consumer group name for Trident API WebSocket readers.
const DefaultGroupName = "trident-api"

// streamKey is a private alias for StreamKey, kept so this file's many
// internal references don't all need renaming.
const streamKey = StreamKey

// consumerGroupName resolves the consumer group name to use for this replica.
// If WS_CONSUMER_GROUP is set, that explicit group is used. Otherwise, in a
// multi-replica fan-out deployment, each replica requires its own consumer group
// so all replicas receive every published event rather than Redis load-balancing
// messages across them (issue #807).
func consumerGroupName(consumerName string) string {
	if g := os.Getenv("WS_CONSUMER_GROUP"); g != "" {
		return g
	}
	return DefaultGroupName + "-" + consumerName
}

// parseStreamEntry extracts the contractID and event payload from a Redis stream entry (issue #793).
// Supports both:
// 1. Canonical Indexer schema: top-level fields "contract_id", "data", "topics", "id", "network", etc.
// 2. Legacy wrapper schema: "data" -> {"contract_id": "...", "payload": ...}
func parseStreamEntry(values map[string]interface{}) (string, []byte, bool) {
	// 1. Canonical Indexer schema: contract_id is a top-level field in Redis stream entry.
	if cidRaw, ok := values["contract_id"]; ok {
		contractID := redisString(cidRaw)
		if contractID != "" {
			eventMap := make(map[string]interface{}, len(values)+6)
			for k, v := range values {
				eventMap[k] = v
			}
			id := redisString(values["id"])
			if id == "" {
				id = redisString(values["event_id"])
			}
			if id != "" {
				eventMap["id"] = id
				eventMap["eventId"] = id
			}
			eventMap["contractId"] = contractID
			eventMap["contract_id"] = contractID

			if netVal := redisString(values["network"]); netVal != "" {
				eventMap["network"] = netVal
			}
			if txHash := redisString(values["transaction_hash"]); txHash != "" {
				eventMap["txHash"] = txHash
				eventMap["transaction_hash"] = txHash
			}
			if seqStr := redisString(values["ledger_sequence"]); seqStr != "" {
				if seq, err := strconv.ParseInt(seqStr, 10, 64); err == nil {
					eventMap["ledger_sequence"] = seq
					eventMap["ledgerSequence"] = seq
				}
			}
			if topicsStr := redisString(values["topics"]); topicsStr != "" {
				var topicsArr []interface{}
				if err := json.Unmarshal([]byte(topicsStr), &topicsArr); err == nil {
					eventMap["topics"] = topicsArr
				}
			}
			if dataStr := redisString(values["data"]); dataStr != "" {
				var dataVal interface{}
				if err := json.Unmarshal([]byte(dataStr), &dataVal); err == nil {
					eventMap["data"] = dataVal
				} else {
					eventMap["data"] = dataStr
				}
			}

			payload, err := json.Marshal(eventMap)
			if err == nil {
				return contractID, payload, true
			}
		}
	}

	// 2. Legacy wrapper schema: "data" field contains JSON with contract_id and payload.
	if raw, ok := values["data"]; ok {
		rawStr := redisString(raw)
		if rawStr != "" {
			var legacy struct {
				ContractID string          `json:"contract_id"`
				Payload    json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal([]byte(rawStr), &legacy); err == nil && legacy.ContractID != "" {
				return legacy.ContractID, legacy.Payload, true
			}
		}
	}

	return "", nil, false
}

func redisString(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return fmt.Sprintf("%v", v)
	}
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
	groupName := consumerGroupName(consumerName)

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

	// Clean up dynamic consumer group on shutdown to avoid Redis stream metadata accumulation.
	if os.Getenv("WS_CONSUMER_GROUP") == "" {
		go func() {
			<-ctx.Done()
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = rdb.XGroupDestroy(cleanupCtx, streamKey, groupName).Err()
		}()
	}

	// Run PEL recovery (XAUTOCLAIM) once at startup.
	recoverPending(ctx, rdb, hub, groupName, consumerName)

	// Periodic PEL recovery every 30 seconds in background.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				recoverPending(ctx, rdb, hub, groupName, consumerName)
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
				contractID, payload, ok := parseStreamEntry(msg.Values)
				if !ok {
					slog.Warn("ws: failed to parse stream entry", "id", msg.ID)
					if err := rdb.XAck(ctx, streamKey, groupName, msg.ID).Err(); err != nil {
						slog.Warn("ws: failed to XACK malformed message", "id", msg.ID, "err", err)
					}
					continue
				}

				hub.Broadcast(contractID, payload)

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
func recoverPending(ctx context.Context, rdb *redis.Client, hub *Hub, groupName, consumerName string) {
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
		contractID, payload, ok := parseStreamEntry(msg.Values)
		if !ok {
			slog.Warn("ws: failed to parse recovered message", "id", msg.ID)
			if err := rdb.XAck(ctx2, streamKey, groupName, msg.ID).Err(); err != nil {
				slog.Warn("ws: failed to XACK recovered malformed message", "id", msg.ID, "err", err)
			}
			continue
		}

		hub.Broadcast(contractID, payload)
		if err := rdb.XAck(ctx2, streamKey, groupName, msg.ID).Err(); err != nil {
			slog.Warn("ws: failed to XACK recovered message", "id", msg.ID, "err", err)
		}
	}
}
