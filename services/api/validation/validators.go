package validation

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Depo-dev/trident/services/api/cursor"
)

// This file holds the reusable, per-parameter validators shared by every
// handler (issue #222). Handlers must not re-implement parameter parsing: they
// call these helpers and render the returned *ValidationError through the
// canonical INVALID_ARGUMENT envelope, so a bad `limit` on /v1/events and a bad
// `limit` on /v1/stats/contracts produce byte-identical error shapes.
//
// Every message names the offending field so an SDK can surface it directly.

// Errorf builds a *ValidationError whose message is prefixed with the field
// name, e.g. `limit must be an integer between 1 and 200`.
func Errorf(field, format string, args ...any) *ValidationError {
	return &ValidationError{
		Field:   field,
		Message: field + " " + fmt.Sprintf(format, args...),
	}
}

// ValidateContractID checks a Stellar contract strkey (C… 56 chars).
// An empty value is treated as "not supplied" and accepted; use
// ValidateRequiredContractID when the parameter is mandatory.
func ValidateContractID(field, value string) *ValidationError {
	if value == "" {
		return nil
	}
	if !stellarContractRE.MatchString(value) {
		return Errorf(field, "must be a valid Stellar contract address (C… strkey, 56 characters)")
	}
	return nil
}

// ValidateRequiredContractID rejects an absent contract id as well as a
// malformed one.
func ValidateRequiredContractID(field, value string) *ValidationError {
	if value == "" {
		return Errorf(field, "is required")
	}
	return ValidateContractID(field, value)
}

// ValidateUUID checks a canonical UUID v4 value (path or query parameter).
func ValidateUUID(field, value string) *ValidationError {
	if value == "" {
		return Errorf(field, "is required")
	}
	if !uuidV4RE.MatchString(strings.ToLower(value)) {
		return Errorf(field, "must be a valid UUID v4 (e.g. 550e8400-e29b-41d4-a716-446655440000)")
	}
	return nil
}

