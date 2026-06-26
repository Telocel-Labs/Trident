// Package config loads and validates required environment variables for the
// Trident Go API. All required variables are collected before exiting so
// operators see every problem in a single run.
package config

import (
	"fmt"
	"os"
)

// Config holds validated environment-sourced configuration for the Go API.
type Config struct {
	DatabaseURL string
	RedisURL    string
	APIGrpcAddr string
}

// Load reads all required env vars, prints any that are missing, and calls
// os.Exit(1) when the set is incomplete. Optional vars are not validated here.
func Load() *Config {
	missing := CollectMissing()

	if len(missing) > 0 {
		fmt.Fprintln(os.Stderr, "[trident-api] missing required env vars:")
		for _, v := range missing {
			fmt.Fprintln(os.Stderr, v)
		}
		os.Exit(1)
	}

	return &Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		RedisURL:    os.Getenv("REDIS_URL"),
		APIGrpcAddr: os.Getenv("API_GRPC_ADDR"),
	}
}

// CollectMissing returns the names of required env vars that are absent or
// empty. Exported so unit tests can assert missing-var detection without
// triggering os.Exit.
func CollectMissing() []string {
	required := []string{"DATABASE_URL", "REDIS_URL", "API_GRPC_ADDR"}
	var missing []string
	for _, key := range required {
		v, ok := os.LookupEnv(key)
		if !ok || v == "" {
			missing = append(missing, key)
		}
	}
	return missing
}
