package schedule

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/abhisheksinghvi09/task-scheduler/internal/metrics"
	"github.com/abhisheksinghvi09/task-scheduler/internal/task"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// dueRow is one schedule locked for firing.
type dueRow struct {
	id            string
	cronExpr      string
	timezone      string
	taskType      string
	payload       json.RawMessage
	priority      int
	maxAttempts   int
	catchupPolicy string
	nextRunAt     time.Time
}

// FireDue locks every schedule whose next_run_at has passed, fires
// whatever its catchup_policy calls for, and advances next_run_at past
// now. Safe to call from multiple coordinator instances concurrently --
// FOR UPDATE SKIP LOCKED means each due schedule is claimed by exactly one
// caller per tick. The row lock is held by tx for the whole call; task
// inserts go through the shared pool (a separate connection is fine --
// what matters is that next_run_at only advances, releasing the lock on
// commit, after its fires have already succeeded or been recorded).
func FireDue(ctx context.Context, db *pgxpool.Pool) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin fire-due transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
SELECT id, cron_expr, timezone, task_type, payload, priority, max_attempts, catchup_policy, next_run_at
FROM schedules WHERE NOT paused AND next_run_at <= now()
FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return fmt.Errorf("query due schedules: %w", err)
	}

	var due []dueRow
	for rows.Next() {
		var d dueRow
		if err := rows.Scan(&d.id, &d.cronExpr, &d.timezone, &d.taskType, &d.payload,
			&d.priority, &d.maxAttempts, &d.catchupPolicy, &d.nextRunAt); err != nil {
			rows.Close()
			return fmt.Errorf("scan due schedule: %w", err)
		}
		due = append(due, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate due schedules: %w", err)
	}

	for _, d := range due {
		if err := fireOne(ctx, tx, d); err != nil {
			slog.Error("schedule: fire failed", "schedule_id", d.id, "error", err)
		}
	}

	return tx.Commit(ctx)
}

// fireOne fires d according to its catchup policy and advances
// next_run_at past now.
//
//   - skip (default): advance past every missed window without firing --
//     nobody wants 400 backlogged hourly jobs firing at once after
//     downtime. Each skipped window increments cron_missed_windows_total.
//   - one: fire once, for the most recent missed window, then advance.
//   - all: fire every missed window in order, hard-capped at
//     maxCatchupFires so a month of downtime can't wedge the firing loop.
func fireOne(ctx context.Context, tx pgx.Tx, d dueRow) error {
	sched, err := ValidateCronExpr(d.cronExpr)
	if err != nil {
		return fmt.Errorf("re-parse stored cron expr: %w", err)
	}
	loc, err := ValidateTimezone(d.timezone)
	if err != nil {
		return fmt.Errorf("re-resolve stored timezone: %w", err)
	}

	now := time.Now()

	// Walk forward from next_run_at collecting every occurrence that has
	// already passed. Under completely normal operation this collects
	// exactly one -- the schedule's own tick just came due -- which must
	// always fire regardless of catchup_policy; there is nothing to "catch
	// up" on. catchup_policy only governs what happens to the *extra*
	// occurrences when more than one has passed (a real outage), which is
	// why this can't just loop "while due, apply policy": that would treat
	// every single ordinary tick as a missed window and a skip-policy
	// schedule would never fire.
	var occurrences []time.Time
	cursor := d.nextRunAt
	for !cursor.After(now) && len(occurrences) < maxCatchupFires {
		occurrences = append(occurrences, cursor)
		cursor = sched.Next(cursor.In(loc))
	}

	var runsToFire []time.Time
	switch {
	case len(occurrences) <= 1:
		runsToFire = occurrences
	case CatchupPolicy(d.catchupPolicy) == CatchupAll:
		runsToFire = occurrences
	case CatchupPolicy(d.catchupPolicy) == CatchupOne:
		runsToFire = occurrences[len(occurrences)-1:]
		metrics.CronMissedWindowsTotal.WithLabelValues(d.id).Add(float64(len(occurrences) - 1))
	default: // CatchupSkip
		metrics.CronMissedWindowsTotal.WithLabelValues(d.id).Add(float64(len(occurrences)))
	}

	var lastFired time.Time
	for _, runAt := range runsToFire {
		idempotencyKey := fmt.Sprintf("sched:%s:%d", d.id, runAt.Unix())
		_, overlapped, err := task.EnqueueScheduled(ctx, tx, task.New{
			Type:           d.taskType,
			Payload:        d.payload,
			ScheduledAt:    runAt,
			Priority:       d.priority,
			MaxAttempts:    d.maxAttempts,
			IdempotencyKey: idempotencyKey,
		}, d.id)
		if err != nil {
			return fmt.Errorf("fire run at %s: %w", runAt, err)
		}
		if overlapped {
			slog.Warn("schedule: skipped firing, a previous run is still active", "schedule_id", d.id, "run_at", runAt)
		}
		lastFired = runAt
	}

	if _, err := tx.Exec(ctx, "UPDATE schedules SET next_run_at = $2, last_run_at = COALESCE($3, last_run_at), updated_at = now() WHERE id = $1",
		d.id, cursor, nullableTime(lastFired)); err != nil {
		return fmt.Errorf("advance next_run_at: %w", err)
	}
	return nil
}

func nullableTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
