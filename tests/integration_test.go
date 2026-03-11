//go:build integration

// Package tests contains integration and cross-package tests for the
// distributed task scheduler. Build with -tags integration to run.
package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bit2swaz/task-scheduler/internal/api"
	"github.com/bit2swaz/task-scheduler/internal/election"
	"github.com/bit2swaz/task-scheduler/internal/executor"
	"github.com/bit2swaz/task-scheduler/internal/hashring"
	"github.com/bit2swaz/task-scheduler/internal/registry"
	"github.com/bit2swaz/task-scheduler/internal/scheduler"
)

// ---- test-only stubs -------------------------------------------------------

// alwaysLeader is a stub that reports this node as the current leader.
type alwaysLeader struct{ nodeID string }

func (a *alwaysLeader) IsLeader() bool                                  { return true }
func (a *alwaysLeader) CurrentLeader(_ context.Context) (string, error) { return a.nodeID, nil }

// ---- shared test environment -----------------------------------------------

const (
	testSecret = "integration-test-secret"
	testNodeID = "test-node"
)

// testEnv holds a fully wired subsystem stack for API integration tests.
type testEnv struct {
	mr     *miniredis.Miniredis
	rdb    *redis.Client
	reg    *registry.RedisRegistry
	ring   *hashring.Ring
	exec   *executor.HTTPExecutor
	sched  *scheduler.Scheduler
	router http.Handler
	secret string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	reg := registry.New(rdb)
	ring := hashring.New(50)
	ring.Add(testNodeID)
	exec := executor.New(rdb, testNodeID, executor.Options{})
	sched := scheduler.New(reg, exec, ring, testNodeID)

	h := api.NewHandler(api.Config{
		Tasks:     reg,
		Ring:      ring,
		Exec:      exec,
		Nodes:     reg,
		Election:  &alwaysLeader{nodeID: testNodeID},
		Sched:     sched,
		JWTSecret: testSecret,
		NodeID:    testNodeID,
	})

	t.Cleanup(sched.Stop)

	return &testEnv{
		mr:     mr,
		rdb:    rdb,
		reg:    reg,
		ring:   ring,
		exec:   exec,
		sched:  sched,
		router: api.NewRouter(h),
		secret: testSecret,
	}
}

// do fires an HTTP request against the test router and returns the recorder.
func (e *testEnv) do(method, path string, body []byte, token string) *httptest.ResponseRecorder {
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

// token issues a valid JWT for testNodeID via the /auth/token endpoint.
func (e *testEnv) token(t *testing.T) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"node_id": testNodeID, "secret": e.secret})
	w := e.do(http.MethodPost, "/auth/token", body, "")
	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	return resp["token"]
}

// createTask creates one task via POST /tasks and returns its assigned ID.
func (e *testEnv) createTask(t *testing.T, token, endpoint string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{
		"name":      "it-task",
		"cron_expr": "* * * * *",
		"endpoint":  endpoint,
		"payload":   "",
		"enabled":   true,
	})
	w := e.do(http.MethodPost, "/tasks", body, token)
	require.Equal(t, http.StatusCreated, w.Code, "create task response: %s", w.Body.String())
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	return resp["id"].(string)
}

// makeClient creates a new go-redis client pointing at the given miniredis.
func makeClient(mr *miniredis.Miniredis) *redis.Client {
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

// expiredToken returns a JWT whose exp claim is in the past.
func expiredToken(secret string) string {
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"node_id": testNodeID,
		"exp":     time.Now().Add(-time.Hour).Unix(),
		"iat":     time.Now().Add(-2 * time.Hour).Unix(),
	})
	signed, _ := tok.SignedString([]byte(secret))
	return signed
}

// ---- IT-01: leader uniqueness -----------------------------------------------

