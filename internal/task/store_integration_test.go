//go:build integration

package task

import (
	"context"
	"encoding/json"
	"sync"
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
		t.Fatalf("connect to database: %v", err)
	}
	if err := db.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "TRUNCATE tasks")
		pool.Close()
	})
	pool.Exec(context.Background(), "TRUNCATE tasks")
	return pool
}

func TestClaim_ExclusiveUnderConcurrency(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	const numTasks = 50
	ids := make([]string, numTasks)
	for i := 0; i < numTasks; i++ {
		id, _, err := Enqueue(ctx, pool, New{Type: "noop", ScheduledAt: time.Now().Add(-time.Minute)})
		if err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		ids[i] = id
	}

	claimed, err := MarkQueued(ctx, pool, numTasks)
	if err != nil {
		t.Fatalf("MarkQueued: %v", err)
	}
	if len(claimed) != numTasks {
		t.Fatalf("MarkQueued() returned %d tasks, want %d", len(claimed), numTasks)
	}

	var mu sync.Mutex
	claimedBy := make(map[string]int)
	var wg sync.WaitGroup
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for _, c := range claimed {
				res, err := ClaimByID(ctx, pool, c.ID, "worker-x", time.Minute)
				if err == ErrAlreadyClaimed {
					continue
				}
				if err != nil {
					t.Errorf("ClaimByID: %v", err)
					continue
				}
				mu.Lock()
				claimedBy[res.ID]++
				mu.Unlock()
			}
		}(g)
	}
	wg.Wait()

	if len(claimedBy) != numTasks {
		t.Fatalf("expected %d tasks claimed exactly once, got %d distinct claims", numTasks, len(claimedBy))
	}
	for id, count := range claimedBy {
		if count != 1 {
			t.Errorf("task %s was claimed %d times, want exactly 1", id, count)
		}
	}
}

