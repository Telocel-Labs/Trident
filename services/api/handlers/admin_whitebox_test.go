package handlers

import "testing"

// TestValidAdminKey_TableDriven exercises validAdminKey's accept/reject
// behavior directly. validAdminKey is the shared constant-time comparison
// used for X-Admin-Key (AdminDB, AdminKeyUsage, api-keys admin endpoints,
// contract admin endpoints) and X-Internal-Key (InternalStatus).
//
// This does not attempt to measure timing — a statistical timing assertion
// in a unit test is unreliable and flaky given scheduler/GC jitter, so
// instead this proves correctness of usage: validAdminKey is implemented in
// terms of crypto/subtle.ConstantTimeCompare (see admin.go), and this test
// exercises every shape of input (equal, different length, same length but
// mismatched, empty) that a constant-time comparator must still get right.
func TestValidAdminKey_TableDriven(t *testing.T) {
	tests := []struct {
		name     string
		expected string
		provided string
		want     bool
	}{
		{name: "exact match", expected: "s3cr3t-admin-key", provided: "s3cr3t-admin-key", want: true},
		{name: "empty provided", expected: "s3cr3t-admin-key", provided: "", want: false},
		{name: "shorter provided (length mismatch)", expected: "s3cr3t-admin-key", provided: "s3cr3t", want: false},
		{name: "longer provided (length mismatch)", expected: "s3cr3t-admin-key", provided: "s3cr3t-admin-key-extra", want: false},
		{name: "same length, mismatched content", expected: "s3cr3t-admin-key", provided: "s3cr3t-admin-kex", want: false},
		{name: "both empty (provided empty is always rejected)", expected: "", provided: "", want: false},
		{name: "empty expected, nonempty provided", expected: "", provided: "anything", want: false},
		{name: "case-sensitive mismatch", expected: "S3cr3t-Admin-Key", provided: "s3cr3t-admin-key", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validAdminKey(tt.expected, tt.provided)
			if got != tt.want {
				t.Errorf("validAdminKey(%q, %q) = %v, want %v", tt.expected, tt.provided, got, tt.want)
			}
		})
	}
}
