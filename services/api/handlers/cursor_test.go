package handlers_test

import (
	"testing"

	"github.com/Depo-dev/trident/services/api/handlers"
)

// TestCursorKey_MatchesIndexerFormat verifies that CursorKey produces the
// namespaced key matching crates/indexer/src/db/mod.rs::cursor_key across
// all supported networks (issues #600, #716).
func TestCursorKey_MatchesIndexerFormat(t *testing.T) {
	tests := []struct {
		network string
		want    string
	}{
		{network: "mainnet", want: "latest_ledger_cursor:mainnet"},
		{network: "testnet", want: "latest_ledger_cursor:testnet"},
		{network: "futurenet", want: "latest_ledger_cursor:futurenet"},
		{network: "sandbox", want: "latest_ledger_cursor:sandbox"},
	}

	for _, tt := range tests {
		t.Run(tt.network, func(t *testing.T) {
			got := handlers.CursorKey(tt.network)
			if got != tt.want {
				t.Errorf("CursorKey(%q) = %q, want %q", tt.network, got, tt.want)
			}
		})
	}
}
