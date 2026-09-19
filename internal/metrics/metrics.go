// Package metrics defines every Prometheus collector the scheduler
// exposes. Cardinality rules are enforced here, not left to callers:
// never label by task_id or an unbounded tenant set, error_class is a
// fixed enum, and task_type is bounded by the handler registry so it is
// safe to use as a label.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ErrorClass buckets a failure into one of a small fixed set of reasons --
// never the raw error string, which would be unbounded cardinality.
type ErrorClass string

const (
	ErrorClassTimeout ErrorClass = "timeout"
	ErrorClassHandler ErrorClass = "handler"
	ErrorClassPanic   ErrorClass = "panic"
	ErrorClassUnknown ErrorClass = "unknown"
)

var (
	TasksSubmittedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tasks_submitted_total",
		Help: "Tasks accepted for scheduling.",
	}, []string{"task_type", "priority"})

	TasksCompletedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tasks_completed_total",
		Help: "Tasks that reached a terminal succeeded state.",
	}, []string{"task_type"})

	TasksFailedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tasks_failed_total",
		Help: "Task execution failures, before retry/dead-letter classification.",
	}, []string{"task_type", "error_class"})

	TasksRetriedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tasks_retried_total",
		Help: "Task attempts that resulted in a retry rather than a terminal state.",
	}, []string{"task_type", "attempt"})

	TasksDeadLetteredTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tasks_dead_lettered_total",
		Help: "Tasks that exhausted max_attempts and moved to dead_letter.",
	}, []string{"task_type"})

	ReaperReclaimedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "reaper_reclaimed_total",
		Help: "Tasks reclaimed by the reaper, by the status they were reclaimed from.",
	}, []string{"from_status"})

	ReaperDeadLetteredTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "reaper_dead_lettered_total",
		Help: "Tasks the reaper moved directly to dead_letter (max attempts exhausted on reclaim).",
	})

	NATSPublishErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "nats_publish_errors_total",
		Help: "Failed attempts to publish a dispatch envelope to NATS.",
	})

	CronMissedWindowsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "cron_missed_windows_total",
		Help: "Cron windows skipped because the schedule was found already behind (catchup_policy=skip).",
	}, []string{"schedule_id"})

	TaskExecutionDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "tasks_execution_duration_seconds",
		Help:    "Wall-clock time a handler spent executing one attempt.",
		Buckets: []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300},
	}, []string{"task_type", "outcome"})

	// TaskDispatchLatency is the scheduler's core SLI: time from
	// scheduled_at to the moment execution actually began.
	TaskDispatchLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "tasks_dispatch_latency_seconds",
		Help:    "Time between a task's scheduled_at and when a worker began executing it.",
		Buckets: []float64{.01, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
	}, []string{"priority"})

	ReaperRunDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "reaper_run_duration_seconds",
		Help:    "Wall-clock time one reaper pass took.",
		Buckets: []float64{.001, .005, .01, .05, .1, .5, 1},
	})

	TasksQueueDepth = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tasks_queue_depth",
		Help: "Current number of tasks in each status.",
	}, []string{"status"})

	TasksOldestPendingAgeSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "tasks_oldest_pending_age_seconds",
		Help: "Age of the oldest task still waiting to be picked up.",
	})
)

// ClassifyError maps an arbitrary error into a bounded label value.
// Callers should prefer a specific classification (timeout, handler, panic)
// when they know it; Unknown is the safe fallback, never the raw message.
func ClassifyError(isTimeout, isPanic bool) ErrorClass {
	switch {
	case isPanic:
		return ErrorClassPanic
	case isTimeout:
		return ErrorClassTimeout
	default:
		return ErrorClassHandler
	}
}
