// Package election implements Redis-based leader election using SET NX EX
// with periodic lease renewal and graceful failover detection.
package election

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

const leaderKey = "scheduler:leader"

// ElectorOptions configures a LeaderElector.
// Zero values are replaced with safe production defaults in New.
type ElectorOptions struct {
	// LeaseTTL is how long the leader key lives in Redis before expiring.
	// Default: 10s.
	LeaseTTL time.Duration

	// RenewInterval is kept for API symmetry with config.Config.
	RenewInterval time.Duration

	// PollInterval controls how often each node checks/renews leadership.
	// Default: 5s.
	PollInterval time.Duration

	// OnElected is called once when this node wins the election.
	// It runs in the same goroutine as Run; keep it short or launch a goroutine.
	OnElected func()

	// OnEvicted is called once when this node loses leadership.
	// Same threading rules as OnElected.
	OnEvicted func()
}

// LeaderElector performs Redis-based leader election for a single node.
// Create one with New; start it with Run.
type LeaderElector struct {
	rdb    *redis.Client
	nodeID string
	opts   ElectorOptions
	leader atomic.Bool
}

// New returns a LeaderElector for nodeID using rdb. opts fields that are zero
// are replaced with safe defaults. Nil callbacks are replaced with no-ops.
func New(rdb *redis.Client, nodeID string, opts ElectorOptions) *LeaderElector {
	if opts.LeaseTTL == 0 {
		opts.LeaseTTL = 10 * time.Second
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = 5 * time.Second
	}
	if opts.RenewInterval == 0 {
		opts.RenewInterval = opts.PollInterval
	}
	if opts.OnElected == nil {
		opts.OnElected = func() {}
	}
	if opts.OnEvicted == nil {
		opts.OnEvicted = func() {}
	}
	return &LeaderElector{rdb: rdb, nodeID: nodeID, opts: opts}
}

// Run blocks, polling Redis every PollInterval until ctx is cancelled.
// If this node is not the leader it attempts election; if it is the leader it
// renews the lease. Run is safe to call in its own goroutine.
func (e *LeaderElector) Run(ctx context.Context) {
	ticker := time.NewTicker(e.opts.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Go's select does not prioritise ctx.Done() when both cases are ready.
			// Check again so we never renew after cancellation, which would extend
			// the TTL and delay failover by up to another LeaseTTL.
			if ctx.Err() != nil {
				return
			}
			if e.leader.Load() {
				e.renew(ctx)
			} else {
				e.tryElect(ctx)
			}
		}
	}
}

// tryElect attempts to win the election via SET NX. If successful it marks
// this node as leader and fires OnElected.
func (e *LeaderElector) tryElect(ctx context.Context) {
	res, err := e.rdb.SetArgs(ctx, leaderKey, e.nodeID, redis.SetArgs{
		Mode: "NX",
		TTL:  e.opts.LeaseTTL,
	}).Result()
	if err != nil || res != "OK" {
		return
	}
	e.leader.Store(true)
	e.opts.OnElected()
}

// renew verifies this node still owns the leader key, then refreshes its TTL.
// The GET and PExpire are two separate calls; the tiny race window between them
// is acceptable for the expected renewal intervals (seconds in production).
// A future optimisation can replace this with an atomic Lua EVAL.
func (e *LeaderElector) renew(ctx context.Context) {
	val, err := e.rdb.Get(ctx, leaderKey).Result()
	if errors.Is(err, redis.Nil) || (err == nil && val != e.nodeID) || (err != nil && !errors.Is(err, redis.Nil)) {
		e.leader.Store(false)
		e.opts.OnEvicted()
		return
	}
	// Still the owner; push out the expiry.
	_ = e.rdb.PExpire(ctx, leaderKey, e.opts.LeaseTTL).Err()
}

// IsLeader reports whether this node currently holds the leader lease.
// Safe to call from any goroutine.
func (e *LeaderElector) IsLeader() bool {
	return e.leader.Load()
}

// CurrentLeader returns the nodeID stored in the leader key, or an empty
// string if no leader is currently elected. It is a direct Redis read and
// reflects the state at the moment of the call.
func (e *LeaderElector) CurrentLeader(ctx context.Context) (string, error) {
	val, err := e.rdb.Get(ctx, leaderKey).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return val, err
}
