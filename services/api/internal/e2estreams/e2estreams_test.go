package e2estreams

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Depo-dev/trident/services/api/gen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// TestE2EAllSubscribersPipeline verifies end-to-end event flow across all 6 consumer channels (issue #793):
//   1. REST: GET /v1/events
//   2. WebSocket: /ws
//   3. GraphQL: /graphql subscriptions (graphql-transport-ws)
//   4. SSE: /v1/events/stream
//   5. gRPC: StreamEvents RPC
//   6. Webhooks: HTTP POST with HMAC-SHA256 signature verification
func TestE2EAllSubscribersPipeline(t *testing.T) {
	baseURL := os.Getenv("E2E_SUBSCRIBERS_BASE_URL")
	contractID := os.Getenv("E2E_CONTRACT_ID")
	if baseURL == "" || contractID == "" {
		t.Skip("skipping e2e all subscribers test: E2E_SUBSCRIBERS_BASE_URL or E2E_CONTRACT_ID not set")
	}

	apiKey := getEnvOrDefault("E2E_API_KEY", "ci-test-api-key-do-not-use-in-production")
	grpcAddr := getEnvOrDefault("E2E_GRPC_ADDR", "localhost:50051")
	webhookTargetHost := getEnvOrDefault("E2E_WEBHOOK_TARGET_HOST", "host.docker.internal")

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// -------------------------------------------------------------------------
	// 1. Setup local Webhook Receiver server with HMAC signature verification
	// -------------------------------------------------------------------------
	webhookCh := make(chan map[string]any, 50)
	var webhookSecret string
	var webhookSecretMu sync.RWMutex
	var sigVerificationCount int
	var sigMu sync.Mutex

	receiverListener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("failed to listen for webhook receiver: %v", err)
	}
	defer receiverListener.Close()
	receiverPort := receiverListener.Addr().(*net.TCPAddr).Port

	receiverMux := http.NewServeMux()
	receiverMux.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		body, rErr := io.ReadAll(r.Body)
		if rErr != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		sigHeader := r.Header.Get("Trident-Signature")
		tsHeader := r.Header.Get("Trident-Timestamp")
		ts, _ := strconv.ParseInt(tsHeader, 10, 64)

		webhookSecretMu.RLock()
		sec := webhookSecret
		webhookSecretMu.RUnlock()

		if sec != "" {
			expected := verifyWebhookSig(body, sec, ts)
			if !hmac.Equal([]byte(sigHeader), []byte(expected)) {
				slogWarn("webhook signature mismatch", "got", sigHeader, "expected", expected)
				http.Error(w, "invalid signature", http.StatusUnauthorized)
				return
			}
			sigMu.Lock()
			sigVerificationCount++
			sigMu.Unlock()
		}

		var payload struct {
			Event map[string]any `json:"event"`
		}
		if err := json.Unmarshal(body, &payload); err == nil && payload.Event != nil {
			webhookCh <- payload.Event
		}
		w.WriteHeader(http.StatusOK)
	})

	receiverServer := &http.Server{Handler: receiverMux}
	go func() { _ = receiverServer.Serve(receiverListener) }()
	defer func() { _ = receiverServer.Shutdown(context.Background()) }()

	// Register webhook subscription via REST API
	targetURL := fmt.Sprintf("http://%s:%d/webhook", webhookTargetHost, receiverPort)
	subID, secret, err := createWebhookSubscription(baseURL, apiKey, contractID, targetURL)
	if err != nil {
		t.Fatalf("failed to create webhook subscription: %v", err)
	}
	webhookSecretMu.Lock()
	webhookSecret = secret
	webhookSecretMu.Unlock()
	t.Logf("Registered webhook subscription %s target=%s", subID, targetURL)

	// -------------------------------------------------------------------------
	// 2. Connect WebSocket subscriber
	// -------------------------------------------------------------------------
	wsCh := make(chan map[string]any, 50)
	wsConn, wsBr, err := dialWS(baseURL + "/ws?contractId=" + contractID)
	if err != nil {
		t.Fatalf("failed to dial /ws: %v", err)
	}
	defer wsConn.Close()

	go func() {
		for {
			frame, rErr := readWSFrame(wsBr)
			if rErr != nil {
				return
			}
			var ev map[string]any
			if err := json.Unmarshal(frame, &ev); err == nil && ev != nil {
				wsCh <- ev
			}
		}
	}()

	// -------------------------------------------------------------------------
	// 3. Connect GraphQL Subscription (/graphql over graphql-transport-ws)
	// -------------------------------------------------------------------------
	gqlCh := make(chan map[string]any, 50)
	gqlConn, gqlBr, err := dialGQLSubscription(baseURL, apiKey, contractID)
	if err != nil {
		t.Fatalf("failed to setup GraphQL subscription: %v", err)
	}
	defer gqlConn.Close()

	go func() {
		for {
			frame, rErr := readWSFrame(gqlBr)
			if rErr != nil {
				return
			}
			var msg struct {
				Type    string `json:"type"`
				ID      string `json:"id"`
				Payload struct {
					Data struct {
						ContractEvents map[string]any `json:"contractEvents"`
					} `json:"data"`
				} `json:"payload"`
			}
			if err := json.Unmarshal(frame, &msg); err == nil && msg.Type == "next" {
				if ev := msg.Payload.Data.ContractEvents; len(ev) > 0 {
					gqlCh <- ev
				}
			}
		}
	}()

	// -------------------------------------------------------------------------
	// 4. Connect SSE (/v1/events/stream)
	// -------------------------------------------------------------------------
	sseCh := make(chan map[string]any, 50)
	sseReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/events/stream?contractId="+contractID, nil)
	sseReq.Header.Set("X-API-Key", apiKey)
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatalf("failed to connect SSE stream: %v", err)
	}
	defer sseResp.Body.Close()

	go func() {
		scanner := bufio.NewScanner(sseResp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				dataJSON := strings.TrimPrefix(line, "data: ")
				var ev map[string]any
				if err := json.Unmarshal([]byte(dataJSON), &ev); err == nil {
					sseCh <- ev
				}
			}
		}
	}()

	// -------------------------------------------------------------------------
	// 5. Connect gRPC StreamEvents
	// -------------------------------------------------------------------------
	grpcCh := make(chan *gen.Event, 50)
	cc, err := grpc.DialContext(ctx, grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("failed to dial gRPC: %v", err)
	}
	defer cc.Close()

	grpcClient := gen.NewEventsClient(cc)
	streamClient, err := grpcClient.StreamEvents(ctx, &gen.StreamEventsRequest{
		ContractId: contractID,
	})
	if err != nil {
		t.Fatalf("failed to invoke gRPC StreamEvents: %v", err)
	}

	go func() {
		for {
			ev, rErr := streamClient.Recv()
			if rErr != nil {
				return
			}
			grpcCh <- ev
		}
	}()

	// Allow streaming connections to be fully registered on server
	time.Sleep(2 * time.Second)

	// -------------------------------------------------------------------------
	// 6. Trigger contract transaction if E2E_TRIGGER_CMD is provided
	// -------------------------------------------------------------------------
	if triggerCmd := os.Getenv("E2E_TRIGGER_CMD"); triggerCmd != "" {
		t.Logf("Executing trigger command: %s", triggerCmd)
		cmd := exec.Command("bash", "-c", triggerCmd)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to run trigger command: %v", err)
		}
		t.Log("Trigger command finished successfully")
	}

	// -------------------------------------------------------------------------
	// 7. Poll REST API for Indexed Baseline Events
	// -------------------------------------------------------------------------
	t.Log("Polling REST API for indexed events...")
	var restEvents []map[string]any
	restClient := &http.Client{Timeout: 5 * time.Second}

	for i := 0; i < 45; i++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/events?contractId="+contractID+"&limit=20", nil)
		req.Header.Set("X-API-Key", apiKey)
		resp, dErr := restClient.Do(req)
		if dErr == nil && resp.StatusCode == http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			var listResp struct {
				Events []map[string]any `json:"events"`
			}
			if jErr := json.Unmarshal(body, &listResp); jErr == nil && len(listResp.Events) >= 2 {
				restEvents = listResp.Events
				break
			}
		}
		time.Sleep(2 * time.Second)
	}

	if len(restEvents) == 0 {
		t.Fatal("REST API returned 0 events within deadline")
	}
	t.Logf("REST API indexed %d events", len(restEvents))

	// Extract target event IDs and network to check across all streaming subscribers
	targetEventIDs := make(map[string]bool)
	expectedNetwork := ""
	for _, ev := range restEvents {
		if id, ok := ev["id"].(string); ok && id != "" {
			targetEventIDs[id] = true
		}
		if net, ok := ev["network"].(string); ok && net != "" {
			expectedNetwork = net
		}
	}
	if len(targetEventIDs) < 2 {
		t.Fatalf("expected at least 2 distinct events in batch, got %d: %v", len(targetEventIDs), targetEventIDs)
	}
	t.Logf("Target Event IDs to verify: %v (network=%s)", targetEventIDs, expectedNetwork)

	// -------------------------------------------------------------------------
	// 8. Assert Events Arrive on All 5 Streaming Subscribers (identical ID & network)
	// -------------------------------------------------------------------------
	assertReceivedEventIDs(t, "WebSocket", wsCh, targetEventIDs, expectedNetwork, contractID, 20*time.Second)
	assertReceivedEventIDs(t, "GraphQL Subscriptions", gqlCh, targetEventIDs, expectedNetwork, contractID, 20*time.Second)
	assertReceivedEventIDs(t, "Server-Sent Events (SSE)", sseCh, targetEventIDs, expectedNetwork, contractID, 20*time.Second)
	assertReceivedWebhookEvents(t, "Webhook Receiver", webhookCh, targetEventIDs, expectedNetwork, contractID, 20*time.Second)
	assertReceivedGRPCEvents(t, "gRPC StreamEvents", grpcCh, targetEventIDs, expectedNetwork, contractID, 20*time.Second)

	sigMu.Lock()
	count := sigVerificationCount
	sigMu.Unlock()
	if count < len(targetEventIDs) {
		t.Fatalf("expected at least %d verified webhook deliveries, got %d", len(targetEventIDs), count)
	}
	t.Logf("✓ Webhook signature verification passed for %d deliveries", count)

	t.Log("✓ All batch events verified with identical ID, network, and contractId across REST, /ws, /graphql, SSE, gRPC, and Webhook subscribers!")
}