// ValidateLedger parses a single ledger sequence bound. A blank value returns
// (nil, nil) — the bound is simply absent.
func ValidateLedger(field, value string) (*int64, *ValidationError) {
	if value == "" {
		return nil, nil
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 {
		return nil, Errorf(field, "must be a non-negative integer")
	}
	return &n, nil
}

// ValidateLedgerRange parses both bounds of a ledger range and enforces
// from <= to when both are present.
func ValidateLedgerRange(fromField, toField, fromValue, toValue string) (*int64, *int64, *ValidationError) {
	from, verr := ValidateLedger(fromField, fromValue)
	if verr != nil {
		return nil, nil, verr
	}
	to, verr := ValidateLedger(toField, toValue)
	if verr != nil {
		return nil, nil, verr
	}
	if from != nil && to != nil && *to < *from {
		return nil, nil, Errorf(toField, "must be >= %s (%d)", fromField, *from)
	}
	return from, to, nil
}

// ValidateLimit parses a page-size parameter and bounds it to [min, max],
// falling back to def when the value is absent.
func ValidateLimit(field, value string, min, max, def int64) (int64, *ValidationError) {
	if value == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < min || n > max {
		return 0, Errorf(field, "must be an integer between %d and %d", min, max)
	}
	return n, nil
}

// ValidateCursor decodes an opaque pagination cursor and returns the underlying
// paging token. A blank cursor is accepted and yields an empty token.
func ValidateCursor(field, value string) (string, *ValidationError) {
	if value == "" {
		return "", nil
	}
	token, err := cursor.Decode(value)
	if err != nil {
		return "", Errorf(field, "is not a valid pagination cursor")
	}
	return token, nil
}

// ValidateNetwork checks the network enum, returning def when absent.
func ValidateNetwork(field, value, def string) (string, *ValidationError) {
	if value == "" {
		return def, nil
	}
	normalized := NormalizeNetwork(value)
	if !AllowedNetworks[normalized] {
		return "", Errorf(field, "must be one of: %s", allowedValues(AllowedNetworks))
	}
	return normalized, nil
}

// ValidateEventType checks the event-type enum. An empty value means "no
// filter" and is accepted.
func ValidateEventType(field, value string) (string, *ValidationError) {
	if value == "" {
		return "", nil
	}
	lower := strings.ToLower(value)
	if !validEventTypes[lower] {
		return "", Errorf(field, "must be one of: %s", allowedValues(validEventTypes))
	}
	return lower, nil
}

// ValidateRFC3339 parses a required RFC3339 timestamp parameter.
func ValidateRFC3339(field, value string) (time.Time, *ValidationError) {
	if value == "" {
		return time.Time{}, Errorf(field, "is required")
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, Errorf(field, "must be an RFC 3339 timestamp (e.g. 2024-01-15T12:00:00Z)")
	}
	return t, nil
}

// ValidateTimeRange parses both ends of a time window. It enforces:
//   - both from and to are present and valid RFC3339 timestamps
//   - from <= to
//   - (to - from) <= maxDuration (when maxDuration > 0)
func ValidateTimeRange(fromField, toField, fromValue, toValue string, maxDuration time.Duration) (time.Time, time.Time, *ValidationError) {
	from, verr := ValidateRFC3339(fromField, fromValue)
	if verr != nil {
		return time.Time{}, time.Time{}, verr
	}
	to, verr := ValidateRFC3339(toField, toValue)
	if verr != nil {
		return time.Time{}, time.Time{}, verr
	}
	if to.Before(from) {
		return time.Time{}, time.Time{}, Errorf(toField, "must be >= %s", fromField)
	}
	if maxDuration > 0 && to.Sub(from) > maxDuration {
		return time.Time{}, time.Time{}, Errorf(
			toField,
			"range (%s to %s) exceeds the maximum allowed window of %s",
			fromField,
			toField,
			formatDuration(maxDuration),
		)
	}
	return from, to, nil
}

func formatDuration(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		days := d / (24 * time.Hour)
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	}
	return d.String()
}

// RejectUnknownParams returns a ValidationError naming the first unrecognized
// query parameter key, or nil when all supplied keys are in the allowed set
// (issue #222). Keys are sorted so the error message is deterministic.
func RejectUnknownParams(q url.Values, allowed ...string) *ValidationError {
	known := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		known[a] = true
	}

	var unknown []string
	for name := range q {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)

	return &ValidationError{
		Field: unknown[0],
		Message: fmt.Sprintf(
			"unknown query parameter(s): %s; supported: %s",
			strings.Join(unknown, ", "),
			strings.Join(sortedCopy(allowed), ", "),
		),
	}
}

// allowedValues renders an enum set as a stable, comma-separated list.
func allowedValues(set map[string]bool) string {
	values := make([]string, 0, len(set))
	for v := range set {
		values = append(values, v)
	}
	sort.Strings(values)
	return strings.Join(values, ", ")
}

func sortedCopy(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Issue #252 — Network scoping and validation
// ---------------------------------------------------------------------------

// AllowedNetworks is the canonical set of supported Stellar network names.
var AllowedNetworks = map[string]bool{
	"pubnet":    true,
	"testnet":   true,
	"futurenet": true,
	"local":     true,
}

// NormalizeNetwork maps network aliases to their canonical enum representation.
// (e.g. mainnet -> pubnet, standalone -> local).
func NormalizeNetwork(val string) string {
	normalized := strings.TrimSpace(strings.ToLower(val))
	switch normalized {
	case "mainnet":
		return "pubnet"
	case "standalone":
		return "local"
	default:
		return normalized
	}
}

// ValidateRequiredNetwork rejects empty or invalid network names.
func ValidateRequiredNetwork(field, value string) (string, *ValidationError) {
	if strings.TrimSpace(value) == "" {
		return "", Errorf(field, "is required")
	}
	return ValidateNetwork(field, value, "")
}
