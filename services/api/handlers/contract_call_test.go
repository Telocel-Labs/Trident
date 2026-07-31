package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Depo-dev/trident/services/api/handlers"
	"github.com/stellar/go/xdr"
)

// validCallContractID is a real, checksum-valid strkey contract address
// (the all-zero contract id). Unlike the package's shared testContractID
// (which only needs to satisfy the lightweight regex in
// validation_envelope_test.go), buildSimulateEnvelope decodes this one via
// strkey.Decode, which additionally verifies the base32 checksum.
const validCallContractID = "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4"

// fakeSorobanRPC lets tests control the simulateTransaction response without
// a live RPC endpoint, and records the method/params it was called with so
// tests can assert the handler never calls anything but simulateTransaction.
type fakeSorobanRPC struct {
	calledMethod string
	respBody     string // raw JSON to unmarshal into result
	err          error
}

func (f *fakeSorobanRPC) Call(_ context.Context, method string, _ any, result any) error {
	f.calledMethod = method
	if f.err != nil {
		return f.err
	}
	return json.Unmarshal([]byte(f.respBody), result)
}

func newCallRequest(t *testing.T, contractID string, body any) *http.Request {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/contracts/"+contractID+"/call", bytes.NewReader(raw))
	req.SetPathValue("id", contractID)
	return req
}

func scValU64B64(t *testing.T, n uint64) string {
	t.Helper()
	v := xdr.Uint64(n)
	scv := xdr.ScVal{Type: xdr.ScValTypeScvU64, U64: &v}
	b64, err := xdr.MarshalBase64(scv)
	if err != nil {
		t.Fatalf("marshal ScVal: %v", err)
	}
	return b64
}

func TestCallContract_Success_DecodesU64Balance(t *testing.T) {
	resultXDR := scValU64B64(t, 42)
	rpc := &fakeSorobanRPC{
		respBody: `{"results":[{"xdr":"` + resultXDR + `"}]}`,
	}

	req := newCallRequest(t, validCallContractID, map[string]any{
		"function": "balance",
		"args":     []string{},
	})
	rec := httptest.NewRecorder()

	handlers.CallContract(rpc)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	if rpc.calledMethod != "simulateTransaction" {
		t.Fatalf("rpc method: got %q, want simulateTransaction", rpc.calledMethod)
	}

	var resp handlers.ContractCallResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Success {
		t.Fatalf("success: got false, error=%q", resp.Error)
	}
	if resp.RawXDR != resultXDR {
		t.Fatalf("raw_xdr: got %q, want %q", resp.RawXDR, resultXDR)
	}
	// JSON round-trips numbers as float64.
	got, ok := resp.Result.(float64)
	if !ok || got != 42 {
		t.Fatalf("result: got %#v, want 42", resp.Result)
	}
}

func TestCallContract_MissingFunction_Returns400(t *testing.T) {
	rpc := &fakeSorobanRPC{}
	req := newCallRequest(t, testContractID, map[string]any{"function": "", "args": []string{}})
	rec := httptest.NewRecorder()

	handlers.CallContract(rpc)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
	if rpc.calledMethod != "" {
		t.Fatalf("rpc must not be called when validation fails, got method %q", rpc.calledMethod)
	}
}

func TestCallContract_MalformedContractID_Returns400(t *testing.T) {
	rpc := &fakeSorobanRPC{}
	req := newCallRequest(t, "not-a-contract-id", map[string]any{"function": "balance", "args": []string{}})
	rec := httptest.NewRecorder()

	handlers.CallContract(rpc)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
	if rpc.calledMethod != "" {
		t.Fatalf("rpc must not be called when validation fails, got method %q", rpc.calledMethod)
	}
}

func TestCallContract_MalformedArgXDR_Returns400(t *testing.T) {
	rpc := &fakeSorobanRPC{}
	req := newCallRequest(t, testContractID, map[string]any{
		"function": "transfer",
		"args":     []string{"not-valid-base64-xdr!!!"},
	})
	rec := httptest.NewRecorder()

	handlers.CallContract(rpc)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
	if rpc.calledMethod != "" {
		t.Fatalf("rpc must not be called when arg decoding fails, got method %q", rpc.calledMethod)
	}
}

func TestCallContract_TooManyArgs_Returns400(t *testing.T) {
	rpc := &fakeSorobanRPC{}
	args := make([]string, 33) // over contractCallMaxArgs (32)
	for i := range args {
		args[i] = scValU64B64(t, uint64(i))
	}
	req := newCallRequest(t, testContractID, map[string]any{"function": "f", "args": args})
	rec := httptest.NewRecorder()

	handlers.CallContract(rpc)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
	if rpc.calledMethod != "" {
		t.Fatalf("rpc must not be called when arg count is rejected, got method %q", rpc.calledMethod)
	}
}

func TestCallContract_RPCTransportError_Returns502(t *testing.T) {
	rpc := &fakeSorobanRPC{err: context.DeadlineExceeded}
	req := newCallRequest(t, validCallContractID, map[string]any{"function": "balance", "args": []string{}})
	rec := httptest.NewRecorder()

	handlers.CallContract(rpc)(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status: got %d, want 502, body=%s", rec.Code, rec.Body.String())
	}
}

func TestCallContract_SimulationError_ReturnsRejectionNotTransportError(t *testing.T) {
	rpc := &fakeSorobanRPC{respBody: `{"error":"contract trap: some failure"}`}
	req := newCallRequest(t, validCallContractID, map[string]any{"function": "balance", "args": []string{}})
	rec := httptest.NewRecorder()

	handlers.CallContract(rpc)(rec, req)

	// A simulation-level rejection (contract trap) is reported inside a 200
	// envelope with success=false, not as an HTTP error — the RPC call itself
	// succeeded, only the simulated invocation did not.
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var resp handlers.ContractCallResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Success {
		t.Fatal("success: got true, want false for a simulation error")
	}
	if resp.Error == "" {
		t.Fatal("expected a non-empty error message")
	}
}

func TestCallContract_NilRPC_Returns503(t *testing.T) {
	req := newCallRequest(t, testContractID, map[string]any{"function": "balance", "args": []string{}})
	rec := httptest.NewRecorder()

	handlers.CallContract(nil)(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want 503, body=%s", rec.Code, rec.Body.String())
	}
}

func TestCallContract_MalformedRequestBody_Returns400(t *testing.T) {
	rpc := &fakeSorobanRPC{}
	req := httptest.NewRequest(http.MethodPost, "/v1/contracts/"+testContractID+"/call", bytes.NewReader([]byte("not json")))
	req.SetPathValue("id", testContractID)
	rec := httptest.NewRecorder()

	handlers.CallContract(rpc)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
}
