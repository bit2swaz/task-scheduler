// Package metrics registers and exposes Prometheus counters, histograms,
// and gauges for the distributed task scheduler.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds all instrumentation collectors for the scheduler.
type Metrics struct {
	gatherer      prometheus.Gatherer
	tasksExecuted *prometheus.CounterVec
	tasksFailed   *prometheus.CounterVec
	taskDuration  *prometheus.HistogramVec
	isLeader      prometheus.Gauge
	activeTasks   prometheus.Gauge
	nodesTotal    prometheus.Gauge
}

// New creates a Metrics instance that registers into the provided reg/gatherer.
// Use prometheus.NewRegistry() in tests; use prometheus.DefaultRegisterer /
// prometheus.DefaultGatherer in production (via NewDefault).
func New(reg prometheus.Registerer, gatherer prometheus.Gatherer) *Metrics {
	m := &Metrics{
		gatherer: gatherer,
		tasksExecuted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "scheduler_tasks_executed_total",
			Help: "total tasks executed successfully",
		}, []string{"task"}),
		tasksFailed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "scheduler_tasks_failed_total",
			Help: "total task executions that failed",
		}, []string{"task"}),
		taskDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "scheduler_task_duration_seconds",
			Help:    "task execution duration in seconds",
			Buckets: prometheus.DefBuckets,
		}, []string{"task"}),
		isLeader: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "scheduler_is_leader",
			Help: "1 if this node is the current leader, 0 otherwise",
		}),
		activeTasks: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "scheduler_active_tasks_total",
			Help: "number of tasks currently scheduled on this node",
		}),
		nodesTotal: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "scheduler_nodes_total",
			Help: "number of nodes in the cluster",
		}),
	}
	reg.MustRegister(
		m.tasksExecuted,
		m.tasksFailed,
		m.taskDuration,
		m.isLeader,
		m.activeTasks,
		m.nodesTotal,
	)
	return m
}

// NewDefault creates a Metrics instance that registers into the global default registry.
func NewDefault() *Metrics {
	return New(prometheus.DefaultRegisterer, prometheus.DefaultGatherer)
}

// RecordExecution records a task execution result and its duration.
func (m *Metrics) RecordExecution(taskName string, d time.Duration, success bool) {
	if success {
		m.tasksExecuted.WithLabelValues(taskName).Inc()
	} else {
		m.tasksFailed.WithLabelValues(taskName).Inc()
	}
	m.taskDuration.WithLabelValues(taskName).Observe(d.Seconds())
}

// SetLeader updates the is_leader gauge (1 = leader, 0 = standby).
func (m *Metrics) SetLeader(is bool) {
	if is {
		m.isLeader.Set(1)
	} else {
		m.isLeader.Set(0)
	}
}

// SetActiveTasks updates the active_tasks_total gauge.
func (m *Metrics) SetActiveTasks(n int) {
	m.activeTasks.Set(float64(n))
}

// SetNodesTotal updates the nodes_total gauge.
func (m *Metrics) SetNodesTotal(n int) {
	m.nodesTotal.Set(float64(n))
}

// Handler returns an http.Handler that serves Prometheus metrics in text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.gatherer, promhttp.HandlerOpts{})
}
