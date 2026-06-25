package handlers

import (
	"net/http"

	"github.com/Depo-dev/trident/services/api/validation"
)

// EventsResponse is a placeholder response envelope for the events endpoints.
// The gRPC backend integration is wired separately; validation is the scope here.
type EventsResponse struct {
	Events []any `json:"events"`
}

// ListEvents handles GET /v1/events.
//
// Validates query parameters via the validation package before forwarding to
// the gRPC backend (issue #42). Returns 400 Bad Request on any validation failure.
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

	// TODO: forward params to gRPC backend once the gRPC client is wired in.
	_ = params

	writeJSON(w, http.StatusOK, EventsResponse{Events: []any{}})
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
