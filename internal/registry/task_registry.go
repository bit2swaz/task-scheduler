package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Sentinel errors returned by registry operations.
var (
	ErrTaskNotFound      = errors.New("task not found")
	ErrTaskAlreadyExists = errors.New("task already exists")
)

const (
	taskKeyPrefix = "scheduler:task:"
	tasksSetKey   = "scheduler:tasks"
	nodesSetKey   = "scheduler:nodes"
)

// Task is the core domain model for a scheduled job.
type Task struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CronExpr  string    `json:"cron_expr"`
	Endpoint  string    `json:"endpoint"`
	Payload   string    `json:"payload"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RedisRegistry is a Redis-backed implementation of task and node CRUD.
type RedisRegistry struct {
	rdb *redis.Client
}

// New creates a new RedisRegistry backed by the given Redis client.
func New(rdb *redis.Client) *RedisRegistry {
	return &RedisRegistry{rdb: rdb}
}

func taskKey(id string) string {
	return fmt.Sprintf("%s%s", taskKeyPrefix, id)
}

// Create stores a new task. Returns ErrTaskAlreadyExists if the ID is taken.
func (r *RedisRegistry) Create(ctx context.Context, task Task) error {
	key := taskKey(task.ID)

	// Check existence using SISMEMBER on the tasks set — avoids a separate GET.
	exists, err := r.rdb.SIsMember(ctx, tasksSetKey, task.ID).Result()
	if err != nil {
		return fmt.Errorf("registry create check: %w", err)
	}
	if exists {
		return ErrTaskAlreadyExists
	}

	now := time.Now().UTC()
	task.CreatedAt = now
	task.UpdatedAt = now

	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("registry create marshal: %w", err)
	}

	if err := r.rdb.Set(ctx, key, data, 0).Err(); err != nil {
		return fmt.Errorf("registry create set: %w", err)
	}
	if err := r.rdb.SAdd(ctx, tasksSetKey, task.ID).Err(); err != nil {
		return fmt.Errorf("registry create sadd: %w", err)
	}
	return nil
}

// Get retrieves a task by ID. Returns ErrTaskNotFound if absent.
func (r *RedisRegistry) Get(ctx context.Context, id string) (*Task, error) {
	data, err := r.rdb.Get(ctx, taskKey(id)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, ErrTaskNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("registry get: %w", err)
	}

	var task Task
	if err := json.Unmarshal([]byte(data), &task); err != nil {
		return nil, fmt.Errorf("registry get unmarshal: %w", err)
	}
	return &task, nil
}

// Update overwrites an existing task. Returns ErrTaskNotFound if absent.
func (r *RedisRegistry) Update(ctx context.Context, task Task) error {
	exists, err := r.rdb.SIsMember(ctx, tasksSetKey, task.ID).Result()
	if err != nil {
		return fmt.Errorf("registry update check: %w", err)
	}
	if !exists {
		return ErrTaskNotFound
	}

	// Preserve original CreatedAt.
	original, err := r.Get(ctx, task.ID)
	if err != nil {
		return err
	}
	task.CreatedAt = original.CreatedAt
	task.UpdatedAt = time.Now().UTC()

	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("registry update marshal: %w", err)
	}
	if err := r.rdb.Set(ctx, taskKey(task.ID), data, 0).Err(); err != nil {
		return fmt.Errorf("registry update set: %w", err)
	}
	return nil
}

// Delete removes a task by ID. No-op if the task does not exist.
func (r *RedisRegistry) Delete(ctx context.Context, id string) error {
	if err := r.rdb.Del(ctx, taskKey(id)).Err(); err != nil {
		return fmt.Errorf("registry delete key: %w", err)
	}
	if err := r.rdb.SRem(ctx, tasksSetKey, id).Err(); err != nil {
		return fmt.Errorf("registry delete srem: %w", err)
	}
	return nil
}

// ListAll returns all tasks. Returns an empty (non-nil) slice when none exist.
func (r *RedisRegistry) ListAll(ctx context.Context) ([]Task, error) {
	ids, err := r.rdb.SMembers(ctx, tasksSetKey).Result()
	if err != nil {
		return nil, fmt.Errorf("registry listall smembers: %w", err)
	}

	tasks := make([]Task, 0, len(ids))
	for _, id := range ids {
		task, err := r.Get(ctx, id)
		if err != nil {
			// Skip stale references (shouldn't happen in normal operation).
			continue
		}
		tasks = append(tasks, *task)
	}
	return tasks, nil
}

// RegisterNode adds a node to the cluster membership set.
func (r *RedisRegistry) RegisterNode(ctx context.Context, nodeID string) error {
	if err := r.rdb.SAdd(ctx, nodesSetKey, nodeID).Err(); err != nil {
		return fmt.Errorf("registry register node: %w", err)
	}
	return nil
}

// DeregisterNode removes a node from the cluster membership set.
func (r *RedisRegistry) DeregisterNode(ctx context.Context, nodeID string) error {
	if err := r.rdb.SRem(ctx, nodesSetKey, nodeID).Err(); err != nil {
		return fmt.Errorf("registry deregister node: %w", err)
	}
	return nil
}

// ListNodes returns all registered node IDs.
func (r *RedisRegistry) ListNodes(ctx context.Context) ([]string, error) {
	nodes, err := r.rdb.SMembers(ctx, nodesSetKey).Result()
	if err != nil {
		return nil, fmt.Errorf("registry list nodes: %w", err)
	}
	return nodes, nil
}
