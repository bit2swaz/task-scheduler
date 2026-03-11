package scheduler_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bit2swaz/task-scheduler/internal/hashring"
	"github.com/bit2swaz/task-scheduler/internal/registry"
	"github.com/bit2swaz/task-scheduler/internal/scheduler"
)

// mockExecutor captures Execute calls for test assertions.
type mockExecutor struct {
	mu    sync.Mutex
	calls []registry.Task
}

func (m *mockExecutor) Execute(_ context.Context, task registry.Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, task)
	return nil
}

func (m *mockExecutor) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockExecutor) callsFor(taskID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, t := range m.calls {
		if t.ID == taskID {
			n++
		}
	}
	return n
}

func (m *mockExecutor) calledWith(taskID string) bool {
	return m.callsFor(taskID) > 0
}

func (m *mockExecutor) firstCall() (registry.Task, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return registry.Task{}, false
	}
	return m.calls[0], true
}

// newSchedRegistry creates a miniredis-backed registry for scheduler tests.
func newSchedRegistry(t *testing.T) *registry.RedisRegistry {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return registry.New(rdb)
}

func newRing(nodes ...string) *hashring.Ring {
	r := hashring.New(150)
	for _, n := range nodes {
		r.Add(n)
	}
	return r
}

// shortTask returns a task that fires every second for test execution.
// robfig/cron clamps sub-second @every durations to 1s, so 1s is the minimum.
func shortTask(id string) registry.Task {
	return registry.Task{
		ID:       id,
		Name:     "task-" + id,
		CronExpr: "@every 1s",
		Endpoint: "http://example.com/" + id,
		Enabled:  true,
	}
}

// waitFor polls fn every 10ms until it returns true or timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, fn func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// T1: LoadAndSchedule schedules nothing when no task maps to this node.
func TestLoadAndSchedule_SkipsTasksOnOtherNodes(t *testing.T) {
	ctx := context.Background()
	reg := newSchedRegistry(t)
	exec := &mockExecutor{}
	// All tasks will hash to "node-B"; scheduler is "node-A".
	ring := newRing("node-B")

	for _, id := range []string{"t1", "t2", "t3"} {
		require.NoError(t, reg.Create(ctx, shortTask(id)))
	}

	sched := scheduler.New(reg, exec, ring, "node-A")
	defer sched.Stop()

	require.NoError(t, sched.LoadAndSchedule(ctx))

	assert.Equal(t, 0, sched.ScheduledCount())
	time.Sleep(150 * time.Millisecond)
	assert.Equal(t, 0, exec.count())
}

// T2: LoadAndSchedule schedules exactly the tasks assigned to this node.
func TestLoadAndSchedule_CorrectNodeFiltering(t *testing.T) {
	ctx := context.Background()
	reg := newSchedRegistry(t)
	exec := &mockExecutor{}
	ring := newRing("node-A", "node-B")

	ids := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
	var ownedIDs, otherIDs []string
	for _, id := range ids {
		require.NoError(t, reg.Create(ctx, shortTask(id)))
		if ring.Get(id) == "node-A" {
			ownedIDs = append(ownedIDs, id)
		} else {
			otherIDs = append(otherIDs, id)
		}
	}

	// Ensure non-degenerate ring distribution.
	if len(ownedIDs) == 0 || len(otherIDs) == 0 {
		t.Skip("degenerate ring assignment for chosen task IDs; adjust IDs")
	}

	sched := scheduler.New(reg, exec, ring, "node-A")
	defer sched.Stop()
	require.NoError(t, sched.LoadAndSchedule(ctx))

	assert.Equal(t, len(ownedIDs), sched.ScheduledCount())

	// Wait for all owned tasks to fire at least once.
	ok := waitFor(t, 3*time.Second, func() bool {
		for _, id := range ownedIDs {
			if !exec.calledWith(id) {
				return false
			}
		}
		return true
	})
	assert.True(t, ok, "all owned tasks should have executed")

	// Tasks not owned by this node must not have fired.
	for _, id := range otherIDs {
		assert.False(t, exec.calledWith(id), "task %s must not run on node-A", id)
	}
}

// T3: AddTask dynamically schedules a task without a full registry reload.
func TestAddTask_DynamicWithoutReload(t *testing.T) {
	ctx := context.Background()
	reg := newSchedRegistry(t)
	exec := &mockExecutor{}
	ring := newRing("node-A")

	sched := scheduler.New(reg, exec, ring, "node-A")
	defer sched.Stop()

	// Start with empty registry.
	require.NoError(t, sched.LoadAndSchedule(ctx))
	assert.Equal(t, 0, sched.ScheduledCount())

	// Dynamically add a task.
	require.NoError(t, sched.AddTask(ctx, shortTask("dyn")))
	assert.Equal(t, 1, sched.ScheduledCount())

	ok := waitFor(t, 3*time.Second, func() bool {
		return exec.calledWith("dyn")
	})
	assert.True(t, ok, "dynamically added task should execute")
}

