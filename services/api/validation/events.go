// Package validation provides request parameter validation for the Trident
// REST API before parameters are forwarded to the gRPC backend.
package validation

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Validation limits for GET /v1/events.
const (
	LimitMin     = 1
	LimitMax     = 200
	LimitDefault = 50
)

// validEventTypes holds the accepted values for the ?event_type filter.
var validEventTypes = map[string]bool{
	"contract":   true,
	"system":     true,
	"diagnostic": true,
}

// stellarContractRE matches a Stellar contract strkey: C followed by 55
// uppercase base32 characters (total 56 chars).
var stellarContractRE = regexp.MustCompile(`^C[A-Z2-7]{55}$`)

// uuidV4RE matches a UUID v4 in canonical lowercase form.
var uuidV4RE = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
)

// ValidationError carries a structured error to be returned as 400 Bad Request.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation error on %q: %s", e.Field, e.Message)
}

// QueryEventsParams holds validated parameters for GET /v1/events.
type QueryEventsParams struct {
	Limit      int
	LedgerFrom *int64
	LedgerTo   *int64
	ContractID string
	Cursor     string
	EventType  string // empty = no filter; otherwise "contract", "system", or "diagnostic"
}

// ValidateQueryEvents parses and validates query-string values for GET /v1/events.
// It returns populated QueryEventsParams on success, or a *ValidationError on the
// first validation failure.
//
// Validation rules:
//   - limit:      integer in [1, 200]; defaults to 50 if absent
//   - ledgerFrom: non-negative integer if present
//   - ledgerTo:   non-negative integer if present; must be >= ledgerFrom when both present
//   - contractId: valid Stellar contract strkey (C…, 56 chars) if present
//   - cursor:     non-empty string if present (opaque; no further validation)
//   - eventType:  one of "contract", "system", "diagnostic" (case-insensitive) if present
func ValidateQueryEvents(
	limitStr, ledgerFromStr, ledgerToStr, contractID, cursor, eventTypeStr string,
) (*QueryEventsParams, *ValidationError) {
	p := &QueryEventsParams{
		Limit:      LimitDefault,
		ContractID: contractID,
		Cursor:     cursor,
	}

	// limit
	if limitStr != "" {
		n, err := strconv.Atoi(limitStr)
		if err != nil || n < LimitMin || n > LimitMax {
			return nil, &ValidationError{
				Field:   "limit",
				Message: fmt.Sprintf("must be an integer between %d and %d", LimitMin, LimitMax),
			}
		}
		p.Limit = n
	}

	// ledgerFrom
	if ledgerFromStr != "" {
		n, err := strconv.ParseInt(ledgerFromStr, 10, 64)
		if err != nil || n < 0 {
			return nil, &ValidationError{
				Field:   "ledgerFrom",
				Message: "must be a non-negative integer",
			}
		}
		p.LedgerFrom = &n
	}

	// ledgerTo
	if ledgerToStr != "" {
		n, err := strconv.ParseInt(ledgerToStr, 10, 64)
		if err != nil || n < 0 {
			return nil, &ValidationError{
				Field:   "ledgerTo",
				Message: "must be a non-negative integer",
			}
		}
		p.LedgerTo = &n
	}

	// ledgerFrom <= ledgerTo when both are present
	if p.LedgerFrom != nil && p.LedgerTo != nil && *p.LedgerTo < *p.LedgerFrom {
		return nil, &ValidationError{
			Field:   "ledgerTo",
			Message: fmt.Sprintf("must be >= ledgerFrom (%d)", *p.LedgerFrom),
		}
	}

	// contractId format
	if contractID != "" && !stellarContractRE.MatchString(contractID) {
		return nil, &ValidationError{
			Field:   "contractId",
			Message: "must be a valid Stellar contract address (C… strkey, 56 characters)",
		}
	}

	// cursor non-empty check (presence already checked by caller via non-empty string)
	// The query string "cursor=" with no value arrives here as "" and is simply ignored.

	// eventType
	if eventTypeStr != "" {
		lower := strings.ToLower(eventTypeStr)
		if !validEventTypes[lower] {
			return nil, &ValidationError{
				Field:   "event_type",
				Message: "must be one of: contract, system, diagnostic",
			}
		}
		p.EventType = lower
	}

	return p, nil
}

