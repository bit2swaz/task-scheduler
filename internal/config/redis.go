// Package config redis.go -- Redis client factory.
package config

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// NewRedisClient parses cfg.RedisURL, dials Redis, and confirms connectivity
// with a PING. The caller is responsible for calling Close on the returned
// client when it is no longer needed.
func NewRedisClient(ctx context.Context, cfg *Config) (*redis.Client, error) {
	if cfg.RedisURL == "" {
		return nil, fmt.Errorf("redis URL is required")
	}

	opt, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return nil, fmt.Errorf("invalid redis URL %q: %w", cfg.RedisURL, err)
	}

	rdb := redis.NewClient(opt)

	if _, err := rdb.Ping(ctx).Result(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("redis ping failed: %w", err)
	}

	return rdb, nil
}