func TestIT01_LeaderUniqueness(t *testing.T) {
	mr := miniredis.RunT(t)
	probe := makeClient(mr)

	opts := election.ElectorOptions{
		LeaseTTL:     300 * time.Millisecond,
		PollInterval: 50 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	e1 := election.New(makeClient(mr), "node-1", opts)
	e2 := election.New(makeClient(mr), "node-2", opts)
	e3 := election.New(makeClient(mr), "node-3", opts)

	go e1.Run(ctx)
	go e2.Run(ctx)
	go e3.Run(ctx)

	// A leader must emerge within 500ms.
	require.Eventually(t, func() bool {
		v, _ := probe.Get(context.Background(), "scheduler:leader").Result()
		return v != ""
	}, 500*time.Millisecond, 10*time.Millisecond, "no leader elected within 500ms")

	// Poll for 1s: the key should always hold a valid node ID.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		v, err := probe.Get(context.Background(), "scheduler:leader").Result()
		if err == nil {
			assert.Contains(t, []string{"node-1", "node-2", "node-3"}, v,
				"leader key must be a valid node ID")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ---- IT-02: failover time ---------------------------------------------------

func TestIT02_Failover(t *testing.T) {
	mr := miniredis.RunT(t)
	probe := makeClient(mr)

	leaseTTL := 300 * time.Millisecond
	pollInterval := 50 * time.Millisecond

	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()

	makeOpts := func() election.ElectorOptions {
		return election.ElectorOptions{LeaseTTL: leaseTTL, PollInterval: pollInterval}
	}

	// Start node-1 and wait for it to win the election.
	e1 := election.New(makeClient(mr), "node-1", makeOpts())
	go e1.Run(ctxA)
	require.Eventually(t, func() bool {
		v, _ := probe.Get(context.Background(), "scheduler:leader").Result()
		return v == "node-1"
	}, 500*time.Millisecond, 10*time.Millisecond, "node-1 should win first election")

	// Start node-2, then crash node-1 by cancelling its context.
	e2 := election.New(makeClient(mr), "node-2", makeOpts())
	go e2.Run(ctxB)
	cancelA()
	// Advance miniredis clock past leaseTTL so the leader key expires immediately,
	// making the failover window deterministic regardless of wall-clock timing.
	mr.FastForward(leaseTTL + time.Millisecond)

	start := time.Now()
	maxFailover := 5*pollInterval + 100*time.Millisecond

	require.Eventually(t, func() bool {
		v, _ := probe.Get(context.Background(), "scheduler:leader").Result()
		return v == "node-2"
	}, maxFailover, 20*time.Millisecond, "node-2 should take over after node-1 crashes")

	t.Logf("failover in %v (limit %v)", time.Since(start).Round(time.Millisecond), maxFailover)
}

// ---- IT-03: ring rebalance --------------------------------------------------

func TestIT03_RingRebalance(t *testing.T) {
	ring := hashring.New(50)
	ring.Add("node-1")
	ring.Add("node-2")
	ring.Add("node-3")

	const numTasks = 30
	taskIDs := make([]string, numTasks)
	initial := make(map[string]string, numTasks)
	for i := range taskIDs {
		id := fmt.Sprintf("task-%03d", i)
		taskIDs[i] = id
		initial[id] = ring.Get(id)
	}

	// Adding node-4 should move roughly 25%% of tasks to it.
	ring.Add("node-4")

	reassigned := 0
	for _, id := range taskIDs {
		after := ring.Get(id)
		if after != initial[id] {
			reassigned++
			assert.Equal(t, "node-4", after, "task %s: moved to unexpected node", id)
		}
	}

	assert.Greater(t, reassigned, 0, "some tasks should move to node-4")
	assert.Less(t, reassigned, numTasks, "not all tasks should move to node-4")
	t.Logf("%d/%d tasks reassigned to node-4", reassigned, numTasks)
}

// ---- IT-04: create, schedule, execute lifecycle ----------------------------

func TestIT04_CreateScheduleExecute(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := makeClient(mr)
	ctx := context.Background()

	// Endpoint server that the executor will POST to.
	called := make(chan struct{}, 1)
	taskSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case called <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer taskSrv.Close()

	reg := registry.New(rdb)
	ring := hashring.New(50)
	ring.Add("node-1")
	exec := executor.New(rdb, "node-1", executor.Options{})
	sched := scheduler.New(reg, exec, ring, "node-1")
	t.Cleanup(sched.Stop)

	task := registry.Task{
		ID:       "task-it04",
		Name:     "lifecycle-task",
		CronExpr: "* * * * *",
		Endpoint: taskSrv.URL,
		Enabled:  true,
	}
	require.NoError(t, reg.Create(ctx, task))

	got, err := reg.Get(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, "lifecycle-task", got.Name)

	require.NoError(t, sched.LoadAndSchedule(ctx))
	assert.Equal(t, 1, sched.ScheduledCount(), "one task should be loaded into scheduler")

	// Execute immediately rather than waiting for the cron tick.
	require.NoError(t, exec.Execute(ctx, *got))

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("task endpoint was not called within 2s")
	}

	logs, err := exec.GetLogs(ctx, task.ID, 10)
	require.NoError(t, err)
	require.NotEmpty(t, logs, "execution log should exist after Execute")
	assert.Equal(t, "success", logs[0].Status)
}

// ---- IT-05: manual trigger via API -----------------------------------------

func TestIT05_ManualTrigger(t *testing.T) {
	env := newTestEnv(t)
	tok := env.token(t)

	called := make(chan struct{}, 1)
	taskSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case called <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer taskSrv.Close()

	taskID := env.createTask(t, tok, taskSrv.URL)

	w := env.do(http.MethodPost, "/tasks/"+taskID+"/run", nil, tok)
	assert.Equal(t, http.StatusAccepted, w.Code)

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("task endpoint not reached after POST /tasks/:id/run")
	}
}

// ---- IT-06: execution log after run ----------------------------------------

func TestIT06_ExecutionLogAfterRun(t *testing.T) {
	env := newTestEnv(t)
	tok := env.token(t)

	taskSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer taskSrv.Close()

	taskID := env.createTask(t, tok, taskSrv.URL)

	w := env.do(http.MethodPost, "/tasks/"+taskID+"/run", nil, tok)
	require.Equal(t, http.StatusAccepted, w.Code)

	// Execute is synchronous; log is written before handler returns.
	// A short sleep guards against any in-process scheduling latency.
	time.Sleep(50 * time.Millisecond)

	w = env.do(http.MethodGet, "/tasks/"+taskID+"/logs", nil, tok)
	assert.Equal(t, http.StatusOK, w.Code)

	var logs []map[string]interface{}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&logs))
	require.NotEmpty(t, logs, "at least one log entry expected")
	assert.Equal(t, "success", logs[0]["status"])
	assert.Equal(t, taskID, logs[0]["task_id"])
}