// ValidateEventID validates the :id path parameter for GET /v1/events/:id.
// Returns a *ValidationError if the value is not a valid UUID v4.
func ValidateEventID(id string) *ValidationError {
	if !uuidV4RE.MatchString(id) {
		return &ValidationError{
			Field:   "id",
			Message: "must be a valid UUID v4 (e.g. 550e8400-e29b-41d4-a716-446655440000)",
		}
	}
	return nil
}

// Validation limits for GET /v1/stats/contracts.
const (
	StatsLimitMin     = 1
	StatsLimitMax     = 100
	StatsLimitDefault = 50
)

// validNetworks holds the accepted values for the ?network filter.
var validNetworks = map[string]bool{
	"testnet":  true,
	"mainnet":  true,
}

// QueryStatsParams holds validated parameters for GET /v1/stats/contracts.
type QueryStatsParams struct {
	FromLedger    int64
	FromLedgerPtr *int64 // nil if not specified (for SQL NULL handling)
	ToLedger      int64
	ToLedgerPtr   *int64 // nil if not specified (for SQL NULL handling)
	Network       string
	Limit         int64
}

// ValidateQueryStats parses and validates query-string values for GET /v1/stats/contracts.
// It returns populated QueryStatsParams on success, or a *ValidationError on the
// first validation failure.
//
// Validation rules:
//   - from_ledger: non-negative integer if present; default 0 (all time)
//   - to_ledger:   non-negative integer if present; default latest indexed
//   - network:     one of "testnet", "mainnet"; default "testnet"
//   - limit:       integer in [1, 100]; default 50
func ValidateQueryStats(
	fromLedgerStr, toLedgerStr, networkStr, limitStr string,
) (*QueryStatsParams, *ValidationError) {
	p := &QueryStatsParams{
		Network: "testnet",
		Limit:   int64(StatsLimitDefault),
	}

	// from_ledger
	if fromLedgerStr != "" {
		n, err := strconv.ParseInt(fromLedgerStr, 10, 64)
		if err != nil || n < 0 {
			return nil, &ValidationError{
				Field:   "from_ledger",
				Message: "must be a non-negative integer",
			}
		}
		p.FromLedger = n
		p.FromLedgerPtr = &n
	}

	// to_ledger
	if toLedgerStr != "" {
		n, err := strconv.ParseInt(toLedgerStr, 10, 64)
		if err != nil || n < 0 {
			return nil, &ValidationError{
				Field:   "to_ledger",
				Message: "must be a non-negative integer",
			}
		}
		p.ToLedger = n
		p.ToLedgerPtr = &n
	}

	// from_ledger <= to_ledger when both are present
	if p.FromLedgerPtr != nil && p.ToLedgerPtr != nil && *p.ToLedgerPtr < *p.FromLedgerPtr {
		return nil, &ValidationError{
			Field:   "to_ledger",
			Message: "must be >= from_ledger",
		}
	}

	// network
	if networkStr != "" {
		lower := strings.ToLower(networkStr)
		if !validNetworks[lower] {
			return nil, &ValidationError{
				Field:   "network",
				Message: "must be one of: testnet, mainnet",
			}
		}
		p.Network = lower
	}

	// limit
	if limitStr != "" {
		n, err := strconv.ParseInt(limitStr, 10, 64)
		if err != nil || n < int64(StatsLimitMin) || n > int64(StatsLimitMax) {
			return nil, &ValidationError{
				Field:   "limit",
				Message: fmt.Sprintf("must be an integer between %d and %d", StatsLimitMin, StatsLimitMax),
			}
		}
		p.Limit = n
	}

	return p, nil
}
