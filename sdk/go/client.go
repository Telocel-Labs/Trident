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

		if resp.StatusCode != http.StatusOK {
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
		return bodyBytes, nil
	}
}

// Subscription represents an active WebSocket subscription stream.
type Subscription struct {
	Events     <-chan *SorobanEvent
	Errors     <-chan error
	cancelFunc context.CancelFunc
	closeOnce  sync.Once
	done       chan struct{}
}

// Unsubscribe closes the subscription and terminates the background reconnection loop.
func (s *Subscription) Unsubscribe() {
	s.closeOnce.Do(func() {
		s.cancelFunc()
		close(s.done)
	})
}

// SubscribeToContract opens a WebSocket subscription to real-time events for the specified contract.
func (c *Client) SubscribeToContract(ctx context.Context, params SubscribeToContractParams) (*Subscription, error) {
	if params.ContractID == "" {
		return nil, fmt.Errorf("contractID is required")
	}
	if err := c.config.requireAPIKey(); err != nil {
		return nil, err
	}

	parsedBase, err := url.Parse(c.config.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid BaseURL: %w", err)
	}

	// Determine WebSocket scheme
	wsScheme := "ws"
	if parsedBase.Scheme == "https" {
		wsScheme = "wss"
	}

	wsURL := url.URL{
		Scheme: wsScheme,
		Host:   parsedBase.Host,
		Path:   "/ws",
	}

	q := wsURL.Query()
	q.Set("contractId", params.ContractID)
	if params.Topic0 != "" {
		q.Set("topic0", params.Topic0)
	}
	wsURL.RawQuery = q.Encode()

	eventsChan := make(chan *SorobanEvent, 128)
	errorsChan := make(chan error, 16)

	subCtx, cancel := context.WithCancel(ctx)
	sub := &Subscription{
		Events:     eventsChan,
		Errors:     errorsChan,
		cancelFunc: cancel,
		done:       make(chan struct{}),
	}

	go c.runSubscriptionLoop(subCtx, wsURL.String(), eventsChan, errorsChan, sub.done)

	return sub, nil
}

func (c *Client) runSubscriptionLoop(ctx context.Context, wsAddr string, events chan<- *SorobanEvent, errorsChan chan<- error, done <-chan struct{}) {
	defer close(events)
	defer close(errorsChan)

	backoff := 500 * time.Millisecond
	maxBackoff := 30 * time.Second

	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		default:
		}

		// Ensure origin header is set as required by some websocket implementations
		origin := c.config.BaseURL
		if !strings.HasPrefix(origin, "http://") && !strings.HasPrefix(origin, "https://") {
			origin = "http://" + origin
		}

		headers := http.Header{}
		if c.config.APIKey != "" {
			headers.Set("X-API-Key", c.config.APIKey)
		}

		config, err := websocket.NewConfig(wsAddr, origin)
		var conn *websocket.Conn
		if err == nil {
			config.Header = headers
			conn, err = websocket.DialConfig(config)
		}

		if err != nil {
			select {
			case errorsChan <- fmt.Errorf("websocket connection failed: %w", err):
			default:
			}

			// Exponential backoff with cancellation awareness
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-time.After(backoff):
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				continue
			}
		}

		// Reset backoff on successful connection
		backoff = 500 * time.Millisecond

		// Read loop for this connection
		readErrChan := make(chan error, 1)
		go func() {
			for {
				var msg []byte
				err := websocket.Message.Receive(conn, &msg)
				if err != nil {
					readErrChan <- err
					return
				}

				var ev SorobanEvent
				if err := json.Unmarshal(msg, &ev); err != nil {
					// Pings might be empty or non-event frames, but they are not handled by Message.Receive usually,
					// except control frames which x/net/websocket handles internally.
					// Let's filter out non-JSON or empty payloads gracefully.
					continue
				}

				select {
				case events <- &ev:
				default:
					// Slow consumer: skip or queue is full
				}
			}
		}()

		// Monitor read errors or termination
		var readErr error
		select {
		case <-ctx.Done():
			conn.Close()
			return
		case <-done:
			conn.Close()
			return
		case readErr = <-readErrChan:
			conn.Close()
		}

		if readErr != nil && readErr != io.EOF {
			select {
			case errorsChan <- fmt.Errorf("websocket read error: %w", readErr):
			default:
			}
		}

		// Brief sleep before reconnecting
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}
