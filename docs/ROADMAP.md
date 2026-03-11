# Distributed Task Scheduler — Build Roadmap

> **Methodology:** Test-Driven Development (TDD) throughout.
> Write the test → watch it fail (RED) → implement the minimum code to pass (GREEN) → refactor (REFACTOR).
> Every phase ships working, tested, committed code before the next phase begins.

---

## Phase Overview

| # | Phase | Focus | TDD |
|---|-------|-------|-----|
| 0 | Project Scaffold | Repo, modules, tooling, CI skeleton | — |
| 1 | Config & Infrastructure | Env config, Redis client, shared types | Partial |
| 2 | Consistent Hash Ring | Core algorithm, virtual nodes, rebalance | ✅ Full |
| 3 | Leader Election | Redis SET NX EX, renewal, failover | ✅ Full |
| 4 | Task Registry | Task CRUD in Redis, node membership | ✅ Full |
| 5 | Scheduler Core | robfig/cron wrapper, task dispatch | ✅ Full |
| 6 | Executor | HTTP task execution, retry, execution log | ✅ Full |
| 7 | REST API | chi router, handlers, request validation | ✅ Full |
| 8 | Auth Middleware | JWT issue + validation, route protection | ✅ Full |
| 9 | Observability | Prometheus metrics, health endpoint | Partial |
| 10 | Containerization | Dockerfile, Docker Compose 3-node cluster | Manual |
| 11 | Integration Testing | End-to-end flows, multi-node scenarios | ✅ Full |
| 12 | Documentation & Polish | README, API docs, runbooks, final cleanup | — |

---

## Phase 0 — Project Scaffold

**Goal:** A clean, runnable Go module with the full directory tree, linter, and a passing smoke test.

### 0.1 — Repository Init
- [ ] Create root directory `task-scheduler/`
- [ ] `git init`, add `.gitignore` (Go template: binaries, `.env`, `vendor/`)
- [ ] `go mod init github.com/<username>/task-scheduler`
- [ ] Pin Go version to `1.22` in `go.mod`

### 0.2 — Dependency Bootstrap
Add all dependencies in one pass so the module graph is stable:
```
github.com/go-chi/chi/v5
github.com/redis/go-redis/v9
github.com/robfig/cron/v3
github.com/golang-jwt/jwt/v5
github.com/prometheus/client_golang
github.com/google/uuid
github.com/stretchr/testify
github.com/joho/godotenv
```
- [ ] `go get` each dependency
- [ ] `go mod tidy` — commit `go.mod` + `go.sum`

### 0.3 — Directory Skeleton
Create all packages (empty `doc.go` or stub files) so the compiler is happy:
```
cmd/scheduler/main.go
internal/config/config.go
internal/election/leader.go
internal/hashring/ring.go
internal/registry/task_registry.go
internal/scheduler/scheduler.go
internal/scheduler/cron.go
internal/executor/executor.go
internal/api/handler.go
internal/api/middleware.go
internal/metrics/metrics.go
tests/
docker/
```
- [ ] Each file has `package <name>` only — no logic yet
- [ ] `go build ./...` must succeed with zero errors

### 0.4 — Tooling
- [ ] `Makefile` with targets: `build`, `test`, `lint`, `run`, `docker-up`, `docker-down`
- [ ] `.env.example` with all required env vars populated with safe defaults
- [ ] Install `golangci-lint`; add `.golangci.yml` (enable `errcheck`, `govet`, `staticcheck`, `gofmt`)
- [ ] Add GitHub Actions CI skeleton (`.github/workflows/ci.yml`): `go test ./...` + lint on every push

### 0.5 — Smoke Test
- [ ] Write `TestMain_Smoke` in `cmd/scheduler/main_test.go` — just asserts `true`
- [ ] `make test` goes GREEN

**Exit criteria:** `go build ./...`, `go test ./...`, and `golangci-lint run` all pass on a clean clone.

---

## Phase 1 — Config & Infrastructure

**Goal:** Typed configuration from environment variables; a tested Redis client factory.

