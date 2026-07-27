package trident

import (
	"encoding/json"
	"fmt"
)

// TridentApiError is returned on all non-2xx responses from the Trident API.
// It carries the HTTP status code, machine-readable error code, human-readable
// message, and an optional field pointer when the error stems from a specific
// request field (issue #278).
type TridentApiError struct {
	Status  int
	Code    string
	Message string
	Field   string // empty when absent
}

func (e *TridentApiError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("trident API error %d (%s): %s (field: %s)", e.Status, e.Code, e.Message, e.Field)
	}
	return fmt.Sprintf("trident API error %d (%s): %s", e.Status, e.Code, e.Message)
}

type apiErrorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Field   string `json:"field,omitempty"`
	} `json:"error"`
}

// parseApiError parses a non-2xx response body into a TridentApiError.
// Reads the canonical {"error":{"code","message","field"}} envelope; falls back
// to Code="INTERNAL" and the raw body when the body is not a valid envelope.
func parseApiError(status int, body string) *TridentApiError {
	var env apiErrorEnvelope
	if err := json.Unmarshal([]byte(body), &env); err == nil && env.Error.Code != "" {
		return &TridentApiError{
			Status:  status,
			Code:    env.Error.Code,
			Message: env.Error.Message,
			Field:   env.Error.Field,
		}
	}
	msg := body
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d", status)
	}
	return &TridentApiError{Status: status, Code: "INTERNAL", Message: msg}
}
