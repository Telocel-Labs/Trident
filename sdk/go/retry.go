package trident

import (
	"context"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RetryConfig configures automatic retries with backoff for idempotent (GET)
// requests. Honours the Retry-After header on 429/503 responses, falling
// back to exponential backoff with jitter otherwise.
//
// Any zero-valued field falls back to the corresponding DefaultRetryConfig
// value.
type RetryConfig struct {
	// MaxAttempts is the total number of attempts, including the first.
	MaxAttempts int
	// BaseDelay is the base delay used for exponential backoff.
	BaseDelay time.Duration
	// MaxDelay caps a single computed backoff delay.
	MaxDelay time.Duration
	// MaxTotalWait caps total time spent waiting across all retries
	// (including any honoured Retry-After).
	MaxTotalWait time.Duration
	// DisableJitter turns off randomization of computed delays.
	DisableJitter bool
}

// DefaultRetryConfig is applied when a client or call does not specify a
// custom RetryConfig and retries are not disabled.
var DefaultRetryConfig = RetryConfig{
	MaxAttempts:  3,
	BaseDelay:    100 * time.Millisecond,
	MaxDelay:     2 * time.Second,
	MaxTotalWait: 10 * time.Second,
}

func fillRetryDefaults(cfg RetryConfig) RetryConfig {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = DefaultRetryConfig.MaxAttempts
	}
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = DefaultRetryConfig.BaseDelay
	}
	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = DefaultRetryConfig.MaxDelay
	}
	if cfg.MaxTotalWait <= 0 {
		cfg.MaxTotalWait = DefaultRetryConfig.MaxTotalWait
	}
	return cfg
}

// requestOptions holds per-call overrides collected from RequestOption values.
type requestOptions struct {
	retry         *RetryConfig
	retryDisabled bool
}

// RequestOption customizes a single call, overriding client-level config.
type RequestOption func(*requestOptions)

// WithRetry overrides the retry policy for a single call.
func WithRetry(cfg RetryConfig) RequestOption {
	return func(o *requestOptions) { o.retry = &cfg }
}

// WithRetryDisabled disables retries for a single call.
func WithRetryDisabled() RequestOption {
	return func(o *requestOptions) { o.retryDisabled = true }
}

// effectiveRetryConfig merges per-call options with the client-level policy.
// Returns nil when retries are disabled.
func (c *Client) effectiveRetryConfig(opts []RequestOption) *RetryConfig {
	ro := requestOptions{}
	for _, opt := range opts {
		opt(&ro)
	}

	if ro.retryDisabled {
		return nil
	}
	if ro.retry != nil {
		filled := fillRetryDefaults(*ro.retry)
		return &filled
	}
	if c.config.RetryDisabled {
		return nil
	}
	if c.config.Retry != nil {
		filled := fillRetryDefaults(*c.config.Retry)
		return &filled
	}

	cfg := DefaultRetryConfig
	return &cfg
}

// isRetryableStatus reports whether a status code is eligible for retry.
// Only 429 (rate limited) and 503 (service unavailable) are retried.
func isRetryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable
}

// parseRetryAfter parses a Retry-After header value, which per RFC 9110 is
// either a number of seconds or an HTTP date.
func parseRetryAfter(header string) (time.Duration, bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(header); err == nil {
		if secs < 0 {
			secs = 0
		}
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(header); err == nil {
		d := time.Until(t)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

// computeBackoff returns an exponential backoff delay with optional full
// jitter, capped at cfg.MaxDelay.
func computeBackoff(attempt int, cfg *RetryConfig) time.Duration {
	exp := cfg.BaseDelay * time.Duration(int64(1)<<uint(attempt-1))
	if exp > cfg.MaxDelay || exp < 0 {
		exp = cfg.MaxDelay
	}
	if cfg.DisableJitter {
		return exp
	}
	return time.Duration(rand.Int63n(int64(exp) + 1))
}

// retryAfterOrBackoff honours a parsed Retry-After header if present,
// otherwise falls back to computed exponential backoff.
func retryAfterOrBackoff(header string, attempt int, cfg *RetryConfig) time.Duration {
	if d, ok := parseRetryAfter(header); ok {
		return d
	}
	return computeBackoff(attempt, cfg)
}

// sleepCtx sleeps for d, returning false early if ctx is cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
