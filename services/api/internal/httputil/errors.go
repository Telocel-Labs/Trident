package httputil

import (
	"encoding/json"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ErrorCode represents the machine-readable error type
type ErrorCode string

const (
	NOT_FOUND        ErrorCode = "NOT_FOUND"
	UNAUTHORIZED     ErrorCode = "UNAUTHORIZED"
	RATE_LIMITED     ErrorCode = "RATE_LIMITED"
	INVALID_ARGUMENT ErrorCode = "INVALID_ARGUMENT"
	UNAVAILABLE      ErrorCode = "UNAVAILABLE"
	INTERNAL         ErrorCode = "INTERNAL"
)

// ErrorDetail is the nested error object required by the OpenAPI ErrorResponse
// schema and parsed by the client SDKs.
type ErrorDetail struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// ErrorResponse is the standardized JSON error body: {"error":{"code","message"}}.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// WriteError writes a standardized JSON error response matching the OpenAPI
// ErrorResponse schema ({"error":{"code","message"}}).
func WriteError(w http.ResponseWriter, statusCode int, code ErrorCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(ErrorResponse{
		Error: ErrorDetail{Code: code, Message: message},
	})
}

// GRPCToHTTP maps a gRPC error to HTTP status code and error code
func GRPCToHTTP(err error) (int, ErrorCode) {
	if err == nil {
		return http.StatusOK, ""
	}

	st, ok := status.FromError(err)
	if !ok {
		return http.StatusInternalServerError, INTERNAL
	}

	switch st.Code() {
	case codes.NotFound:
		return http.StatusNotFound, NOT_FOUND
	case codes.InvalidArgument:
		return http.StatusBadRequest, INVALID_ARGUMENT
	case codes.Unauthenticated:
		return http.StatusUnauthorized, UNAUTHORIZED
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests, RATE_LIMITED
	default:
		return http.StatusInternalServerError, INTERNAL
	}
}
