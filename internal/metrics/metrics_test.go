package metrics_test

import (
"net/http/httptest"
"testing"
"time"

"github.com/bit2swaz/task-scheduler/internal/metrics"
"github.com/prometheus/client_golang/prometheus"
dto "github.com/prometheus/client_model/go"
"github.com/stretchr/testify/assert"
"github.com/stretchr/testify/require"
)

// newTestMetrics creates a Metrics instance backed by an isolated registry.
func newTestMetrics(t *testing.T) (*metrics.Metrics, *prometheus.Registry) {
t.Helper()
reg := prometheus.NewRegistry()
return metrics.New(reg, reg), reg
}

// gatherFloat returns the first float64 value for the given metric family name
// and label value from the gathered registry output.
func gatherFloat(t *testing.T, reg *prometheus.Registry, family, labelVal string) float64 {
t.Helper()
mfs, err := reg.Gather()
require.NoError(t, err)
for _, mf := range mfs {
if mf.GetName() != family {
continue
}
for _, m := range mf.GetMetric() {
for _, l := range m.GetLabel() {
if l.GetValue() == labelVal {
switch mf.GetType() {
case dto.MetricType_COUNTER:
return m.GetCounter().GetValue()
case dto.MetricType_GAUGE:
return m.GetGauge().GetValue()
case dto.MetricType_HISTOGRAM:
return float64(m.GetHistogram().GetSampleCount())
}
}
}
// gauge / histogram without labels
if labelVal == "" {
switch mf.GetType() {
case dto.MetricType_GAUGE:
return m.GetGauge().GetValue()
}
}
}
}
return 0
}

func gatherGauge(t *testing.T, reg *prometheus.Registry, family string) float64 {
t.Helper()
mfs, err := reg.Gather()
require.NoError(t, err)
for _, mf := range mfs {
if mf.GetName() == family {
for _, m := range mf.GetMetric() {
return m.GetGauge().GetValue()
}
}
}
return 0
}

// T1: RecordExecution success increments scheduler_tasks_executed_total.
func TestRecordExecution_Success(t *testing.T) {
m, reg := newTestMetrics(t)
m.RecordExecution("my-task", 50*time.Millisecond, true)
assert.Equal(t, float64(1), gatherFloat(t, reg, "scheduler_tasks_executed_total", "my-task"))
}

// T2: RecordExecution failure increments scheduler_tasks_failed_total.
func TestRecordExecution_Failed(t *testing.T) {
m, reg := newTestMetrics(t)
m.RecordExecution("bad-task", 10*time.Millisecond, false)
assert.Equal(t, float64(1), gatherFloat(t, reg, "scheduler_tasks_failed_total", "bad-task"))
}

// T3: RecordExecution records one observation in the duration histogram.
func TestRecordExecution_Histogram(t *testing.T) {
m, reg := newTestMetrics(t)
m.RecordExecution("hist-task", 200*time.Millisecond, true)
assert.Equal(t, float64(1), gatherFloat(t, reg, "scheduler_task_duration_seconds", "hist-task"))
}

// T4: SetLeader true sets gauge to 1; false sets it to 0.
func TestSetLeader_Gauge(t *testing.T) {
m, reg := newTestMetrics(t)
m.SetLeader(true)
assert.Equal(t, float64(1), gatherGauge(t, reg, "scheduler_is_leader"))
m.SetLeader(false)
assert.Equal(t, float64(0), gatherGauge(t, reg, "scheduler_is_leader"))
}

// T5: Handler returns valid Prometheus text output at /metrics.
func TestHandler_ServeMetrics(t *testing.T) {
m, _ := newTestMetrics(t)
m.RecordExecution("probe-task", 1*time.Millisecond, true)
req := httptest.NewRequest("GET", "/metrics", nil)
rr := httptest.NewRecorder()
m.Handler().ServeHTTP(rr, req)
assert.Equal(t, 200, rr.Code)
assert.Contains(t, rr.Body.String(), "scheduler_tasks_executed_total")
}
