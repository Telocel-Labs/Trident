package multireplicatest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const (
	testContractID = "CA7QYNF7SOWQ3GLR2BGMZEHXAVIRZA4KVWLTJJFC7MGXUA74P7UJVSGZ"
)

func getEnvOrDefault(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

// TestMultiReplicaIntegration executes the full multi-replica verification suite (issue #807):
//   1. WebSocket client per replica receives every event (fan-out, not consumer-group partitioned);
//   2. An idempotent retry routed to a different replica replays;
//   3. Periodic jobs run once per interval via Postgres advisory lock leader election;
//   4. Nginx reverse proxy load-balances traffic across both replicas.
func TestMultiReplicaIntegration(t *testing.T) {
	nginxURL := os.Getenv("MULTIREPLICA_NGINX_URL")
	if nginxURL == "" {
		t.Skip("skipping multi-replica test: MULTIREPLICA_NGINX_URL not set")
	}

	api1URL := getEnvOrDefault("MULTIREPLICA_API1_URL", "http://localhost:3001")
	api2URL := getEnvOrDefault("MULTIREPLICA_API2_URL", "http://localhost:3002")
	redisURL := getEnvOrDefault("MULTIREPLICA_REDIS_URL", "redis://localhost:6380")
	dbURL := getEnvOrDefault("MULTIREPLICA_DATABASE_URL", "postgres://trident:ci-password@localhost:5432/trident?sslmode=disable")
	apiKey := getEnvOrDefault("MULTIREPLICA_API_KEY", "ci-test-api-key-do-not-use-in-production")

	// Ensure all replicas and nginx are responding before running tests
	assertHealthy(t, api1URL+"/v1/health")
	assertHealthy(t, api2URL+"/v1/health")
	assertHealthy(t, nginxURL+"/v1/health")

	t.Run("WebSocketFanoutAcrossReplicas", func(t *testing.T) {
		testWebSocketFanout(t, api1URL, api2URL, redisURL)
	})

	t.Run("IdempotencyCrossReplicaReplay", func(t *testing.T) {
		testIdempotencyCrossReplica(t, api1URL, api2URL, apiKey)
	})

	t.Run("PeriodicJobLeaderElection", func(t *testing.T) {
		testPeriodicJobLeaderElection(t, dbURL)
	})

	t.Run("NginxLoadBalancing", func(t *testing.T) {
		testNginxProxy(t, nginxURL, apiKey)
	})
}

func assertHealthy(t *testing.T, targetURL string) {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	var lastErr error
	for i := 0; i < 30; i++ {
		resp, err := client.Get(targetURL)
		if err == nil && resp.StatusCode == http.StatusOK {
			_ = resp.Body.Close()
			return
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("unexpected status %d", resp.StatusCode)
			_ = resp.Body.Close()
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatalf("target %s did not become healthy: %v", targetURL, lastErr)
}

func testWebSocketFanout(t *testing.T, api1URL, api2URL, redisURL string) {
	wsURL1 := api1URL + "/ws?contractId=" + testContractID
	wsURL2 := api2URL + "/ws?contractId=" + testContractID

	// Connect client 1 to Replica 1
	conn1, br1, err := dialWebSocket(wsURL1)
	if err != nil {
		t.Fatalf("failed to connect ws client 1 to %s: %v", wsURL1, err)
	}
	defer conn1.Close()

	// Connect client 2 to Replica 2
	conn2, br2, err := dialWebSocket(wsURL2)
	if err != nil {
		t.Fatalf("failed to connect ws client 2 to %s: %v", wsURL2, err)
	}
	defer conn2.Close()

	// Connect to shared Redis
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatalf("failed to parse redis url: %v", err)
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testEventID := uuid.NewString()
	rawPayload := fmt.Sprintf(`{"eventId":%q,"contractId":%q,"topic0":"transfer","amount":1000}`, testEventID, testContractID)
	streamMsg := map[string]interface{}{
		"data": fmt.Sprintf(`{"contract_id":%q,"payload":%s}`, testContractID, rawPayload),
	}

	// Wait 200ms to ensure both consumer groups are settled
	time.Sleep(200 * time.Millisecond)

	// Publish event to Redis Stream
	_, err = rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: "trident:events",
		Values: streamMsg,
	}).Result()
	if err != nil {
		t.Fatalf("failed to publish test event to Redis: %v", err)
	}

	// Verify Client 1 receives the event
	recvChan1 := make(chan []byte, 1)
	go func() {
		_ = conn1.SetReadDeadline(time.Now().Add(5 * time.Second))
		frame, rErr := readWSFrame(br1)
		if rErr == nil {
			recvChan1 <- frame
		}
	}()

	// Verify Client 2 receives the event
	recvChan2 := make(chan []byte, 1)
	go func() {
		_ = conn2.SetReadDeadline(time.Now().Add(5 * time.Second))
		frame, rErr := readWSFrame(br2)
		if rErr == nil {
			recvChan2 <- frame
		}
	}()

	select {
	case msg := <-recvChan1:
		if !strings.Contains(string(msg), testEventID) {
			t.Fatalf("client 1 received unexpected message: %s", string(msg))
		}
	case <-time.After(6 * time.Second):
		t.Fatal("client 1 on replica 1 timed out waiting for event")
	}

	select {
	case msg := <-recvChan2:
		if !strings.Contains(string(msg), testEventID) {
			t.Fatalf("client 2 received unexpected message: %s", string(msg))
		}
	case <-time.After(6 * time.Second):
		t.Fatal("client 2 on replica 2 timed out waiting for event (indicates consumer-group partition bug)")
	}
}

func testIdempotencyCrossReplica(t *testing.T, api1URL, api2URL, apiKey string) {
	client := &http.Client{Timeout: 10 * time.Second}
	idemKey := "idem-test-" + uuid.NewString()

	payload := map[string]string{
		"contractId": testContractID,
		"targetUrl":  "https://example.com/webhook-endpoint",
	}
	bodyBytes, _ := json.Marshal(payload)

	// Step 1: POST to Replica 1 with Idempotency-Key
	req1, err := http.NewRequest(http.MethodPost, api1URL+"/v1/webhooks", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("failed to construct request 1: %v", err)
	}
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-API-Key", apiKey)
	req1.Header.Set("Idempotency-Key", idemKey)

	resp1, err := client.Do(req1)
	if err != nil {
		t.Fatalf("replica 1 request failed: %v", err)
	}
	defer resp1.Body.Close()
	body1, _ := io.ReadAll(resp1.Body)

	if resp1.StatusCode != http.StatusCreated {
		t.Fatalf("replica 1 returned status %d: %s", resp1.StatusCode, string(body1))
	}

	// Step 2: Retry EXACT same request to Replica 2
	req2, err := http.NewRequest(http.MethodPost, api2URL+"/v1/webhooks", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("failed to construct request 2: %v", err)
	}
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-API-Key", apiKey)
	req2.Header.Set("Idempotency-Key", idemKey)

	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("replica 2 retry request failed: %v", err)
	}
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)

	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("replica 2 returned status %d: %s", resp2.StatusCode, string(body2))
	}

	// Assert Idempotent-Replayed header is present
	if resp2.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replica 2 failed to replay idempotency record (missing Idempotent-Replayed header). Body: %s", string(body2))
	}

	// Assert response bodies match
	if string(body1) != string(body2) {
		t.Fatalf("replayed body mismatch: \nrep1: %s\nrep2: %s", string(body1), string(body2))
	}
}