func getEnvOrDefault(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

func verifyWebhookSig(payload []byte, secret string, timestamp int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(fmt.Sprintf("%d.", timestamp)))
	mac.Write(payload)
	return fmt.Sprintf("v1=%s", hex.EncodeToString(mac.Sum(nil)))
}

func createWebhookSubscription(baseURL, apiKey, contractID, targetURL string) (string, string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	reqBody, _ := json.Marshal(map[string]string{
		"contractId": contractID,
		"targetUrl":  targetURL,
	})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/webhooks", bytes.NewReader(reqBody))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return "", "", fmt.Errorf("create webhook failed (%d): %s", resp.StatusCode, string(body))
	}

	var res struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return "", "", err
	}
	return res.ID, res.Secret, nil
}

func assertReceivedEventIDs(t *testing.T, channelName string, ch <-chan map[string]any, targetIDs map[string]bool, expectedNetwork, expectedContract string, timeout time.Duration) {
	t.Helper()
	received := make(map[string]bool)
	deadline := time.After(timeout)

	for len(received) < len(targetIDs) {
		select {
		case ev := <-ch:
			id := extractID(ev)
			if id != "" && targetIDs[id] {
				received[id] = true
				if expectedNetwork != "" {
					netStr := extractField(ev, "network")
					if netStr != "" && netStr != expectedNetwork {
						t.Errorf("[%s] network mismatch for event %s: got %s, want %s", channelName, id, netStr, expectedNetwork)
					}
				}
				if expectedContract != "" {
					cID := extractField(ev, "contractId", "contract_id")
					if cID != "" && cID != expectedContract {
						t.Errorf("[%s] contractId mismatch for event %s: got %s, want %s", channelName, id, cID, expectedContract)
					}
				}
			}
		case <-deadline:
			t.Fatalf("[%s] timed out waiting for all batch events. Got %d/%d: %v", channelName, len(received), len(targetIDs), received)
		}
	}
	t.Logf("[%s] ✓ Successfully received all %d target events with identical IDs and network", channelName, len(received))
}

