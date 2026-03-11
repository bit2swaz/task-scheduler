# ADR-002: Custom consistent hash ring

**Date:** 2026-03-12
**Status:** accepted

## Context

Tasks need to be deterministically assigned to exactly one node so that no task is executed twice and the assignment is stable when the cluster is unchanged. The assignment must also rebalance gracefully when nodes join or leave.

## Decision

Implement a consistent hash ring using SHA-256 hashed virtual nodes. Each physical node gets `HASH_RING_REPLICAS` (default 150) virtual slots on the ring. A task ID is hashed and mapped to the next virtual node clockwise; that virtual node's physical node executes the task.

## Rationale

- **150 virtual nodes** achieves a standard deviation of roughly 10% across nodes, keeping load distribution even without exceeding memory overhead.
- **SHA-256 (first 8 bytes as uint64)** produces a uniform distribution across the ring with zero collisions in practice at cluster sizes of 10-100 nodes.
- **Custom implementation vs. a library** was chosen to keep the dependency tree minimal and to allow direct unit testing of distribution properties. The ring is only ~130 lines of code.
- When a 4th node is added to a 3-node cluster, approximately 25% of tasks migrate — only those in the arc that now belongs to the new node. All other tasks retain their assignments.

## Consequences

- Adding or removing a node requires calling `ring.Add`/`ring.Remove` and then calling `sched.LoadAndSchedule` so the scheduler recomputes its owned tasks.
- Ring state is in-memory only; on restart each node reconstructs its ring from `registry.ListNodes`. This is safe because all nodes reconstruct the same ring from the same set of registered nodes.
