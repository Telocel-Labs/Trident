package main

import (
	"errors"
	"strings"
	"testing"
)

func TestRedactConnErr(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		mustNot string // substring that must not appear in the output
		must    string // substring that must appear in the output
	}{
		{
			name:    "redis URL with credentials",
			err:     errors.New("redis: invalid URL scheme: redis://user:supersecret@host:6379/0"),
			mustNot: "supersecret",
			must:    "redis://[redacted]@host:6379/0",
		},
		{
			name:    "postgres DSN with credentials",
			err:     errors.New(`dial error: failed to connect to postgres://trident:hunter2@db-host:5432/trident: connection refused`),
			mustNot: "hunter2",
			must:    "postgres://[redacted]@db-host:5432/trident",
		},
		{
			name: "no embedded DSN — passed through unchanged",
			err:  errors.New("connection refused"),
			must: "connection refused",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactConnErr(tc.err)
			if tc.mustNot != "" && strings.Contains(got, tc.mustNot) {
				t.Fatalf("redactConnErr(%q) = %q; must not contain %q", tc.err, got, tc.mustNot)
			}
			if tc.must != "" && !strings.Contains(got, tc.must) {
				t.Fatalf("redactConnErr(%q) = %q; must contain %q", tc.err, got, tc.must)
			}
		})
	}

	if got := redactConnErr(nil); got != "" {
		t.Fatalf("redactConnErr(nil) = %q; want empty string", got)
	}
}
