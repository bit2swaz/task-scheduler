# Runbook: Task execution debugging

## Trace a single task end-to-end

### 1. Confirm the task exists

```bash
TOKEN=$(curl -s -X POST localhost:3001/auth/token \
  -H "Content-Type: application/json" \
  -d '{"node_id":"ops","secret":"..."}' | jq -r .token)

TASK_ID="a3f4b1c2"

curl -s localhost:3001/tasks/$TASK_ID \
  -H "Authorization: Bearer $TOKEN" | jq .
```

Check:
- `enabled` is `true`
- `cron_expr` is what you expect
- `assigned_node` shows the node that should execute it

### 2. Check which node owns the task

```bash
curl -s localhost:3001/tasks/$TASK_ID \
  -H "Authorization: Bearer $TOKEN" | jq .assigned_node
```

The assigned node is determined by the consistent hash ring. All nodes should agree on the assignment.

### 3. Check if the owning node is the leader

Only the leader loads tasks into its cron runner. If the assigned node is not the leader, the task will not fire automatically.

```bash
for port in 3001 3002 3003; do
  echo "port $port: $(curl -s localhost:$port/health | jq .is_leader,.tasks_assigned)"
done
```

### 4. Manually trigger the task to test the endpoint

```bash
curl -s -X POST localhost:3001/tasks/$TASK_ID/run \
  -H "Authorization: Bearer $TOKEN" | jq .
```

A 202 response with an `exec_id` means the executor was called. A 500 means the endpoint was unreachable or returned an error.

### 5. Read the execution logs

```bash
curl -s localhost:3001/tasks/$TASK_ID/logs \
  -H "Authorization: Bearer $TOKEN" | jq '.[0]'
```

Key fields to check:

| Field | What to look for |
|---|---|
| `status` | `"success"` or `"failed"` |
| `status_code` | HTTP status from the task endpoint |
| `duration_ms` | Unusually high values indicate a slow endpoint |
| `error` | Non-empty on failure; often contains the HTTP or network error |
| `executed_by` | Confirms which node ran the task |

### 6. Check Prometheus metrics

```bash
# execution counts by task
curl -s localhost:3001/metrics | grep scheduler_tasks_executed_total

# failure counts
curl -s localhost:3001/metrics | grep scheduler_tasks_failed_total

# latency histogram
curl -s localhost:3001/metrics | grep scheduler_task_duration_seconds
```

### 7. Inspect Redis directly

```bash
# list all task IDs
redis-cli SMEMBERS scheduler:tasks

# get raw task JSON
redis-cli GET scheduler:task:$TASK_ID

# list recent execution IDs for a task (newest first)
redis-cli LRANGE scheduler:task-logs:$TASK_ID 0 4

# get a specific execution log
EXEC_ID=$(redis-cli LINDEX scheduler:task-logs:$TASK_ID 0)
redis-cli GET scheduler:exec:$EXEC_ID
```

## Common failure patterns

| Symptom | Likely cause | Action |
|---|---|---|
| `status: failed`, `status_code: 0` | Task endpoint unreachable | Check endpoint URL, network, firewall |
| `status: failed`, `status_code: 4xx` | Endpoint rejected the request | Check payload, auth on the endpoint |
| Task never fires | Node not leader, or `enabled: false` | Check `is_leader`, `enabled` field |
| Task fires on wrong node | Ring inconsistency | Restart all nodes to rebuild ring from registry |
| Logs missing | Log TTL expired (7 days) | Data is gone; check Prometheus counters for history |
