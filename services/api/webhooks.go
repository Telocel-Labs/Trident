package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
)

type webhookSubscription struct {
	ID         string     `json:"id"`
	APIKeyID   string     `json:"apiKeyId,omitempty"`
	ContractID string     `json:"contractId"`
	Topic0     *string    `json:"topic0,omitempty"`
	TargetURL  string     `json:"targetUrl"`
	Secret     string     `json:"secret,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	PausedAt   *time.Time `json:"pausedAt,omitempty"`
	Network    string     `json:"network"`
}

type webhookEvent struct {
	ID              string         `json:"id"`
	ContractID      string         `json:"contractId"`
	LedgerSequence  int64          `json:"ledgerSequence"`
	Topic0          string         `json:"topic0"`
	Data            map[string]any `json:"data"`
	TransactionHash string         `json:"txHash"`
	Network         string         `json:"network"`
}

type webhookPayload struct {
	ID          string        `json:"id"`
	WebhookID   string        `json:"webhook_id"`
	Event       webhookEvent  `json:"event"`
	DeliveredAt string        `json:"delivered_at"`
}

type webhookDelivery struct {
	ID             int64     `json:"id"`
	SubscriptionID string    `json:"subscriptionId"`
	EventID        string    `json:"eventId"`
	Attempt        int       `json:"attempt"`
	StatusCode     *int      `json:"statusCode,omitempty"`
	ResponseBody   string    `json:"responseBody,omitempty"`
	DeliveredAt    time.Time `json:"deliveredAt"`
	Success        bool      `json:"success"`
}

func resolveAPIKeyID(ctx context.Context, db *sql.DB, r *http.Request) (string, error) {
	if db == nil {
		return "", nil
	}
	if header := strings.TrimSpace(r.Header.Get("X-API-Key")); header != "" {
		var id string
		if err := db.QueryRowContext(ctx, `SELECT id FROM api_keys WHERE id = $1`, header).Scan(&id); err == nil {
			return id, nil
		}
	}
	var id string
	if err := db.QueryRowContext(ctx, `INSERT INTO api_keys DEFAULT VALUES RETURNING id`).Scan(&id); err != nil {
		return "", err
	}
	return id, nil
}

type webhookDeliveryResult struct {
	Success     bool
	StatusCode  int
	ResponseBody string
	Err         error
}

func signWebhookBody(body string, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func verifyWebhookSignature(body string, signature string, secret string) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	expected := "sha256=" + signWebhookBody(body, secret)
	return subtle.ConstantTimeCompare([]byte(signature), []byte(expected)) == 1
}

func newDB() (*sql.DB, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, errors.New("DATABASE_URL is not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func startWebhookWorker(ctx context.Context, db *sql.DB, redisClient *redis.Client) {
	if db == nil || redisClient == nil {
		return
	}
	streamKey := os.Getenv("REDIS_STREAM_KEY")
	if streamKey == "" {
		streamKey = "trident:events"
	}
	groupName := os.Getenv("WEBHOOK_CONSUMER_GROUP")
	if groupName == "" {
		groupName = "trident-webhooks"
	}
	consumerName := os.Getenv("WEBHOOK_CONSUMER_NAME")
	if consumerName == "" {
		consumerName = "webhook-worker"
	}

	go func() {
		for {
			entries, err := redisClient.XReadGroup(ctx, &redis.XReadGroupArgs{
				Group:    groupName,
				Consumer: consumerName,
				Streams:  []string{streamKey, ">"},
				Count:    10,
				Block:    2 * time.Second,
				NoAck:    false,
			}).Result()
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, redis.Nil) {
					return
				}
				slog.Error("webhook worker read failed", "err", err)
				time.Sleep(time.Second)
				continue
			}
			for _, stream := range entries {
				for _, message := range stream.Messages {
					var event webhookEvent
					if raw, ok := message.Values["event"]; ok {
						if parsed, err := parseWebhookEvent(raw); err == nil {
							event = parsed
						} else {
							slog.Error("failed to parse webhook event", "err", err)
							if _, ackErr := redisClient.XAck(ctx, streamKey, groupName, message.ID).Result(); ackErr != nil {
								slog.Warn("failed to ack message", "err", ackErr)
							}
							continue
						}
					} else if raw, ok := message.Values["payload"]; ok {
						if parsed, err := parseWebhookEvent(raw); err == nil {
							event = parsed
						} else {
							slog.Error("failed to parse webhook payload", "err", err)
							if _, ackErr := redisClient.XAck(ctx, streamKey, groupName, message.ID).Result(); ackErr != nil {
								slog.Warn("failed to ack message", "err", ackErr)
							}
							continue
						}
					} else {
						slog.Warn("webhook worker received empty payload", "id", message.ID)
						if _, ackErr := redisClient.XAck(ctx, streamKey, groupName, message.ID).Result(); ackErr != nil {
							slog.Warn("failed to ack message", "err", ackErr)
						}
						continue
					}
					if err := processWebhookEvent(ctx, db, event); err != nil {
						slog.Error("webhook delivery failed", "err", err)
					}
					if _, err := redisClient.XAck(ctx, streamKey, groupName, message.ID).Result(); err != nil {
						slog.Warn("failed to ack message", "err", err)
					}
				}
			}
		}
	}()
}

func startWebhookCleanupJob(ctx context.Context, db *sql.DB) {
	if db == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := db.ExecContext(ctx, `DELETE FROM webhook_deliveries WHERE delivered_at < NOW() - INTERVAL '7 days'`); err != nil {
					slog.Warn("webhook cleanup failed", "err", err)
				}
			}
		}
	}()
}

func parseWebhookEvent(raw any) (webhookEvent, error) {
	switch value := raw.(type) {
	case string:
		var event webhookEvent
		if err := json.Unmarshal([]byte(value), &event); err != nil {
			return webhookEvent{}, err
		}
		return event, nil
	case []byte:
		var event webhookEvent
		if err := json.Unmarshal(value, &event); err != nil {
			return webhookEvent{}, err
		}
		return event, nil
	case map[string]any:
		payload, err := json.Marshal(value)
		if err != nil {
			return webhookEvent{}, err
		}
		var event webhookEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			return webhookEvent{}, err
		}
		return event, nil
	default:
		return webhookEvent{}, fmt.Errorf("unsupported event payload type %T", raw)
	}
}

func processWebhookEvent(ctx context.Context, db *sql.DB, event webhookEvent) error {
	if db == nil {
		return nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, api_key_id, contract_id, topic0, target_url, secret, created_at, paused_at, network
		FROM webhook_subscriptions
		WHERE contract_id = $1
		  AND paused_at IS NULL
		  AND (topic0 IS NULL OR topic0 = $2)
		  AND network = $3
	`, event.ContractID, event.Topic0, event.Network)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var sub webhookSubscription
		var topic0 sql.NullString
		var pausedAt sql.NullTime
		if err := rows.Scan(&sub.ID, &sub.APIKeyID, &sub.ContractID, &topic0, &sub.TargetURL, &sub.Secret, &sub.CreatedAt, &pausedAt, &sub.Network); err != nil {
			return err
		}
		if topic0.Valid {
			sub.Topic0 = &topic0.String
		}
		if pausedAt.Valid {
			sub.PausedAt = &pausedAt.Time
		}
		if err := deliverSubscriptionWithRetry(ctx, db, sub, event); err != nil {
			slog.Warn("webhook delivery failed for subscription", "subscription_id", sub.ID, "err", err)
		}
	}
	return rows.Err()
}

