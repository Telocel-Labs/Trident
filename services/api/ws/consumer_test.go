package ws

import (
	"os"
	"testing"
)

func TestConsumerGroupName(t *testing.T) {
	t.Run("default derives from consumerName", func(t *testing.T) {
		orig := os.Getenv("WS_CONSUMER_GROUP")
		_ = os.Unsetenv("WS_CONSUMER_GROUP")
		t.Cleanup(func() {
			if orig != "" {
				_ = os.Setenv("WS_CONSUMER_GROUP", orig)
			}
		})

		consumerName := "replica-1-pod-xyz"
		got := consumerGroupName(consumerName)
		want := "trident-api-replica-1-pod-xyz"
		if got != want {
			t.Fatalf("expected %q, got %q", want, got)
		}
	})

	t.Run("explicit WS_CONSUMER_GROUP overrides", func(t *testing.T) {
		t.Setenv("WS_CONSUMER_GROUP", "custom-group")
		got := consumerGroupName("replica-1-pod-xyz")
		if got != "custom-group" {
			t.Fatalf("expected custom-group, got %q", got)
		}
	})
}
