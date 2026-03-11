# ADR-001: Redis-based leader election

**Date:** 2026-03-12
**Status:** accepted

## Context

The scheduler must guarantee that exactly one node runs the cron dispatcher at any time. Without coordination, every node in the cluster would execute every task, causing duplicate runs and thundering-herd problems on downstream services.

## Decision

Use Redis `SET <key> <nodeID> NX EX <leaseTTL>` as the election primitive. The node that successfully sets the key becomes the leader for the duration of `leaseTTL`. It renews the TTL every `pollInterval` using a `GET` (to verify ownership) followed by `PEXPIRE`. If the leader crashes or is partitioned, the key expires and any standby can win the next election.

## Rationale

- **Redis is already a dependency** for the task registry, so there is no additional infrastructure to operate.
- **SET NX EX is atomic** — no race window between checking and setting the key.
- **Simple failure model** — the worst-case failover time is bounded: `leaseTTL + pollInterval` (10s + 5s = 15s by default). This is acceptable for cron workloads where a missed minute is tolerable.
- Alternatives considered:
  - **etcd / Zookeeper** — stronger consistency guarantees but significant operational overhead for a single-service deployment.
  - **Consul sessions** — similar to Redis TTL-based leases but requires a Consul cluster.

## Consequences

- There is a narrow window where two leaders can coexist: if the network partitions and the outgoing leader continues renewing while a new one has elected. In practice the TTL caps this at 10s, which is acceptable for cron-latency workloads.
- A future improvement is to replace the `GET + PEXPIRE` renewal with a single atomic Lua `EVAL` to eliminate the tiny race between the two Redis calls.
