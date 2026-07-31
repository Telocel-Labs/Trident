package trident

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestConfigPrecedenceExplicitOverEnv(t *testing.T) {
	t.Setenv(EnvAPIKey, "env-key")
	t.Setenv(EnvBaseURL, "https://env.example.com")

	cfg := TridentClientConfig{
		APIKey:  "explicit-key",
		BaseURL: "https://explicit.example.com",
	}.resolve()

	if cfg.APIKey != "explicit-key" {
		t.Errorf("expected explicit APIKey to win, got %q", cfg.APIKey)
	}
	if cfg.BaseURL != "https://explicit.example.com" {
		t.Errorf("expected explicit BaseURL to win, got %q", cfg.BaseURL)
	}
}

func TestConfigPrecedenceFallsBackToEnv(t *testing.T) {
	t.Setenv(EnvAPIKey, "env-key")
	t.Setenv(EnvBaseURL, "https://env.example.com")

	cfg := TridentClientConfig{}.resolve()

	if cfg.APIKey != "env-key" {
		t.Errorf("expected APIKey from env, got %q", cfg.APIKey)
	}
	if cfg.BaseURL != "https://env.example.com" {
		t.Errorf("expected BaseURL from env, got %q", cfg.BaseURL)
	}
}

func TestConfigMissingAPIKeyReturnsClearError(t *testing.T) {
	t.Setenv(EnvAPIKey, "")
	t.Setenv(EnvBaseURL, "")

	client := NewClient(TridentClientConfig{BaseURL: "https://api.example.com"})

	_, err := client.QueryEvents(context.Background(), QueryEventsParams{})
	if !errors.Is(err, ErrMissingAPIKey) {
		t.Fatalf("expected ErrMissingAPIKey, got %v", err)
	}

	_, err = client.GetEventByID(context.Background(), "some-id")
	if !errors.Is(err, ErrMissingAPIKey) {
		t.Fatalf("expected ErrMissingAPIKey, got %v", err)
	}

	_, err = client.SubscribeToContract(context.Background(), SubscribeToContractParams{ContractID: "C123"})
	if !errors.Is(err, ErrMissingAPIKey) {
		t.Fatalf("expected ErrMissingAPIKey, got %v", err)
	}
}

func TestConfigRedactsAPIKeyInString(t *testing.T) {
	cfg := TridentClientConfig{
		BaseURL: "https://api.example.com",
		APIKey:  "super-secret-value",
	}

	repr := cfg.String()
	if strings.Contains(repr, "super-secret-value") {
		t.Fatalf("expected APIKey to be redacted, got %q", repr)
	}
	if !strings.HasSuffix(strings.TrimSuffix(repr, "}"), "-value") {
		t.Fatalf("expected redacted suffix to be preserved for debugging, got %q", repr)
	}
}

func TestRedactKeyShortKeys(t *testing.T) {
	if got := redactKey(""); got != "<empty>" {
		t.Errorf("expected <empty>, got %q", got)
	}
	if got := redactKey("abc"); got != "***" {
		t.Errorf("expected ***, got %q", got)
	}
}
