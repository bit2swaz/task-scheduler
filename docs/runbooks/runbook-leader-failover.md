# Runbook: Leader failover

## Overview

The leader holds the `scheduler:leader` key in Redis. If the leader crashes or loses connectivity, the key expires and a standby node takes over within `leaseTTL + pollInterval` (15 seconds by default).

## Observing leadership

```bash
# which node is currently the leader?
redis-cli GET scheduler:leader

# check all nodes for is_leader
for port in 3001 3002 3003; do
  echo -n "port $port: "; curl -s localhost:$port/health | jq .is_leader
done

# Prometheus gauge (1 = leader, 0 = standby)
curl -s localhost:3001/metrics | grep scheduler_is_leader
```

## Triggering a manual failover

There is no graceful hand-off command. To force a failover:

1. Stop or kill the current leader container:
   ```bash
   docker compose -f docker/docker-compose.yml stop scheduler-1
   ```
2. Wait up to 15 seconds for a standby to take over.
3. Verify the new leader:
   ```bash
   redis-cli GET scheduler:leader   # should return node-2 or node-3
   ```

## Forcing failover without stopping the process

Delete the leader key directly. The current leader detects the missing key on its next renewal tick and calls `OnEvicted`. A standby wins the next election.

```bash
redis-cli DEL scheduler:leader
```

Use this only in development or for testing purposes.

## After failover

- The new leader calls `LoadAndSchedule` to pick up tasks assigned to it via the ring.
- The previous leader stops its cron runner; it becomes a standby and participates in future elections.
- No tasks are lost; they remain in Redis and will be rescheduled by the new leader.

## Checking for split-brain

Split-brain (two simultaneous leaders) is prevented by the atomic `SET NX` primitive. To verify there is exactly one leader at any point:

```bash
for port in 3001 3002 3003; do
  echo "port $port: $(curl -s localhost:$port/health | jq .is_leader)"
done
```

Exactly one node should print `true`.
