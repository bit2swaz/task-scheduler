// Package hashring implements a consistent hash ring with virtual nodes for
// deterministic, balanced task-to-node assignment. Each physical node is
// represented by Ring.replicas virtual positions on the ring; keys are
// assigned to the nearest virtual node in clockwise order with wrap-around.
package hashring

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Ring is a thread-safe consistent hash ring.
// The zero value is not usable; create one with New.
type Ring struct {
	mu       sync.RWMutex
	replicas int                 // virtual nodes per physical node
	ring     map[uint64]string   // hash point -> physical nodeID
	sorted   []uint64            // ring points in ascending order
	nodes    map[string]struct{} // set of physical node IDs
}

// New creates an empty Ring with the given number of virtual nodes (replicas)
// per physical node. A value of 150 gives good load distribution in practice.
func New(replicas int) *Ring {
	if replicas < 1 {
		replicas = 1
	}
	return &Ring{
		replicas: replicas,
		ring:     make(map[uint64]string),
		nodes:    make(map[string]struct{}),
	}
}

// Add inserts nodeID into the ring by placing Ring.replicas virtual points.
// Adding a node that is already present is a no-op (idempotent).
func (r *Ring) Add(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.nodes[nodeID]; exists {
		return
	}
	r.nodes[nodeID] = struct{}{}

	for i := 0; i < r.replicas; i++ {
		h := hashKey(fmt.Sprintf("%s:%d", nodeID, i))
		r.ring[h] = nodeID
		r.sorted = append(r.sorted, h)
	}
	sort.Slice(r.sorted, func(i, j int) bool { return r.sorted[i] < r.sorted[j] })
}

// Remove deletes nodeID and all its virtual points from the ring.
// Removing a node that is not present is a no-op.
func (r *Ring) Remove(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.nodes[nodeID]; !exists {
		return
	}
	delete(r.nodes, nodeID)

	// Build the set of virtual points that belong to this node.
	toRemove := make(map[uint64]struct{}, r.replicas)
	for i := 0; i < r.replicas; i++ {
		h := hashKey(fmt.Sprintf("%s:%d", nodeID, i))
		delete(r.ring, h)
		toRemove[h] = struct{}{}
	}

	// Rebuild sorted slice without the removed points (O(n log n)).
	filtered := r.sorted[:0:len(r.sorted)]
	for _, h := range r.sorted {
		if _, skip := toRemove[h]; !skip {
			filtered = append(filtered, h)
		}
	}
	r.sorted = filtered
}

// Get returns the nodeID responsible for key by finding the first ring point
// >= hash(key). If the hash falls beyond all ring points, it wraps to the
// first point. Returns "" if the ring is empty.
func (r *Ring) Get(key string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.sorted) == 0 {
		return ""
	}

	h := hashKey(key)
	idx := sort.Search(len(r.sorted), func(i int) bool { return r.sorted[i] >= h })
	if idx == len(r.sorted) {
		idx = 0 // wrap around
	}
	return r.ring[r.sorted[idx]]
}

// Nodes returns a deduplicated, sorted slice of all physical node IDs currently
// on the ring.
func (r *Ring) Nodes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]string, 0, len(r.nodes))
	for n := range r.nodes {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// String returns a human-readable summary of the ring useful for debugging.
// Example: "Ring{nodes: [node-1, node-2], points: 300}"
func (r *Ring) String() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	nodes := make([]string, 0, len(r.nodes))
	for n := range r.nodes {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	return fmt.Sprintf("Ring{nodes: [%s], points: %d}", strings.Join(nodes, ", "), len(r.sorted))
}

// hashKey hashes s using SHA-256 and returns the first 8 bytes as a uint64.
// This gives a uniform 64-bit hash with negligible collision probability.
func hashKey(s string) uint64 {
	sum := sha256.Sum256([]byte(s))
	return binary.BigEndian.Uint64(sum[:8])
}
