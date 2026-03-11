// Package scheduler wraps robfig/cron and dispatches tasks to the executor,
// filtering by consistent hash ring assignment for this node.
package scheduler
