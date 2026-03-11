package config_test

import (
	"context"
	"testing"

	"github.com/bit2swaz/task-scheduler/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewRedisClient_EmptyURL verifies NewRedisClient returns an error when
// RedisURL is an empty string.
func TestNewRedisClient_EmptyURL(t *testing.T) {
	cfg := &config.Config{RedisURL: ""}
	_, err := config.NewRedisClient(context.Background(), cfg)
	require.Error(t, err)
}

// TestNewRedisClient_UnsupportedScheme verifies NewRedisClient returns an error
// when the URL has an unsupported scheme (not redis://, rediss://, or unix://).
func TestNewRedisClient_UnsupportedScheme(t *testing.T) {
	cfg := &config.Config{RedisURL: "ftp://localhost:6379"}
	_, err := config.NewRedisClient(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ftp")
}
