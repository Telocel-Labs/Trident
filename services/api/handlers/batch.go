package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/Depo-dev/trident/services/api/gen"
	"github.com/Depo-dev/trident/services/api/internal/httputil"
	"github.com/Depo-dev/trident/services/api/validation"
)

const batchEventsMaxIDs = 100

type batchRequest struct {
	IDs []string `json:"ids"`
}

// BatchEventsResponse is the response envelope for POST /v1/events/batch.
type BatchEventsResponse struct {
	Events  []*EventJSON `json:"events"`
	Missing []string     `json:"missing"`
}

// BatchGetEvents handles POST /v1/events/batch.
//
// Accepts a JSON body `{"ids": ["uuid1", ...]}`, validates each ID as a UUID v4,
// fetches up to batchEventsMaxIDs events in parallel via gRPC GetEvent, and
// returns found events plus a missing array for any IDs that were not indexed.
func BatchGetEvents(w http.ResponseWriter, r *http.Request) {
	if eventsClient == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, httputil.INTERNAL, "gRPC backend unavailable")
		return
	}

	var req batchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, httputil.INVALID_ARGUMENT, "request body must be valid JSON")
		return
	}

	if len(req.IDs) > batchEventsMaxIDs {
		httputil.WriteError(w, http.StatusBadRequest, httputil.INVALID_ARGUMENT,
			"maximum 100 IDs per request")
		return
	}

	// Validate all UUIDs up front; collect invalid ones and return 400.
	var invalid []string
	for _, id := range req.IDs {
		if err := validation.ValidateEventID(id); err != nil {
			invalid = append(invalid, id)
		}
	}
	if len(invalid) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":       "one or more IDs are not valid UUID v4",
			"code":        string(httputil.INVALID_ARGUMENT),
			"invalid_ids": invalid,
		})
		return
	}

	type result struct {
		id    string
		event *gen.Event
		found bool
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	results := make([]result, len(req.IDs))
	var wg sync.WaitGroup
	for i, id := range req.IDs {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			event, err := eventsClient.GetEvent(ctx, &gen.GetEventRequest{Id: id})
			if err != nil {
				results[i] = result{id: id, found: false}
				return
			}
			results[i] = result{id: id, event: event, found: true}
		}(i, id)
	}
	wg.Wait()

	events := make([]*EventJSON, 0, len(req.IDs))
	var missing []string
	for _, r := range results {
		if r.found {
			events = append(events, protoEventToJSON(r.event))
		} else {
			missing = append(missing, r.id)
		}
	}
	if missing == nil {
		missing = []string{}
	}

	writeJSON(w, http.StatusOK, BatchEventsResponse{
		Events:  events,
		Missing: missing,
	})
}
