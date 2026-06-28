package trident

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

// Client is the Trident Go Client.
type Client struct {
	config TridentClientConfig
	client *http.Client
}

// NewClient creates a new Trident Go Client.
func NewClient(config TridentClientConfig) *Client {
	return &Client{
		config: config,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// QueryEvents fetches a page of historical events matching the filter.
func (c *Client) QueryEvents(ctx context.Context, params QueryEventsParams) (*PaginatedEvents, error) {
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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create query request: %w", err)
	}

	if c.config.APIKey != "" {
		req.Header.Set("X-API-Key", c.config.APIKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute query request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("query events failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var res PaginatedEvents
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("decode query response: %w", err)
	}

	return &res, nil
}

// GetEventByID fetches a single event by its UUID ID.
func (c *Client) GetEventByID(ctx context.Context, id string) (*SorobanEvent, error) {
	reqURL, err := url.Parse(c.config.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid BaseURL: %w", err)
	}

	reqURL.Path = "/v1/events/" + id

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create get request: %w", err)
	}

	if c.config.APIKey != "" {
		req.Header.Set("X-API-Key", c.config.APIKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute get request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get event failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var wrapper struct {
		Event *SorobanEvent `json:"event"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return nil, fmt.Errorf("decode get response: %w", err)
	}

	if wrapper.Event == nil {
		return nil, fmt.Errorf("event not found in response envelope")
	}

	return wrapper.Event, nil
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
