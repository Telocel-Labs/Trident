// Code generated from JSON Schema using quicktype. DO NOT EDIT.
// To parse and unparse this JSON data, add this code to your project and do:
//
//    openAPIModels, err := UnmarshalOpenAPIModels(bytes)
//    bytes, err = openAPIModels.Marshal()

package openapi

import "time"

import "encoding/json"

func UnmarshalOpenAPIModels(data []byte) (OpenAPIModels, error) {
	var r OpenAPIModels
	err := json.Unmarshal(data, &r)
	return r, err
}

func (r *OpenAPIModels) Marshal() ([]byte, error) {
	return json.Marshal(r)
}

type OpenAPIModels struct {
	ContractStats         *ContractStats         `json:"ContractStats,omitempty"`
	ContractStatsResponse *ContractStatsResponse `json:"ContractStatsResponse,omitempty"`
	ErrorResponse         *ErrorResponse         `json:"ErrorResponse,omitempty"`
	EventListResponse     *EventListResponse     `json:"EventListResponse,omitempty"`
	HealthResponse        *HealthResponse        `json:"HealthResponse,omitempty"`
	IndexerStatsResponse  *IndexerStatsResponse  `json:"IndexerStatsResponse,omitempty"`
	SorobanEvent          *SorobanEvent          `json:"SorobanEvent,omitempty"`
}

type ContractStats struct {
	// Soroban contract address                           
	ContractID                                  string    `json:"contract_id"`
	// Total events for this contract in range            
	EventCount                                  int64     `json:"event_count"`
	// Timestamp of last event for this contract          
	LastSeenAt                                  time.Time `json:"last_seen_at"`
	// Latest ledger sequence for this contract           
	LastSeenLedger                              int64     `json:"last_seen_ledger"`
}

type ContractStatsResponse struct {
	// Contracts sorted by event count (descending)                
	Contracts                                      []ContractStats `json:"contracts"`
	// Lower bound of queried ledger range                         
	FromLedger                                     int64           `json:"from_ledger"`
	// Timestamp when response was generated                       
	GeneratedAt                                    time.Time       `json:"generated_at"`
	// Network queried                                             
	Network                                        Network         `json:"network"`
	// Upper bound of queried ledger range                         
	ToLedger                                       int64           `json:"to_ledger"`
}

type ErrorResponse struct {
	Error Error `json:"error"`
}

type Error struct {
	// Error code (e.g., INVALID_ARGUMENT, INTERNAL, UNAVAILABLE)        
	Code                                                         string  `json:"code"`
	// Human-readable error message                                      
	Message                                                      string  `json:"message"`
	// Request ID for debugging                                          
	RequestID                                                    *string `json:"request_id,omitempty"`
}

type EventListResponse struct {
	// List of events                                                        
	Events                                                    []SorobanEvent `json:"events"`
	// Whether more results are available                                    
	HasMore                                                   bool           `json:"has_more"`
	// Opaque cursor for next page (null if has_more is false)               
	NextCursor                                                *string        `json:"next_cursor,omitempty"`
}

type SorobanEvent struct {
	// Soroban contract address                     
	ContractID                            string    `json:"contract_id"`
	// Timestamp when event was indexed             
	CreatedAt                             time.Time `json:"created_at"`
	// Event data (XDR-encoded)                     
	Data                                  string    `json:"data"`
	// Event index within transaction               
	EventIndex                            int64     `json:"event_index"`
	// Type of event                                
	EventType                             EventType `json:"event_type"`
	// Unique event identifier                      
	ID                                    string    `json:"id"`
	// Ledger sequence number                       
	LedgerSequence                        int64     `json:"ledger_sequence"`
	// Ledger timestamp in ISO 8601 format          
	LedgerTimestamp                       time.Time `json:"ledger_timestamp"`
	// Event topics (XDR-encoded)                   
	Topics                                []string  `json:"topics"`
	// Transaction hash (XDR-encoded)               
	TransactionHash                       string    `json:"transaction_hash"`
}

type HealthResponse struct {
	Indexer                 Indexer              `json:"indexer"`
	// Overall system status                     
	Status                  HealthResponseStatus `json:"status"`
}

type Indexer struct {
	// Latest indexed ledger sequence                      
	LastLedgerIndexed                           int64      `json:"last_ledger_indexed"`
	// Timestamp of last successful indexer poll           
	LastPollAt                                  *time.Time `json:"last_poll_at,omitempty"`
}

type IndexerStatsResponse struct {
	// Average poll duration in milliseconds                                    
	AvgPollDurationMS                                *int64                     `json:"avg_poll_duration_ms,omitempty"`
	// Current chain tip ledger (from RPC)                                      
	ChainTipLedger                                   *int64                     `json:"chain_tip_ledger,omitempty"`
	// Cumulative events indexed                                                
	EventsIndexedTotal                               *int64                     `json:"events_indexed_total,omitempty"`
	// Events processed in last poll                                            
	EventsLastPoll                                   *int64                     `json:"events_last_poll,omitempty"`
	// Number of ledgers behind chain tip                                       
	LagLedgers                                       *int64                     `json:"lag_ledgers,omitempty"`
	// Latest indexed ledger sequence                                           
	LastLedgerIndexed                                *int64                     `json:"last_ledger_indexed,omitempty"`
	// Timestamp of last successful poll                                        
	LastPollAt                                       *time.Time                 `json:"last_poll_at,omitempty"`
	// Network name from NETWORK environment variable                           
	Network                                          string                     `json:"network"`
	// Indexer health status                                                    
	Status                                           IndexerStatsResponseStatus `json:"status"`
}

// Network queried
type Network string

const (
	Mainnet Network = "mainnet"
	Testnet Network = "testnet"
)

// Type of event
type EventType string

const (
	Contract   EventType = "contract"
	Diagnostic EventType = "diagnostic"
	System     EventType = "system"
)

// Overall system status
type HealthResponseStatus string

const (
	Degraded HealthResponseStatus = "degraded"
	Ok       HealthResponseStatus = "ok"
)

// Indexer health status
type IndexerStatsResponseStatus string

const (
	Healthy IndexerStatsResponseStatus = "healthy"
	Lagging IndexerStatsResponseStatus = "lagging"
	Stalled IndexerStatsResponseStatus = "stalled"
)
