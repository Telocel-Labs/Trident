package trident

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func fastRetryConfig(maxAttempts int) *RetryConfig {
	return &RetryConfig{
		MaxAttempts:   maxAttempts,
		BaseDelay:     1 * time.Millisecond,
		MaxDelay:      20 * time.Millisecond,
		MaxTotalWait:  1 * time.Second,
		DisableJitter: true,
	}
}

func TestQueryEvents_SucceedsAfterTransient503s(t *testing.T) {
	var calls int32
	mockResponse := PaginatedEvents{Events: []*SorobanEvent{{ID: "evt-1"}}}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("temporarily unavailable"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(mockResponse)
	}))
	defer server.Close()

	client := NewClient(TridentClientConfig{
		BaseURL: server.URL,
		Retry:   fastRetryConfig(3),
	})

	res, err := client.QueryEvents(context.Background(), QueryEventsParams{})
	if err != nil {
		t.Fatalf("expected success after retries, got error: %v", err)
	}
	if len(res.Events) != 1 || res.Events[0].ID != "evt-1" {
		t.Errorf("unexpected result: %+v", res)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("expected 3 calls, got %d", got)
	}
}

func TestQueryEvents_HonoursRetryAfterOn429(t *testing.T) {
	var calls int32
	var firstCallTime, secondCallTime time.Time
	mockResponse := PaginatedEvents{Events: []*SorobanEvent{{ID: "evt-1"}}}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			firstCallTime = time.Now()
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("slow down"))
			return
		}
		secondCallTime = time.Now()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(mockResponse)
	}))
	defer server.Close()

	// Base delay is large; Retry-After: 0 must be honoured instead, so this
	// completes fast rather than waiting out the (large) computed backoff.
	client := NewClient(TridentClientConfig{
		BaseURL: server.URL,
		Retry: &RetryConfig{
			MaxAttempts:   3,
			BaseDelay:     5 * time.Second,
			MaxDelay:      5 * time.Second,
			MaxTotalWait:  30 * time.Second,
			DisableJitter: true,
		},
	})

	done := make(chan error, 1)
	go func() {
		_, err := client.QueryEvents(context.Background(), QueryEventsParams{})
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected success, got error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out — Retry-After header was not honoured over base backoff")
	}

	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("expected 2 calls, got %d", calls)
	}
	_ = firstCallTime
	_ = secondCallTime
}

func TestQueryEvents_GivesUpAfterMaxAttempts(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("still down"))
	}))
	defer server.Close()

	client := NewClient(TridentClientConfig{
		BaseURL: server.URL,
		Retry:   fastRetryConfig(3),
	})

	_, err := client.QueryEvents(context.Background(), QueryEventsParams{})
	if err == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}

	apiErr, ok := err.(*TridentApiError)
	if !ok {
		t.Fatalf("expected *TridentApiError, got %T: %v", err, err)
	}
	if apiErr.Status != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", apiErr.Status)
	}
	if apiErr.Attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", apiErr.Attempts)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("expected 3 calls to server, got %d", got)
	}
}

func TestQueryEvents_DoesNotRetryNonRetryableStatus(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("bad key"))
	}))
	defer server.Close()

	client := NewClient(TridentClientConfig{
		BaseURL: server.URL,
		Retry:   fastRetryConfig(5),
	})

	_, err := client.QueryEvents(context.Background(), QueryEventsParams{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	apiErr, ok := err.(*TridentApiError)
	if !ok {
		t.Fatalf("expected *TridentApiError, got %T", err)
	}
	if apiErr.Attempts != 1 {
		t.Errorf("expected 1 attempt for non-retryable status, got %d", apiErr.Attempts)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("expected exactly 1 call, got %d", got)
	}
}

func TestQueryEvents_RetriesDisabledAtClientLevel(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("down"))
	}))
	defer server.Close()

	client := NewClient(TridentClientConfig{
		BaseURL:       server.URL,
		RetryDisabled: true,
	})

	_, err := client.QueryEvents(context.Background(), QueryEventsParams{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("expected exactly 1 call when retries disabled, got %d", got)
	}
}

func TestQueryEvents_PerCallOptionOverridesClientPolicy(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("down"))
	}))
	defer server.Close()

	client := NewClient(TridentClientConfig{
		BaseURL: server.URL,
		Retry:   fastRetryConfig(5),
	})

	// Disable retries for just this call.
	_, err := client.QueryEvents(context.Background(), QueryEventsParams{}, WithRetryDisabled())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("expected exactly 1 call with per-call retry disabled, got %d", got)
	}
}

func TestGetEventByID_AppliesRetryPolicy(t *testing.T) {
	var calls int32
	mockResponse := struct {
		Event *SorobanEvent `json:"event"`
	}{Event: &SorobanEvent{ID: "evt-1"}}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(mockResponse)
	}))
	defer server.Close()

	client := NewClient(TridentClientConfig{
		BaseURL: server.URL,
		Retry:   fastRetryConfig(3),
	})

	res, err := client.GetEventByID(context.Background(), "evt-1")
	if err != nil {
		t.Fatalf("expected success after retry, got error: %v", err)
	}
	if res.ID != "evt-1" {
		t.Errorf("unexpected event: %+v", res)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected 2 calls, got %d", got)
	}
}

func TestParseRetryAfter_Seconds(t *testing.T) {
	d, ok := parseRetryAfter("5")
	if !ok || d != 5*time.Second {
		t.Errorf("expected 5s true, got %v %v", d, ok)
	}
}

func TestParseRetryAfter_HTTPDate(t *testing.T) {
	future := time.Now().Add(10 * time.Second).UTC().Format(http.TimeFormat)
	d, ok := parseRetryAfter(future)
	if !ok {
		t.Fatal("expected ok=true for valid HTTP date")
	}
	if d <= 0 || d > 11*time.Second {
		t.Errorf("expected ~10s, got %v", d)
	}
}

func TestParseRetryAfter_Invalid(t *testing.T) {
	if _, ok := parseRetryAfter(""); ok {
		t.Error("expected ok=false for empty header")
	}
	if _, ok := parseRetryAfter("not-a-number-or-date"); ok {
		t.Error("expected ok=false for garbage header")
	}
}

func TestComputeBackoff_ExponentialGrowthCappedAtMaxDelay(t *testing.T) {
	cfg := &RetryConfig{BaseDelay: 10 * time.Millisecond, MaxDelay: 100 * time.Millisecond, DisableJitter: true}
	for attempt, want := range map[int]time.Duration{
		1: 10 * time.Millisecond,
		2: 20 * time.Millisecond,
		3: 40 * time.Millisecond,
		4: 80 * time.Millisecond,
		5: 100 * time.Millisecond, // capped
	} {
		if got := computeBackoff(attempt, cfg); got != want {
			t.Errorf("attempt %d: expected %v, got %v", attempt, want, got)
		}
	}
}

func TestTridentApiError_ErrorMessageIncludesAttempts(t *testing.T) {
	err := &TridentApiError{Status: 503, Code: "INTERNAL", Message: "down", Attempts: 3}
	msg := err.Error()
	if want := strconv.Itoa(3); msg == "" || !contains(msg, want) {
		t.Errorf("expected error message to mention attempts, got %q", msg)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}
