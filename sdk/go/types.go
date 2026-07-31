package trident

// TridentClientConfig configurations for the Trident Go Client.
type TridentClientConfig struct {
	// BaseURL is the HTTP address of the Trident API (e.g., "http://localhost:3000")
	BaseURL string
	// APIKey is the API Key used for authentication (sent via X-API-Key header)
	APIKey string
	// Retry configures automatic retries with backoff for idempotent (GET)
	// requests. A nil value uses DefaultRetryConfig. Overridden per-call by
	// WithRetry. Ignored when RetryDisabled is true.
	Retry *RetryConfig
	// RetryDisabled disables automatic retries for this client entirely.
	// Overridden per-call by WithRetry/WithRetryDisabled.
	RetryDisabled bool
}

// QueryEventsParams options to filter historical events.
type QueryEventsParams struct {
	ContractID string  `json:"contract_id,omitempty"`
	Topic0     string  `json:"topic_0,omitempty"`
	Topic1     string  `json:"topic_1,omitempty"`
	LedgerFrom *uint64 `json:"ledger_from,omitempty"`
	LedgerTo   *uint64 `json:"ledger_to,omitempty"`
	Cursor     string  `json:"cursor,omitempty"`
	Limit      int     `json:"limit,omitempty"`
}

// PaginatedEvents envelope containing a list of events and cursor for pagination.
type PaginatedEvents struct {
	Events     []*SorobanEvent `json:"events"`
	HasMore    bool            `json:"has_more"`
	NextCursor string          `json:"next_cursor"`
}

// SorobanEvent represents a single Soroban contract event indexed by Trident.
type SorobanEvent struct {
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

// SubscribeToContractParams options for real-time contract event subscription.
type SubscribeToContractParams struct {
	ContractID string
	Topic0     string
}

// BatchEventsResult is the response envelope for BatchGetEvents. Events and
// Missing both preserve the request order of ids; duplicate ids are
// deduplicated on first occurrence (issue #228).
type BatchEventsResult struct {
	Events  []*SorobanEvent `json:"events"`
	Missing []string        `json:"missing"`
}

// IndexerStats mirrors the response of GET /v1/stats/indexer: real-time
// indexer health, throughput, and ingest lag (issue #294). Pointer fields are
// nil when the underlying value is not yet known (e.g. chain tip lookup
// failed, or no poll has completed since startup).
type IndexerStats struct {
	// Status is one of "healthy", "lagging", or "stalled".
	Status  string `json:"status"`
	Network string `json:"network"`

	LastLedgerIndexed *int64 `json:"last_ledger_indexed"`
	ChainTipLedger    *int64 `json:"chain_tip_ledger"`
	LagLedgers        *int64 `json:"lag_ledgers"`
	// LagSecondsEstimated approximates wall-clock staleness as
	// LagLedgers * average ledger close time. Nil whenever LagLedgers is nil.
	LagSecondsEstimated *float64 `json:"lag_seconds_estimated"`

	EventsIndexedTotal *int64  `json:"events_indexed_total"`
	EventsLastPoll     *int64  `json:"events_last_poll"`
	AvgPollDurationMs  *int64  `json:"avg_poll_duration_ms"`
	LastPollAt         *string `json:"last_poll_at"`
}
