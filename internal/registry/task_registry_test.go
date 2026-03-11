package registry_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bit2swaz/task-scheduler/internal/registry"
)

func newTestRegistry(t *testing.T) (*registry.RedisRegistry, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return registry.New(rdb), mr
}

func sampleTask() registry.Task {
	return registry.Task{
		ID:       "task-001",
		Name:     "send-report",
		CronExpr: "0 9 * * *",
		Endpoint: "http://example.com/report",
		Payload:  `{"format":"pdf"}`,
		Enabled:  true,
	}
}

// T1: Create then Get roundtrip preserves all fields.
func TestCreate_Get_Roundtrip(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	task := sampleTask()
	require.NoError(t, reg.Create(ctx, task))

	got, err := reg.Get(ctx, task.ID)
	require.NoError(t, err)

	assert.Equal(t, task.ID, got.ID)
	assert.Equal(t, task.Name, got.Name)
	assert.Equal(t, task.CronExpr, got.CronExpr)
	assert.Equal(t, task.Endpoint, got.Endpoint)
	assert.Equal(t, task.Payload, got.Payload)
	assert.Equal(t, task.Enabled, got.Enabled)
	assert.False(t, got.CreatedAt.IsZero(), "CreatedAt should be set")
	assert.False(t, got.UpdatedAt.IsZero(), "UpdatedAt should be set")
}

// T2: Creating a task with a duplicate ID returns ErrTaskAlreadyExists.
func TestCreate_DuplicateID_ReturnsError(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	task := sampleTask()
	require.NoError(t, reg.Create(ctx, task))

	err := reg.Create(ctx, task)
	assert.ErrorIs(t, err, registry.ErrTaskAlreadyExists)
}

// T3: Get on a non-existent ID returns ErrTaskNotFound.
func TestGet_NonExistent_ReturnsError(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	_, err := reg.Get(ctx, "no-such-id")
	assert.ErrorIs(t, err, registry.ErrTaskNotFound)
}

// T4: Update mutates stored fields and advances UpdatedAt.
func TestUpdate_MutatesFields(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	task := sampleTask()
	require.NoError(t, reg.Create(ctx, task))

	original, err := reg.Get(ctx, task.ID)
	require.NoError(t, err)

	// Small sleep to ensure UpdatedAt advances
	time.Sleep(2 * time.Millisecond)

	task.Name = "updated-report"
	task.Enabled = false
	require.NoError(t, reg.Update(ctx, task))

	updated, err := reg.Get(ctx, task.ID)
	require.NoError(t, err)

	assert.Equal(t, "updated-report", updated.Name)
	assert.False(t, updated.Enabled)
	assert.True(t, updated.UpdatedAt.After(original.UpdatedAt),
		"UpdatedAt should advance after update")
}

// T5: Update on a non-existent ID returns ErrTaskNotFound.
func TestUpdate_NonExistent_ReturnsError(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	task := sampleTask()
	err := reg.Update(ctx, task)
	assert.ErrorIs(t, err, registry.ErrTaskNotFound)
}

// T6: Delete removes task from Get and ListAll.
func TestDelete_RemovesTask(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	task := sampleTask()
	require.NoError(t, reg.Create(ctx, task))

	require.NoError(t, reg.Delete(ctx, task.ID))

	_, err := reg.Get(ctx, task.ID)
	assert.ErrorIs(t, err, registry.ErrTaskNotFound)

	tasks, err := reg.ListAll(ctx)
	require.NoError(t, err)
	assert.Empty(t, tasks)
}

// T7: ListAll returns an empty (non-nil) slice when no tasks exist.
func TestListAll_EmptyReturnsEmptySlice(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	tasks, err := reg.ListAll(ctx)
	require.NoError(t, err)
	assert.NotNil(t, tasks)
	assert.Len(t, tasks, 0)
}

// T8: RegisterNode + ListNodes reflects membership;
//
//	DeregisterNode removes the node.
func TestNodeMembership(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	require.NoError(t, reg.RegisterNode(ctx, "node-1"))
	require.NoError(t, reg.RegisterNode(ctx, "node-2"))

	nodes, err := reg.ListNodes(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"node-1", "node-2"}, nodes)

	require.NoError(t, reg.DeregisterNode(ctx, "node-1"))

	nodes, err = reg.ListNodes(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"node-2"}, nodes)
}

// T9: JSON marshal/unmarshal cycle preserves all fields including time zone.
func TestJSONSerializationFidelity(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)

	task := registry.Task{
		ID:        "task-tz",
		Name:      "tz-test",
		CronExpr:  "@daily",
		Endpoint:  "http://example.com",
		Payload:   `{}`,
		Enabled:   true,
		CreatedAt: time.Now().In(loc).Truncate(time.Second),
		UpdatedAt: time.Now().In(loc).Truncate(time.Second),
	}

	// Use JSON directly to verify round-trip fidelity.
	data, err := json.Marshal(task)
	require.NoError(t, err)

	var decoded registry.Task
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.Equal(t, task.ID, decoded.ID)
	assert.Equal(t, task.Name, decoded.Name)
	assert.Equal(t, task.CronExpr, decoded.CronExpr)
	assert.Equal(t, task.Endpoint, decoded.Endpoint)
	assert.Equal(t, task.Payload, decoded.Payload)
	assert.Equal(t, task.Enabled, decoded.Enabled)
	assert.True(t, task.CreatedAt.Equal(decoded.CreatedAt),
		"CreatedAt time should be equal after JSON round-trip")

	// Also verify through registry store/retrieve.
	require.NoError(t, reg.Create(ctx, task))
	stored, err := reg.Get(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, task.ID, stored.ID)
	assert.Equal(t, task.Name, stored.Name)
}