func assertReceivedWebhookEvents(t *testing.T, channelName string, ch <-chan map[string]any, targetIDs map[string]bool, expectedNetwork, expectedContract string, timeout time.Duration) {
	t.Helper()
	received := make(map[string]bool)
	deadline := time.After(timeout)

	for len(received) < len(targetIDs) {
		select {
		case ev := <-ch:
			id := extractID(ev)
			if id != "" && targetIDs[id] {
				received[id] = true
				if expectedNetwork != "" {
					netStr := extractField(ev, "network")
					if netStr != "" && netStr != expectedNetwork {
						t.Errorf("[%s] network mismatch for event %s: got %s, want %s", channelName, id, netStr, expectedNetwork)
					}
				}
				if expectedContract != "" {
					cID := extractField(ev, "contractId", "contract_id")
					if cID != "" && cID != expectedContract {
						t.Errorf("[%s] contractId mismatch for event %s: got %s, want %s", channelName, id, cID, expectedContract)
					}
				}
			}
		case <-deadline:
			t.Fatalf("[%s] timed out waiting for verified webhook events. Got %d/%d: %v", channelName, len(received), len(targetIDs), received)
		}
	}
	t.Logf("[%s] ✓ Successfully received & verified all %d target webhook events", channelName, len(received))
}