### 1.1 — Config Struct (`internal/config/config.go`)
Define and implement `Config`:
```go
type Config struct {
    Port             string
    NodeID           string
    RedisURL         string
    JWTSecret        string
    HashRingReplicas int
    LeaseTTL         time.Duration
    RenewInterval    time.Duration
    PollInterval     time.Duration
}
```
- [ ] `Load() (*Config, error)` reads from env via `os.Getenv` + `godotenv`
- [ ] Defaults: `PORT=3000`, `HASH_RING_REPLICAS=150`, TTLs from SSOT
- [ ] Validation: return error if `NODE_ID` or `JWT_SECRET` are empty

### 1.2 — Config Tests (`internal/config/config_test.go`)
- [ ] **RED:** Test `Load()` returns error when `NODE_ID` is missing
- [ ] **GREEN:** Implement validation
- [ ] **RED:** Test all defaults are applied when env is empty (except required fields)
- [ ] **GREEN:** Implement defaults
- [ ] **REFACTOR:** Extract `setDefaults()` helper

### 1.3 — Redis Client Factory (`internal/config/redis.go`)
- [ ] `NewRedisClient(cfg *Config) (*redis.Client, error)` — parses `RedisURL`, pings on creation
- [ ] Returns a wrapped error if ping fails

### 1.4 — Redis Client Tests
- [ ] **RED:** Test factory returns error on bad URL
- [ ] **GREEN:** Implement URL validation before dialing
- [ ] Note: Tests that require a live Redis are tagged `//go:build integration` and skipped in unit runs

**Exit criteria:** `make test` (unit only) passes; `go vet ./internal/config/...` clean.

---

## Phase 2 — Consistent Hash Ring

**Goal:** A correct, thread-safe consistent hash ring with virtual nodes. This is the most algorithmically critical piece — TDD first.

### 2.1 — Interface Definition
Define the contract before implementation:
```go
type HashRing interface {
    Add(nodeID string)
    Remove(nodeID string)
    Get(key string) string
    Nodes() []string
}
```

### 2.2 — Tests First (`tests/hashring_test.go` + `internal/hashring/ring_test.go`)

Write ALL tests before any implementation:

- [ ] **T1 — Empty ring:** `Get()` on empty ring returns `""` (not panic)
- [ ] **T2 — Single node:** Any key maps to the only node
- [ ] **T3 — Determinism:** Same key always returns same node across multiple calls
- [ ] **T4 — Distribution:** With 3 nodes + 150 replicas, 10,000 random keys distribute within ±15% of equal share
- [ ] **T5 — Add node rebalance:** After adding a 4th node, ~25% of keys reassign (not all of them)
- [ ] **T6 — Remove node:** After removing a node, its keys reassign to remaining nodes; other keys unchanged
- [ ] **T7 — Concurrency:** 50 goroutines calling `Get()` + 5 calling `Add()`/`Remove()` simultaneously — no race (run with `-race`)
- [ ] **T8 — Wrap-around:** Key that hashes beyond the last ring point correctly wraps to the first node

### 2.3 — Implementation (`internal/hashring/ring.go`)
- [ ] `Ring` struct with `sync.RWMutex`, `replicas int`, `ring map[uint64]string`, `sorted []uint64`
- [ ] `hash(key string) uint64` via SHA-256 first 8 bytes
- [ ] `Add(nodeID string)` — add `replicas` virtual nodes, sort
- [ ] `Remove(nodeID string)` — remove all virtual nodes for nodeID, rebuild sorted slice
- [ ] `Get(key string) string` — binary search with wrap-around
- [ ] `Nodes() []string` — return deduplicated list of physical nodes

### 2.4 — Refactor
- [ ] Ensure `Remove` is O(n log n) — rebuild sorted slice, don't iterate naively
- [ ] Add `String() string` method for debug dumps

**Exit criteria:** All 8 tests GREEN, `go test -race ./internal/hashring/...` passes.

---

## Phase 3 — Leader Election

**Goal:** Reliable Redis-based leader election with lease renewal and graceful failover. Zero split-brain.

### 3.1 — Interface & Types
```go
type LeaderElector interface {
    Run(ctx context.Context)
    IsLeader() bool
    CurrentLeader(ctx context.Context) (string, error)
}
```

