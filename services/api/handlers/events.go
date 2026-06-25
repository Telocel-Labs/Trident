package handlers

import (
	"net/http"

	"github.com/Depo-dev/trident/services/api/cursor"
	"github.com/Depo-dev/trident/services/api/validation"
)

// ListEventsResponse is the response envelope for GET /v1/events.
type ListEventsResponse struct {
	Events     []any  `json:"events"`
	NextCursor string `json:"next_cursor"`
}

// ListEvents handles GET /v1/events (issues #42, #44).
//
// Validates query parameters and decodes the opaque pagination cursor before
// forwarding to the gRPC backend. Returns 400 on any validation failure.
func ListEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	params, verr := validation.ValidateQueryEvents(
		q.Get("limit"),
		q.Get("ledgerFrom"),
		q.Get("ledgerTo"),
		q.Get("contractId"),
		q.Get("cursor"),
	)
	if verr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{
				"field":   verr.Field,
				"message": verr.Message,
			},
		})
		return
	}

	// Decode opaque cursor → internal paging token (issue #44).
	var pagingToken string
	if raw := q.Get("cursor"); raw != "" {
		decoded, err := cursor.Decode(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": map[string]string{
					"field":   "cursor",
					"message": "invalid cursor",
				},
			})
			return
		}
		pagingToken = decoded
	}

	// TODO: forward params + pagingToken to gRPC backend once wired in.
	_ = params

	writeJSON(w, http.StatusOK, ListEventsResponse{
		Events:     []any{},
		NextCursor: cursor.Encode(pagingToken),
	})
}

// GetEvent handles GET /v1/events/{id}.
//
// Validates the :id path parameter as a UUID v4 (issue #42).
// Returns 400 Bad Request when the format is invalid.
func GetEvent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if verr := validation.ValidateEventID(id); verr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{
				"field":   verr.Field,
				"message": verr.Message,
			},
		})
		return
	}

	// TODO: fetch from gRPC backend once the gRPC client is wired in.
	writeJSON(w, http.StatusOK, map[string]any{"event": nil})
}