// T4: RemoveTask stops future executions; other tasks remain unaffected.
func TestRemoveTask_StopsFutureExecutions(t *testing.T) {
	ctx := context.Background()
	reg := newSchedRegistry(t)
	exec := &mockExecutor{}
	ring := newRing("node-A")

	require.NoError(t, reg.Create(ctx, shortTask("a")))
	require.NoError(t, reg.Create(ctx, shortTask("b")))

	sched := scheduler.New(reg, exec, ring, "node-A")
	defer sched.Stop()
	require.NoError(t, sched.LoadAndSchedule(ctx))
	assert.Equal(t, 2, sched.ScheduledCount())

	// Wait for both tasks to fire at least once.
	ok := waitFor(t, 3*time.Second, func() bool {
		return exec.calledWith("a") && exec.calledWith("b")
	})
	require.True(t, ok, "both tasks must fire before removal test")

	// Remove task-a.
	require.NoError(t, sched.RemoveTask("a"))
	assert.Equal(t, 1, sched.ScheduledCount())

	// Settle any in-flight "a" execution.
	time.Sleep(80 * time.Millisecond)
	aCount1 := exec.callsFor("a")

	// Verify no new "a" calls over the next interval.
	time.Sleep(150 * time.Millisecond)
	assert.Equal(t, aCount1, exec.callsFor("a"), "task-a must not fire after RemoveTask")

	// task-b must keep running.
	bCount := exec.callsFor("b")
	assert.Greater(t, bCount, 0, "task-b should still be running")
}

// T5: Executor is called with the exact task fields from the registry.
func TestExecutorCalledWithCorrectTask(t *testing.T) {
	ctx := context.Background()
	reg := newSchedRegistry(t)
	exec := &mockExecutor{}
	ring := newRing("node-A")

	task := registry.Task{
		ID:       "precise",
		Name:     "precise-task",
		CronExpr: "@every 1s",
		Endpoint: "http://svc.example.com/run",
		Payload:  `{"env":"test"}`,
		Enabled:  true,
	}
	require.NoError(t, reg.Create(ctx, task))

	sched := scheduler.New(reg, exec, ring, "node-A")
	defer sched.Stop()
	require.NoError(t, sched.LoadAndSchedule(ctx))

	ok := waitFor(t, 3*time.Second, func() bool { return exec.count() > 0 })
	require.True(t, ok, "executor should have been called")

	called, ok := exec.firstCall()
	require.True(t, ok)
	assert.Equal(t, task.ID, called.ID)
	assert.Equal(t, task.Name, called.Name)
	assert.Equal(t, task.Endpoint, called.Endpoint)
	assert.Equal(t, task.Payload, called.Payload)
}

// T6: Stop halts all executions; no new calls after Stop returns.
func TestStop_HaltsAllExecutions(t *testing.T) {
	ctx := context.Background()
	reg := newSchedRegistry(t)
	exec := &mockExecutor{}
	ring := newRing("node-A")

	require.NoError(t, reg.Create(ctx, shortTask("s")))

	sched := scheduler.New(reg, exec, ring, "node-A")
	// Do NOT defer Stop here — we call it manually to test its effect.
	require.NoError(t, sched.LoadAndSchedule(ctx))

	// Wait for at least two fires.
	ok := waitFor(t, 5*time.Second, func() bool { return exec.count() >= 2 })
	require.True(t, ok, "task should fire at least twice before stop")

	sched.Stop() // blocks until all running jobs finish
	countAtStop := exec.count()

	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, countAtStop, exec.count(), "no new calls after Stop")
}

// T7: AddTask with an invalid cron expression returns an error and adds nothing.
func TestAddTask_InvalidCronExpr_ReturnsError(t *testing.T) {
	ctx := context.Background()
	reg := newSchedRegistry(t)
	exec := &mockExecutor{}
	ring := newRing("node-A")

	sched := scheduler.New(reg, exec, ring, "node-A")
	defer sched.Stop()
	require.NoError(t, sched.LoadAndSchedule(ctx))

	err := sched.AddTask(ctx, registry.Task{
		ID:       "bad",
		Name:     "bad-task",
		CronExpr: "not a valid cron expr",
		Enabled:  true,
	})
	assert.Error(t, err)
	assert.Equal(t, 0, sched.ScheduledCount())
}

// T8: Calling LoadAndSchedule again after a ring change re-evaluates assignments.
func TestLoadAndSchedule_ReschedulesAfterRingChange(t *testing.T) {
	ctx := context.Background()
	reg := newSchedRegistry(t)
	exec := &mockExecutor{}

	// Ring has only "node-B"; scheduler is "node-A" → nothing scheduled.
	ring := newRing("node-B")

	for _, id := range []string{"r1", "r2", "r3"} {
		require.NoError(t, reg.Create(ctx, shortTask(id)))
	}

	sched := scheduler.New(reg, exec, ring, "node-A")
	defer sched.Stop()
	require.NoError(t, sched.LoadAndSchedule(ctx))
	assert.Equal(t, 0, sched.ScheduledCount())

	// Ring change: remove "node-B", add "node-A".
	ring.Remove("node-B")
	ring.Add("node-A")

	// Re-evaluate — all 3 tasks should now belong to "node-A".
	require.NoError(t, sched.LoadAndSchedule(ctx))
	assert.Equal(t, 3, sched.ScheduledCount())

	ok := waitFor(t, 3*time.Second, func() bool { return exec.count() >= 3 })
	assert.True(t, ok, "all tasks should execute after ring change")
}