Callbacks:
```go
type ElectorOptions struct {
    OnElected func()   // called when this node becomes leader
    OnEvicted func()   // called when leadership is lost
}
```

### 3.2 — Tests First (`tests/election_test.go`)

Use a real Redis (test containers or local Redis via build tag `integration`):

- [ ] **T1 — Acquire lock:** Node successfully sets key with NX EX when key is absent
- [ ] **T2 — No split-brain:** Two electors started simultaneously; exactly one wins (loop 100 times)
- [ ] **T3 — Renewal:** Leader renews TTL before expiry; key remains after 2× TTL with renewal running
- [ ] **T4 — Eviction detection:** Manually delete the key; elector detects it within one poll interval and calls `onEvicted`
- [ ] **T5 — Failover:** Kill the leader elector; standby acquires leadership within `pollInterval + leaseTTL`
- [ ] **T6 — Context cancellation:** `Run()` exits cleanly when context is cancelled; no goroutine leak
- [ ] **T7 — `CurrentLeader`:** Returns the node ID stored in the key, or `""` if no leader
- [ ] **T8 — `IsLeader` thread safety:** 50 goroutines reading `IsLeader()` while leader state changes — no race

### 3.3 — Implementation (`internal/election/leader.go`)
- [ ] `LeaderElector` struct with `rdb`, `nodeID`, `isLeader` (`atomic.Bool`), callbacks, timers
- [ ] `Run(ctx)` — starts polling ticker; calls `tryElect` or `renew` each tick
- [ ] `tryElect(ctx)` — `SetNX` with `leaseTTL`; on success set `isLeader = true`, call `onElected`
- [ ] `renew(ctx)` — `GET` key, compare value; if mismatch → `isLeader = false`, call `onEvicted`; else `Expire`
- [ ] `IsLeader() bool` — returns `atomic.Bool` value (race-free read)
- [ ] `CurrentLeader(ctx) (string, error)` — `GET` key

### 3.4 — Refactor
- [ ] Replace `bool` field with `sync/atomic` — `atomic.Bool` (Go 1.19+)
- [ ] Extract Lua script for atomic renew (`GET + EXPIRE` → single round-trip via `EVAL`)

**Exit criteria:** All 8 tests GREEN under `-race`; integration tests pass against local Redis.

---

## Phase 4 — Task Registry

**Goal:** Persistent, Redis-backed CRUD for tasks; node membership tracking.

### 4.1 — Task Model (`internal/registry/task_registry.go`)
```go
type Task struct {
    ID        string    `json:"id"`
    Name      string    `json:"name"`
    CronExpr  string    `json:"cron_expr"`
    Endpoint  string    `json:"endpoint"`
    Payload   string    `json:"payload"`
    Enabled   bool      `json:"enabled"`
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
}
```

### 4.2 — Registry Interface
```go
type TaskRegistry interface {
    Create(ctx, task Task) error
    Get(ctx, id string) (*Task, error)
    Update(ctx, task Task) error
    Delete(ctx, id string) error
    ListAll(ctx) ([]Task, error)
    RegisterNode(ctx, nodeID string) error
    DeregisterNode(ctx, nodeID string) error
    ListNodes(ctx) ([]string, error)
}
```

### 4.3 — Tests First (`internal/registry/task_registry_test.go`)

Use `miniredis` (in-memory Redis mock) for unit tests — no real Redis required:

- [ ] **T1 — Create + Get roundtrip:** Create a task, retrieve by ID, assert all fields equal
- [ ] **T2 — Create duplicate ID:** Returns `ErrTaskAlreadyExists`
- [ ] **T3 — Get non-existent:** Returns `ErrTaskNotFound`
- [ ] **T4 — Update:** Mutates stored fields; `UpdatedAt` advances
- [ ] **T5 — Update non-existent:** Returns `ErrTaskNotFound`
- [ ] **T6 — Delete:** Task no longer returned by `Get` or `ListAll`
- [ ] **T7 — ListAll:** Returns all created tasks; empty slice (not nil) when none exist
- [ ] **T8 — Node registration:** `RegisterNode` + `ListNodes` reflects membership
- [ ] **T9 — JSON serialization fidelity:** Marshal/unmarshal cycle preserves all fields including time zone