func deliverSubscriptionWithRetry(ctx context.Context, db *sql.DB, sub webhookSubscription, event webhookEvent) error {
	for attempt := 1; attempt <= 5; attempt++ {
		result := performWebhookDelivery(ctx, sub, event)
		if err := recordWebhookDelivery(ctx, db, sub.ID, event.ID, attempt, result); err != nil {
			slog.Warn("failed to record webhook delivery", "err", err)
		}
		if result.Success {
			return nil
		}
		if attempt == 5 {
			if _, err := db.ExecContext(ctx, `UPDATE webhook_subscriptions SET paused_at = NOW() WHERE id = $1`, sub.ID); err != nil {
				slog.Warn("failed to pause webhook subscription", "subscription_id", sub.ID, "err", err)
			}
			return result.Err
		}
		sleepDuration := time.Duration(1<<uint(attempt-1)) * time.Second
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleepDuration):
		}
	}
	return nil
}

func performWebhookDelivery(ctx context.Context, sub webhookSubscription, event webhookEvent) webhookDeliveryResult {
	payload, err := buildWebhookPayload(sub.ID, event)
	if err != nil {
		return webhookDeliveryResult{Err: err}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.TargetURL, bytes.NewReader(payload))
	if err != nil {
		return webhookDeliveryResult{Err: err}
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Trident-Signature", "sha256="+signWebhookBody(string(payload), sub.Secret))

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return webhookDeliveryResult{Err: err}
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
	responseBody := strings.TrimSpace(string(bodyBytes))
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return webhookDeliveryResult{Success: true, StatusCode: resp.StatusCode, ResponseBody: responseBody}
	}
	return webhookDeliveryResult{Success: false, StatusCode: resp.StatusCode, ResponseBody: responseBody, Err: fmt.Errorf("webhook returned status %d", resp.StatusCode)}
}

func buildWebhookPayload(subscriptionID string, event webhookEvent) ([]byte, error) {
	payload := webhookPayload{
		ID:        fmt.Sprintf("wh_%d", time.Now().UnixNano()),
		WebhookID: subscriptionID,
		Event:     event,
		DeliveredAt: time.Now().UTC().Format(time.RFC3339),
	}
	return json.Marshal(payload)
}

