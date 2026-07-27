package trident

import (
	"testing"
)

func TestParseApiError_ValidEnvelope(t *testing.T) {
	body := `{"error":{"code":"NOT_FOUND","message":"event not found"}}`
	err := parseApiError(404, body)
	if err.Status != 404 {
		t.Errorf("expected Status=404, got %d", err.Status)
	}
	if err.Code != "NOT_FOUND" {
		t.Errorf("expected Code=NOT_FOUND, got %s", err.Code)
	}
	if err.Message != "event not found" {
		t.Errorf("expected Message='event not found', got %s", err.Message)
	}
	if err.Field != "" {
		t.Errorf("expected empty Field, got %s", err.Field)
	}
}

func TestParseApiError_EnvelopeWithField(t *testing.T) {
	body := `{"error":{"code":"INVALID_ARGUMENT","message":"must be positive","field":"limit"}}`
	err := parseApiError(400, body)
	if err.Code != "INVALID_ARGUMENT" {
		t.Errorf("expected Code=INVALID_ARGUMENT, got %s", err.Code)
	}
	if err.Field != "limit" {
		t.Errorf("expected Field=limit, got %s", err.Field)
	}
}

func TestParseApiError_FlatBodyFallsBackToInternal(t *testing.T) {
	err := parseApiError(500, "internal server error")
	if err.Status != 500 {
		t.Errorf("expected Status=500, got %d", err.Status)
	}
	if err.Code != "INTERNAL" {
		t.Errorf("expected Code=INTERNAL, got %s", err.Code)
	}
	if err.Message != "internal server error" {
		t.Errorf("expected raw body as message, got %s", err.Message)
	}
}

func TestParseApiError_EmptyBodyFallsBackToHTTPStatus(t *testing.T) {
	err := parseApiError(503, "")
	if err.Code != "INTERNAL" {
		t.Errorf("expected Code=INTERNAL, got %s", err.Code)
	}
	if err.Message != "HTTP 503" {
		t.Errorf("expected Message='HTTP 503', got %s", err.Message)
	}
}

func TestParseApiError_MalformedJSONFallsBackToInternal(t *testing.T) {
	err := parseApiError(400, "{not valid json")
	if err.Code != "INTERNAL" {
		t.Errorf("expected Code=INTERNAL, got %s", err.Code)
	}
}

func TestTridentApiError_ErrorMessage(t *testing.T) {
	err := &TridentApiError{Status: 429, Code: "RATE_LIMITED", Message: "slow down"}
	want := "trident API error 429 (RATE_LIMITED): slow down"
	if err.Error() != want {
		t.Errorf("expected %q, got %q", want, err.Error())
	}
}

func TestTridentApiError_ErrorMessageWithField(t *testing.T) {
	err := &TridentApiError{Status: 400, Code: "INVALID_ARGUMENT", Message: "must be positive", Field: "limit"}
	want := "trident API error 400 (INVALID_ARGUMENT): must be positive (field: limit)"
	if err.Error() != want {
		t.Errorf("expected %q, got %q", want, err.Error())
	}
}

// Golden payload shared across SDKs (issue #278).
func TestParseApiError_CrossSdkGoldenPayload(t *testing.T) {
	// This JSON must decode identically in all SDK language implementations.
	golden := `{"error":{"code":"UNAUTHORIZED","message":"invalid or missing API key"}}`
	err := parseApiError(401, golden)
	if err.Status != 401 || err.Code != "UNAUTHORIZED" || err.Field != "" {
		t.Errorf("golden payload mismatch: %+v", err)
	}
}
