package executor_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/bit2swaz/task-scheduler/internal/executor"
	"github.com/bit2swaz/task-scheduler/internal/registry"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newRDB(t *testing.T, mr *miniredis.Miniredis) *redis.Client {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	return rdb
}

func makeTask(endpoint string) registry.Task {
	return registry.Task{
		ID:       "task-1",
		Name:     "test task",
		CronExpr: "* * * * *",
		Endpoint: endpoint,
	}
}

func fetchLog(t *testing.T, rdb *redis.Client, key string) executor.ExecutionLog {
	t.Helper()
	raw, err := rdb.Get(context.Background(), key).Result()
	require.NoError(t, err)
	var el executor.ExecutionLog
	require.NoError(t, json.Unmarshal([]byte(raw), &el))
	return el
}

// T1: 200 response -> status "success", duration > 0.
func TestExecute_Success(t *testing.T) {
	mr := miniredis.RunT(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rdb := newRDB(t, mr)
	ex := executor.New(rdb, "node-1", executor.Options{})

	err := ex.Execute(context.Background(), makeTask(srv.URL))
	require.NoError(t, err)

	keys, err := rdb.Keys(context.Background(), "scheduler:exec:*").Result()
	require.NoError(t, err)
	require.Len(t, keys, 1)

	el := fetchLog(t, rdb, keys[0])
	assert.Equal(t, "success", el.Status)
	assert.Equal(t, http.StatusOK, el.StatusCode)
	assert.Greater(t, el.Duration, int64(0))
}

// T2: 4xx -> status "failed", StatusCode captured.
func TestExecute_4xxFailure(t *testing.T) {
	mr := miniredis.RunT(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	rdb := newRDB(t, mr)
	ex := executor.New(rdb, "node-1", executor.Options{})

	err := ex.Execute(context.Background(), makeTask(srv.URL))
	require.Error(t, err)

	keys, _ := rdb.Keys(context.Background(), "scheduler:exec:*").Result()
	require.Len(t, keys, 1)
	el := fetchLog(t, rdb, keys[0])
	assert.Equal(t, "failed", el.Status)
	assert.Equal(t, http.StatusNotFound, el.StatusCode)
}

// T3: 5xx -> status "failed".
func TestExecute_5xxFailure(t *testing.T) {
	mr := miniredis.RunT(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	rdb := newRDB(t, mr)
	ex := executor.New(rdb, "node-1", executor.Options{})

	err := ex.Execute(context.Background(), makeTask(srv.URL))
	require.Error(t, err)

	keys, _ := rdb.Keys(context.Background(), "scheduler:exec:*").Result()
	require.Len(t, keys, 1)
	el := fetchLog(t, rdb, keys[0])
	assert.Equal(t, "failed", el.Status)
	assert.Equal(t, http.StatusInternalServerError, el.StatusCode)
}

// T4: connection closed by server -> status "failed", Error field non-empty.
func TestExecute_NetworkError(t *testing.T) {
	mr := miniredis.RunT(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		conn, _, _ := hj.Hijack()
		conn.Close()
	}))
	defer srv.Close()

	rdb := newRDB(t, mr)
	ex := executor.New(rdb, "node-1", executor.Options{})

	err := ex.Execute(context.Background(), makeTask(srv.URL))
	require.Error(t, err)

	keys, _ := rdb.Keys(context.Background(), "scheduler:exec:*").Result()
	require.Len(t, keys, 1)
	el := fetchLog(t, rdb, keys[0])
	assert.Equal(t, "failed", el.Status)
	assert.NotEmpty(t, el.Error)
}

// T5: mock sleeps longer than executor timeout -> returns error, elapsed < 3s.
func TestExecute_Timeout(t *testing.T) {
	mr := miniredis.RunT(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rdb := newRDB(t, mr)
	ex := executor.New(rdb, "node-1", executor.Options{HTTPTimeout: 500 * time.Millisecond})

	start := time.Now()
	err := ex.Execute(context.Background(), makeTask(srv.URL))
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Less(t, elapsed, 3*time.Second)

	keys, _ := rdb.Keys(context.Background(), "scheduler:exec:*").Result()
	require.Len(t, keys, 1)
	el := fetchLog(t, rdb, keys[0])
	assert.Equal(t, "failed", el.Status)
	assert.NotEmpty(t, el.Error)
}

// T6: GetLogs returns the log written by Execute.
func TestGetLogs_ReturnsSingleLog(t *testing.T) {
	mr := miniredis.RunT(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rdb := newRDB(t, mr)
	ex := executor.New(rdb, "node-1", executor.Options{})
	task := makeTask(srv.URL)

	require.NoError(t, ex.Execute(context.Background(), task))

	logs, err := ex.GetLogs(context.Background(), task.ID, 10)
	require.NoError(t, err)
	require.Len(t, logs, 1)
	assert.Equal(t, task.ID, logs[0].TaskID)
	assert.Equal(t, "success", logs[0].Status)
	assert.Equal(t, "node-1", logs[0].ExecutedBy)
}

// T7: execution log TTL is 7 days.
func TestExecute_LogTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rdb := newRDB(t, mr)
	ex := executor.New(rdb, "node-1", executor.Options{})

	require.NoError(t, ex.Execute(context.Background(), makeTask(srv.URL)))

	keys, err := rdb.Keys(context.Background(), "scheduler:exec:*").Result()
	require.NoError(t, err)
	require.Len(t, keys, 1)

	ttl, err := rdb.TTL(context.Background(), keys[0]).Result()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, ttl, 7*24*time.Hour-time.Minute)
}

// T8: multiple executions -> GetLogs returns newest first.
func TestGetLogs_NewestFirst(t *testing.T) {
	mr := miniredis.RunT(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rdb := newRDB(t, mr)
	ex := executor.New(rdb, "node-1", executor.Options{})
	task := makeTask(srv.URL)

	for i := 0; i < 3; i++ {
		time.Sleep(2 * time.Millisecond)
		require.NoError(t, ex.Execute(context.Background(), task))
	}

	logs, err := ex.GetLogs(context.Background(), task.ID, 10)
	require.NoError(t, err)
	require.Len(t, logs, 3)
	assert.False(t, logs[0].ExecutedAt.Before(logs[1].ExecutedAt), "log[0] should be newest")
	assert.False(t, logs[1].ExecutedAt.Before(logs[2].ExecutedAt), "log[1] should be newer than log[2]")
}
