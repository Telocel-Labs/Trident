package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	trident "github.com/Depo-dev/trident/services/api/internal/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// EventsHandler proxies REST requests to the Rust gRPC API.
type EventsHandler struct {
	client trident.EventsClient
}

func NewEventsHandler(client trident.EventsClient) *EventsHandler {
	return &EventsHandler{client: client}
}

// ListEvents handles GET /v1/events
func (h *EventsHandler) ListEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	var ledgerFrom, ledgerTo uint64
	if s := q.Get("ledgerFrom"); s != "" {
		ledgerFrom, _ = strconv.ParseUint(s, 10, 64)
	}
	if s := q.Get("ledgerTo"); s != "" {
		ledgerTo, _ = strconv.ParseUint(s, 10, 64)
	}

	limit := uint32(100)
	if s := q.Get("limit"); s != "" {
		if n, err := strconv.ParseUint(s, 10, 32); err == nil {
			limit = uint32(n)
		}
	}

	resp, err := h.client.ListEvents(r.Context(), &trident.ListEventsRequest{
		ContractId: q.Get("contractId"),
		Topic_0:    q.Get("topic0"),
		Topic_1:    q.Get("topic1"),
		LedgerFrom: ledgerFrom,
		LedgerTo:   ledgerTo,
		Cursor:     q.Get("cursor"),
		Limit:      limit,
	})
	if err != nil {
		writeGRPCError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// GetEvent handles GET /v1/events/{id}
func (h *EventsHandler) GetEvent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	resp, err := h.client.GetEvent(r.Context(), &trident.GetEventRequest{Id: id})
	if err != nil {
		writeGRPCError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// Health handles GET /v1/health
func Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// helpers

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Body already started; nothing we can do.
		return
	}
}

func writeGRPCError(w http.ResponseWriter, err error) {
	st, _ := status.FromError(err)
	switch st.Code() {
	case codes.NotFound:
		http.Error(w, st.Message(), http.StatusNotFound)
	case codes.InvalidArgument:
		http.Error(w, st.Message(), http.StatusBadRequest)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