### 4.4 — Implementation
- [ ] Redis key scheme: `scheduler:task:{id}` (string/JSON), `scheduler:tasks` (set of IDs)
- [ ] Node membership: `scheduler:nodes` (set)
- [ ] `uuid.New().String()` for ID generation on `Create`
- [ ] Error sentinel values: `var ErrTaskNotFound = errors.New("task not found")`

### 4.5 — Cron Validation (`internal/scheduler/cron.go`)
- [ ] `ValidateCronExpr(expr string) error` using `robfig/cron`'s parser
- [ ] Tests: valid standard 5-field, valid `@hourly`/`@daily`, invalid string → error

**Exit criteria:** All tests GREEN using `miniredis`; no real Redis dependency in unit suite.

---

## Phase 5 — Scheduler Core

**Goal:** Wire robfig/cron to the registry and hash ring; only execute tasks assigned to this node.

### 5.1 — Scheduler Interface
```go
type Scheduler interface {
    LoadAndSchedule(ctx context.Context) error
    AddTask(ctx context.Context, task registry.Task) error
    RemoveTask(taskID string) error
    Stop()
}
```

### 5.2 — Tests First (`tests/scheduler_test.go`)

Use `miniredis` + mock executor:

- [ ] **T1 — Load tasks:** 3 tasks in registry; only the one assigned to this node's ID is scheduled
- [ ] **T2 — Correct node filtering:** Change ring assignment; correct task runs on correct node
- [ ] **T3 — AddTask dynamic:** `AddTask` schedules a new cron entry without full reload
- [ ] **T4 — RemoveTask:** `RemoveTask` stops future executions without affecting other tasks
- [ ] **T5 — Executor called:** When cron fires, executor's `Execute` is called with the correct task
- [ ] **T6 — Stop:** `Stop()` halts all cron entries; no executor calls after stop
- [ ] **T7 — Invalid cron expr:** `AddTask` with bad cron expression returns validation error
- [ ] **T8 — Re-schedule on ring change:** When a node joins/leaves, scheduler re-evaluates assignments

### 5.3 — Implementation (`internal/scheduler/scheduler.go`)
- [ ] `Scheduler` struct: `cron *cron.Cron`, `registry`, `executor`, `ring`, `nodeID`, `entryIDs map[string]cron.EntryID`
- [ ] `LoadAndSchedule`: list all tasks → filter by ring → `cron.AddFunc` each
- [ ] `AddTask`: validate expr → `cron.AddFunc` → store `entryID`
- [ ] `RemoveTask`: `cron.Remove(entryID)` → delete from map
- [ ] `Stop`: `cron.Stop()`

### 5.4 — Refactor
- [ ] Protect `entryIDs` map with `sync.Mutex`
- [ ] Extract `isOwnedByNode(taskID string) bool` helper

**Exit criteria:** All 8 tests GREEN; no data race under `-race`.

---

## Phase 6 — Executor

**Goal:** HTTP task execution with timeout, retry, and execution logging to Redis.

### 6.1 — Execution Log Model
```go
type ExecutionLog struct {
    TaskID     string        `json:"task_id"`
    ExecID     string        `json:"exec_id"`
    Status     string        `json:"status"`    // "success" | "failed"
    StatusCode int           `json:"status_code"`
    Duration   int64         `json:"duration_ms"`
    ExecutedAt time.Time     `json:"executed_at"`
    ExecutedBy string        `json:"executed_by"` // nodeID
    Error      string        `json:"error,omitempty"`
}
```

### 6.2 — Executor Interface
```go
type Executor interface {
    Execute(ctx context.Context, task registry.Task) error
    GetLogs(ctx context.Context, taskID string, limit int) ([]ExecutionLog, error)
}
```

### 6.3 — Tests First (`internal/executor/executor_test.go`)

Use `httptest.NewServer` to mock downstream endpoints:

- [ ] **T1 — Successful POST:** Mock returns 200; `ExecutionLog.Status == "success"`, duration > 0
- [ ] **T2 — HTTP 4xx:** Log status `"failed"`, `StatusCode` captured
- [ ] **T3 — HTTP 5xx:** Log status `"failed"`
- [ ] **T4 — Network error:** Mock server closes connection; log status `"failed"`, `Error` field populated
- [ ] **T5 — Timeout:** Mock sleeps 10s; executor timeout is 5s; returns within ~5s with `"failed"`
- [ ] **T6 — Log persistence:** `GetLogs` returns the log written by `Execute`
- [ ] **T7 — Log TTL:** Logs stored with 7-day TTL in Redis (verify with `TTL` command on key)
- [ ] **T8 — Log ordering:** Multiple executions of same task; `GetLogs` returns newest first

### 6.4 — Implementation (`internal/executor/executor.go`)
- [ ] `Executor` struct: `rdb`, `nodeID`, `httpClient *http.Client` (with 30s timeout)
- [ ] `Execute`: generate `execID` → POST with timeout → write `ExecutionLog` to `scheduler:exec:{execID}`
- [ ] Log index: append `execID` to `scheduler:task-logs:{taskID}` (Redis list, capped at 100)
- [ ] `GetLogs`: `LRANGE` log index → batch `MGET` log entries → parse JSON

### 6.5 — Refactor
- [ ] Inject `http.Client` via options (enables test mocking without `httptest`)
- [ ] Ensure `Execute` always writes a log even on panic (deferred write)

**Exit criteria:** All 8 tests GREEN; executor handles all error paths without panicking.

---

## Phase 7 — REST API

**Goal:** Full chi-based HTTP API with proper request validation and error responses.

### 7.1 — Error Conventions
```go
type APIError struct {
    Code    string `json:"code"`
    Message string `json:"message"`
}
// 400 → "INVALID_REQUEST"
// 401 → "UNAUTHORIZED"
// 404 → "NOT_FOUND"
// 409 → "CONFLICT"
// 500 → "INTERNAL_ERROR"
```

### 7.2 — Request/Response DTOs
Define all request + response types before writing handlers:
- `CreateTaskRequest`, `UpdateTaskRequest`
- `TaskResponse` (includes `AssignedNode string`)
- `NodeListResponse`
- `HealthResponse`

### 7.3 — Tests First (`internal/api/handler_test.go`)

Use `httptest.NewRecorder` + chi router; mock all dependencies via interfaces:

**Task Endpoints:**
- [ ] **T1 — POST /tasks 201:** Valid payload → task created, response has `id`
- [ ] **T2 — POST /tasks 400:** Missing `name` → `INVALID_REQUEST`
- [ ] **T3 — POST /tasks 400:** Invalid `cron_expr` → descriptive error
- [ ] **T4 — POST /tasks 409:** Duplicate task name → `CONFLICT`
- [ ] **T5 — GET /tasks 200:** Returns list with `assigned_node` populated from ring
- [ ] **T6 — GET /tasks/:id 200:** Returns correct task
- [ ] **T7 — GET /tasks/:id 404:** Non-existent ID
- [ ] **T8 — PUT /tasks/:id 200:** Update fields, `updated_at` changes
- [ ] **T9 — DELETE /tasks/:id 204:** Task gone; subsequent GET returns 404
- [ ] **T10 — POST /tasks/:id/run 202:** Executor called; returns `exec_id`
- [ ] **T11 — GET /tasks/:id/logs 200:** Returns log array

**Node/Health Endpoints:**
- [ ] **T12 — GET /nodes 200:** Returns node list + marks current leader
- [ ] **T13 — GET /health 200:** Returns `{"node_id", "is_leader", "tasks_assigned", "uptime_seconds"}`

### 7.4 — Implementation (`internal/api/handler.go`)
- [ ] Chi router setup with middleware chain: `requestID → logger → recoverer → auth` (auth skipped for `/health`, `/metrics`, `/auth/token`)
- [ ] All handlers: decode JSON → validate → call service layer → encode response
- [ ] Use `chi.URLParam(r, "id")` for path parameters

