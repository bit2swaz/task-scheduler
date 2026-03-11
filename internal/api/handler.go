// Package api provides chi-based HTTP handlers for task management,
// node inspection, health, and Prometheus metrics endpoints.
package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/golang-jwt/jwt/v5"

	"github.com/bit2swaz/task-scheduler/internal/executor"
	"github.com/bit2swaz/task-scheduler/internal/registry"
	"github.com/bit2swaz/task-scheduler/internal/scheduler"
)

// error code constants.
const (
	codeInvalid  = "INVALID_REQUEST"
	codeNotFound = "NOT_FOUND"
	codeConflict = "CONFLICT"
	codeInternal = "INTERNAL_ERROR"
	codeUnauth   = "UNAUTHORIZED"
)

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// --- service interfaces ---

// TaskStore is the subset of the task registry used by the handler.
type TaskStore interface {
	Create(ctx context.Context, task registry.Task) error
	Get(ctx context.Context, id string) (*registry.Task, error)
	Update(ctx context.Context, task registry.Task) error
	Delete(ctx context.Context, id string) error
	ListAll(ctx context.Context) ([]registry.Task, error)
}

// NodeAssigner maps a task ID to the responsible node via consistent hashing.
type NodeAssigner interface {
	Get(key string) string
}

// TaskRunner executes tasks and retrieves execution logs.
type TaskRunner interface {
	Execute(ctx context.Context, task registry.Task) error
	GetLogs(ctx context.Context, taskID string, limit int) ([]executor.ExecutionLog, error)
}

// NodeLister returns cluster node membership.
type NodeLister interface {
	ListNodes(ctx context.Context) ([]string, error)
}

// LeaderChecker reports leadership state.
type LeaderChecker interface {
	IsLeader() bool
	CurrentLeader(ctx context.Context) (string, error)
}

// ScheduleCounter reports how many tasks are scheduled on this node.
type ScheduleCounter interface {
	ScheduledCount() int
}

// --- DTOs ---

type createTaskRequest struct {
	Name     string `json:"name"`
	CronExpr string `json:"cron_expr"`
	Endpoint string `json:"endpoint"`
	Payload  string `json:"payload"`
	Enabled  bool   `json:"enabled"`
}

type updateTaskRequest struct {
	Name     *string `json:"name,omitempty"`
	CronExpr *string `json:"cron_expr,omitempty"`
	Endpoint *string `json:"endpoint,omitempty"`
	Payload  *string `json:"payload,omitempty"`
	Enabled  *bool   `json:"enabled,omitempty"`
}

