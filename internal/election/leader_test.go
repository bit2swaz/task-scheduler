package election_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/bit2swaz/task-scheduler/internal/election"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test timing constants -- short enough for fast runs, long enough to be reliable.
const (
	testLease = 150 * time.Millisecond
	testPoll  = 50 * time.Millisecond
)

func newTestClient(t *testing.T, mr *miniredis.Miniredis) *redis.Client {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func baseOpts() election.ElectorOptions {
	return election.ElectorOptions{
		LeaseTTL:     testLease,
		PollInterval: testPoll,
	}
}

// T1: Node acquires the leader lock when the key is absent.
func TestTryElect_AcquiresLock(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := newTestClient(t, mr)

	electedc := make(chan struct{}, 1)
	opts := baseOpts()
	opts.OnElected = func() { close(electedc) }

	e := election.New(rdb, "node-1", opts)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go e.Run(ctx)

	select {
	case <-electedc:
	case <-time.After(time.Second):
		t.Fatal("node-1 did not become leader within 1s")
	}

	assert.True(t, e.IsLeader())

	leader, err := e.CurrentLeader(ctx)
	require.NoError(t, err)
	assert.Equal(t, "node-1", leader)
}

// T2: Two electors started simultaneously; exactly one wins (no split-brain).
func TestNoSplitBrain(t *testing.T) {
	for i := 0; i < 10; i++ {
		mr := miniredis.RunT(t)
		rdb1 := newTestClient(t, mr)
		rdb2 := newTestClient(t, mr)

		e1 := election.New(rdb1, "node-1", baseOpts())
		e2 := election.New(rdb2, "node-2", baseOpts())

		ctx, cancel := context.WithCancel(context.Background())

		go e1.Run(ctx)
		go e2.Run(ctx)

		time.Sleep(testPoll * 3)

		leaders := 0
		if e1.IsLeader() {
			leaders++
		}
		if e2.IsLeader() {
			leaders++
		}

		cancel()
		assert.Equal(t, 1, leaders, "iteration %d: expected exactly 1 leader, got %d", i, leaders)
		mr.FlushAll()
	}
}

// T3: Leader renews its TTL; key survives well beyond the original TTL.
func TestRenewal_KeySurvivesTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := newTestClient(t, mr)

	electedc := make(chan struct{}, 1)
	opts := baseOpts()
	opts.OnElected = func() { close(electedc) }

	e := election.New(rdb, "node-1", opts)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go e.Run(ctx)

	select {
	case <-electedc:
	case <-time.After(time.Second):
		t.Fatal("did not become leader")
	}

	// Wait for 5x TTL; renewal must have fired many times to keep the key alive.
	time.Sleep(testLease * 5)

	assert.True(t, mr.Exists("scheduler:leader"), "leader key must still exist after repeated renewal")
	assert.True(t, e.IsLeader())
}

// T4: Manually deleting the leader key triggers OnEvicted within one poll interval.
func TestEvictionDetection(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := newTestClient(t, mr)

	electedc := make(chan struct{}, 1)
	evictedc := make(chan struct{}, 1)
	opts := baseOpts()
	opts.OnElected = func() { close(electedc) }
	opts.OnEvicted = func() { close(evictedc) }

	e := election.New(rdb, "node-1", opts)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go e.Run(ctx)

	select {
	case <-electedc:
	case <-time.After(time.Second):
		t.Fatal("did not become leader")
	}

	// Manually remove the key to simulate lease loss.
	mr.Del("scheduler:leader")

	select {
	case <-evictedc:
	case <-time.After(time.Second):
		t.Fatal("OnEvicted was not called within 1s after key deletion")
	}

	assert.False(t, e.IsLeader())
}

// T5: Standby acquires leadership after the leader's context is cancelled.
func TestFailover(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb1 := newTestClient(t, mr)
	rdb2 := newTestClient(t, mr)

	e1Elected := make(chan struct{}, 1)
	opts1 := baseOpts()
	opts1.OnElected = func() { close(e1Elected) }

	e2Elected := make(chan struct{}, 1)
	opts2 := baseOpts()
	opts2.OnElected = func() { close(e2Elected) }

	e1 := election.New(rdb1, "node-1", opts1)
	e2 := election.New(rdb2, "node-2", opts2)

	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	go e1.Run(ctx1)

	select {
	case <-e1Elected:
	case <-time.After(time.Second):
		t.Fatal("e1 did not become leader")
	}

	go e2.Run(ctx2)
	cancel1() // kill the leader

	// Give e1's goroutine time to observe ctx cancellation and stop renewing.
	time.Sleep(testPoll * 3)

	// Fast-forward miniredis past the lease TTL so the key expires immediately.
	// This bypasses miniredis lazy-expiry and makes the test deterministic.
	mr.FastForward(testLease * 2)

	// e2 should pick up the lock on its very next poll tick (at most testPoll away).
	select {
	case <-e2Elected:
	case <-time.After(time.Second):
		t.Fatal("e2 did not take over leadership within expected time")
	}

	assert.True(t, e2.IsLeader())
}

// T6: Run() exits promptly when its context is cancelled.
func TestContextCancellation_ExitsCleanly(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := newTestClient(t, mr)

	e := election.New(rdb, "node-1", baseOpts())
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		e.Run(ctx)
		close(done)
	}()

	time.Sleep(testPoll * 2)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run() did not exit within 1s after context cancellation")
	}
}

// T7: CurrentLeader returns the nodeID in the key, or empty string if none.
func TestCurrentLeader(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := newTestClient(t, mr)
	ctx := context.Background()

	e := election.New(rdb, "node-1", baseOpts())

	leader, err := e.CurrentLeader(ctx)
	require.NoError(t, err)
	assert.Equal(t, "", leader, "no leader yet")

	mr.Set("scheduler:leader", "node-42")

	leader, err = e.CurrentLeader(ctx)
	require.NoError(t, err)
	assert.Equal(t, "node-42", leader)
}

// T8: IsLeader() is race-free under concurrent reads and state changes.
func TestIsLeader_ThreadSafety(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := newTestClient(t, mr)

	e := election.New(rdb, "node-1", baseOpts())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go e.Run(ctx)

	var wg sync.WaitGroup
	// 50 goroutines reading IsLeader() concurrently.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = e.IsLeader()
				time.Sleep(time.Millisecond)
			}
		}()
	}

	// Flip the leader key while readers are running.
	go func() {
		for i := 0; i < 5; i++ {
			time.Sleep(testPoll)
			mr.Del("scheduler:leader")
			time.Sleep(testPoll)
		}
	}()

	wg.Wait()
	// Reaching here without -race detector firing = PASS.
}
