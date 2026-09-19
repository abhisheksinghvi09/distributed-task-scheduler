//go:build integration

package schedule

import (
	"context"
	"testing"
	"time"

	"github.com/abhisheksinghvi09/task-scheduler/internal/common"
	"github.com/abhisheksinghvi09/task-scheduler/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := common.ConnectToDatabase(context.Background(), common.GetDBConnectionString())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := db.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "TRUNCATE tasks, schedules CASCADE")
		pool.Close()
	})
	pool.Exec(context.Background(), "TRUNCATE tasks, schedules CASCADE")
	return pool
}

func TestFireDue_FiresATaskWhenDue(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	id, err := Create(ctx, pool, New{
		Name:     "every-minute",
		CronExpr: "* * * * *",
		TaskType: "noop",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Force it due right now rather than waiting for a real minute boundary.
	if _, err := pool.Exec(ctx, "UPDATE schedules SET next_run_at = now() - interval '1 second' WHERE id = $1", id); err != nil {
		t.Fatalf("backdate next_run_at: %v", err)
	}

	if err := FireDue(ctx, pool); err != nil {
		t.Fatalf("FireDue: %v", err)
	}

	var taskCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM tasks WHERE schedule_id = $1", id).Scan(&taskCount); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if taskCount != 1 {
		t.Fatalf("tasks for schedule = %d, want 1", taskCount)
	}

	sched, err := Get(ctx, pool, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !sched.NextRunAt.After(time.Now()) {
		t.Fatalf("next_run_at = %v, want a time in the future", sched.NextRunAt)
	}
	if sched.LastRunAt == nil {
		t.Fatal("last_run_at was not set after firing")
	}
}

func TestFireDue_PreventsOverlap(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	id, err := Create(ctx, pool, New{
		Name:     "overlap-test",
		CronExpr: "* * * * *",
		TaskType: "noop",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE schedules SET next_run_at = now() - interval '1 second' WHERE id = $1", id); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	if err := FireDue(ctx, pool); err != nil {
		t.Fatalf("FireDue (first): %v", err)
	}

	// Force it due again immediately, while the first run is still pending
	// -- the partial unique index must prevent a second concurrent run.
	if _, err := pool.Exec(ctx, "UPDATE schedules SET next_run_at = now() - interval '1 second' WHERE id = $1", id); err != nil {
		t.Fatalf("backdate again: %v", err)
	}
	if err := FireDue(ctx, pool); err != nil {
		t.Fatalf("FireDue (second): %v", err)
	}

	var taskCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM tasks WHERE schedule_id = $1", id).Scan(&taskCount); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if taskCount != 1 {
		t.Fatalf("tasks for schedule after two due cycles = %d, want 1 (overlap must be prevented)", taskCount)
	}
}

func TestFireDue_SkipCatchupAdvancesWithoutFiring(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	id, err := Create(ctx, pool, New{
		Name:          "skip-catchup",
		CronExpr:      "* * * * *",
		TaskType:      "noop",
		CatchupPolicy: CatchupSkip,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Simulate a long outage: many missed minutes in the past.
	if _, err := pool.Exec(ctx, "UPDATE schedules SET next_run_at = now() - interval '1 hour' WHERE id = $1", id); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	if err := FireDue(ctx, pool); err != nil {
		t.Fatalf("FireDue: %v", err)
	}

	var taskCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM tasks WHERE schedule_id = $1", id).Scan(&taskCount); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if taskCount != 0 {
		t.Fatalf("tasks fired under catchup_policy=skip = %d, want 0", taskCount)
	}

	sched, err := Get(ctx, pool, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !sched.NextRunAt.After(time.Now()) {
		t.Fatalf("next_run_at = %v, want advanced past now despite skipping", sched.NextRunAt)
	}
}

func TestFireDue_PausedScheduleNeverFires(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	id, err := Create(ctx, pool, New{Name: "paused", CronExpr: "* * * * *", TaskType: "noop"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := SetPaused(ctx, pool, id, true); err != nil {
		t.Fatalf("SetPaused: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE schedules SET next_run_at = now() - interval '1 second' WHERE id = $1", id); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	if err := FireDue(ctx, pool); err != nil {
		t.Fatalf("FireDue: %v", err)
	}

	var taskCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM tasks WHERE schedule_id = $1", id).Scan(&taskCount); err != nil {
		t.Fatalf("count: %v", err)
	}
	if taskCount != 0 {
		t.Fatalf("paused schedule fired %d tasks, want 0", taskCount)
	}
}

func TestCreate_RejectsInvalidCron(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	if _, err := Create(ctx, pool, New{Name: "bad", CronExpr: "not a cron expression", TaskType: "noop"}); err == nil {
		t.Fatal("Create() with an invalid cron expression returned nil error")
	}
}
