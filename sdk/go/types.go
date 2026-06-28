package trident

// TridentClientConfig configurations for the Trident Go Client.
type TridentClientConfig struct {
	// BaseURL is the HTTP address of the Trident API (e.g., "http://localhost:3000")
	BaseURL string
	// APIKey is the API Key used for authentication (sent via X-API-Key header)
	APIKey string
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
