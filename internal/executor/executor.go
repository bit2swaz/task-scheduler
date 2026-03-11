// Package executor handles task execution via HTTP POST, records execution
// logs in Redis with a 7-day TTL, and reports Prometheus metrics.
package executor

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/bit2swaz/task-scheduler/internal/registry"
	"github.com/redis/go-redis/v9"
)

const (
	execKeyPrefix      = "scheduler:exec:"
	taskLogsPrefix     = "scheduler:task-logs:"
	logTTL             = 7 * 24 * time.Hour
	logListCap         = 100
	defaultHTTPTimeout = 30 * time.Second
)

// ExecutionLog records the outcome of a single task execution.
type ExecutionLog struct {
	TaskID     string    `json:"task_id"`
	ExecID     string    `json:"exec_id"`
	Status     string    `json:"status"`
	StatusCode int       `json:"status_code"`
	Duration   int64     `json:"duration_ms"`
	ExecutedAt time.Time `json:"executed_at"`
	ExecutedBy string    `json:"executed_by"`
	Error      string    `json:"error,omitempty"`
}

// Executor is the interface for executing tasks and retrieving logs.
type Executor interface {
	Execute(ctx context.Context, task registry.Task) error
	GetLogs(ctx context.Context, taskID string, limit int) ([]ExecutionLog, error)
}

// Options configures an HTTPExecutor.
type Options struct {
	HTTPClient  *http.Client
	HTTPTimeout time.Duration
}

// HTTPExecutor executes tasks via HTTP POST and persists logs in Redis.
type HTTPExecutor struct {
	rdb        *redis.Client
	nodeID     string
	httpClient *http.Client
}

// New creates an HTTPExecutor with the given Redis client, node ID, and options.
func New(rdb *redis.Client, nodeID string, opts Options) *HTTPExecutor {
	client := opts.HTTPClient
	if client == nil {
		timeout := opts.HTTPTimeout
		if timeout == 0 {
			timeout = defaultHTTPTimeout
		}
		client = &http.Client{Timeout: timeout}
	}
	return &HTTPExecutor{rdb: rdb, nodeID: nodeID, httpClient: client}
}

// Execute performs an HTTP POST to task.Endpoint and records an ExecutionLog in Redis.
func (e *HTTPExecutor) Execute(ctx context.Context, task registry.Task) (retErr error) {
	execID := newExecID()
	start := time.Now()
	el := ExecutionLog{
		TaskID:     task.ID,
		ExecID:     execID,
		ExecutedAt: start,
		ExecutedBy: e.nodeID,
	}

	defer func() {
		el.Duration = time.Since(start).Milliseconds()
		data, _ := json.Marshal(el)
		rctx := context.Background()
		key := execKeyPrefix + execID
		e.rdb.Set(rctx, key, data, logTTL)
		indexKey := taskLogsPrefix + task.ID
		e.rdb.LPush(rctx, indexKey, execID)
		e.rdb.LTrim(rctx, indexKey, 0, logListCap-1)
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, task.Endpoint, nil)
	if err != nil {
		el.Status = "failed"
		el.Error = err.Error()
		return err
	}

	resp, err := e.httpClient.Do(req)
	if err != nil {
		el.Status = "failed"
		el.Error = err.Error()
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	el.StatusCode = resp.StatusCode
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		el.Status = "success"
		return nil
	}
	el.Status = "failed"
	el.Error = fmt.Sprintf("unexpected status %d", resp.StatusCode)
	return fmt.Errorf("task %s: unexpected status %d", task.ID, resp.StatusCode)
}

// GetLogs returns up to limit execution logs for a task, newest first.
func (e *HTTPExecutor) GetLogs(ctx context.Context, taskID string, limit int) ([]ExecutionLog, error) {
	indexKey := taskLogsPrefix + taskID
	ids, err := e.rdb.LRange(ctx, indexKey, 0, int64(limit-1)).Result()
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = execKeyPrefix + id
	}

	vals, err := e.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}

	logs := make([]ExecutionLog, 0, len(vals))
	for _, v := range vals {
		if v == nil {
			continue
		}
		var el ExecutionLog
		if jsonErr := json.Unmarshal([]byte(v.(string)), &el); jsonErr != nil {
			continue
		}
		logs = append(logs, el)
	}
	return logs, nil
}

func newExecID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}
