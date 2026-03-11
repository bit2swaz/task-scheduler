package hashring_test

import (
	"fmt"
	"math"
	"math/rand"
	"sync"
	"testing"

	"github.com/bit2swaz/task-scheduler/internal/hashring"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// T1: Empty ring returns empty string from Get, no panic.
func TestGet_EmptyRing(t *testing.T) {
	r := hashring.New(150)
	result := r.Get("any-key")
	assert.Equal(t, "", result, "empty ring must return empty string")
}

// T2: Single node -- any key maps to the only node.
func TestGet_SingleNode(t *testing.T) {
	r := hashring.New(150)
	r.Add("node-1")
	keys := []string{"a", "z", "task-001", "scheduler", "xyz123"}
	for _, k := range keys {
		assert.Equal(t, "node-1", r.Get(k), "key %q must map to the only node", k)
	}
}

// T3: Determinism -- same key always returns the same node.
func TestGet_Determinism(t *testing.T) {
	r := hashring.New(150)
	r.Add("node-1")
	r.Add("node-2")
	r.Add("node-3")
	keys := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
	first := make(map[string]string, len(keys))
	for _, k := range keys {
		first[k] = r.Get(k)
	}
	// Call again 100 times; result must be identical each time.
	for i := 0; i < 100; i++ {
		for _, k := range keys {
			assert.Equal(t, first[k], r.Get(k), "key %q not deterministic on call %d", k, i)
		}
	}
}

// T4: Distribution -- 3 nodes + 150 replicas, 10 000 keys stay within ±15% of equal share.
func TestGet_Distribution(t *testing.T) {
	r := hashring.New(150)
	nodes := []string{"node-1", "node-2", "node-3"}
	for _, n := range nodes {
		r.Add(n)
	}

	counts := make(map[string]int, len(nodes))
	const total = 10_000
	for i := 0; i < total; i++ {
		key := fmt.Sprintf("task-%d", i)
		counts[r.Get(key)]++
	}

	expected := float64(total) / float64(len(nodes))
	tolerance := expected * 0.15
	for _, n := range nodes {
		diff := math.Abs(float64(counts[n]) - expected)
		assert.LessOrEqualf(t, diff, tolerance,
			"node %s got %d/%d keys (expected ~%.0f ±%.0f)", n, counts[n], total, expected, tolerance)
	}
}

// T5: Add rebalance -- after adding a 4th node ~25% of keys reassign (not all).
func TestAdd_RebalancesPartially(t *testing.T) {
	r := hashring.New(150)
	for _, n := range []string{"node-1", "node-2", "node-3"} {
		r.Add(n)
	}

	const total = 10_000
	before := make(map[string]string, total)
	for i := 0; i < total; i++ {
		k := fmt.Sprintf("task-%d", i)
		before[k] = r.Get(k)
	}

	r.Add("node-4")

	changed := 0
	for i := 0; i < total; i++ {
		k := fmt.Sprintf("task-%d", i)
		if r.Get(k) != before[k] {
			changed++
		}
	}

	// Expect roughly 25% of keys to move; accept 10-40% range.
	pct := float64(changed) / float64(total)
	assert.Greaterf(t, pct, 0.10, "too few keys moved after adding node-4: %.1f%%", pct*100)
	assert.Lessf(t, pct, 0.40, "too many keys moved after adding node-4: %.1f%%", pct*100)
}

// T6: Remove -- after removing a node, other keys remain on their original nodes.
func TestRemove_ReassignsOnlyRemovedNode(t *testing.T) {
	r := hashring.New(150)
	for _, n := range []string{"node-1", "node-2", "node-3"} {
		r.Add(n)
	}

	const total = 10_000
	before := make(map[string]string, total)
	for i := 0; i < total; i++ {
		k := fmt.Sprintf("task-%d", i)
		before[k] = r.Get(k)
	}

	r.Remove("node-3")

	for i := 0; i < total; i++ {
		k := fmt.Sprintf("task-%d", i)
		now := r.Get(k)
		// node-3 keys must have moved somewhere else.
		assert.NotEqual(t, "node-3", now, "key %q still maps to removed node-3", k)
		// Keys that were NOT on node-3 must stay put.
		if before[k] != "node-3" {
			assert.Equal(t, before[k], now, "key %q moved from %s to %s despite its node not being removed", k, before[k], now)
		}
	}
}

// T7: Concurrency -- no data race under -race.
func TestConcurrency_NoRace(t *testing.T) {
	r := hashring.New(150)
	r.Add("node-1")
	r.Add("node-2")
	r.Add("node-3")

	var wg sync.WaitGroup
	// 50 readers
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = r.Get(fmt.Sprintf("key-%d-%d", i, j))
			}
		}(i)
	}
	// 5 writers interleaved with readers
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			nodeID := fmt.Sprintf("dynamic-%d", i)
			r.Add(nodeID)
			// short yield then remove to not permanently grow ring
			r.Remove(nodeID)
		}(i)
	}
	wg.Wait()
	// If we reach here without -race flagging anything, the test passes.
}

// T8: Wrap-around -- a key hashing beyond the last ring point maps to the first node.
func TestGet_WrapAround(t *testing.T) {
	// Use 1 replica to make the ring sparse and force wrap-around more predictably.
	r := hashring.New(1)
	r.Add("only-node")
	// With 1 replica, every key must still resolve to the one node regardless
	// of where on the ring it hashes.
	for i := 0; i < 1000; i++ {
		k := fmt.Sprintf("wrap-key-%d", rand.Int())
		assert.Equal(t, "only-node", r.Get(k), "wrap-around failed for key %q", k)
	}
}

// Bonus: Nodes() returns all added nodes without duplicates.
func TestNodes_ReturnsDeduplicated(t *testing.T) {
	r := hashring.New(150)
	r.Add("node-a")
	r.Add("node-b")
	r.Add("node-a") // duplicate add
	nodes := r.Nodes()
	require.Len(t, nodes, 2, "Nodes() must deduplicate")
	assert.ElementsMatch(t, []string{"node-a", "node-b"}, nodes)
}
