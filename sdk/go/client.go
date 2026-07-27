package trident

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
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
		return nil, parseApiError(resp.StatusCode, string(bodyBytes))
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
		return nil, parseApiError(resp.StatusCode, string(bodyBytes))
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

	wsBase := url.URL{
		Scheme: wsScheme,
		Host:   parsedBase.Host,
		Path:   "/ws",
	}

	eventsChan := make(chan *SorobanEvent, 128)
	errorsChan := make(chan error, 16)

	subCtx, cancel := context.WithCancel(ctx)
	sub := &Subscription{
		Events:     eventsChan,
		Errors:     errorsChan,
		cancelFunc: cancel,
		done:       make(chan struct{}),
	}

	go c.runSubscriptionLoop(subCtx, wsBase, params, eventsChan, errorsChan, sub.done)

	return sub, nil
}

// withJitter adds ±20% random jitter to a duration to avoid reconnect thundering herds.
func withJitter(d time.Duration) time.Duration {
	jitter := time.Duration(rand.Int63n(int64(d / 5)))
	return d + jitter
}

func (c *Client) runSubscriptionLoop(
	ctx context.Context,
	wsBase url.URL,
	params SubscribeToContractParams,
	events chan<- *SorobanEvent,
	errorsChan chan<- error,
	done <-chan struct{},
) {
	defer close(events)
	defer close(errorsChan)

	const initialBackoff = 500 * time.Millisecond
	const maxBackoff = 30 * time.Second
	backoff := initialBackoff
	lastEventID := ""

	origin := c.config.BaseURL
	if !strings.HasPrefix(origin, "http://") && !strings.HasPrefix(origin, "https://") {
		origin = "http://" + origin
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		default:
		}

		// Build reconnect URL, appending cursor to resume from last seen event.
		q := wsBase.Query()
		q.Set("contractId", params.ContractID)
		if params.Topic0 != "" {
			q.Set("topic0", params.Topic0)
		}
		if lastEventID != "" {
			q.Set("cursor", lastEventID)
		}
		wsAddr := wsBase
		wsAddr.RawQuery = q.Encode()

		isResume := lastEventID != ""

		headers := http.Header{}
		if c.config.APIKey != "" {
			headers.Set("X-API-Key", c.config.APIKey)
		}

		config, err := websocket.NewConfig(wsAddr.String(), origin)
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

			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-time.After(withJitter(backoff)):
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				continue
			}
		}

		// Successful connection — reset backoff and fire lifecycle hook.
		backoff = initialBackoff
		if isResume && params.OnResumed != nil {
			params.OnResumed(lastEventID)
		} else if params.OnConnected != nil {
			params.OnConnected()
		}

		// Read loop for this connection.
		readErrChan := make(chan error, 1)
		go func() {
			for {
				var msg []byte
				if err := websocket.Message.Receive(conn, &msg); err != nil {
					readErrChan <- err
					return
				}

				var ev SorobanEvent
				if err := json.Unmarshal(msg, &ev); err != nil {
					continue
				}

				if ev.ID != "" {
					lastEventID = ev.ID
				}

				select {
				case events <- &ev:
				default:
					// Slow consumer: drop rather than block the read loop.
				}
			}
		}()

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

		if params.OnDisconnected != nil {
			params.OnDisconnected()
		}

		if readErr != nil && readErr != io.EOF {
			select {
			case errorsChan <- fmt.Errorf("websocket read error: %w", readErr):
			default:
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-time.After(withJitter(backoff)):
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}
