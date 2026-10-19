package handlers

// CursorKeyPrefix is the prefix for per-network cursor keys in system_state (migration 0033).
const CursorKeyPrefix = "latest_ledger_cursor:"

// CursorKey returns the namespaced system_state key for network (issue #600, #716).
// Matches crates/indexer/src/db/mod.rs::cursor_key.
func CursorKey(network string) string {
	return CursorKeyPrefix + network
}