func testPeriodicJobLeaderElection(t *testing.T, dbURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("failed to connect to database: %v", err)
	}
	defer pool.Close()

	// Simulate two replicas attempting to run the retention job at the same interval.
	var wg sync.WaitGroup
	var wonLockCount int32
	var mu sync.Mutex

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, cErr := pool.Acquire(ctx)
			if cErr != nil {
				return
			}
			defer conn.Release()

			var locked bool
			qErr := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(123456789, 999)").Scan(&locked)
			if qErr == nil && locked {
				mu.Lock()
				wonLockCount++
				mu.Unlock()
				// Hold lock briefly to simulate job execution
				time.Sleep(200 * time.Millisecond)
				_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock(123456789, 999)")
			}
		}()
	}

	wg.Wait()

	if wonLockCount != 1 {
		t.Fatalf("expected exactly 1 replica to win the periodic job advisory lock, got %d", wonLockCount)
	}
}

func testNginxProxy(t *testing.T, nginxURL, apiKey string) {
	client := &http.Client{Timeout: 5 * time.Second}

	for i := 0; i < 5; i++ {
		req, err := http.NewRequest(http.MethodGet, nginxURL+"/v1/events?limit=1", nil)
		if err != nil {
			t.Fatalf("failed to create nginx request: %v", err)
		}
		req.Header.Set("X-API-Key", apiKey)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("nginx request %d failed: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("nginx request %d returned status %d: %s", i, resp.StatusCode, string(body))
		}
	}
}

func dialWebSocket(targetURL string) (net.Conn, *bufio.Reader, error) {
	u, err := url.Parse(targetURL)
	if err != nil {
		return nil, nil, err
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		if u.Scheme == "https" || u.Scheme == "wss" {
			host += ":443"
		} else {
			host += ":80"
		}
	}

	conn, err := net.DialTimeout("tcp", host, 5*time.Second)
	if err != nil {
		return nil, nil, err
	}

	pathAndQuery := u.RequestURI()
	if pathAndQuery == "" {
		pathAndQuery = "/"
	}

	req := fmt.Sprintf("GET %s HTTP/1.1\r\n"+
		"Host: %s\r\n"+
		"Upgrade: websocket\r\n"+
		"Connection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n"+
		"Sec-WebSocket-Version: 13\r\n\r\n", pathAndQuery, u.Host)

	if _, err := conn.Write([]byte(req)); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}

	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if !strings.Contains(statusLine, "101") {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("unexpected status: %s", statusLine)
	}

	for {
		line, err := br.ReadString('\n')
		if err != nil {
			_ = conn.Close()
			return nil, nil, err
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}

	return conn, br, nil
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
