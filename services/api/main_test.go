package main

import (
	"testing"
	"time"
)

const testDSN = "postgres://user:pass@localhost:5432/testdb"

// TestBuildPoolConfig_Defaults verifies buildPoolConfig applies the
// documented defaults when no pool env vars are set (issue #238).
// pgxpool.ParseConfig does not connect, so this needs no live database.
func TestBuildPoolConfig_Defaults(t *testing.T) {
	cfg, err := buildPoolConfig(testDSN, 7)
	if err != nil {
		t.Fatalf("buildPoolConfig: %v", err)
	}

	if cfg.MaxConns != 7 {
		t.Errorf("MaxConns: want 7, got %d", cfg.MaxConns)
	}
	if cfg.MinConns != defaultDBPoolMinConns {
		t.Errorf("MinConns: want %d, got %d", defaultDBPoolMinConns, cfg.MinConns)
	}
	if want := time.Duration(defaultDBPoolMaxConnLifetimeMS) * time.Millisecond; cfg.MaxConnLifetime != want {
		t.Errorf("MaxConnLifetime: want %v, got %v", want, cfg.MaxConnLifetime)
	}
	if want := cfg.MaxConnLifetime * dbPoolMaxConnLifetimeJitterPercent / 100; cfg.MaxConnLifetimeJitter != want {
		t.Errorf("MaxConnLifetimeJitter: want %v, got %v", want, cfg.MaxConnLifetimeJitter)
	}
	if want := time.Duration(defaultDBPoolMaxConnIdleTimeMS) * time.Millisecond; cfg.MaxConnIdleTime != want {
		t.Errorf("MaxConnIdleTime: want %v, got %v", want, cfg.MaxConnIdleTime)
	}
	if want := time.Duration(defaultDBPoolHealthCheckPeriodMS) * time.Millisecond; cfg.HealthCheckPeriod != want {
		t.Errorf("HealthCheckPeriod: want %v, got %v", want, cfg.HealthCheckPeriod)
	}
	if cfg.AfterConnect == nil {
		t.Error("AfterConnect: want non-nil (statement_timeout / idle_in_transaction_session_timeout hook)")
	}
}

// TestBuildPoolConfig_EnvOverrides verifies pool lifecycle env vars are
// honored (issue #238).
func TestBuildPoolConfig_EnvOverrides(t *testing.T) {
	t.Setenv("GO_API_DB_POOL_MIN_CONNS", "3")
	t.Setenv("GO_API_DB_POOL_MAX_CONN_LIFETIME_MS", "60000")
	t.Setenv("GO_API_DB_POOL_MAX_CONN_IDLE_TIME_MS", "20000")
	t.Setenv("GO_API_DB_POOL_HEALTH_CHECK_PERIOD_MS", "5000")

	cfg, err := buildPoolConfig(testDSN, 10)
	if err != nil {
		t.Fatalf("buildPoolConfig: %v", err)
	}

	if cfg.MinConns != 3 {
		t.Errorf("MinConns: want 3, got %d", cfg.MinConns)
	}
	if cfg.MaxConnLifetime != 60*time.Second {
		t.Errorf("MaxConnLifetime: want 60s, got %v", cfg.MaxConnLifetime)
	}
	if cfg.MaxConnLifetimeJitter != 6*time.Second {
		t.Errorf("MaxConnLifetimeJitter: want 6s (10%% of lifetime), got %v", cfg.MaxConnLifetimeJitter)
	}
	if cfg.MaxConnIdleTime != 20*time.Second {
		t.Errorf("MaxConnIdleTime: want 20s, got %v", cfg.MaxConnIdleTime)
	}
	if cfg.HealthCheckPeriod != 5*time.Second {
		t.Errorf("HealthCheckPeriod: want 5s, got %v", cfg.HealthCheckPeriod)
	}
}

// TestBuildPoolConfig_InvalidEnvFallsBackToDefault verifies unparsable pool
// env vars fall back to defaults rather than erroring (issue #238), matching
// dbPoolSizeFromEnv's existing warn-and-fallback convention.
func TestBuildPoolConfig_InvalidEnvFallsBackToDefault(t *testing.T) {
	t.Setenv("GO_API_DB_POOL_MIN_CONNS", "not-a-number")

	cfg, err := buildPoolConfig(testDSN, 5)
	if err != nil {
		t.Fatalf("buildPoolConfig: %v", err)
	}
	if cfg.MinConns != defaultDBPoolMinConns {
		t.Errorf("MinConns: want default %d on invalid input, got %d", defaultDBPoolMinConns, cfg.MinConns)
	}
}

// TestEnvIntBounded_ClampsOutOfRange verifies DB_STATEMENT_TIMEOUT_MS /
// DB_IDLE_IN_TRANSACTION_TIMEOUT_MS style vars are clamped into range rather
// than accepted as-is (issue #238), mirroring the Rust indexer's
// parse_bounded_u64.
func TestEnvIntBounded_ClampsOutOfRange(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  int
	}{
		{"below min clamps to min", "50", statementTimeoutMinMS},
		{"above max clamps to max", "10000000", statementTimeoutMaxMS},
		{"within range passes through", "15000", 15000},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TEST_BOUNDED_TIMEOUT_MS", tc.value)
			got := envIntBounded("TEST_BOUNDED_TIMEOUT_MS", defaultStatementTimeoutMS, statementTimeoutMinMS, statementTimeoutMaxMS)
			if got != tc.want {
				t.Errorf("envIntBounded(%q): want %d, got %d", tc.value, tc.want, got)
			}
		})
	}
}

// TestEnvIntBounded_DefaultsWhenUnset verifies an unset env var returns def
// rather than 0 or an error.
func TestEnvIntBounded_DefaultsWhenUnset(t *testing.T) {
	got := envIntBounded("TEST_BOUNDED_UNSET_VAR", defaultStatementTimeoutMS, statementTimeoutMinMS, statementTimeoutMaxMS)
	if got != defaultStatementTimeoutMS {
		t.Errorf("want default %d, got %d", defaultStatementTimeoutMS, got)
	}
}