// ---- IT-07: disabled task not scheduled ------------------------------------

func TestIT07_DisabledTaskNotScheduled(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := makeClient(mr)
	ctx := context.Background()

	reg := registry.New(rdb)
	ring := hashring.New(50)
	ring.Add("node-1")
	exec := executor.New(rdb, "node-1", executor.Options{})
	sched := scheduler.New(reg, exec, ring, "node-1")
	t.Cleanup(sched.Stop)

	require.NoError(t, reg.Create(ctx, registry.Task{
		ID: "task-disabled", Name: "disabled", CronExpr: "* * * * *",
		Endpoint: "http://localhost:19999/unreachable", Enabled: false,
	}))
	require.NoError(t, reg.Create(ctx, registry.Task{
		ID: "task-enabled", Name: "enabled", CronExpr: "* * * * *",
		Endpoint: "http://localhost:19999/unreachable", Enabled: true,
	}))

	require.NoError(t, sched.LoadAndSchedule(ctx))
	assert.Equal(t, 1, sched.ScheduledCount(), "only the enabled task should be scheduled")
}

// ---- IT-08: delete task removes it from scheduler --------------------------

func TestIT08_DeleteTaskUnscheduled(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := makeClient(mr)
	ctx := context.Background()

	reg := registry.New(rdb)
	ring := hashring.New(50)
	ring.Add("node-1")
	exec := executor.New(rdb, "node-1", executor.Options{})
	sched := scheduler.New(reg, exec, ring, "node-1")
	t.Cleanup(sched.Stop)

	task := registry.Task{
		ID: "task-del", Name: "to-be-deleted", CronExpr: "* * * * *",
		Endpoint: "http://localhost:19999/unreachable", Enabled: true,
	}
	require.NoError(t, reg.Create(ctx, task))
	require.NoError(t, sched.LoadAndSchedule(ctx))
	assert.Equal(t, 1, sched.ScheduledCount())

	require.NoError(t, reg.Delete(ctx, task.ID))
	require.NoError(t, sched.RemoveTask(task.ID))

	assert.Equal(t, 0, sched.ScheduledCount(), "scheduler should be empty after delete")
}

// ---- IT-09: token lifecycle -------------------------------------------------

func TestIT09_TokenLifecycle(t *testing.T) {
	env := newTestEnv(t)
	tok := env.token(t)

	// Valid token accesses protected route.
	w := env.do(http.MethodGet, "/tasks", nil, tok)
	assert.Equal(t, http.StatusOK, w.Code, "valid token should reach protected route")

	// Expired token is rejected.
	w = env.do(http.MethodGet, "/tasks", nil, expiredToken(env.secret))
	assert.Equal(t, http.StatusUnauthorized, w.Code, "expired token should be rejected")
}

// ---- IT-10: unauthorized access --------------------------------------------

func TestIT10_UnauthorizedAccess(t *testing.T) {
	env := newTestEnv(t)

	// No token.
	w := env.do(http.MethodGet, "/tasks", nil, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code, "missing token should be rejected")

	// Malformed token string.
	w = env.do(http.MethodGet, "/tasks", nil, "not-a-jwt")
	assert.Equal(t, http.StatusUnauthorized, w.Code, "garbage token should be rejected")

	// Valid JWT but signed with the wrong secret.
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"node_id": testNodeID,
		"exp":     time.Now().Add(time.Hour).Unix(),
		"iat":     time.Now().Unix(),
	})
	wrongSig, _ := tok.SignedString([]byte("wrong-secret"))
	w = env.do(http.MethodGet, "/tasks", nil, wrongSig)
	assert.Equal(t, http.StatusUnauthorized, w.Code, "wrong-secret token should be rejected")
}
