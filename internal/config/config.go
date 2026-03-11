// Package config provides environment-based configuration loading with defaults
// and validation for the distributed task scheduler.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

// Config holds all runtime configuration for a scheduler node.
type Config struct {
	// Port is the HTTP port the node listens on. Default: 3000.
	Port string

	// NodeID uniquely identifies this node in the cluster. Required.
	NodeID string

	// RedisURL is the Redis connection URL. Default: redis://localhost:6379.
	RedisURL string

	// JWTSecret is the HMAC secret used to sign and validate JWT tokens. Required.
	JWTSecret string

	// HashRingReplicas is the number of virtual nodes per physical node in the
	// consistent hash ring. Default: 150.
	HashRingReplicas int

	// LeaseTTL is how long a leader lease lives in Redis before expiring. Default: 10s.
	LeaseTTL time.Duration

	// RenewInterval is how often the current leader renews its lease. Default: 5s.
	RenewInterval time.Duration

	// PollInterval is how often standby nodes poll to take over leadership. Default: 5s.
	PollInterval time.Duration
}

// Load reads configuration from the process environment, applying defaults where
// values are absent. It silently ignores a missing .env file so the function
// is safe to call in production (no .env) and in tests (no .env).
func Load() (*Config, error) {
	// Silently ignore a missing .env file; it is optional.
	_ = godotenv.Load()

	cfg := &Config{}
	setDefaults(cfg)
	readEnv(cfg)

	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	return cfg, nil
}

// setDefaults applies safe defaults to all optional fields of cfg.
func setDefaults(cfg *Config) {
	cfg.Port = "3000"
	cfg.RedisURL = "redis://localhost:6379"
	cfg.HashRingReplicas = 150
	cfg.LeaseTTL = 10 * time.Second
	cfg.RenewInterval = 5 * time.Second
	cfg.PollInterval = 5 * time.Second
}

// readEnv overwrites any cfg field for which the corresponding env var is set.
func readEnv(cfg *Config) {
	if v := os.Getenv("PORT"); v != "" {
		cfg.Port = v
	}
	if v := os.Getenv("NODE_ID"); v != "" {
		cfg.NodeID = v
	}
	if v := os.Getenv("REDIS_URL"); v != "" {
		cfg.RedisURL = v
	}
	if v := os.Getenv("JWT_SECRET"); v != "" {
		cfg.JWTSecret = v
	}
	if v := os.Getenv("HASH_RING_REPLICAS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.HashRingReplicas = n
		}
		// invalid value: silently keep default
	}
}

// validate returns an error for any required field that is still empty after
// reading the environment.
func validate(cfg *Config) error {
	if cfg.NodeID == "" {
		return errors.New("NODE_ID is required but not set")
	}
	if cfg.JWTSecret == "" {
		return errors.New("JWT_SECRET is required but not set")
	}
	return nil
}