type taskResponse struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	CronExpr     string    `json:"cron_expr"`
	Endpoint     string    `json:"endpoint"`
	Payload      string    `json:"payload"`
	Enabled      bool      `json:"enabled"`
	AssignedNode string    `json:"assigned_node"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type tokenRequest struct {
	NodeID string `json:"node_id"`
	Secret string `json:"secret"`
}

// --- Handler ---

// Config holds all dependencies required to create a Handler.
type Config struct {
	Tasks     TaskStore
	Ring      NodeAssigner
	Exec      TaskRunner
	Nodes     NodeLister
	Election  LeaderChecker
	Sched     ScheduleCounter
	JWTSecret string
	NodeID    string
}

// Handler holds all service dependencies for HTTP request handling.
type Handler struct {
	tasks     TaskStore
	ring      NodeAssigner
	exec      TaskRunner
	nodes     NodeLister
	election  LeaderChecker
	sched     ScheduleCounter
	jwtSecret string
	nodeID    string
	startedAt time.Time
}

// NewHandler creates a Handler from the provided Config.
func NewHandler(cfg Config) *Handler {
	return &Handler{
		tasks:     cfg.Tasks,
		ring:      cfg.Ring,
		exec:      cfg.Exec,
		nodes:     cfg.Nodes,
		election:  cfg.Election,
		sched:     cfg.Sched,
		jwtSecret: cfg.JWTSecret,
		nodeID:    cfg.NodeID,
		startedAt: time.Now(),
	}
}

// NewRouter builds the chi router with all routes mounted.
// Public routes: /health, /auth/token.
// Protected routes: /tasks/*, /nodes — require a valid Bearer JWT.
func NewRouter(h *Handler) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(responseRequestID)
	r.Use(middleware.Recoverer)

	// public — no auth required
	r.Post("/auth/token", h.issueToken)
	r.Get("/health", h.health)

	// protected — JWT required
	r.Group(func(r chi.Router) {
		r.Use(JWTMiddleware(h.jwtSecret))
		r.Route("/tasks", func(r chi.Router) {
			r.Get("/", h.listTasks)
			r.Post("/", h.createTask)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", h.getTask)
				r.Put("/", h.updateTask)
				r.Delete("/", h.deleteTask)
				r.Post("/run", h.runTask)
				r.Get("/logs", h.taskLogs)
			})
		})
		r.Get("/nodes", h.listNodes)
	})

	return r
}

// --- helpers ---

// responseRequestID copies the chi request ID from the context into the
// X-Request-Id response header so callers can correlate requests.
func responseRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reqID := middleware.GetReqID(r.Context()); reqID != "" {
			w.Header().Set(middleware.RequestIDHeader, reqID)
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiError{Code: code, Message: msg})
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func taskToResponse(t registry.Task, assignedNode string) taskResponse {
	return taskResponse{
		ID:           t.ID,
		Name:         t.Name,
		CronExpr:     t.CronExpr,
		Endpoint:     t.Endpoint,
		Payload:      t.Payload,
		Enabled:      t.Enabled,
		AssignedNode: assignedNode,
		CreatedAt:    t.CreatedAt,
		UpdatedAt:    t.UpdatedAt,
	}
}

// --- handlers ---

func (h *Handler) issueToken(w http.ResponseWriter, r *http.Request) {
	var req tokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalid, "invalid request body")
		return
	}
	if req.Secret != h.jwtSecret {
		writeError(w, http.StatusUnauthorized, codeUnauth, "invalid secret")
		return
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"node_id": req.NodeID,
		"exp":     time.Now().Add(24 * time.Hour).Unix(),
		"iat":     time.Now().Unix(),
	})
	signed, err := token.SignedString([]byte(h.jwtSecret))
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "could not sign token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": signed})
}

func (h *Handler) createTask(w http.ResponseWriter, r *http.Request) {
	var req createTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalid, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, codeInvalid, "name is required")
		return
	}
	if req.Endpoint == "" {
		writeError(w, http.StatusBadRequest, codeInvalid, "endpoint is required")
		return
	}
	if req.CronExpr == "" {
		writeError(w, http.StatusBadRequest, codeInvalid, "cron_expr is required")
		return
	}
	if err := scheduler.ValidateCronExpr(req.CronExpr); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalid, fmt.Sprintf("invalid cron_expr: %s", err.Error()))
		return
	}
	task := registry.Task{
		ID:       randHex(8),
		Name:     req.Name,
		CronExpr: req.CronExpr,
		Endpoint: req.Endpoint,
		Payload:  req.Payload,
		Enabled:  req.Enabled,
	}
	if err := h.tasks.Create(r.Context(), task); err != nil {
		if errors.Is(err, registry.ErrTaskAlreadyExists) {
			writeError(w, http.StatusConflict, codeConflict, "task already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, taskToResponse(task, h.ring.Get(task.ID)))
}

func (h *Handler) listTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := h.tasks.ListAll(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	resp := make([]taskResponse, len(tasks))
	for i, t := range tasks {
		resp[i] = taskToResponse(t, h.ring.Get(t.ID))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) getTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	task, err := h.tasks.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, registry.ErrTaskNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "task not found")
			return
		}
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, taskToResponse(*task, h.ring.Get(task.ID)))
}

func (h *Handler) updateTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	task, err := h.tasks.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, registry.ErrTaskNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "task not found")
			return
		}
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	var req updateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalid, "invalid request body")
		return
	}
	if req.Name != nil {
		task.Name = *req.Name
	}
	if req.CronExpr != nil {
		if err := scheduler.ValidateCronExpr(*req.CronExpr); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalid, fmt.Sprintf("invalid cron_expr: %s", err.Error()))
			return
		}
		task.CronExpr = *req.CronExpr
	}
	if req.Endpoint != nil {
		task.Endpoint = *req.Endpoint
	}
	if req.Payload != nil {
		task.Payload = *req.Payload
	}
	if req.Enabled != nil {
		task.Enabled = *req.Enabled
	}
	task.UpdatedAt = time.Now().UTC()
	if err := h.tasks.Update(r.Context(), *task); err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, taskToResponse(*task, h.ring.Get(task.ID)))
}

func (h *Handler) deleteTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.tasks.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) runTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	task, err := h.tasks.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, registry.ErrTaskNotFound) {
			writeError(w, http.StatusNotFound, codeNotFound, "task not found")
			return
		}
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	execID := randHex(16)
	if err := h.exec.Execute(r.Context(), *task); err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"exec_id": execID})
}

func (h *Handler) taskLogs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	logs, err := h.exec.GetLogs(r.Context(), id, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	if logs == nil {
		logs = []executor.ExecutionLog{}
	}
	writeJSON(w, http.StatusOK, logs)
}

func (h *Handler) listNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.nodes.ListNodes(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	leader, err := h.election.CurrentLeader(r.Context())
	if err != nil {
		leader = ""
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"nodes":  nodes,
		"leader": leader,
	})
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"node_id":        h.nodeID,
		"is_leader":      h.election.IsLeader(),
		"tasks_assigned": h.sched.ScheduledCount(),
		"uptime_seconds": int64(time.Since(h.startedAt).Seconds()),
	})
}
