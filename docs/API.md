# API Reference

Base URL when running locally: `http://localhost:3001` (or 3002/3003 for other nodes).

All protected endpoints require a `Bearer` JWT in the `Authorization` header.

## Error format

All error responses share the same JSON body:

```json
{
  "code": "NOT_FOUND",
  "message": "task not found"
}
```

| HTTP status | code |
|---|---|
| 400 | `INVALID_REQUEST` |
| 401 | `UNAUTHORIZED` |
| 404 | `NOT_FOUND` |
| 409 | `CONFLICT` |
| 500 | `INTERNAL_ERROR` |

---

## POST /auth/token

Issue a signed JWT. No authentication required.

**Request body**

```json
{
  "node_id": "string",
  "secret":  "string"
}
```

`secret` must equal the `JWT_SECRET` environment variable of the target node.

**Response 200**

```json
{
  "token": "<HS256 JWT, 24h expiry>"
}
```

**curl**

```bash
curl -s -X POST http://localhost:3001/auth/token \
  -H "Content-Type: application/json" \
  -d '{"node_id":"node-1","secret":"change-me-before-use"}' | jq .
```

**Errors:** `401 UNAUTHORIZED` if `secret` does not match.

---

## GET /health

Node liveness and leadership status. No authentication required.

**Response 200**

```json
{
  "node_id":        "node-1",
  "is_leader":      true,
  "tasks_assigned": 3,
  "uptime_seconds": 142
}
```

**curl**

```bash
curl -s http://localhost:3001/health | jq .
```

---

## GET /metrics

Prometheus text-format metrics. No authentication required.

```bash
curl -s http://localhost:3001/metrics | grep scheduler_
```

Exposed metrics:

| Metric | Type | Labels | Description |
|---|---|---|---|
| `scheduler_tasks_executed_total` | counter | `task` | Total successful executions |
| `scheduler_tasks_failed_total` | counter | `task` | Total failed executions |
| `scheduler_task_duration_seconds` | histogram | `task` | Execution latency |
| `scheduler_is_leader` | gauge | | 1 if this node is the leader |
| `scheduler_active_tasks_total` | gauge | | Tasks currently scheduled |
| `scheduler_nodes_total` | gauge | | Cluster size |

---

## POST /tasks

Register a new task. **JWT required.**

**Request body**

```json
{
  "name":      "daily-report",
  "cron_expr": "0 9 * * *",
  "endpoint":  "http://report-service/generate",
  "payload":   "{\"format\": \"pdf\"}",
  "enabled":   true
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | string | yes | Human-readable label |
| `cron_expr` | string | yes | Standard 5-field cron expression |
| `endpoint` | string | yes | HTTP endpoint to POST to on each execution |
| `payload` | string | no | Request body forwarded to the endpoint |
| `enabled` | bool | no | Defaults to `false` |

**Response 201**

```json
{
  "id":            "a3f4b1c2",
  "name":          "daily-report",
  "cron_expr":     "0 9 * * *",
  "endpoint":      "http://report-service/generate",
  "payload":       "{\"format\": \"pdf\"}",
  "enabled":       true,
  "assigned_node": "node-2",
  "created_at":    "2026-03-12T09:00:00Z",
  "updated_at":    "2026-03-12T09:00:00Z"
}
```

**curl**

```bash
curl -s -X POST http://localhost:3001/tasks \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"ping","cron_expr":"* * * * *","endpoint":"http://httpbin.org/post","enabled":true}' | jq .
```

**Errors:** `400 INVALID_REQUEST` (missing/invalid fields), `409 CONFLICT` (duplicate task ID).

---

## GET /tasks

List all registered tasks. **JWT required.**

**Response 200** — array of task objects (same shape as POST /tasks response).

**curl**

```bash
curl -s http://localhost:3001/tasks \
  -H "Authorization: Bearer $TOKEN" | jq .
```

---

## GET /tasks/{id}

Get a single task by ID. **JWT required.**

**Response 200** — single task object.

**curl**

```bash
curl -s http://localhost:3001/tasks/a3f4b1c2 \
  -H "Authorization: Bearer $TOKEN" | jq .
```

**Errors:** `404 NOT_FOUND`.

---

## PUT /tasks/{id}

Update task fields. All fields are optional; omitted fields are unchanged. **JWT required.**

**Request body**

```json
{
  "name":      "updated-name",
  "cron_expr": "0 10 * * *",
  "endpoint":  "http://new-service/endpoint",
  "payload":   "",
  "enabled":   false
}
```

**Response 200** — updated task object.

**curl**

```bash
curl -s -X PUT http://localhost:3001/tasks/a3f4b1c2 \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"enabled":false}' | jq .
```

**Errors:** `400 INVALID_REQUEST` (bad cron expression), `404 NOT_FOUND`.

---

## DELETE /tasks/{id}

Delete a task. **JWT required.**

**Response 204** — no body.

**curl**

```bash
curl -s -X DELETE http://localhost:3001/tasks/a3f4b1c2 \
  -H "Authorization: Bearer $TOKEN" -o /dev/null -w "%{http_code}"
```

---

## POST /tasks/{id}/run

Manually trigger a task immediately, bypassing its cron schedule. **JWT required.**

**Response 202**

```json
{
  "exec_id": "d9e1f2a3b4c5d6e7"
}
```

**curl**

```bash
curl -s -X POST http://localhost:3001/tasks/a3f4b1c2/run \
  -H "Authorization: Bearer $TOKEN" | jq .
```

**Errors:** `404 NOT_FOUND`, `500 INTERNAL_ERROR` (endpoint unreachable).

---

## GET /tasks/{id}/logs

Retrieve the most recent execution logs for a task (up to 50, newest first). **JWT required.**

**Response 200**

```json
[
  {
    "task_id":     "a3f4b1c2",
    "exec_id":     "d9e1f2a3b4c5d6e7",
    "status":      "success",
    "status_code": 200,
    "duration_ms": 142,
    "executed_at": "2026-03-12T09:01:00Z",
    "executed_by": "node-2"
  }
]
```

| Field | Description |
|---|---|
| `status` | `"success"` or `"failed"` |
| `status_code` | HTTP status returned by the task endpoint |
| `duration_ms` | Wall-clock execution time in milliseconds |
| `executed_by` | Node ID that ran the task |
| `error` | Present only on failure; describes the error |

**curl**

```bash
curl -s http://localhost:3001/tasks/a3f4b1c2/logs \
  -H "Authorization: Bearer $TOKEN" | jq .
```

---

## GET /nodes

List cluster members and identify the current leader. **JWT required.**

**Response 200**

```json
{
  "nodes":  ["node-1", "node-2", "node-3"],
  "leader": "node-1"
}
```

**curl**

```bash
curl -s http://localhost:3001/nodes \
  -H "Authorization: Bearer $TOKEN" | jq .
```

---

## Cron expression reference

The scheduler uses standard 5-field cron syntax (`minute hour day month weekday`):

| Expression | Meaning |
|---|---|
| `* * * * *` | Every minute |
| `0 * * * *` | Every hour (on the hour) |
| `0 9 * * 1-5` | 09:00 on weekdays |
| `0 9 * * *` | 09:00 every day |
| `*/15 * * * *` | Every 15 minutes |
| `@hourly` | Every hour |
| `@daily` | Midnight every day |
| `@weekly` | Midnight Sunday |