func recordWebhookDelivery(ctx context.Context, db *sql.DB, subscriptionID string, eventID string, attempt int, result webhookDeliveryResult) error {
	if db == nil {
		return nil
	}
	var statusCode *int
	if result.StatusCode != 0 {
		statusCode = &result.StatusCode
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO webhook_deliveries (subscription_id, event_id, attempt, status_code, response_body, success)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, subscriptionID, eventID, attempt, statusCode, truncateString(result.ResponseBody, 500), result.Success)
	return err
}

func truncateString(input string, max int) string {
	if len(input) <= max {
		return input
	}
	return input[:max]
}

func listWebhooksHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if db == nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		apiKeyID, err := resolveAPIKeyID(r.Context(), db, r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rows, err := db.QueryContext(r.Context(), `
			SELECT id, api_key_id, contract_id, topic0, target_url, secret, created_at, paused_at, network
			FROM webhook_subscriptions
			WHERE api_key_id = $1
			ORDER BY created_at DESC
		`, apiKeyID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var subscriptions []webhookSubscription
		for rows.Next() {
			var sub webhookSubscription
			var topic0 sql.NullString
			var pausedAt sql.NullTime
			if err := rows.Scan(&sub.ID, &sub.APIKeyID, &sub.ContractID, &topic0, &sub.TargetURL, &sub.Secret, &sub.CreatedAt, &pausedAt, &sub.Network); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if topic0.Valid {
				sub.Topic0 = &topic0.String
			}
			if pausedAt.Valid {
				sub.PausedAt = &pausedAt.Time
			}
			subscriptions = append(subscriptions, sub)
		}
		writeJSON(w, http.StatusOK, subscriptions)
	}
}

func createWebhookHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if db == nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		var req struct {
			ContractID string  `json:"contractId"`
			Topic0     *string `json:"topic0"`
			TargetURL  string  `json:"targetUrl"`
			Network    string  `json:"network"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.TargetURL == "" || req.ContractID == "" {
			http.Error(w, "contractId and targetUrl are required", http.StatusBadRequest)
			return
		}
		if req.Network == "" {
			req.Network = "testnet"
		}
		secret := generateWebhookSecret()
		apiKeyID, err := resolveAPIKeyID(r.Context(), db, r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var topic0 sql.NullString
		if req.Topic0 != nil {
			topic0 = sql.NullString{String: *req.Topic0, Valid: true}
		}
		var id string
		err = db.QueryRowContext(r.Context(), `
			INSERT INTO webhook_subscriptions (api_key_id, contract_id, topic0, target_url, secret, network)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id
		`, apiKeyID, req.ContractID, topic0, req.TargetURL, secret, req.Network).Scan(&id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": id, "secret": secret, "targetUrl": req.TargetURL, "contractId": req.ContractID, "network": req.Network})
	}
}

func deleteWebhookHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "missing webhook id", http.StatusBadRequest)
			return
		}
		if db == nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		result, err := db.ExecContext(r.Context(), `DELETE FROM webhook_subscriptions WHERE id = $1`, id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		affected, _ := result.RowsAffected()
		if affected == 0 {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func pauseWebhookHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "missing webhook id", http.StatusBadRequest)
			return
		}
		if db == nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		if _, err := db.ExecContext(r.Context(), `UPDATE webhook_subscriptions SET paused_at = NOW() WHERE id = $1`, id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
	}
}

func resumeWebhookHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "missing webhook id", http.StatusBadRequest)
			return
		}
		if db == nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		if _, err := db.ExecContext(r.Context(), `UPDATE webhook_subscriptions SET paused_at = NULL WHERE id = $1`, id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "resumed"})
	}
}

func deliveriesWebhookHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "missing webhook id", http.StatusBadRequest)
			return
		}
		if db == nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		rows, err := db.QueryContext(r.Context(), `
			SELECT id, subscription_id, event_id, attempt, status_code, response_body, delivered_at, success
			FROM webhook_deliveries
			WHERE subscription_id = $1
			ORDER BY delivered_at DESC
			LIMIT 100
		`, id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var deliveries []webhookDelivery
		for rows.Next() {
			var delivery webhookDelivery
			var statusCode sql.NullInt64
			if err := rows.Scan(&delivery.ID, &delivery.SubscriptionID, &delivery.EventID, &delivery.Attempt, &statusCode, &delivery.ResponseBody, &delivery.DeliveredAt, &delivery.Success); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if statusCode.Valid {
				code := int(statusCode.Int64)
				delivery.StatusCode = &code
			}
			deliveries = append(deliveries, delivery)
		}
		writeJSON(w, http.StatusOK, deliveries)
	}
}

func generateWebhookSecret() string {
	return fmt.Sprintf("whsec_%d", time.Now().UnixNano())
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

var deliverWebhook = func(ctx context.Context, sub webhookSubscription, event webhookEvent) error {
	result := performWebhookDelivery(ctx, sub, event)
	if result.Success {
		return nil
	}
	return result.Err
}
