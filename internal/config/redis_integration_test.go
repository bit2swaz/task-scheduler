//go:build integration

package config_test

import (
	"context"
	"os"
	"testing"

	"github.com/bit2swaz/task-scheduler/internal/config"
	"github.com/stretchr/testify/require"
)

// TestNewRedisClient_Ping_Integration verifies NewRedisClient can connect to a
// real Redis instance and respond to a ping. Requires REDIS_URL in env or a
// local Redis on localhost:6379.
func TestNewRedisClient_Ping_Integration(t *testing.T) {
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379"
	}

	cfg := &config.Config{RedisURL: redisURL}
	client, err := config.NewRedisClient(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, client)
	_ = client.Close()
}
