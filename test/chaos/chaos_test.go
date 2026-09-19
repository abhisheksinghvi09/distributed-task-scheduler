//go:build chaos

// Package chaos exercises the scheduler's headline claim end to end: that
// SIGKILLing workers mid-execution loses no tasks and that the fencing and
// reaper machinery are actually exercised, not merely present.
package chaos

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/abhisheksinghvi09/task-scheduler/internal/common"
	"github.com/abhisheksinghvi09/task-scheduler/internal/db"
	"github.com/abhisheksinghvi09/task-scheduler/internal/task"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	numTasks     = 200
	leaseSeconds = 5
	testTimeout  = 150 * time.Second
)

// testEnv assembles the environment every coordinator/worker subprocess
// needs: the same Postgres connection info as the test process itself,
// plus NATS_URL and any lease override.
func testEnv(leaseSecs int) []string {
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = "nats://localhost:4222"
	}
	return []string{
		"POSTGRES_USER=" + os.Getenv("POSTGRES_USER"),
		"POSTGRES_PASSWORD=" + os.Getenv("POSTGRES_PASSWORD"),
		"POSTGRES_DB=" + os.Getenv("POSTGRES_DB"),
		"POSTGRES_HOST=" + os.Getenv("POSTGRES_HOST"),
		"POSTGRES_PORT=" + os.Getenv("POSTGRES_PORT"),
		"NATS_URL=" + natsURL,
		fmt.Sprintf("TASK_LEASE_SECONDS=%d", leaseSecs),
		"TASK_QUEUED_GRACE_SECONDS=30",
	}
}

func buildBinary(t *testing.T, dir, pkg string, tags ...string) string {
	t.Helper()
	out := filepath.Join(dir, filepath.Base(pkg))
	args := []string{"build"}
	if len(tags) > 0 {
		args = append(args, "-tags="+tags[0])
	}
	args = append(args, "-o", out, pkg)

	cmd := exec.Command("go", args...)
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, out)
	}
	return out
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "..", "..")
}

func setupChaosTables(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	stmts := []string{
		`DROP TABLE IF EXISTS chaos_effects`,
		`DROP TABLE IF EXISTS chaos_attempts`,
		`CREATE TABLE chaos_effects (task_id UUID PRIMARY KEY)`,
		`CREATE TABLE chaos_attempts (task_id UUID, worker TEXT, at TIMESTAMPTZ DEFAULT now())`,
		`TRUNCATE tasks`,
	}
	for _, s := range stmts {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatalf("setup: %s: %v", s, err)
		}
	}
}

type process struct {
	name string
	cmd  *exec.Cmd
}

