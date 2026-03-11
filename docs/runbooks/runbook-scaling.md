# Runbook: Scaling — adding a new node

## Overview

Nodes register themselves in the `scheduler:nodes` Redis set at startup. The consistent hash ring is rebuilt from that set. Adding a node causes approximately `1/N` of the total tasks to migrate to the new node (where N is the new cluster size).

## Steps

1. Start the new node with a unique `NODE_ID`:

   ```bash
   NODE_ID=node-4 PORT=3004 REDIS_URL=redis://localhost:6379 JWT_SECRET=... go run ./cmd/scheduler
   ```

   Or add a new service to `docker-compose.yml` and run `make docker-up`.

2. The new node calls `registry.RegisterNode("node-4")` on startup, adding itself to `scheduler:nodes`.

3. Verify the node appears in the cluster:

   ```bash
   TOKEN=$(curl -s -X POST localhost:3001/auth/token \
     -H "Content-Type: application/json" \
     -d '{"node_id":"ops","secret":"..."}' | jq -r .token)
   curl -s localhost:3001/nodes -H "Authorization: Bearer $TOKEN" | jq .
   ```

4. The leader will detect the new node on its next `LoadAndSchedule` call (triggered by leader re-election or a manual restart). To force immediate rebalancing, restart the leader:

   ```bash
   redis-cli DEL scheduler:leader   # triggers failover to a standby
   ```

   Once the new leader wins, it calls `LoadAndSchedule` with the updated ring that includes node-4.

5. Verify task distribution has shifted:

   ```bash
   curl -s localhost:3001/tasks -H "Authorization: Bearer $TOKEN" | jq '.[].assigned_node' | sort | uniq -c
   ```

## Removing a node

1. Stop the node process or container.
2. The node calls `registry.DeregisterNode` in its shutdown hook, removing itself from `scheduler:nodes`.
3. On the next leader election, `LoadAndSchedule` rebuilds the ring without the removed node, rebalancing its tasks to the remaining nodes.

## Notes

- Ring state is in-memory. All nodes must reconstruct the ring identically from `registry.ListNodes`. There is no explicit ring synchronization message.
- Tasks are not migrated explicitly — the next `LoadAndSchedule` call re-evaluates which tasks belong to each node and adds/removes cron entries accordingly.
