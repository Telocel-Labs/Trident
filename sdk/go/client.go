package trident

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Depo-dev/trident/sdk/go/openapi"
)

// Client is the Trident Go Client.
type Client struct {
	config TridentClientConfig
	client *http.Client
}

// NewClient creates a new Trident Go Client.
//
// Config precedence: an explicit config.APIKey/config.BaseURL always wins;
// when either is left empty it falls back to the TRIDENT_API_KEY /
// TRIDENT_BASE_URL environment variables respectively.
func NewClient(config TridentClientConfig) *Client {
	return &Client{
		config: config.resolve(),
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// QueryEvents fetches a page of historical events matching the filter.
func (c *Client) QueryEvents(ctx context.Context, params QueryEventsParams, opts ...RequestOption) (*PaginatedEvents, error) {
	reqURL, err := url.Parse(c.config.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid BaseURL: %w", err)
	}

	reqURL.Path = "/v1/events"
	q := reqURL.Query()

	if params.ContractID != "" {
		q.Set("contractId", params.ContractID)
	}
	if params.Topic0 != "" {
		q.Set("topic0", params.Topic0)
	}
	if params.Topic1 != "" {
		q.Set("topic1", params.Topic1)
	}
	if params.LedgerFrom != nil {
		q.Set("ledgerFrom", strconv.FormatUint(*params.LedgerFrom, 10))
	}
	if params.LedgerTo != nil {
		q.Set("ledgerTo", strconv.FormatUint(*params.LedgerTo, 10))
	}
	if params.Cursor != "" {
		q.Set("cursor", params.Cursor)
	}
	if params.Limit > 0 {
		q.Set("limit", strconv.Itoa(params.Limit))
	}

	reqURL.RawQuery = q.Encode()

	bodyBytes, err := c.do(ctx, http.MethodGet, reqURL.String(), nil, opts)
	if err != nil {
		return nil, err
	}

	var res PaginatedEvents
	if err := json.Unmarshal(bodyBytes, &res); err != nil {
		return nil, fmt.Errorf("decode query response: %w", err)
	}

	return &res, nil
}

// AllEvents returns an iterator that transparently pages through every event
// matching params, following next_cursor until has_more is false (issue
// #280). Iteration stops after the first error, which is yielded so the
// caller can distinguish "no more events" from "a request failed":
//
//	for event, err := range client.AllEvents(ctx, params) {
//		if err != nil {
//			// handle and stop; range exits automatically after this
//			// iteration since no more values are yielded.
//			break
//		}
//		...
//	}
//
// params.Cursor, if set, is honoured as the starting page.
func (c *Client) AllEvents(ctx context.Context, params QueryEventsParams, opts ...RequestOption) iter.Seq2[*SorobanEvent, error] {
	return func(yield func(*SorobanEvent, error) bool) {
		cursor := params.Cursor
		for {
			pageParams := params
			pageParams.Cursor = cursor

			page, err := c.QueryEvents(ctx, pageParams, opts...)
			if err != nil {
				yield(nil, err)
				return
			}

			for _, ev := range page.Events {
				if !yield(ev, nil) {
					return
				}
			}

			if !page.HasMore || page.NextCursor == "" {
				return
			}
			cursor = page.NextCursor
		}
	}
}

// GetEventByID fetches a single event by its UUID ID.
func (c *Client) GetEventByID(ctx context.Context, id string, opts ...RequestOption) (*SorobanEvent, error) {
	reqURL, err := url.Parse(c.config.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid BaseURL: %w", err)
	}

	reqURL.Path = "/v1/events/" + id

	bodyBytes, err := c.do(ctx, http.MethodGet, reqURL.String(), nil, opts)
	if err != nil {
		return nil, err
	}

	var wrapper struct {
		Event *SorobanEvent `json:"event"`
	}
	if err := json.Unmarshal(bodyBytes, &wrapper); err != nil {
		return nil, fmt.Errorf("decode get response: %w", err)
	}

	if wrapper.Event == nil {
		return nil, fmt.Errorf("event not found in response envelope")
	}

	return wrapper.Event, nil
}

// batchEventsMaxIDs mirrors the cap enforced by the server on POST /v1/events/batch.
const batchEventsMaxIDs = 100

// BatchGetEvents fetches up to 100 events by id in a single request (issue
// #228). IDs that were not indexed are returned in BatchEventsResult.Missing
// rather than causing an error; both slices preserve the request order of
// ids, with duplicates deduplicated on first occurrence.
func (c *Client) BatchGetEvents(ctx context.Context, ids []string, opts ...RequestOption) (*BatchEventsResult, error) {
	if len(ids) == 0 {
		return &BatchEventsResult{Events: []*SorobanEvent{}, Missing: []string{}}, nil
	}
	if len(ids) > batchEventsMaxIDs {
		return nil, fmt.Errorf("trident: batch get supports at most %d ids, got %d", batchEventsMaxIDs, len(ids))
	}

	reqURL, err := url.Parse(c.config.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid BaseURL: %w", err)
	}
	reqURL.Path = "/v1/events/batch"

	reqBody, err := json.Marshal(struct {
		IDs []string `json:"ids"`
	}{IDs: ids})
	if err != nil {
		return nil, fmt.Errorf("encode batch request: %w", err)
	}

	bodyBytes, err := c.do(ctx, http.MethodPost, reqURL.String(), reqBody, opts)
	if err != nil {
		return nil, err
	}

	var res BatchEventsResult
	if err := json.Unmarshal(bodyBytes, &res); err != nil {
		return nil, fmt.Errorf("decode batch response: %w", err)
	}
	return &res, nil
}

// GetIndexerStats fetches GET /v1/stats/indexer: real-time indexer health,
// throughput, and ingest lag (issue #294).
func (c *Client) GetIndexerStats(ctx context.Context, opts ...RequestOption) (*IndexerStats, error) {
	reqURL, err := url.Parse(c.config.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid BaseURL: %w", err)
	}
	reqURL.Path = "/v1/stats/indexer"

	bodyBytes, err := c.do(ctx, http.MethodGet, reqURL.String(), nil, opts)
	if err != nil {
		return nil, err
	}

	var res IndexerStats
	if err := json.Unmarshal(bodyBytes, &res); err != nil {
		return nil, fmt.Errorf("decode stats response: %w", err)
	}
	return &res, nil
}

// ---------------------------------------------------------------------
// Webhook subscription management (issue #677)
//
// Signature verification for INCOMING deliveries is VerifyWebhookSignature
// in webhook.go, deliberately separate from these methods — these manage
// the subscription resource itself via the /v1/webhooks API. Request and
// response shapes are the generated OpenAPI models in the openapi package;
// do not hand-edit those, they are regenerated from api/openapi.yaml by
// scripts/generate_sdk_models.py.
// ---------------------------------------------------------------------

// CreateWebhook creates a webhook subscription for a contract's events.
//
// The returned WebhookCreateResponse.Secret is shown only once — store it
// to verify incoming deliveries with VerifyWebhookSignature.
func (c *Client) CreateWebhook(ctx context.Context, req openapi.WebhookCreateRequest, opts ...RequestOption) (*openapi.WebhookCreateResponse, error) {
	reqURL, err := url.Parse(c.config.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid BaseURL: %w", err)
	}
	reqURL.Path = "/v1/webhooks"

	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode webhook create request: %w", err)
	}

	bodyBytes, err := c.do(ctx, http.MethodPost, reqURL.String(), reqBody, opts)
	if err != nil {
		return nil, err
	}

	var res openapi.WebhookCreateResponse
	if err := json.Unmarshal(bodyBytes, &res); err != nil {
		return nil, fmt.Errorf("decode webhook create response: %w", err)
	}
	return &res, nil
}

// ListWebhooks lists webhook subscriptions for the caller's API key,
// cursor-paginated. Pass limit <= 0 to use the server default, and an empty
// cursor for the first page.
func (c *Client) ListWebhooks(ctx context.Context, limit int, cursor string, opts ...RequestOption) (*openapi.ListWebhooksResponse, error) {
	reqURL, err := url.Parse(c.config.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid BaseURL: %w", err)
	}
	reqURL.Path = "/v1/webhooks"

	q := reqURL.Query()
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	reqURL.RawQuery = q.Encode()

	bodyBytes, err := c.do(ctx, http.MethodGet, reqURL.String(), nil, opts)
	if err != nil {
		return nil, err
	}

	var res openapi.ListWebhooksResponse
	if err := json.Unmarshal(bodyBytes, &res); err != nil {
		return nil, fmt.Errorf("decode webhook list response: %w", err)
	}
	return &res, nil
}

// DeleteWebhook permanently deletes a webhook subscription.
//
// A subscription owned by a different API key returns the same "not found"
// error as one that doesn't exist at all — the API deliberately does not
// distinguish the two.
func (c *Client) DeleteWebhook(ctx context.Context, id string, opts ...RequestOption) error {
	reqURL, err := url.Parse(c.config.BaseURL)
	if err != nil {
		return fmt.Errorf("invalid BaseURL: %w", err)
	}
	reqURL.Path = "/v1/webhooks/" + id

	_, err = c.do(ctx, http.MethodDelete, reqURL.String(), nil, opts)
	return err
}

// PauseWebhook pauses deliveries for a webhook subscription without
// deleting it.
func (c *Client) PauseWebhook(ctx context.Context, id string, opts ...RequestOption) (*openapi.WebhookStatusResponse, error) {
	return c.setWebhookPauseState(ctx, id, "pause", opts)
}

// ResumeWebhook resumes deliveries for a previously paused webhook
// subscription.
func (c *Client) ResumeWebhook(ctx context.Context, id string, opts ...RequestOption) (*openapi.WebhookStatusResponse, error) {
	return c.setWebhookPauseState(ctx, id, "resume", opts)
}

func (c *Client) setWebhookPauseState(ctx context.Context, id, action string, opts []RequestOption) (*openapi.WebhookStatusResponse, error) {
	reqURL, err := url.Parse(c.config.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid BaseURL: %w", err)
	}
	reqURL.Path = "/v1/webhooks/" + id + "/" + action

	bodyBytes, err := c.do(ctx, http.MethodPatch, reqURL.String(), nil, opts)
	if err != nil {
		return nil, err
	}

	var res openapi.WebhookStatusResponse
	if err := json.Unmarshal(bodyBytes, &res); err != nil {
		return nil, fmt.Errorf("decode webhook status response: %w", err)
	}
	return &res, nil
}

// RotateWebhookSecret rotates a webhook subscription's signing secret.
//
// The previous secret remains valid for the overlap window described by
// VerifyWebhookSignature's multi-token header handling — update your
// receiver to the new WebhookRotateSecretResponse.Secret before the old one
// is fully retired.
func (c *Client) RotateWebhookSecret(ctx context.Context, id string, opts ...RequestOption) (*openapi.WebhookRotateSecretResponse, error) {
	reqURL, err := url.Parse(c.config.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid BaseURL: %w", err)
	}
	reqURL.Path = "/v1/webhooks/" + id + "/rotate-secret"

	bodyBytes, err := c.do(ctx, http.MethodPost, reqURL.String(), nil, opts)
	if err != nil {
		return nil, err
	}

	var res openapi.WebhookRotateSecretResponse
	if err := json.Unmarshal(bodyBytes, &res); err != nil {
		return nil, fmt.Errorf("decode webhook rotate-secret response: %w", err)
	}
	return &res, nil
}

// do issues an HTTP request, retrying according to the effective retry
// policy (client-level config merged with any per-call opts). Retries apply
// uniformly regardless of method here because every endpoint wrapped by this
// client is a read (batch-get included), so retrying is always safe. Returns
// the response body on a 200 OK, or a typed error (*TridentApiError /
// *RequestError) once retries are exhausted or the status is non-retryable.
//
// Cancellation: every attempt is issued via http.NewRequestWithContext, so a
// cancelled or deadline-exceeded ctx aborts an in-flight attempt immediately
// and short-circuits any pending backoff sleep (issue #283).
func (c *Client) do(ctx context.Context, method, reqURL string, body []byte, opts []RequestOption) ([]byte, error) {
	retryCfg := c.effectiveRetryConfig(opts)
	maxAttempts := 1
	if retryCfg != nil {
		maxAttempts = retryCfg.MaxAttempts
	}

	var totalWaited time.Duration

	for attempt := 1; ; attempt++ {
		var bodyReader io.Reader
		if body != nil {
			bodyReader = bytes.NewReader(body)
		}

		req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.config.APIKey != "" {
			req.Header.Set("X-API-Key", c.config.APIKey)
		}

		resp, err := c.client.Do(req)
		if err != nil {
			if retryCfg != nil && attempt < maxAttempts {
				wait := computeBackoff(attempt, retryCfg)
				if totalWaited+wait <= retryCfg.MaxTotalWait {
					totalWaited += wait
					if !sleepCtx(ctx, wait) {
						return nil, ctx.Err()
					}
					continue
				}
			}
			return nil, &RequestError{Attempts: attempt, Err: err}
		}

		// Every existing caller's endpoint returns 200, so this was
		// previously hardcoded to StatusOK; the webhook management
		// endpoints (issue #677) return 201 (create) and 204 (delete), so
		// the check is widened to any 2xx rather than adding a second
		// request path just for those two status codes.
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			if retryCfg != nil && isRetryableStatus(resp.StatusCode) && attempt < maxAttempts {
				wait := retryAfterOrBackoff(resp.Header.Get("Retry-After"), attempt, retryCfg)
				if totalWaited+wait <= retryCfg.MaxTotalWait {
					totalWaited += wait
					if !sleepCtx(ctx, wait) {
						return nil, ctx.Err()
					}
					continue
				}
			}
			apiErr := parseApiError(resp.StatusCode, string(respBody))
			apiErr.Attempts = attempt
			return nil, apiErr
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read response body: %w", err)
		}
		return respBody, nil
	}
}