### 7.5 — Auth Endpoint (`POST /auth/token`)
- [ ] Accepts `{"node_id": "...", "secret": "..."}` (secret must match `JWT_SECRET`)
- [ ] Returns signed JWT with `node_id` claim, 24h expiry

**Exit criteria:** All 13 handler tests GREEN with mocked dependencies; no real Redis or HTTP calls.

---

## Phase 8 — Auth Middleware

**Goal:** JWT middleware that protects all routes except the whitelist.

### 8.1 — Tests First (`internal/api/middleware_test.go`)

- [ ] **T1 — No Authorization header:** Returns 401 `UNAUTHORIZED`
- [ ] **T2 — Malformed Bearer token:** Returns 401
- [ ] **T3 — Expired token:** Returns 401 with message "token expired"
- [ ] **T4 — Wrong signing secret:** Returns 401
- [ ] **T5 — Valid token:** Request passes through; handler called
- [ ] **T6 — Claims in context:** Handler can extract `node_id` from context
- [ ] **T7 — Public routes bypass:** `GET /health` and `GET /metrics` pass without token
- [ ] **T8 — Request ID middleware:** Every response has `X-Request-ID` header set

### 8.2 — Implementation (`internal/api/middleware.go`)
- [ ] `JWTMiddleware(secret string) func(http.Handler) http.Handler`
- [ ] Parse `Authorization: Bearer <token>` → validate → store claims in `context.WithValue`
- [ ] `RequestIDMiddleware` — generate `uuid` if not present in headers
- [ ] `LoggingMiddleware` — structured log: method, path, status, duration, request-id

**Exit criteria:** All 8 middleware tests GREEN; JWT package correctly integrated.

---

## Phase 9 — Observability

**Goal:** Prometheus metrics for all measurable events; health endpoint with real data.

### 9.1 — Metrics (`internal/metrics/metrics.go`)
Implement all metrics from SSOT:
- [ ] `scheduler_tasks_executed_total{task}` — Counter
- [ ] `scheduler_tasks_failed_total{task}` — Counter
- [ ] `scheduler_task_duration_seconds{task}` — Histogram (default buckets)
- [ ] `scheduler_is_leader` — Gauge (0/1)
- [ ] `scheduler_active_tasks_total` — Gauge (tasks currently scheduled on this node)
- [ ] `scheduler_nodes_total` — Gauge (cluster size)

### 9.2 — Wire Metrics
- [ ] Executor calls `metrics.RecordExecution(taskName, duration, success)` after each run
- [ ] Leader elector calls `metrics.SetLeader(true/false)` on state change
- [ ] Registry calls `metrics.SetActiveTasks(count)` after load

### 9.3 — Metrics Tests
- [ ] **T1 — Counter increments:** Execute a task successfully; assert `scheduler_tasks_executed_total` incremented
- [ ] **T2 — Failed counter:** Execute with error; assert `scheduler_tasks_failed_total` incremented
- [ ] **T3 — Duration recorded:** Histogram has one observation after execution
- [ ] **T4 — Leader gauge:** `SetLeader(true)` → gauge = 1; `SetLeader(false)` → gauge = 0

### 9.4 — `/metrics` Endpoint
- [ ] Wire `promhttp.Handler()` to `GET /metrics`
- [ ] No auth required

**Exit criteria:** All 4 metric tests GREEN; `GET /metrics` returns valid Prometheus text format.

---

## Phase 10 — Containerization

**Goal:** Reproducible multi-stage Docker build; 3-node cluster runnable with one command.

### 10.1 — Dockerfile (`docker/Dockerfile`)
- [ ] **Stage 1 (builder):** `golang:1.22-alpine` → `COPY` module files → `go mod download` → `COPY` source → `go build -o /app/scheduler ./cmd/scheduler`
- [ ] **Stage 2 (runtime):** `alpine:3.19` → copy binary → set non-root user → `EXPOSE 3000` → `ENTRYPOINT`
- [ ] Verify: `docker build -f docker/Dockerfile .` succeeds; image < 50 MB