func TestComplete_FencesStaleAttempt(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	id, _, err := Enqueue(ctx, pool, New{Type: "noop", ScheduledAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := MarkQueued(ctx, pool, 1); err != nil {
		t.Fatalf("MarkQueued: %v", err)
	}
	claimed, err := ClaimByID(ctx, pool, id, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("ClaimByID: %v", err)
	}

	// A stale attempt number (as if reported by a zombie worker) must not
	// be able to mark the task complete.
	ok, err := Complete(ctx, pool, id, claimed.Attempt-1)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if ok {
		t.Fatal("Complete() with a stale attempt succeeded, want it fenced out")
	}

	got, err := Get(ctx, pool, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusRunning {
		t.Fatalf("task status = %s, want %s (fenced update must not change state)", got.Status, StatusRunning)
	}

	// The real attempt number must still succeed.
	ok, err = Complete(ctx, pool, id, claimed.Attempt)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !ok {
		t.Fatal("Complete() with the correct attempt was fenced out, want success")
	}
}

func TestFail_DeadLettersAfterMaxAttempts(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	id, _, err := Enqueue(ctx, pool, New{Type: "noop", ScheduledAt: time.Now().Add(-time.Minute), MaxAttempts: 1})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := MarkQueued(ctx, pool, 1); err != nil {
		t.Fatalf("MarkQueued: %v", err)
	}
	claimed, err := ClaimByID(ctx, pool, id, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("ClaimByID: %v", err)
	}

	dead, err := Fail(ctx, pool, id, claimed.Attempt, "boom", 0)
	if err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if !dead {
		t.Fatal("Fail() at max_attempts did not dead-letter the task")
	}

	got, err := Get(ctx, pool, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusDeadLetter {
		t.Fatalf("task status = %s, want %s", got.Status, StatusDeadLetter)
	}
	if got.LastError != "boom" {
		t.Fatalf("last_error = %q, want %q", got.LastError, "boom")
	}
}

func TestReapExpired_RecoversAbandonedLeaseAndStaleQueue(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	// A task whose lease has already expired (worker died mid-execution).
	leaseID, _, err := Enqueue(ctx, pool, New{Type: "noop", ScheduledAt: time.Now().Add(-time.Minute), MaxAttempts: 3})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := MarkQueued(ctx, pool, 1); err != nil {
		t.Fatalf("MarkQueued: %v", err)
	}
	if _, err := ClaimByID(ctx, pool, leaseID, "worker-dead", time.Minute); err != nil {
		t.Fatalf("ClaimByID: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE tasks SET lease_expires_at = now() - interval '1 second' WHERE id = $1", leaseID); err != nil {
		t.Fatalf("backdate lease: %v", err)
	}

	// A task stuck in queued (a lost dispatch message).
	queuedID, _, err := Enqueue(ctx, pool, New{Type: "noop", ScheduledAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := MarkQueued(ctx, pool, 1); err != nil {
		t.Fatalf("MarkQueued: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE tasks SET queued_at = now() - interval '10 minutes' WHERE id = $1", queuedID); err != nil {
		t.Fatalf("backdate queued_at: %v", err)
	}

	result, err := ReapExpired(ctx, pool, time.Minute)
	if err != nil {
		t.Fatalf("ReapExpired: %v", err)
	}
	if result.RequeuedFromRunning != 1 || result.RequeuedFromQueued != 1 || result.DeadLettered != 0 {
		t.Fatalf("ReapExpired() = %+v, want {RequeuedFromRunning:1 RequeuedFromQueued:1 DeadLettered:0}", result)
	}

	leaseTask, err := Get(ctx, pool, leaseID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if leaseTask.Status != StatusPending {
		t.Fatalf("lease task status = %s, want %s", leaseTask.Status, StatusPending)
	}
	if leaseTask.Attempts != 1 {
		t.Fatalf("lease task attempts = %d, want 1 (preserved, not reset)", leaseTask.Attempts)
	}

	queuedTask, err := Get(ctx, pool, queuedID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if queuedTask.Status != StatusPending {
		t.Fatalf("queued task status = %s, want %s", queuedTask.Status, StatusPending)
	}
}

func TestEnqueue_IdempotencyKeyDedupes(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	id1, created1, err := Enqueue(ctx, pool, New{Type: "noop", ScheduledAt: time.Now().Add(-time.Minute), IdempotencyKey: "dedupe-me"})
	if err != nil {
		t.Fatalf("Enqueue (first): %v", err)
	}
	if !created1 {
		t.Fatal("first Enqueue() with a fresh idempotency key reported created=false")
	}

	id2, created2, err := Enqueue(ctx, pool, New{Type: "noop", ScheduledAt: time.Now().Add(-time.Minute), IdempotencyKey: "dedupe-me"})
	if err != nil {
		t.Fatalf("Enqueue (second): %v", err)
	}
	if created2 {
		t.Fatal("second Enqueue() with the same idempotency key reported created=true")
	}
	if id1 != id2 {
		t.Fatalf("second Enqueue() returned a different id: %s != %s", id1, id2)
	}
}

func TestPayloadRoundTrips(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	payload := json.RawMessage(`{"url":"https://example.com","method":"POST"}`)
	id, _, err := Enqueue(ctx, pool, New{Type: "http_request", Payload: payload, ScheduledAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	got, err := Get(ctx, pool, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Postgres reformats JSONB on the way back out (spacing, key order is
	// preserved here but isn't guaranteed in general), so compare decoded
	// values rather than raw bytes.
	var want, have map[string]any
	if err := json.Unmarshal(payload, &want); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	if err := json.Unmarshal(got.Payload, &have); err != nil {
		t.Fatalf("unmarshal have: %v", err)
	}
	if len(want) != len(have) {
		t.Fatalf("payload = %v, want %v", have, want)
	}
	for k, v := range want {
		if have[k] != v {
			t.Fatalf("payload[%q] = %v, want %v", k, have[k], v)
		}
	}
}

func TestMarkQueued_RespectsDependency(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	parentID, _, err := Enqueue(ctx, pool, New{Type: "noop", ScheduledAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatalf("Enqueue parent: %v", err)
	}

	childID, _, err := Enqueue(ctx, pool, New{Type: "noop", ScheduledAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatalf("Enqueue child: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE tasks SET depends_on = $2 WHERE id = $1", childID, []string{parentID}); err != nil {
		t.Fatalf("set depends_on: %v", err)
	}

	// The child must not become eligible while its parent is still pending.
	claimed, err := MarkQueued(ctx, pool, 10)
	if err != nil {
		t.Fatalf("MarkQueued: %v", err)
	}
	for _, c := range claimed {
		if c.ID == childID {
			t.Fatal("MarkQueued() queued the child before its dependency succeeded")
		}
	}

	// Complete the parent, then the child should become eligible.
	parentClaimed, err := ClaimByID(ctx, pool, parentID, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("ClaimByID parent: %v", err)
	}
	if ok, err := Complete(ctx, pool, parentID, parentClaimed.Attempt); err != nil || !ok {
		t.Fatalf("Complete parent: ok=%v err=%v", ok, err)
	}

	claimed, err = MarkQueued(ctx, pool, 10)
	if err != nil {
		t.Fatalf("MarkQueued (after parent succeeded): %v", err)
	}
	found := false
	for _, c := range claimed {
		if c.ID == childID {
			found = true
		}
	}
	if !found {
		t.Fatal("MarkQueued() did not queue the child after its dependency succeeded")
	}
}

func TestBlockOrphaned_PropagatesPermanentFailure(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	parentID, _, err := Enqueue(ctx, pool, New{Type: "noop", ScheduledAt: time.Now().Add(-time.Minute), MaxAttempts: 1})
	if err != nil {
		t.Fatalf("Enqueue parent: %v", err)
	}
	childID, _, err := Enqueue(ctx, pool, New{Type: "noop", ScheduledAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatalf("Enqueue child: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE tasks SET depends_on = $2 WHERE id = $1", childID, []string{parentID}); err != nil {
		t.Fatalf("set depends_on: %v", err)
	}

	if _, err := MarkQueued(ctx, pool, 10); err != nil {
		t.Fatalf("MarkQueued: %v", err)
	}
	claimed, err := ClaimByID(ctx, pool, parentID, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("ClaimByID parent: %v", err)
	}
	dead, err := Fail(ctx, pool, parentID, claimed.Attempt, "boom", 0)
	if err != nil || !dead {
		t.Fatalf("Fail parent: dead=%v err=%v", dead, err)
	}

	blocked, err := BlockOrphaned(ctx, pool)
	if err != nil {
		t.Fatalf("BlockOrphaned: %v", err)
	}
	if blocked != 1 {
		t.Fatalf("BlockOrphaned() = %d, want 1", blocked)
	}

	childTask, err := Get(ctx, pool, childID)
	if err != nil {
		t.Fatalf("Get child: %v", err)
	}
	if childTask.Status != StatusBlocked {
		t.Fatalf("child status = %s, want %s", childTask.Status, StatusBlocked)
	}
}

func TestMarkQueued_RespectsConcurrencyCap(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, "DELETE FROM task_type_limits WHERE task_type = 'capped_type'"); err != nil {
		t.Fatalf("cleanup limit: %v", err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO task_type_limits (task_type, max_running) VALUES ('capped_type', 2)"); err != nil {
		t.Fatalf("insert limit: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM task_type_limits WHERE task_type = 'capped_type'")
	})

	for i := 0; i < 5; i++ {
		if _, _, err := Enqueue(ctx, pool, New{Type: "capped_type", ScheduledAt: time.Now().Add(-time.Minute)}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	claimed, err := MarkQueued(ctx, pool, 10)
	if err != nil {
		t.Fatalf("MarkQueued: %v", err)
	}
	if len(claimed) != 2 {
		t.Fatalf("MarkQueued() queued %d capped_type tasks, want 2 (the configured max_running)", len(claimed))
	}

	// With both slots occupied (queued counts toward the cap), a second
	// call must queue none of the remaining three.
	claimed2, err := MarkQueued(ctx, pool, 10)
	if err != nil {
		t.Fatalf("MarkQueued (second call): %v", err)
	}
	for _, c := range claimed2 {
		if c.TaskType == "capped_type" {
			t.Fatalf("MarkQueued() queued another capped_type task while the cap was already met")
		}
	}
}

func TestRequeue_OnlyFromTerminalState(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	id, _, err := Enqueue(ctx, pool, New{Type: "noop", ScheduledAt: time.Now().Add(-time.Minute), MaxAttempts: 1})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if ok, err := Requeue(ctx, pool, id); err != nil || ok {
		t.Fatalf("Requeue() on a pending task: ok=%v err=%v, want ok=false", ok, err)
	}

	if _, err := MarkQueued(ctx, pool, 10); err != nil {
		t.Fatalf("MarkQueued: %v", err)
	}
	claimed, err := ClaimByID(ctx, pool, id, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("ClaimByID: %v", err)
	}
	if _, err := Fail(ctx, pool, id, claimed.Attempt, "boom", 0); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	ok, err := Requeue(ctx, pool, id)
	if err != nil || !ok {
		t.Fatalf("Requeue() on a dead_letter task: ok=%v err=%v, want ok=true", ok, err)
	}

	got, err := Get(ctx, pool, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusPending || got.Attempts != 0 {
		t.Fatalf("after Requeue: status=%s attempts=%d, want pending/0", got.Status, got.Attempts)
	}
}

func TestCancel_ImmediateForPending(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	id, _, err := Enqueue(ctx, pool, New{Type: "noop", ScheduledAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	result, err := Cancel(ctx, pool, id)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if result != CancelledImmediately {
		t.Fatalf("Cancel() = %v, want CancelledImmediately", result)
	}

	got, err := Get(ctx, pool, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusCancelled {
		t.Fatalf("status = %s, want %s", got.Status, StatusCancelled)
	}
}

func TestCancel_FlagsRunningTask(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	id, _, err := Enqueue(ctx, pool, New{Type: "noop", ScheduledAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := MarkQueued(ctx, pool, 1); err != nil {
		t.Fatalf("MarkQueued: %v", err)
	}
	claimed, err := ClaimByID(ctx, pool, id, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("ClaimByID: %v", err)
	}

	result, err := Cancel(ctx, pool, id)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if result != CancelRequested {
		t.Fatalf("Cancel() = %v, want CancelRequested", result)
	}

	requested, err := IsCancelRequested(ctx, pool, id)
	if err != nil {
		t.Fatalf("IsCancelRequested: %v", err)
	}
	if !requested {
		t.Fatal("IsCancelRequested() = false, want true after Cancel()")
	}

	ok, err := MarkCancelled(ctx, pool, id, claimed.Attempt)
	if err != nil || !ok {
		t.Fatalf("MarkCancelled: ok=%v err=%v", ok, err)
	}
	got, err := Get(ctx, pool, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusCancelled {
		t.Fatalf("status = %s, want %s", got.Status, StatusCancelled)
	}
}

func TestEnqueueBatch_ResolvesDependencies(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	items := []BatchItem{
		{LocalRef: "a", New: New{Type: "noop", ScheduledAt: time.Now()}},
		{LocalRef: "b", DependsOnRefs: []string{"a"}, New: New{Type: "noop", ScheduledAt: time.Now()}},
	}

	refToID, err := EnqueueBatch(ctx, pool, items)
	if err != nil {
		t.Fatalf("EnqueueBatch: %v", err)
	}
	if len(refToID) != 2 {
		t.Fatalf("EnqueueBatch() returned %d ids, want 2", len(refToID))
	}

	b, err := Get(ctx, pool, refToID["b"])
	if err != nil {
		t.Fatalf("Get b: %v", err)
	}
	var deps []string
	if err := pool.QueryRow(ctx, "SELECT depends_on::text[] FROM tasks WHERE id = $1", b.ID).Scan(&deps); err != nil {
		t.Fatalf("query depends_on: %v", err)
	}
	if len(deps) != 1 || deps[0] != refToID["a"] {
		t.Fatalf("b.depends_on = %v, want [%s]", deps, refToID["a"])
	}
}

func TestEnqueueBatch_UnknownRefFails(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	items := []BatchItem{
		{LocalRef: "a", DependsOnRefs: []string{"does-not-exist"}, New: New{Type: "noop", ScheduledAt: time.Now()}},
	}
	if _, err := EnqueueBatch(ctx, pool, items); err == nil {
		t.Fatal("EnqueueBatch() with an unknown local ref returned nil error")
	}

	// Whole batch rolls back -- the item that would have succeeded on its
	// own must not be left behind.
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM tasks").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("tasks table has %d rows after a failed batch, want 0 (the transaction should have rolled back entirely)", count)
	}
}
