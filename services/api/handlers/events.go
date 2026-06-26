package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/Depo-dev/trident/services/api/cursor"
	"github.com/Depo-dev/trident/services/api/gen"
	"github.com/Depo-dev/trident/services/api/internal/httputil"
	"github.com/Depo-dev/trident/services/api/validation"
)

// ListEventsResponse is the response envelope for GET /v1/events.
type ListEventsResponse struct {
	Events     []*EventJSON `json:"events"`
	HasMore    bool         `json:"has_more"`
	NextCursor *string      `json:"next_cursor"`
}

// EventJSON is the JSON representation of an event
type EventJSON struct {
	ID              string   `json:"id"`
	ContractID      string   `json:"contract_id"`
	LedgerSequence  uint64   `json:"ledger_sequence"`
	LedgerTimestamp string   `json:"ledger_timestamp"`
	TransactionHash string   `json:"transaction_hash"`
	EventIndex      uint32   `json:"event_index"`
	EventType       string   `json:"event_type"`
	Topics          []string `json:"topics"`
	Data            string   `json:"data"`
	CreatedAt       string   `json:"created_at"`
}

// eventsClient is set at initialization
var eventsClient gen.EventsClient

// SetEventsClient sets the gRPC events client (called from main)
func SetEventsClient(client gen.EventsClient) {
	eventsClient = client
}

// ListEvents handles GET /v1/events (issues #42, #44, #143).
//
// Validates query parameters and decodes the opaque pagination cursor before
// forwarding to the gRPC backend. Returns 400 on any validation failure.
// The response always includes has_more and next_cursor per issue #143.
func ListEvents(w http.ResponseWriter, r *http.Request) {
	if eventsClient == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, httputil.INTERNAL, "gRPC backend unavailable")
		return
	}

	q := r.URL.Query()
	params, verr := validation.ValidateQueryEvents(
		q.Get("limit"),
		q.Get("ledgerFrom"),
		q.Get("ledgerTo"),
		q.Get("contractId"),
		q.Get("cursor"),
	)
	if verr != nil {
		httputil.WriteError(w, http.StatusBadRequest, httputil.INVALID_ARGUMENT, verr.Message)
		return
	}

	// Decode opaque cursor → internal paging token (issue #44).
	var pagingToken string
	if raw := q.Get("cursor"); raw != "" {
		decoded, err := cursor.Decode(raw)
		if err != nil {
			httputil.WriteError(w, http.StatusBadRequest, httputil.INVALID_ARGUMENT, "invalid cursor")
			return
		}
		pagingToken = decoded
	}

	// Build gRPC request
	grpcReq := &gen.ListEventsRequest{
		ContractId: params.ContractID,
		Topic0:     q.Get("topic0"),
		Topic1:     q.Get("topic1"),
		Cursor:     pagingToken,
		Limit:      uint32(params.Limit),
	}

	if params.LedgerFrom != nil {
		grpcReq.LedgerFrom = uint64(*params.LedgerFrom)
	}
	if params.LedgerTo != nil {
		grpcReq.LedgerTo = uint64(*params.LedgerTo)
	}

	// Call gRPC backend with timeout
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	resp, err := eventsClient.ListEvents(ctx, grpcReq)
	if err != nil {
		statusCode, code := httputil.GRPCToHTTP(err)
		slog.ErrorContext(r.Context(), "grpc ListEvents failed", "err", err)
		httputil.WriteError(w, statusCode, code, "failed to fetch events")
		return
	}

	events := make([]*EventJSON, len(resp.Events))
	for i, event := range resp.Events {
		events[i] = protoEventToJSON(event)
	}

	hasMore := resp.HasMore
	var nextCursor *string
	if resp.NextCursor != "" {
		encoded := cursor.Encode(resp.NextCursor)
		nextCursor = &encoded
	}

	writeJSON(w, http.StatusOK, ListEventsResponse{
		Events:     events,
		HasMore:    hasMore,
		NextCursor: nextCursor,
	})
}

// GetEvent handles GET /v1/events/{id}.
//
// Validates the :id path parameter as a UUID v4 (issue #42).
// Returns 400 Bad Request when the format is invalid.
func GetEvent(w http.ResponseWriter, r *http.Request) {
	if eventsClient == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, httputil.INTERNAL, "gRPC backend unavailable")
		return
	}

	id := r.PathValue("id")
	if verr := validation.ValidateEventID(id); verr != nil {
		httputil.WriteError(w, http.StatusBadRequest, httputil.INVALID_ARGUMENT, verr.Message)
		return
	}

	// Call gRPC backend
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	event, err := eventsClient.GetEvent(ctx, &gen.GetEventRequest{Id: id})
	if err != nil {
		statusCode, code := httputil.GRPCToHTTP(err)
		slog.ErrorContext(r.Context(), "grpc GetEvent failed", "err", err)
		httputil.WriteError(w, statusCode, code, "event not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"event": protoEventToJSON(event),
	})
}

// protoEventToJSON converts a proto Event to EventJSON
func protoEventToJSON(event *gen.Event) *EventJSON {
	return &EventJSON{
		ID:              event.Id,
		ContractID:      event.ContractId,
		LedgerSequence:  event.LedgerSequence,
		LedgerTimestamp: event.LedgerTimestamp,
		TransactionHash: event.TransactionHash,
		EventIndex:      event.EventIndex,
		EventType:       event.EventType,
		Topics:          event.Topics,
		Data:            event.Data,
		CreatedAt:       event.CreatedAt,
	}
}