### 10.2 — Docker Compose (`docker/docker-compose.yml`)
Per SSOT spec:
- [ ] `scheduler-1` on port 3001, `NODE_ID=node-1`
- [ ] `scheduler-2` on port 3002, `NODE_ID=node-2`
- [ ] `scheduler-3` on port 3003, `NODE_ID=node-3`
- [ ] `redis:7-alpine` with healthcheck; all schedulers `depends_on: redis`
- [ ] `prom/prometheus:latest` with `prometheus.yml` (scrape all 3 nodes + itself)
- [ ] Named volume `redis_data`

### 10.3 — Prometheus Config (`docker/prometheus.yml`)
```yaml
scrape_configs:
  - job_name: 'scheduler'
    static_configs:
      - targets: ['scheduler-1:3000', 'scheduler-2:3000', 'scheduler-3:3000']
```

### 10.4 — Manual Verification Checklist
- [ ] `docker compose up --build` — all 5 containers healthy
- [ ] `curl localhost:3001/health` → 200 with correct node ID
- [ ] `curl localhost:3002/health` → 200
- [ ] `curl localhost:3003/health` → 200
- [ ] Exactly one node reports `"is_leader": true`
- [ ] `curl localhost:9090` → Prometheus UI accessible
- [ ] Kill scheduler-1 container; within 15s a standby node becomes leader
- [ ] `GET /nodes` shows correct cluster membership after kills/restarts

**Exit criteria:** `make docker-up` starts clean cluster; manual checklist passes.

---

## Phase 11 — Integration Testing

**Goal:** End-to-end tests that run the full system (real Redis, real HTTP) in CI.

### 11.1 — Test Setup (`tests/integration_test.go`)
- Build tag: `//go:build integration`
- Setup: start `miniredis` server OR use `testcontainers-go` to spin up Redis container
- Teardown: flush Redis, stop containers

### 11.2 — Integration Test Scenarios

**Cluster Behavior:**
- [ ] **IT-1 — Leader uniqueness:** Start 3 electors against same Redis; assert exactly 1 is leader at all times over 30s
- [ ] **IT-2 — Failover time:** Stop leader elector; measure time until next leader elected; assert < `leaseTTL + pollInterval` (15s)
- [ ] **IT-3 — Ring rebalance:** Add a 4th virtual node to ring; tasks previously on node-3 partially reassign

**Full Task Lifecycle:**
- [ ] **IT-4 — Create → Schedule → Execute:** POST task via API → scheduler picks it up → mock endpoint called
- [ ] **IT-5 — Manual trigger:** `POST /tasks/:id/run` calls executor immediately regardless of cron schedule
- [ ] **IT-6 — Execution log:** After execution, `GET /tasks/:id/logs` returns non-empty log list
- [ ] **IT-7 — Disable task:** Set `enabled: false`; cron fires but executor is NOT called
- [ ] **IT-8 — Delete task:** Task removed; scheduler removes cron entry; no further executions

**Auth Flow:**
- [ ] **IT-9 — Token lifecycle:** `POST /auth/token` → use token on protected route → 200; use expired token → 401
- [ ] **IT-10 — Unauthorized access:** All JWT-protected endpoints return 401 without token

### 11.3 — CI Integration
- [ ] Add `make test-integration` target that runs with `-tags integration`
- [ ] Add separate CI job for integration tests (can be slower; runs on push to `main` only)

**Exit criteria:** All 10 integration tests GREEN; CI pipeline passes end-to-end.

---

## Phase 12 — Documentation & Polish

**Goal:** The project is fully documented, clean, and ready for portfolio/production use.

### 12.1 — README.md (Root)
- [ ] Project description + architecture diagram (ASCII from SSOT)
- [ ] **Quick start:** `git clone` → `cp .env.example .env` → `make docker-up` → test with curl examples
- [ ] **Local development:** Prerequisites, `go run ./cmd/scheduler`, local Redis setup
- [ ] **Running tests:** `make test`, `make test-integration`, `make test-race`
- [ ] **Environment variables table** (all vars, types, defaults, required flag)
- [ ] **Architecture explanation:** Leader election, consistent hashing, task dispatch — one paragraph each
- [ ] Badges: CI status, Go version, license

