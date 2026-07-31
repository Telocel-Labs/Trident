package trident

import (
	"bufio"
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
)

// Subscription represents an active real-time event subscription, backed by
// a resumable Server-Sent Events connection to GET /v1/events/stream.
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

// SubscribeToContract opens a subscription to real-time events for the
// specified contract via the SSE stream endpoint (issue #281).
//
// The connection reconnects automatically with exponential backoff on
// transport failure. Every event carries the SSE `id:` field the server
// assigns it; on reconnect the client sends that id back as `Last-Event-ID`
// so the server resumes the stream from where the connection left off
// instead of silently skipping events emitted during the gap (server-side
// resume support: issue #235). If the requested id has aged out of the
// stream's retention window, the server resumes from the oldest available
// event and emits a `gap` notice, which the client treats as informational.
func (c *Client) SubscribeToContract(ctx context.Context, params SubscribeToContractParams) (*Subscription, error) {
	if params.ContractID == "" {
		return nil, fmt.Errorf("contractID is required")
	}

	reqURL, err := url.Parse(c.config.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid BaseURL: %w", err)
	}
	reqURL.Path = "/v1/events/stream"

	q := reqURL.Query()
	q.Set("contractId", params.ContractID)
	if params.Topic0 != "" {
		q.Set("topic0", params.Topic0)
	}
	reqURL.RawQuery = q.Encode()

	eventsChan := make(chan *SorobanEvent, 128)
	errorsChan := make(chan error, 16)

	subCtx, cancel := context.WithCancel(ctx)
	sub := &Subscription{
		Events:     eventsChan,
		Errors:     errorsChan,
		cancelFunc: cancel,
		done:       make(chan struct{}),
	}

	go c.runStreamLoop(subCtx, reqURL.String(), eventsChan, errorsChan, sub.done)

	return sub, nil
}

// runStreamLoop owns the connect/read/reconnect cycle for one subscription.
func (c *Client) runStreamLoop(ctx context.Context, streamURL string, events chan<- *SorobanEvent, errorsChan chan<- error, done <-chan struct{}) {
	defer close(events)
	defer close(errorsChan)

	const (
		initialBackoff = 500 * time.Millisecond
		maxBackoff     = 30 * time.Second
	)
	backoff := initialBackoff
	var lastEventID string

	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		default:
		}

		body, err := c.dialStream(ctx, streamURL, lastEventID)
		if err != nil {
			sendNonBlocking(errorsChan, err)
			if !waitBackoff(ctx, done, &backoff, maxBackoff) {
				return
			}
			continue
		}

		// A connection was established: reset backoff for the next failure.
		backoff = initialBackoff

		readErr := readSSE(body, events, &lastEventID, done)
		body.Close()

		if readErr != nil && ctx.Err() == nil {
			sendNonBlocking(errorsChan, fmt.Errorf("stream read error: %w", readErr))
		}

		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		default:
		}

		if !waitBackoff(ctx, done, &backoff, maxBackoff) {
			return
		}
	}
}

// dialStream opens the SSE connection, sending Last-Event-ID when resuming
// after a prior disconnect. The caller owns closing the returned body.
func (c *Client) dialStream(ctx context.Context, streamURL, lastEventID string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build stream request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if c.config.APIKey != "" {
		req.Header.Set("X-API-Key", c.config.APIKey)
	}
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("stream connection failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, parseApiError(resp.StatusCode, string(respBody))
	}
	return resp.Body, nil
}

// readSSE parses `id:`/`data:` frames from an SSE body, forwarding decoded
// events on events and advancing *lastEventID as ids are seen. It returns
// when the body is exhausted (server closed the connection) or done fires.
func readSSE(body io.Reader, events chan<- *SorobanEvent, lastEventID *string, done <-chan struct{}) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var currentID string
	var dataLines []string

	flush := func() {
		if len(dataLines) == 0 {
			return
		}
		data := strings.Join(dataLines, "\n")
		dataLines = nil

		if currentID != "" {
			*lastEventID = currentID
			currentID = ""
		}

		ev, err := decodeStreamEvent([]byte(data))
		if err != nil {
			// Malformed or non-event frames (e.g. the `gap` notice) are not
			// forwarded to the caller; the resume-from-oldest behaviour they
			// signal is transparent to the SDK.
			return
		}

		select {
		case events <- ev:
		case <-done:
		default:
			// Slow consumer: drop rather than block the read loop.
		}
	}

	for scanner.Scan() {
		select {
		case <-done:
			return nil
		default:
		}

		line := scanner.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "id:"):
			currentID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		default:
			// Ignore other SSE fields (event:, comments, retry:).
		}
	}
	flush()
	return scanner.Err()
}

// streamEventWire is the flat field shape the indexer writes to the Redis
// stream (crates/indexer/src/redis_stream/mod.rs) and the API relays
// verbatim over SSE: every value is a string, including topics (itself
// JSON-encoded) and the numeric fields.
type streamEventWire struct {
	ContractID      string `json:"contract_id"`
	LedgerSequence  string `json:"ledger_sequence"`
	LedgerTimestamp string `json:"ledger_timestamp"`
	TransactionHash string `json:"transaction_hash"`
	EventIndex      string `json:"event_index"`
	EventType       string `json:"event_type"`
	Topics          string `json:"topics"`
	Data            string `json:"data"`
	EventID         string `json:"event_id"`
}

func decodeStreamEvent(raw []byte) (*SorobanEvent, error) {
	var wire streamEventWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("decode stream event: %w", err)
	}

	var topics []string
	if wire.Topics != "" {
		if err := json.Unmarshal([]byte(wire.Topics), &topics); err != nil {
			return nil, fmt.Errorf("decode stream event topics: %w", err)
		}
	}

	ledgerSeq, err := strconv.ParseUint(wire.LedgerSequence, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("decode stream event ledger_sequence: %w", err)
	}
	eventIndex, err := strconv.ParseUint(wire.EventIndex, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("decode stream event event_index: %w", err)
	}

	return &SorobanEvent{
		ID:              wire.EventID,
		ContractID:      wire.ContractID,
		LedgerSequence:  ledgerSeq,
		LedgerTimestamp: wire.LedgerTimestamp,
		TransactionHash: wire.TransactionHash,
		EventIndex:      uint32(eventIndex),
		EventType:       wire.EventType,
		Topics:          topics,
		Data:            wire.Data,
	}, nil
}

func sendNonBlocking(ch chan<- error, err error) {
	select {
	case ch <- err:
	default:
	}
}

// waitBackoff sleeps for *backoff (doubling it afterward, capped at max),
// returning false if ctx or done fire first.
func waitBackoff(ctx context.Context, done <-chan struct{}, backoff *time.Duration, max time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-done:
		return false
	case <-time.After(*backoff):
		*backoff *= 2
		if *backoff > max {
			*backoff = max
		}
		return true
	}
}
