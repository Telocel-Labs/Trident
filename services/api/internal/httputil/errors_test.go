package httputil_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Depo-dev/trident/services/api/internal/httputil"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestWriteError(t *testing.T) {
	rr := httptest.NewRecorder()
	httputil.WriteError(rr, http.StatusBadRequest, httputil.INVALID_ARGUMENT, "test error")

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}

	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected application/json, got %s", ct)
	}

	var resp httputil.ErrorResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if resp.Error.Code != httputil.INVALID_ARGUMENT {
		t.Errorf("expected INVALID_ARGUMENT, got %s", resp.Error.Code)
	}

	if resp.Error.Message != "test error" {
		t.Errorf("expected 'test error', got %s", resp.Error.Message)
	}
}

func TestGRPCToHTTP_NotFound(t *testing.T) {
	err := status.Error(codes.NotFound, "not found")
	statusCode, code := httputil.GRPCToHTTP(err)

	if statusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", statusCode)
	}
	if code != httputil.NOT_FOUND {
		t.Errorf("expected NOT_FOUND, got %s", code)
	}
}

func TestGRPCToHTTP_InvalidArgument(t *testing.T) {
	err := status.Error(codes.InvalidArgument, "bad request")
	statusCode, code := httputil.GRPCToHTTP(err)

	if statusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", statusCode)
	}
	if code != httputil.INVALID_ARGUMENT {
		t.Errorf("expected INVALID_ARGUMENT, got %s", code)
	}
}

func TestGRPCToHTTP_Unauthenticated(t *testing.T) {
	err := status.Error(codes.Unauthenticated, "unauthorized")
	statusCode, code := httputil.GRPCToHTTP(err)

	if statusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", statusCode)
	}
	if code != httputil.UNAUTHORIZED {
		t.Errorf("expected UNAUTHORIZED, got %s", code)
	}
}

func TestGRPCToHTTP_ResourceExhausted(t *testing.T) {
	err := status.Error(codes.ResourceExhausted, "rate limited")
	statusCode, code := httputil.GRPCToHTTP(err)

	if statusCode != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", statusCode)
	}
	if code != httputil.RATE_LIMITED {
		t.Errorf("expected RATE_LIMITED, got %s", code)
	}
}

func TestGRPCToHTTP_Internal(t *testing.T) {
	err := status.Error(codes.Internal, "internal error")
	statusCode, code := httputil.GRPCToHTTP(err)

	if statusCode != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", statusCode)
	}
	if code != httputil.INTERNAL {
		t.Errorf("expected INTERNAL, got %s", code)
	}
}

func TestGRPCToHTTP_NilError(t *testing.T) {
	statusCode, code := httputil.GRPCToHTTP(nil)

	if statusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", statusCode)
	}
	if code != "" {
		t.Errorf("expected empty code, got %s", code)
	}
}

func TestGRPCToHTTP_DeadlineExceeded(t *testing.T) {
	err := status.Error(codes.DeadlineExceeded, "deadline exceeded")
	statusCode, code := httputil.GRPCToHTTP(err)

	if statusCode != http.StatusGatewayTimeout {
		t.Errorf("expected 504, got %d", statusCode)
	}
	if code != httputil.UNAVAILABLE {
		t.Errorf("expected UNAVAILABLE, got %s", code)
	}
}

func TestGRPCToHTTP_Unavailable(t *testing.T) {
	err := status.Error(codes.Unavailable, "connection refused")
	statusCode, code := httputil.GRPCToHTTP(err)

	if statusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", statusCode)
	}
	if code != httputil.UNAVAILABLE {
		t.Errorf("expected UNAVAILABLE, got %s", code)
	}
}