### 12.2 — API Reference Doc (`docs/API.md`)
Full OpenAPI-style documentation for every endpoint:
- [ ] Method, path, auth required, description
- [ ] Request body schema (with types and constraints)
- [ ] Response body schema (200/201/400/401/404/409/500)
- [ ] `curl` example for every endpoint
- [ ] Error code glossary (`INVALID_REQUEST`, `NOT_FOUND`, etc.)

### 12.3 — Architecture Decision Records (`docs/adr/`)
One ADR per major non-obvious decision:
- [ ] `ADR-001-redis-leader-election.md` — Why Redis SET NX over Zookeeper/etcd
- [ ] `ADR-002-consistent-hashing.md` — Why custom ring over library; virtual node count rationale
- [ ] `ADR-003-chi-router.md` — Why chi over Gin/Echo/stdlib
- [ ] `ADR-004-jwt-auth.md` — Scope and limitations of the auth scheme

### 12.4 — Runbooks (`docs/runbooks/`)
- [ ] `runbook-leader-failover.md` — How to observe and manually trigger failover
- [ ] `runbook-scaling.md` — How to add a new node to the cluster
- [ ] `runbook-task-debugging.md` — How to trace a task execution end-to-end using logs and metrics

### 12.5 — Code Quality Pass
- [ ] Run `golangci-lint run` — zero warnings
- [ ] Run `go test -race ./...` — zero race conditions
- [ ] Run `go vet ./...` — zero issues
- [ ] All exported functions have GoDoc comments
- [ ] No `TODO`, `FIXME`, or `HACK` comments left in production code
- [ ] `go mod tidy` — `go.sum` is clean

### 12.6 — Final Verification
- [ ] Fresh clone on clean machine: `make docker-up && make test` both pass
- [ ] All SSOT API endpoints working and tested
- [ ] Prometheus dashboard shows all metrics after running sample workload
- [ ] README quick-start works exactly as written

**Exit criteria:** A developer with zero prior context can clone, run, and understand the project solely from the documentation.

---

## TDD Quick Reference

```
RED   → Write a failing test that defines expected behavior
GREEN → Write the minimum code to make the test pass
REFACTOR → Clean up: extract helpers, reduce duplication, improve naming
```

**Rules enforced throughout this project:**
1. No production code without a failing test first
2. All tests must pass before committing any phase
3. `go test -race ./...` must pass at end of every phase
4. Integration tests are separate (`build integration` tag) and never block unit test runs
5. Mocks/stubs via interfaces — no monkey-patching, no global state

---

## Makefile Targets Summary

| Target | Command | Description |
|--------|---------|-------------|
| `build` | `go build ./...` | Compile all packages |
| `test` | `go test ./...` | Run unit tests |
| `test-race` | `go test -race ./...` | Unit tests with race detector |
| `test-integration` | `go test -tags integration ./...` | Integration tests (requires Redis) |
| `lint` | `golangci-lint run` | Run all linters |
| `run` | `go run ./cmd/scheduler` | Run single node locally |
| `docker-up` | `docker compose -f docker/docker-compose.yml up --build` | Start 3-node cluster |
| `docker-down` | `docker compose -f docker/docker-compose.yml down -v` | Tear down + clean volumes |

---

## Dependency Graph (Phase Build Order)

```
Phase 0 (Scaffold)
    └── Phase 1 (Config)
            ├── Phase 2 (Hash Ring)     ← no deps beyond config
            ├── Phase 3 (Election)      ← needs config + redis
            └── Phase 4 (Registry)     ← needs config + redis
                    ├── Phase 5 (Scheduler) ← needs registry + ring + executor
                    │       └── Phase 6 (Executor) ← needs registry + redis
                    └── Phase 7 (API)   ← needs all services
                            └── Phase 8 (Auth) ← needs config + jwt
                                    └── Phase 9 (Observability)
                                            └── Phase 10 (Docker)
                                                    └── Phase 11 (Integration)
                                                            └── Phase 12 (Docs)
```

---

*Last updated: Phase 0 — not started*
