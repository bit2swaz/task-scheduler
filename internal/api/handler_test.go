package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bit2swaz/task-scheduler/internal/api"
	"github.com/bit2swaz/task-scheduler/internal/executor"
	"github.com/bit2swaz/task-scheduler/internal/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- mocks ----

type mockTaskStore struct {
	tasks     map[string]registry.Task
	createErr error
}

func newMockStore() *mockTaskStore {
	return &mockTaskStore{tasks: make(map[string]registry.Task)}
}

func (m *mockTaskStore) Create(_ context.Context, task registry.Task) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.tasks[task.ID] = task
	return nil
}

func (m *mockTaskStore) Get(_ context.Context, id string) (*registry.Task, error) {
	t, ok := m.tasks[id]
	if !ok {
		return nil, registry.ErrTaskNotFound
	}
	return &t, nil
}

func (m *mockTaskStore) Update(_ context.Context, task registry.Task) error {
	if _, ok := m.tasks[task.ID]; !ok {
		return registry.ErrTaskNotFound
	}
	m.tasks[task.ID] = task
	return nil
}

func (m *mockTaskStore) Delete(_ context.Context, id string) error {
	delete(m.tasks, id)
	return nil
}

func (m *mockTaskStore) ListAll(_ context.Context) ([]registry.Task, error) {
	out := make([]registry.Task, 0, len(m.tasks))
	for _, t := range m.tasks {
		out = append(out, t)
	}
	return out, nil
}

type mockNodeAssigner struct{ node string }

func (m *mockNodeAssigner) Get(_ string) string { return m.node }

type mockTaskRunner struct {
	executeCalled bool
	logs          []executor.ExecutionLog
}

func (m *mockTaskRunner) Execute(_ context.Context, _ registry.Task) error {
	m.executeCalled = true
	return nil
}

func (m *mockTaskRunner) GetLogs(_ context.Context, _ string, _ int) ([]executor.ExecutionLog, error) {
	return m.logs, nil
}

type mockNodeLister struct{ nodes []string }

func (m *mockNodeLister) ListNodes(_ context.Context) ([]string, error) {
	return m.nodes, nil
}

type mockLeaderChecker struct {
	leader   string
	isLeader bool
}

func (m *mockLeaderChecker) IsLeader() bool { return m.isLeader }

func (m *mockLeaderChecker) CurrentLeader(_ context.Context) (string, error) {
	return m.leader, nil
}

type mockScheduleCounter struct{ count int }

func (m *mockScheduleCounter) ScheduledCount() int { return m.count }

// ---- helpers ----

func newTestHandler() (*api.Handler, *mockTaskStore, *mockTaskRunner) {
	store := newMockStore()
	runner := &mockTaskRunner{}
	h := api.NewHandler(api.Config{
		Tasks:     store,
		Ring:      &mockNodeAssigner{node: "node-1"},
		Exec:      runner,
		Nodes:     &mockNodeLister{nodes: []string{"node-1", "node-2"}},
		Election:  &mockLeaderChecker{leader: "node-1", isLeader: true},
		Sched:     &mockScheduleCounter{count: 2},
		JWTSecret: testSecret,
		NodeID:    "node-1",
	})
	return h, store, runner
}

func doRequest(t *testing.T, h *api.Handler, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		req = httptest.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	// include a valid bearer token so protected routes pass the JWT middleware
	req.Header.Set("Authorization", "Bearer "+makeToken("node-1", testSecret, time.Now().Add(time.Hour)))
	rr := httptest.NewRecorder()
	api.NewRouter(h).ServeHTTP(rr, req)
	return rr
}

// T1: POST /tasks 201 - valid payload, response has id.
func TestCreateTask_Valid(t *testing.T) {
	h, store, _ := newTestHandler()
	rr := doRequest(t, h, http.MethodPost, "/tasks", map[string]interface{}{
		"name":      "my-task",
		"cron_expr": "* * * * *",
		"endpoint":  "http://example.com",
		"enabled":   true,
	})
	require.Equal(t, http.StatusCreated, rr.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	id, ok := resp["id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, id)
	_, err := store.Get(context.Background(), id)
	require.NoError(t, err)
}

// T2: POST /tasks 400 - missing name.
func TestCreateTask_MissingName(t *testing.T) {
	h, _, _ := newTestHandler()
	rr := doRequest(t, h, http.MethodPost, "/tasks", map[string]interface{}{
		"cron_expr": "* * * * *",
		"endpoint":  "http://example.com",
	})
	require.Equal(t, http.StatusBadRequest, rr.Code)
	var resp map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "INVALID_REQUEST", resp["code"])
}

// T3: POST /tasks 400 - invalid cron_expr.
func TestCreateTask_InvalidCron(t *testing.T) {
	h, _, _ := newTestHandler()
	rr := doRequest(t, h, http.MethodPost, "/tasks", map[string]interface{}{
		"name":      "bad-cron-task",
		"cron_expr": "not-a-cron",
		"endpoint":  "http://example.com",
	})
	require.Equal(t, http.StatusBadRequest, rr.Code)
	var resp map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "INVALID_REQUEST", resp["code"])
}

