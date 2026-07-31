// Package sorobanrpc provides a minimal JSON-RPC 2.0 client for the Soroban
// RPC endpoint (STELLAR_RPC_URL). It is transport-only: callers supply the
// method name, params, and a destination for the decoded result.
package sorobanrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// DefaultTimeout bounds a single RPC call. simulateTransaction runs the
// contract in a sandboxed host and can take noticeably longer than a simple
// query like getLatestLedger (stats.go uses 2s for that), so this client
// defaults to a more generous budget. Callers apply it via
// context.WithTimeout before calling Call.
const DefaultTimeout = 10 * time.Second

// Client calls a single Soroban RPC endpoint.
type Client struct {
	URL        string
	HTTPClient *http.Client
}

// NewClient builds a Client targeting url using http.DefaultClient.
func NewClient(url string) *Client {
	return &Client{URL: url, HTTPClient: http.DefaultClient}
}

type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *jsonRPCError) Error() string {
	return fmt.Sprintf("soroban rpc error %d: %s", e.Code, e.Message)
}

type jsonRPCResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *jsonRPCError   `json:"error"`
}

// Call issues a JSON-RPC 2.0 POST for method with params, decoding the
// "result" field into result. A JSON-RPC error envelope is surfaced as a Go
// error (of concrete type *jsonRPCError, unwrap with errors.As if the
// code/message need to be inspected).
func (c *Client) Call(ctx context.Context, method string, params any, result any) error {
	if c.URL == "" {
		return fmt.Errorf("soroban rpc: no endpoint configured")
	}

	reqBody, err := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return fmt.Errorf("soroban rpc: encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("soroban rpc: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("soroban rpc: call failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var rpcResp jsonRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return fmt.Errorf("soroban rpc: decode response: %w", err)
	}
	if rpcResp.Error != nil {
		return rpcResp.Error
	}
	if result != nil && len(rpcResp.Result) > 0 {
		if err := json.Unmarshal(rpcResp.Result, result); err != nil {
			return fmt.Errorf("soroban rpc: decode result: %w", err)
		}
	}
	return nil
}
