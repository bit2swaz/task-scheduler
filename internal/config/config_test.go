package config_test

import (
	"os"
	"testing"
	"time"

	"github.com/bit2swaz/task-scheduler/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetEnv wipes all config-related env vars for the duration of the test
// and restores their original values (or absence) via t.Cleanup.
func resetEnv(t *testing.T) {
	t.Helper()

	keys := []string{"PORT", "NODE_ID", "REDIS_URL", "JWT_SECRET", "HASH_RING_REPLICAS"}
	saved := make(map[string]string, len(keys))
	existed := make(map[string]bool, len(keys))

	for _, k := range keys {
		v, ok := os.LookupEnv(k)
		saved[k] = v
		existed[k] = ok
		_ = os.Unsetenv(k)
	}

	t.Cleanup(func() {
		for _, k := range keys {
			if existed[k] {
				_ = os.Setenv(k, saved[k])
			} else {
				_ = os.Unsetenv(k)
			}
		}
	})
}

// TestLoad_MissingNodeID verifies Load returns an error when NODE_ID is absent.
func TestLoad_MissingNodeID(t *testing.T) {
	resetEnv(t)
	t.Setenv("JWT_SECRET", "test-secret")

	_, err := config.Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "NODE_ID")
}

// TestLoad_MissingJWTSecret verifies Load returns an error when JWT_SECRET is absent.
func TestLoad_MissingJWTSecret(t *testing.T) {
	resetEnv(t)
	t.Setenv("NODE_ID", "test-node")

	_, err := config.Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "JWT_SECRET")
}

// TestLoad_Defaults verifies all default values are applied when optional vars are absent.
func TestLoad_Defaults(t *testing.T) {
	resetEnv(t)
	t.Setenv("NODE_ID", "test-node")
	t.Setenv("JWT_SECRET", "test-secret")

	cfg, err := config.Load()

	require.NoError(t, err)
	assert.Equal(t, "3000", cfg.Port)
	assert.Equal(t, "redis://localhost:6379", cfg.RedisURL)
	assert.Equal(t, 150, cfg.HashRingReplicas)
	assert.Equal(t, 10*time.Second, cfg.LeaseTTL)
	assert.Equal(t, 5*time.Second, cfg.RenewInterval)
	assert.Equal(t, 5*time.Second, cfg.PollInterval)
}

// TestLoad_AllFields verifies every env var is read and stored correctly.
func TestLoad_AllFields(t *testing.T) {
	resetEnv(t)
	t.Setenv("PORT", "8080")
	t.Setenv("NODE_ID", "node-99")
	t.Setenv("REDIS_URL", "redis://custom-host:6380")
	t.Setenv("JWT_SECRET", "super-secret")
	t.Setenv("HASH_RING_REPLICAS", "200")

	cfg, err := config.Load()

	require.NoError(t, err)
	assert.Equal(t, "8080", cfg.Port)
	assert.Equal(t, "node-99", cfg.NodeID)
	assert.Equal(t, "redis://custom-host:6380", cfg.RedisURL)
	assert.Equal(t, "super-secret", cfg.JWTSecret)
	assert.Equal(t, 200, cfg.HashRingReplicas)
}

// TestLoad_InvalidHashRingReplicasFallsBack verifies a non-integer HASH_RING_REPLICAS
// silently falls back to the default of 150 rather than crashing.
func TestLoad_InvalidHashRingReplicasFallsBack(t *testing.T) {
	resetEnv(t)
	t.Setenv("NODE_ID", "test-node")
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("HASH_RING_REPLICAS", "not-a-number")

	cfg, err := config.Load()

	require.NoError(t, err)
	assert.Equal(t, 150, cfg.HashRingReplicas)
}
