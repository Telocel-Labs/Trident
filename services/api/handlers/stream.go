package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	eventStreamKey = "trident:events"
	streamReadWait = time.Second
)

type streamRedisClient interface {
	XRead(ctx context.Context, a *redis.XReadArgs) *redis.XStreamSliceCmd
	XRevRangeN(ctx context.Context, key, start, stop string, count int64) *redis.XMessageSliceCmd
}

// Stream returns an SSE handler that forwards new Redis Stream events for one
// contract. The handler owns the blocking read loop, so request cancellation
// stops all streaming work without a detached goroutine.
func Stream(rdb streamRedisClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		contractID := r.URL.Query().Get("contractId")
		if contractID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": map[string]string{
					"field":   "contractId",
					"message": "contractId is required",
				},
			})
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": map[string]string{"message": "streaming is not supported"},
			})
			return
		}

		lastID, err := latestStreamID(r.Context(), rdb)
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			slog.Error("sse: failed to read Redis Stream tail", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"error": map[string]string{"message": "event stream is unavailable"},
			})
			return
		}

		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		h.Set("Connection", "keep-alive")
		if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil &&
			!errors.Is(err, http.ErrNotSupported) {
			slog.Warn("sse: failed to disable response write deadline", "err", err)
		}
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		topic0 := r.URL.Query().Get("topic0")

		for {
			streams, readErr := rdb.XRead(r.Context(), &redis.XReadArgs{
				Streams: []string{eventStreamKey, lastID},
				Count:   100,
				Block:   streamReadWait,
			}).Result()

			if readErr != nil {
				if r.Context().Err() != nil {
					return
				}
				if errors.Is(readErr, redis.Nil) {
					continue
				}

				slog.Warn("sse: Redis XREAD failed", "err", readErr)
				select {
				case <-r.Context().Done():
					return
				case <-time.After(time.Second):
					continue
				}
			}

			for _, stream := range streams {
				for _, msg := range stream.Messages {
					lastID = msg.ID

					if redisString(msg.Values["contract_id"]) != contractID {
						continue
					}
					if topic0 != "" && !matchesTopic0(msg.Values, topic0) {
						continue
					}

					payload, marshalErr := json.Marshal(msg.Values)
					if marshalErr != nil {
						slog.Warn("sse: failed to marshal stream event", "id", msg.ID, "err", marshalErr)
						continue
					}

					if _, writeErr := fmt.Fprintf(w, "data: %s\n\n", payload); writeErr != nil {
						return
					}
					flusher.Flush()
				}
			}
		}
	}
}

func latestStreamID(ctx context.Context, rdb streamRedisClient) (string, error) {
	messages, err := rdb.XRevRangeN(ctx, eventStreamKey, "+", "-", 1).Result()
	if err != nil {
		return "", err
	}
	if len(messages) == 0 {
		return "0-0", nil
	}
	return messages[0].ID, nil
}

func matchesTopic0(values map[string]any, want string) bool {
	var topics []string
	if err := json.Unmarshal([]byte(redisString(values["topics"])), &topics); err != nil {
		// Malformed topics cannot safely satisfy a server-side filter, so skip.
		return false
	}
	return len(topics) > 0 && topics[0] == want
}

func redisString(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case []byte:
		return string(value)
	default:
		return ""
	}
}
