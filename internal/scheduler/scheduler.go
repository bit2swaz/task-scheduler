package scheduler

import (
	"context"
	"fmt"
	"sync"

	"github.com/robfig/cron/v3"

	"github.com/bit2swaz/task-scheduler/internal/hashring"
	"github.com/bit2swaz/task-scheduler/internal/registry"
)

// Executor is the interface the Scheduler uses to run a task.
type Executor interface {
	Execute(ctx context.Context, task registry.Task) error
}

// TaskLister is the subset of the task registry that the Scheduler needs.
type TaskLister interface {
	ListAll(ctx context.Context) ([]registry.Task, error)
}

// Scheduler wraps robfig/cron and dispatches tasks to the executor,
// filtering by consistent hash ring assignment for this node.
type Scheduler struct {
	cronRunner *cron.Cron
	reg        TaskLister
	exec       Executor
	ring       *hashring.Ring
	nodeID     string
	mu         sync.Mutex
	entryIDs   map[string]cron.EntryID
}

// New creates a new Scheduler and starts its underlying cron runner.
// LoadAndSchedule must be called to populate the schedule from the registry.
func New(reg TaskLister, exec Executor, ring *hashring.Ring, nodeID string) *Scheduler {
	s := &Scheduler{
		cronRunner: cron.New(),
		reg:        reg,
		exec:       exec,
		ring:       ring,
		nodeID:     nodeID,
		entryIDs:   make(map[string]cron.EntryID),
	}
	s.cronRunner.Start()
	return s
}

// LoadAndSchedule fetches all tasks from the registry, filters to those owned
// by this node via the hash ring, and (re)schedules them. It is safe to call
// multiple times; existing entries are replaced on each call, enabling
// re-evaluation after ring topology changes.
func (s *Scheduler) LoadAndSchedule(ctx context.Context) error {
	tasks, err := s.reg.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("scheduler load: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove all existing cron entries.
	for id, entryID := range s.entryIDs {
		s.cronRunner.Remove(entryID)
		delete(s.entryIDs, id)
	}

	// Re-add tasks that are owned by this node and enabled.
	for _, task := range tasks {
		task := task // capture loop variable for closure
		if !s.isOwnedByNode(task.ID) || !task.Enabled {
			continue
		}
		entryID, err := s.cronRunner.AddFunc(task.CronExpr, func() {
			_ = s.exec.Execute(ctx, task)
		})
		if err != nil {
			return fmt.Errorf("scheduler: add func for task %s: %w", task.ID, err)
		}
		s.entryIDs[task.ID] = entryID
	}

	return nil
}

// AddTask schedules a single task on this node without a full registry reload.
// It validates the cron expression and is a no-op if the task is not assigned
// to this node by the hash ring. Replaces any existing entry for the same ID.
func (s *Scheduler) AddTask(ctx context.Context, task registry.Task) error {
	if err := ValidateCronExpr(task.CronExpr); err != nil {
		return err
	}
	if !s.isOwnedByNode(task.ID) {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Replace existing entry if present.
	if existing, ok := s.entryIDs[task.ID]; ok {
		s.cronRunner.Remove(existing)
		delete(s.entryIDs, task.ID)
	}

	entryID, err := s.cronRunner.AddFunc(task.CronExpr, func() {
		_ = s.exec.Execute(ctx, task)
	})
	if err != nil {
		return fmt.Errorf("scheduler: add task %s: %w", task.ID, err)
	}
	s.entryIDs[task.ID] = entryID
	return nil
}

// RemoveTask removes a task from the scheduler. No-op if not currently scheduled.
func (s *Scheduler) RemoveTask(taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entryID, ok := s.entryIDs[taskID]
	if !ok {
		return nil
	}
	s.cronRunner.Remove(entryID)
	delete(s.entryIDs, taskID)
	return nil
}

// Stop halts the cron runner and blocks until all running jobs complete.
func (s *Scheduler) Stop() {
	ctx := s.cronRunner.Stop()
	<-ctx.Done()
}

// ScheduledCount returns the number of tasks currently scheduled on this node.
// Useful for health checks and metrics.
func (s *Scheduler) ScheduledCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entryIDs)
}

// isOwnedByNode returns true if taskID is assigned to this node by the ring.
// Must be called without holding s.mu (ring has its own lock).
func (s *Scheduler) isOwnedByNode(taskID string) bool {
	return s.ring.Get(taskID) == s.nodeID
}