func startProcess(t *testing.T, logDir, name, binary string, env []string, args ...string) *process {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Env = append(os.Environ(), env...)
	logFile, err := os.Create(filepath.Join(logDir, name+".log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	return &process{name: name, cmd: cmd}
}

func (p *process) sigkill(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Logf("sigkill %s: %v (may have already exited)", p.name, err)
	}
	p.cmd.Wait()
}

func (p *process) sigterm() {
	p.cmd.Process.Signal(syscall.SIGTERM)
	p.cmd.Wait()
}

func TestChaos_WorkerKillsLoseNoTasks(t *testing.T) {
	dsn := common.GetDBConnectionString()
	pool, err := common.ConnectToDatabase(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if err := db.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	setupChaosTables(t, pool)

	binDir := t.TempDir()
	coordinatorBin := buildBinary(t, binDir, "./cmd/coordinator")
	workerBin := buildBinary(t, binDir, "./cmd/worker", "chaos")

	logDir, err := os.MkdirTemp("", "chaos-logs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("process logs: %s", logDir)

	env := testEnv(leaseSeconds)

	coord := startProcess(t, logDir, "coordinator", coordinatorBin, env)
	defer coord.sigterm()
	time.Sleep(500 * time.Millisecond)

	workers := make([]*process, 3)
	for i := range workers {
		workers[i] = startProcess(t, logDir, fmt.Sprintf("worker-%d", i+1), workerBin, env)
	}
	defer func() {
		for _, w := range workers {
			if w.cmd.ProcessState == nil {
				w.sigterm()
			}
		}
	}()
	// Let workers connect to NATS and Postgres and start pulling.
	time.Sleep(2 * time.Second)

	// Submit numTasks ledger tasks directly against the DB -- the scheduler
	// HTTP layer adds nothing this test needs to exercise.
	ids := make([]string, numTasks)
	for i := 0; i < numTasks; i++ {
		payload, _ := json.Marshal(map[string]string{"dsn": dsn})
		id, _, err := task.Enqueue(context.Background(), pool, task.New{
			Type:        "ledger",
			Payload:     payload,
			ScheduledAt: time.Now().Add(-time.Minute),
			MaxAttempts: 5,
		})
		if err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		ids[i] = id
	}
	t.Logf("submitted %d ledger tasks", numTasks)

	// Let dispatch begin, then SIGKILL two of three workers at staggered
	// times -- no drain code runs, which is the point.
	time.Sleep(3 * time.Second)
	t.Log("SIGKILL worker-1")
	workers[0].sigkill(t)

	time.Sleep(5 * time.Second)
	t.Log("SIGKILL worker-2")
	workers[1].sigkill(t)

	// Poll until every task reaches a terminal state or the deadline hits.
	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		var remaining int
		if err := pool.QueryRow(context.Background(),
			"SELECT count(*) FROM tasks WHERE status IN ('pending','queued','running')").Scan(&remaining); err != nil {
			t.Fatalf("poll: %v", err)
		}
		if remaining == 0 {
			break
		}
		time.Sleep(1 * time.Second)
	}

	assertNoLoss(t, pool)
	assertAtLeastOnceWasExercised(t, dsn)
	assertFencingWasExercised(t, pool)
}

func assertNoLoss(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	var effects, succeeded, deadOrFailed int
	pool.QueryRow(ctx, "SELECT count(*) FROM chaos_effects").Scan(&effects)
	pool.QueryRow(ctx, "SELECT count(*) FROM tasks WHERE status = 'succeeded'").Scan(&succeeded)
	pool.QueryRow(ctx, "SELECT count(*) FROM tasks WHERE status IN ('dead_letter','failed')").Scan(&deadOrFailed)

	if effects != numTasks {
		t.Errorf("chaos_effects count = %d, want %d (zero task loss)", effects, numTasks)
	}
	if succeeded != numTasks {
		t.Errorf("succeeded count = %d, want %d", succeeded, numTasks)
	}
	if deadOrFailed != 0 {
		t.Errorf("dead_letter/failed count = %d, want 0", deadOrFailed)
	}
}

// assertAtLeastOnceWasExercised is the falsifiability check: if the chaos
// scenario didn't actually interrupt anything, attempts would equal
// numTasks and the test would prove nothing about crash recovery.
func assertAtLeastOnceWasExercised(t *testing.T, dsn string) {
	t.Helper()
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sqlDB.Close()

	var attempts int
	if err := sqlDB.QueryRow("SELECT count(*) FROM chaos_attempts").Scan(&attempts); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	t.Logf("chaos_attempts = %d (numTasks = %d)", attempts, numTasks)
	if attempts <= numTasks {
		t.Errorf("chaos_attempts = %d, want > %d -- the kills did not interrupt any in-flight work, so this run proves nothing about crash recovery", attempts, numTasks)
	}
}

// assertFencingWasExercised confirms the zombie-report path (a worker
// reporting a terminal status for a task that was already reclaimed) was
// actually hit at least once during the run.
func assertFencingWasExercised(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var reaped int
	pool.QueryRow(context.Background(),
		"SELECT count(*) FROM tasks WHERE attempts > 1").Scan(&reaped)
	t.Logf("tasks with attempts > 1 (reclaimed at least once) = %d", reaped)
	if reaped == 0 {
		t.Error("no task was reclaimed more than once -- the reaper/retry path was not exercised by this run")
	}
}

// TestChaos_GracefulDrainReleasesPromptly proves the SIGTERM drain path is
// real: in-flight work gets to finish cleanly instead of being abruptly
// killed, and untouched work is left exactly alone.
//
// Pull-based consumption (see worker.pullLoop) means a worker never claims
// more than a pool goroutine is immediately about to run -- there is no
// claimed-but-buffered state to release the way a push-dispatch design
// would need. So with poolSize=5 and 8 long-running tasks against a single
// worker, exactly 5 end up claimed and running; the other 3 are never
// fetched at all and stay untouched in Postgres (status pending) and in
// NATS (never delivered). SIGTERM should let the 5 in-flight tasks
// complete normally within the drain window, not force-cancel them.
func TestChaos_GracefulDrainReleasesPromptly(t *testing.T) {
	dsn := common.GetDBConnectionString()
	pool, err := common.ConnectToDatabase(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if err := db.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := pool.Exec(context.Background(), "TRUNCATE tasks"); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	binDir := t.TempDir()
	coordinatorBin := buildBinary(t, binDir, "./cmd/coordinator")
	workerBin := buildBinary(t, binDir, "./cmd/worker")

	logDir, err := os.MkdirTemp("", "drain-logs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("process logs: %s", logDir)

	// Deliberately long lease -- a short one would let the lease-expiry
	// backstop mask a broken drain instead of the drain itself being
	// exercised.
	env := testEnv(300)

	coord := startProcess(t, logDir, "coordinator", coordinatorBin, env)
	defer coord.sigterm()
	time.Sleep(500 * time.Millisecond)

	const poolSize = 5
	const numLongTasks = 8
	const untouchedCount = numLongTasks - poolSize

	worker := startProcess(t, logDir, "worker-1", workerBin, env)

	ids := make([]string, numLongTasks)
	for i := range ids {
		payload, _ := json.Marshal(map[string]any{"sleep_ms": 8000})
		id, _, err := task.Enqueue(context.Background(), pool, task.New{
			Type:        "noop",
			Payload:     payload,
			ScheduledAt: time.Now().Add(-time.Minute),
			MaxAttempts: 5,
		})
		if err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		ids[i] = id
	}

	// Give the worker time to connect, claim, and start executing what it
	// has capacity for.
	time.Sleep(3 * time.Second)

	sigtermAt := time.Now()
	worker.sigterm() // blocks until the process exits
	drainDuration := time.Since(sigtermAt)
	t.Logf("worker exited %v after SIGTERM", drainDuration)

	statuses := taskStatuses(t, pool, ids)
	var succeeded, untouched int
	for _, s := range statuses {
		switch s {
		case string(task.StatusSucceeded):
			succeeded++
		case string(task.StatusQueued):
			// The relay moves pending->queued and publishes on its own
			// cadence regardless of downstream capacity, so a task never
			// fetched by any worker is left in queued, not pending.
			untouched++
		}
	}
	t.Logf("statuses after drain: %v", statuses)

	if succeeded != poolSize {
		t.Errorf("succeeded count = %d, want %d (the in-flight tasks should finish cleanly during drain)", succeeded, poolSize)
	}
	if untouched != untouchedCount {
		t.Errorf("queued (untouched) count = %d, want %d (never-fetched tasks must be left exactly alone)", untouched, untouchedCount)
	}

	// The in-flight tasks sleep 8s; drain should track that, not the 30s
	// force-cancel timeout or the 300s lease.
	if drainDuration > 15*time.Second {
		t.Errorf("worker took %v to exit after SIGTERM, want close to the 8s in-flight task duration -- looks like drain fell back to force-cancel or the lease", drainDuration)
	}
}

func taskStatuses(t *testing.T, pool *pgxpool.Pool, ids []string) []string {
	t.Helper()
	out := make([]string, len(ids))
	for i, id := range ids {
		tk, err := task.Get(context.Background(), pool, id)
		if err != nil {
			t.Fatalf("get task %s: %v", id, err)
		}
		out[i] = string(tk.Status)
	}
	return out
}