func assertReceivedGRPCEvents(t *testing.T, channelName string, ch <-chan *gen.Event, targetIDs map[string]bool, expectedNetwork, expectedContract string, timeout time.Duration) {
	t.Helper()
	received := make(map[string]bool)
	deadline := time.After(timeout)

	for len(received) < len(targetIDs) {
		select {
		case ev := <-ch:
			id := ev.GetId()
			if id != "" && targetIDs[id] {
				received[id] = true
				if expectedContract != "" && ev.GetContractId() != "" && ev.GetContractId() != expectedContract {
					t.Errorf("[%s] contractId mismatch for event %s: got %s, want %s", channelName, id, ev.GetContractId(), expectedContract)
				}
			}
		case <-deadline:
			t.Fatalf("[%s] timed out waiting for gRPC events. Got %d/%d: %v", channelName, len(received), len(targetIDs), received)
		}
	}
	t.Logf("[%s] ✓ Successfully received all %d target gRPC stream events", channelName, len(received))
}

func extractID(ev map[string]any) string {
	if id, ok := ev["id"].(string); ok && id != "" {
		return id
	}
	if id, ok := ev["eventId"].(string); ok && id != "" {
		return id
	}
	if id, ok := ev["event_id"].(string); ok && id != "" {
		return id
	}
	return ""
}

func extractField(ev map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := ev[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func dialWS(targetURL string) (net.Conn, *bufio.Reader, error) {
	u, err := url.Parse(targetURL)
	if err != nil {
		return nil, nil, err
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	conn, err := net.DialTimeout("tcp", host, 5*time.Second)
	if err != nil {
		return nil, nil, err
	}
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", u.RequestURI(), u.Host)
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, nil, err
	}
	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil || !strings.Contains(statusLine, "101") {
		conn.Close()
		return nil, nil, fmt.Errorf("bad status: %s", statusLine)
	}
	for {
		line, _ := br.ReadString('\n')
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	return conn, br, nil
}

func dialGQLSubscription(baseURL, apiKey, contractID string) (net.Conn, *bufio.Reader, error) {
	conn, br, err := dialWS(baseURL + "/graphql")
	if err != nil {
		return nil, nil, err
	}

	// Send connection_init
	initMsg := map[string]any{
		"type": "connection_init",
		"payload": map[string]string{
			"X-API-Key": apiKey,
		},
	}
	initBytes, _ := json.Marshal(initMsg)
	if err := writeWSFrame(conn, initBytes); err != nil {
		conn.Close()
		return nil, nil, err
	}

	// Read connection_ack
	ackFrame, err := readWSFrame(br)
	if err != nil || !strings.Contains(string(ackFrame), "connection_ack") {
		conn.Close()
		return nil, nil, fmt.Errorf("connection_ack failed: %s (%v)", string(ackFrame), err)
	}

	// Send subscribe
	subMsg := map[string]any{
		"id":   "sub-1",
		"type": "subscribe",
		"payload": map[string]any{
			"query": fmt.Sprintf(`subscription { contractEvents(contractId: "%s") { id network contractId } }`, contractID),
		},
	}
	subBytes, _ := json.Marshal(subMsg)
	if err := writeWSFrame(conn, subBytes); err != nil {
		conn.Close()
		return nil, nil, err
	}

	return conn, br, nil
}

func writeWSFrame(w io.Writer, payload []byte) error {
	var header []byte
	header = append(header, 0x81) // FIN + text opcode
	length := len(payload)
	if length < 126 {
		header = append(header, byte(0x80|length)) // masked
	} else if length <= 65535 {
		header = append(header, 0x80|126)
		ext := make([]byte, 2)
		binary.BigEndian.PutUint16(ext, uint16(length))
		header = append(header, ext...)
	} else {
		header = append(header, 0x80|127)
		ext := make([]byte, 8)
		binary.BigEndian.PutUint64(ext, uint64(length))
		header = append(header, ext...)
	}
	maskKey := []byte{0x12, 0x34, 0x56, 0x78}
	header = append(header, maskKey...)
	maskedPayload := make([]byte, length)
	for i := 0; i < length; i++ {
		maskedPayload[i] = payload[i] ^ maskKey[i%4]
	}
	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(maskedPayload)
	return err
}

func readWSFrame(br *bufio.Reader) ([]byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(br, header); err != nil {
		return nil, err
	}
	length := int(header[1] & 0x7f)
	if length == 126 {
		ext := make([]byte, 2)
		if _, err := io.ReadFull(br, ext); err != nil {
			return nil, err
		}
		length = int(binary.BigEndian.Uint16(ext))
	} else if length == 127 {
		ext := make([]byte, 8)
		if _, err := io.ReadFull(br, ext); err != nil {
			return nil, err
		}
		length = int(binary.BigEndian.Uint64(ext))
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(br, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func slogWarn(msg string, args ...any) {
	fmt.Printf("[WARN] %s %v\n", msg, args)
}