// T4: POST /tasks 409 - store returns ErrTaskAlreadyExists.
func TestCreateTask_Conflict(t *testing.T) {
	h, store, _ := newTestHandler()
	store.createErr = registry.ErrTaskAlreadyExists
	rr := doRequest(t, h, http.MethodPost, "/tasks", map[string]interface{}{
		"name":      "dup-task",
		"cron_expr": "* * * * *",
		"endpoint":  "http://example.com",
	})
	require.Equal(t, http.StatusConflict, rr.Code)
	var resp map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "CONFLICT", resp["code"])
}

// T5: GET /tasks 200 - returns list with assigned_node populated.
func TestListTasks(t *testing.T) {
	h, store, _ := newTestHandler()
	store.tasks["abc"] = registry.Task{
		ID: "abc", Name: "t1", CronExpr: "* * * * *", Endpoint: "http://x.com", Enabled: true,
	}
	rr := doRequest(t, h, http.MethodGet, "/tasks", nil)
	require.Equal(t, http.StatusOK, rr.Code)
	var tasks []map[string]interface{}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&tasks))
	require.Len(t, tasks, 1)
	assert.Equal(t, "node-1", tasks[0]["assigned_node"])
}

// T6: GET /tasks/:id 200 - returns correct task.
func TestGetTask_Found(t *testing.T) {
	h, store, _ := newTestHandler()
	store.tasks["xyz"] = registry.Task{ID: "xyz", Name: "found-task", CronExpr: "* * * * *", Endpoint: "http://x.com"}
	rr := doRequest(t, h, http.MethodGet, "/tasks/xyz", nil)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "xyz", resp["id"])
	assert.Equal(t, "found-task", resp["name"])
}

// T7: GET /tasks/:id 404 - non-existent ID.
func TestGetTask_NotFound(t *testing.T) {
	h, _, _ := newTestHandler()
	rr := doRequest(t, h, http.MethodGet, "/tasks/nope", nil)
	require.Equal(t, http.StatusNotFound, rr.Code)
	var resp map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "NOT_FOUND", resp["code"])
}

// T8: PUT /tasks/:id 200 - update fields, updated_at advances.
func TestUpdateTask_Valid(t *testing.T) {
	h, store, _ := newTestHandler()
	original := registry.Task{
		ID:        "u1",
		Name:      "old-name",
		CronExpr:  "* * * * *",
		Endpoint:  "http://x.com",
		UpdatedAt: time.Now().Add(-time.Minute),
	}
	store.tasks["u1"] = original
	rr := doRequest(t, h, http.MethodPut, "/tasks/u1", map[string]interface{}{
		"name": "new-name",
	})
	require.Equal(t, http.StatusOK, rr.Code)
	updated := store.tasks["u1"]
	assert.Equal(t, "new-name", updated.Name)
	assert.True(t, updated.UpdatedAt.After(original.UpdatedAt))
}

// T9: DELETE /tasks/:id 204 - task removed.
func TestDeleteTask(t *testing.T) {
	h, store, _ := newTestHandler()
	store.tasks["del1"] = registry.Task{ID: "del1", Name: "to-delete", CronExpr: "* * * * *", Endpoint: "http://x.com"}
	rr := doRequest(t, h, http.MethodDelete, "/tasks/del1", nil)
	require.Equal(t, http.StatusNoContent, rr.Code)
	_, ok := store.tasks["del1"]
	assert.False(t, ok)
}

// T10: POST /tasks/:id/run 202 - executor called, returns exec_id.
func TestRunTask(t *testing.T) {
	h, store, runner := newTestHandler()
	store.tasks["r1"] = registry.Task{ID: "r1", Name: "runnable", CronExpr: "* * * * *", Endpoint: "http://x.com"}
	rr := doRequest(t, h, http.MethodPost, "/tasks/r1/run", nil)
	require.Equal(t, http.StatusAccepted, rr.Code)
	assert.True(t, runner.executeCalled)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.NotEmpty(t, resp["exec_id"])
}

// T11: GET /tasks/:id/logs 200 - returns log array.
func TestTaskLogs(t *testing.T) {
	h, store, runner := newTestHandler()
	store.tasks["l1"] = registry.Task{ID: "l1", Name: "logged", CronExpr: "* * * * *", Endpoint: "http://x.com"}
	runner.logs = []executor.ExecutionLog{
		{TaskID: "l1", ExecID: "e1", Status: "success", StatusCode: 200},
	}
	rr := doRequest(t, h, http.MethodGet, "/tasks/l1/logs", nil)
	require.Equal(t, http.StatusOK, rr.Code)
	var logs []map[string]interface{}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&logs))
	require.Len(t, logs, 1)
	assert.Equal(t, "success", logs[0]["status"])
}

// T12: GET /nodes 200 - returns node list and current leader.
func TestListNodes(t *testing.T) {
	h, _, _ := newTestHandler()
	rr := doRequest(t, h, http.MethodGet, "/nodes", nil)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	nodes, ok := resp["nodes"].([]interface{})
	require.True(t, ok)
	assert.Len(t, nodes, 2)
	assert.Equal(t, "node-1", resp["leader"])
}

// T13: GET /health 200 - returns node health fields.
func TestHealth(t *testing.T) {
	h, _, _ := newTestHandler()
	rr := doRequest(t, h, http.MethodGet, "/health", nil)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "node-1", resp["node_id"])
	assert.Equal(t, true, resp["is_leader"])
	assert.Equal(t, float64(2), resp["tasks_assigned"])
	_, ok := resp["uptime_seconds"]
	assert.True(t, ok)
}
