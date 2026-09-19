package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Execer is satisfied by both *pgxpool.Pool and pgx.Tx, letting a caller
// that already holds a transaction (the cron firing loop, which locks a
// schedules row with FOR UPDATE) run a store function on that same
// connection instead of a fresh one from the pool. This matters beyond
// style: tasks.schedule_id is a foreign key into schedules, so inserting a
// task from a *different* connection while the schedules row is locked by
// an open transaction elsewhere blocks on the FK's visibility check until
// that transaction resolves -- and if the same goroutine owns both, that
// is a guaranteed self-deadlock, not just a slow path.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// backoffSQL mirrors Backoff(attempt) in Postgres so the reaper -- which
// has no worker to ask -- can compute the same retry delay. `attempts` has
// already been incremented by the time this expression runs (both ClaimByID
// and the reaper increment before computing backoff), so it is the count
// including the failed attempt.
const backoffSQL = `LEAST(5 * POWER(2, GREATEST(attempts, 1) - 1), 600) * (0.5 + random())`

// Enqueue inserts a new task, deduplicating on idempotency_key when one is
// given. Returns created=false and the existing id if a task with the same
// key already exists -- the whole operation is one round trip.
func Enqueue(ctx context.Context, db *pgxpool.Pool, t New) (id string, created bool, err error) {
	maxAttempts := t.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	payload := t.Payload
	if payload == nil {
		payload = json.RawMessage(`{}`)
	}

	const q = `
WITH ins AS (
	INSERT INTO tasks (task_type, payload, scheduled_at, priority, max_attempts, idempotency_key, tenant_id, status)
	VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, '')::uuid, 'pending')
	ON CONFLICT (idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
	RETURNING id
)
SELECT id, true FROM ins
UNION ALL
SELECT id, false FROM tasks
	WHERE idempotency_key = NULLIF($6, '') AND NOT EXISTS (SELECT 1 FROM ins)
LIMIT 1`

	row := db.QueryRow(ctx, q, t.Type, payload, t.ScheduledAt, t.Priority, maxAttempts, t.IdempotencyKey, t.TenantID)
	if err := row.Scan(&id, &created); err != nil {
		return "", false, fmt.Errorf("enqueue task: %w", err)
	}
	return id, created, nil
}

// EnqueueScheduled inserts a task on behalf of a recurring schedule,
// tagging it with schedule_id. A partial unique index on
// tasks(schedule_id) prevents a schedule from ever having two runs
// pending/queued/running at once -- overlapped is true when this insert
// hit that constraint, which the caller should treat as "skip this firing
// cycle," not as an error to propagate.
func EnqueueScheduled(ctx context.Context, db Execer, t New, scheduleID string) (id string, overlapped bool, err error) {
	maxAttempts := t.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	payload := t.Payload
	if payload == nil {
		payload = json.RawMessage(`{}`)
	}

	const q = `
WITH ins AS (
	INSERT INTO tasks (task_type, payload, scheduled_at, priority, max_attempts, idempotency_key, schedule_id, status)
	VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, 'pending')
	ON CONFLICT (idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
	RETURNING id
)
SELECT id FROM ins
UNION ALL
SELECT id FROM tasks
	WHERE idempotency_key = NULLIF($6, '') AND NOT EXISTS (SELECT 1 FROM ins)
LIMIT 1`

	row := db.QueryRow(ctx, q, t.Type, payload, t.ScheduledAt, t.Priority, maxAttempts, t.IdempotencyKey, scheduleID)
	if err := row.Scan(&id); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_one_active_run_per_schedule" {
			return "", true, nil
		}
		return "", false, fmt.Errorf("enqueue scheduled task: %w", err)
	}
	return id, false, nil
}

// MarkQueued's eligibility query combines three concerns in one pass:
//   - basic eligibility (pending, due, FOR UPDATE SKIP LOCKED for safe
//     concurrent coordinators)
//   - DAG dependencies: a task with unmet depends_on is not eligible yet
//   - per-task-type concurrency caps, enforced here so an over-limit task
//     is never even published -- a row_number() window against the
//     already-running+queued count per type, capped in the same query
//     rather than as a distributed coordination problem
//
// Postgres forbids FOR UPDATE in the same query as a window function, so
// locking (locked) and ranking (ranked) must be separate CTEs: locked rows
// stay locked for the rest of this statement's execution regardless of
// which later CTE reads them, so splitting this way loses no safety.
const markQueuedSQL = `
WITH running_counts AS (
	SELECT task_type, count(*) AS n FROM tasks
	WHERE status IN ('queued', 'running')
	GROUP BY task_type
),
locked AS (
	SELECT id, task_type, priority, scheduled_at
	FROM tasks
	WHERE status = 'pending' AND scheduled_at <= now()
	  AND NOT EXISTS (
	      SELECT 1 FROM tasks d WHERE d.id = ANY(tasks.depends_on) AND d.status <> 'succeeded'
	  )
	ORDER BY priority DESC, scheduled_at
	FOR UPDATE SKIP LOCKED
	LIMIT 1000
),
ranked AS (
	SELECT id, task_type, priority, scheduled_at,
	       row_number() OVER (PARTITION BY task_type ORDER BY priority DESC, scheduled_at) AS rn
	FROM locked
),
capped AS (
	SELECT e.id FROM ranked e
	LEFT JOIN task_type_limits l ON l.task_type = e.task_type
	LEFT JOIN running_counts r ON r.task_type = e.task_type
	WHERE l.max_running IS NULL OR e.rn <= l.max_running - COALESCE(r.n, 0)
	ORDER BY e.priority DESC, e.scheduled_at
	LIMIT $1
)
UPDATE tasks t
SET status = 'queued', queued_at = now(), updated_at = now()
FROM capped c
WHERE t.id = c.id
RETURNING t.id, t.task_type, t.payload, t.priority, t.attempts, t.max_attempts, t.scheduled_at`

func MarkQueued(ctx context.Context, db *pgxpool.Pool, limit int) ([]Claimed, error) {
	rows, err := db.Query(ctx, markQueuedSQL, limit)
	if err != nil {
		return nil, fmt.Errorf("mark tasks queued: %w", err)
	}
	defer rows.Close()

	return scanClaimed(rows)
}

// BlockOrphaned propagates DAG failure: a pending task whose dependency
// has permanently failed (dead-lettered or cancelled) can never become
// eligible, so it is moved to blocked rather than waiting forever. This is
// deliberately one-directional and does not detect cycles -- a cyclic
// dependency simply leaves its tasks pending forever, which is observable
// (stuck in pending with no blocked/running transition), not prevented.
func BlockOrphaned(ctx context.Context, db *pgxpool.Pool) (int, error) {
	const q = `
UPDATE tasks SET status = 'blocked', last_error = 'upstream dependency failed', updated_at = now()
WHERE status = 'pending'
  AND EXISTS (
      SELECT 1 FROM tasks d WHERE d.id = ANY(tasks.depends_on) AND d.status IN ('dead_letter', 'cancelled')
  )
RETURNING id`

	tag, err := db.Exec(ctx, q)
	if err != nil {
		return 0, fmt.Errorf("block orphaned tasks: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ClaimByID is a worker's attempt to take ownership of one task it was told
// about (by gRPC today, by a NATS message later). The single
// "AND status = 'queued'" is the entire duplicate-delivery defense: at most
// one caller's UPDATE can ever match a given row.
func ClaimByID(ctx context.Context, db *pgxpool.Pool, taskID, workerID string, lease time.Duration) (Claimed, error) {
	const q = `
UPDATE tasks
SET status = 'running', worker_id = $2, attempts = attempts + 1,
    lease_expires_at = now() + make_interval(secs => $3),
    started_at = now(), updated_at = now()
WHERE id = $1 AND status = 'queued'
RETURNING id, task_type, payload, priority, attempts, max_attempts, scheduled_at`

	row := db.QueryRow(ctx, q, taskID, workerID, lease.Seconds())
	claimed, err := scanClaimedRow(row)
	if err == pgx.ErrNoRows {
		return Claimed{}, ErrAlreadyClaimed
	}
	if err != nil {
		return Claimed{}, fmt.Errorf("claim task %s: %w", taskID, err)
	}
	return claimed, nil
}

// RenewLease extends a running task's lease. ok is false if the lease was
// already lost (the reaper reclaimed it) -- the caller must then cancel
// the handler's context, since the task may already be running elsewhere.
func RenewLease(ctx context.Context, db *pgxpool.Pool, taskID, workerID string, lease time.Duration) (ok bool, err error) {
	const q = `
UPDATE tasks
SET lease_expires_at = now() + make_interval(secs => $3), updated_at = now()
WHERE id = $1 AND worker_id = $2 AND status = 'running'`

	tag, err := db.Exec(ctx, q, taskID, workerID, lease.Seconds())
	if err != nil {
		return false, fmt.Errorf("renew lease for task %s: %w", taskID, err)
	}
	return tag.RowsAffected() == 1, nil
}

// Complete marks a task succeeded. attempt must match the attempts value
// captured at claim time -- this fences out zombie reports from a worker
// whose lease already expired and was reclaimed by someone else. A false
// return means the caller no longer owns this task; it should log and
// discard, not retry.
func Complete(ctx context.Context, db *pgxpool.Pool, id string, attempt int32) (bool, error) {
	const q = `
UPDATE tasks SET status = 'succeeded', completed_at = now(),
    lease_expires_at = NULL, worker_id = NULL, last_error = NULL, updated_at = now()
WHERE id = $1 AND attempts = $2 AND status = 'running'`

	tag, err := db.Exec(ctx, q, id, attempt)
	if err != nil {
		return false, fmt.Errorf("complete task %s: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// Fail records a failed attempt. If attempts has reached max_attempts the
// task moves to dead_letter; otherwise it's requeued to pending with
// exponential backoff (or the handler's requested delay, if retryAfter is
// non-zero). dead reports whether this call dead-lettered the task.
func Fail(ctx context.Context, db *pgxpool.Pool, id string, attempt int32, errMsg string, retryAfter time.Duration) (dead bool, err error) {
	var scheduledAtExpr string
	args := []any{id, attempt, errMsg}
	if retryAfter > 0 {
		scheduledAtExpr = "now() + make_interval(secs => $4)"
		args = append(args, retryAfter.Seconds())
	} else {
		scheduledAtExpr = "now() + (" + backoffSQL + ") * interval '1 second'"
	}

	q := fmt.Sprintf(`
UPDATE tasks SET
	status = CASE WHEN attempts >= max_attempts THEN 'dead_letter' ELSE 'pending' END,
	scheduled_at = CASE WHEN attempts >= max_attempts THEN scheduled_at ELSE %s END,
	failed_at = CASE WHEN attempts >= max_attempts THEN now() ELSE failed_at END,
	last_error = left($3, 2000),
	lease_expires_at = NULL, worker_id = NULL, updated_at = now()
WHERE id = $1 AND attempts = $2 AND status = 'running'
RETURNING status = 'dead_letter'`, scheduledAtExpr)

	row := db.QueryRow(ctx, q, args...)
	if err := row.Scan(&dead); err != nil {
		if err == pgx.ErrNoRows {
			return false, nil // fenced out: zombie report, not our task anymore
		}
		return false, fmt.Errorf("fail task %s: %w", id, err)
	}
	return dead, nil
}

// FailPermanent dead-letters a task immediately, regardless of
// attempts/max_attempts -- for a handler that knows via ErrPermanent that
// no amount of retrying will help (a malformed request, an unknown
// resource). Fenced by attempt like Fail.
func FailPermanent(ctx context.Context, db *pgxpool.Pool, id string, attempt int32, errMsg string) (bool, error) {
	const q = `
UPDATE tasks SET status = 'dead_letter', failed_at = now(), last_error = left($3, 2000),
    lease_expires_at = NULL, worker_id = NULL, updated_at = now()
WHERE id = $1 AND attempts = $2 AND status = 'running'`

	tag, err := db.Exec(ctx, q, id, attempt, errMsg)
	if err != nil {
		return false, fmt.Errorf("fail permanent task %s: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// Unqueue reverts a task that was moved to queued for dispatch back to
// pending, without touching attempts (ClaimByID -- the only place attempts
// increments -- has not run yet at this point). Used when the coordinator's
// dispatch call fails outright (e.g. no worker available, connection
// refused): retrying on the very next relay tick beats waiting out the
// reaper's multi-minute queued-timeout grace, which exists as a backstop
// for lost messages, not as the primary recovery path for a failure the
// coordinator already knows about immediately. A no-op (0 rows) if the
// task already progressed past queued -- e.g. the worker actually claimed
// it and only the RPC response was lost, in which case the running-lease
// reaper is the correct recovery path instead.
func Unqueue(ctx context.Context, db *pgxpool.Pool, id string) (bool, error) {
	const q = `
UPDATE tasks SET status = 'pending', queued_at = NULL, updated_at = now()
WHERE id = $1 AND status = 'queued'`

	tag, err := db.Exec(ctx, q, id)
	if err != nil {
		return false, fmt.Errorf("unqueue task %s: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// Release returns a task to pending without consuming a retry attempt --
// used for a graceful drain (SIGTERM) or a dispatch failure, neither of
// which is the task's fault.
func Release(ctx context.Context, db *pgxpool.Pool, id string, attempt int32) (bool, error) {
	const q = `
UPDATE tasks SET status = 'pending', attempts = attempts - 1,
    lease_expires_at = NULL, worker_id = NULL, started_at = NULL, updated_at = now()
WHERE id = $1 AND attempts = $2 AND status = 'running'`

	tag, err := db.Exec(ctx, q, id, attempt)
	if err != nil {
		return false, fmt.Errorf("release task %s: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// ReapExpired recovers tasks abandoned by a dead worker (lease expired
// while running) and tasks that were queued for dispatch but never claimed
// -- the second case is what makes a lost dispatch message recoverable,
// and is the entire reason this design is safe once NATS is introduced.
// ReapResult breaks down what one reaper pass recovered, by which failure
// mode it came from -- callers (metrics in particular) need to distinguish
// a dead worker from a lost dispatch, not just a combined total.
type ReapResult struct {
	RequeuedFromRunning int // lease expired: worker died mid-execution
	RequeuedFromQueued  int // queued too long: dispatch was lost
	DeadLettered        int
}

func ReapExpired(ctx context.Context, db *pgxpool.Pool, queuedTimeout time.Duration) (ReapResult, error) {
	const reapRunning = `
UPDATE tasks SET
	status = CASE WHEN attempts >= max_attempts THEN 'dead_letter' ELSE 'pending' END,
	scheduled_at = CASE WHEN attempts >= max_attempts THEN scheduled_at
		ELSE now() + (` + backoffSQL + `) * interval '1 second' END,
	failed_at = CASE WHEN attempts >= max_attempts THEN now() ELSE failed_at END,
	last_error = 'lease expired (worker ' || COALESCE(worker_id, '?') || ', attempt ' || attempts || ')',
	lease_expires_at = NULL, worker_id = NULL, updated_at = now()
WHERE status = 'running' AND lease_expires_at < now()
RETURNING status = 'dead_letter'`

	rows, err := db.Query(ctx, reapRunning)
	if err != nil {
		return ReapResult{}, fmt.Errorf("reap expired leases: %w", err)
	}
	r1, d1, err := countReaped(rows)
	if err != nil {
		return ReapResult{}, err
	}

	const reapQueued = `
UPDATE tasks SET status = 'pending', updated_at = now()
WHERE status = 'queued' AND queued_at < now() - make_interval(secs => $1)
RETURNING false`

	rows, err = db.Query(ctx, reapQueued, queuedTimeout.Seconds())
	if err != nil {
		return ReapResult{RequeuedFromRunning: r1, DeadLettered: d1}, fmt.Errorf("reap stale queued tasks: %w", err)
	}
	r2, _, err := countReaped(rows)
	if err != nil {
		return ReapResult{RequeuedFromRunning: r1, DeadLettered: d1}, err
	}

	return ReapResult{RequeuedFromRunning: r1, RequeuedFromQueued: r2, DeadLettered: d1}, nil
}

// BatchItem is one task in a batch submission. LocalRef is a client-chosen
// short name (e.g. "a") used only to express dependencies within the same
// batch; DependsOnRefs names other items' LocalRef, resolved to real ids
// server-side. Without batch submission with local refs, task dependencies
// are unusable -- a client can't know a dependency's UUID before it exists.
type BatchItem struct {
	LocalRef      string
	DependsOnRefs []string
	New
}

// EnqueueBatch inserts every item in one transaction and resolves
// DependsOnRefs to real task ids, so a DAG is created atomically -- either
// every task in the batch exists with correct dependency links, or none
// do. Returns a map from LocalRef to the assigned task id.
func EnqueueBatch(ctx context.Context, db *pgxpool.Pool, items []BatchItem) (map[string]string, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin batch transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	refToID := make(map[string]string, len(items))
	for _, item := range items {
		maxAttempts := item.MaxAttempts
		if maxAttempts <= 0 {
			maxAttempts = 3
		}
		payload := item.Payload
		if payload == nil {
			payload = json.RawMessage(`{}`)
		}

		const insertQ = `
INSERT INTO tasks (task_type, payload, scheduled_at, priority, max_attempts, status)
VALUES ($1, $2, $3, $4, $5, 'pending') RETURNING id`

		var id string
		row := tx.QueryRow(ctx, insertQ, item.Type, payload, item.ScheduledAt, item.Priority, maxAttempts)
		if err := row.Scan(&id); err != nil {
			return nil, fmt.Errorf("enqueue batch item %q: %w", item.LocalRef, err)
		}
		if item.LocalRef != "" {
			refToID[item.LocalRef] = id
		}
	}

	for _, item := range items {
		if len(item.DependsOnRefs) == 0 {
			continue
		}
		deps := make([]string, 0, len(item.DependsOnRefs))
		for _, ref := range item.DependsOnRefs {
			depID, ok := refToID[ref]
			if !ok {
				return nil, fmt.Errorf("batch item %q depends on unknown local ref %q", item.LocalRef, ref)
			}
			deps = append(deps, depID)
		}

		const updateQ = `UPDATE tasks SET depends_on = $2 WHERE id = $1`
		if _, err := tx.Exec(ctx, updateQ, refToID[item.LocalRef], deps); err != nil {
			return nil, fmt.Errorf("set dependencies for batch item %q: %w", item.LocalRef, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit batch transaction: %w", err)
	}
	return refToID, nil
}

// Requeue resets a task to pending for immediate reprocessing, clearing
// attempts and errors. Only valid from a terminal state -- requeuing a
// task that's actively queued/running would race the system that's
// already handling it.
func Requeue(ctx context.Context, db *pgxpool.Pool, id string) (bool, error) {
	const q = `
UPDATE tasks SET status = 'pending', attempts = 0, last_error = NULL,
    scheduled_at = now(), cancel_requested = false, updated_at = now()
WHERE id = $1 AND status IN ('succeeded', 'failed', 'dead_letter', 'cancelled', 'blocked')`

	tag, err := db.Exec(ctx, q, id)
	if err != nil {
		return false, fmt.Errorf("requeue task %s: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// CancelResult reports what Cancel actually did, since "cancelled
// immediately" and "cancellation requested" are different promises to the
// caller.
type CancelResult int

const (
	CancelNotFound       CancelResult = iota
	CancelledImmediately              // was pending/queued: one UPDATE, done
	CancelRequested                   // was running: flagged; the worker's
	// lease renewer checks cancel_requested
	// on its own cadence, so this takes up
	// to one renewal interval, not less
	CancelNoop // already in a terminal state
)

// Cancel stops a task. See CancelResult for the honest ceiling on how fast
// this actually takes effect.
func Cancel(ctx context.Context, db *pgxpool.Pool, id string) (CancelResult, error) {
	const immediateQ = `
UPDATE tasks SET status = 'cancelled', updated_at = now()
WHERE id = $1 AND status IN ('pending', 'queued')`

	tag, err := db.Exec(ctx, immediateQ, id)
	if err != nil {
		return CancelNotFound, fmt.Errorf("cancel task %s: %w", id, err)
	}
	if tag.RowsAffected() == 1 {
		return CancelledImmediately, nil
	}

	const flagQ = `UPDATE tasks SET cancel_requested = true, updated_at = now() WHERE id = $1 AND status = 'running'`
	tag, err = db.Exec(ctx, flagQ, id)
	if err != nil {
		return CancelNotFound, fmt.Errorf("flag cancel for task %s: %w", id, err)
	}
	if tag.RowsAffected() == 1 {
		return CancelRequested, nil
	}

	var exists bool
	if err := db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM tasks WHERE id = $1)", id).Scan(&exists); err != nil {
		return CancelNotFound, fmt.Errorf("check task %s exists: %w", id, err)
	}
	if !exists {
		return CancelNotFound, nil
	}
	return CancelNoop, nil
}

// MarkCancelled records that a running task was stopped by request, fenced
// by attempt like Complete/Fail. Distinct from Fail: a cancelled task must
// not be retried or counted as a failure.
func MarkCancelled(ctx context.Context, db *pgxpool.Pool, id string, attempt int32) (bool, error) {
	const q = `
UPDATE tasks SET status = 'cancelled', updated_at = now(),
    lease_expires_at = NULL, worker_id = NULL
WHERE id = $1 AND attempts = $2 AND status = 'running'`

	tag, err := db.Exec(ctx, q, id, attempt)
	if err != nil {
		return false, fmt.Errorf("mark task %s cancelled: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// IsCancelRequested checks the flag Cancel sets on a running task -- the
// worker's lease renewer polls this on its own cadence to decide whether
// to cancel the handler's context.
func IsCancelRequested(ctx context.Context, db *pgxpool.Pool, id string) (bool, error) {
	var requested bool
	err := db.QueryRow(ctx, "SELECT cancel_requested FROM tasks WHERE id = $1", id).Scan(&requested)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check cancel_requested for task %s: %w", id, err)
	}
	return requested, nil
}

// Get returns a single task by id.
func Get(ctx context.Context, db *pgxpool.Pool, id string) (*Task, error) {
	const q = `
SELECT id, status, task_type, payload, attempts, max_attempts, priority,
       scheduled_at, COALESCE(last_error, ''), COALESCE(tenant_id::text, ''), created_at, updated_at
FROM tasks WHERE id = $1`

	var t Task
	err := db.QueryRow(ctx, q, id).Scan(
		&t.ID, &t.Status, &t.TaskType, &t.Payload, &t.Attempts, &t.MaxAttempts,
		&t.Priority, &t.ScheduledAt, &t.LastError, &t.TenantID, &t.CreatedAt, &t.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get task %s: %w", id, err)
	}
	return &t, nil
}

// ListFilter narrows List's result set. TenantID, when non-empty, is
// mandatory tenant isolation, not an optional convenience -- callers that
// have an authenticated tenant must always set it.
type ListFilter struct {
	TenantID string
	Status   string // "" means any
	TaskType string // "" means any
	Limit    int
	// Cursor is the (created_at, id) of the last row of a previous page,
	// formatted "<rfc3339nano>|<id>". Keyset pagination, not OFFSET --
	// stable under concurrent inserts and doesn't degrade on deep pages.
	Cursor string
}

// ListPage is one page of List's results plus the cursor for the next one.
type ListPage struct {
	Tasks      []Task
	NextCursor string // "" means no more pages
}

// List returns tasks matching filter, newest first, keyset-paginated.
func List(ctx context.Context, db *pgxpool.Pool, f ListFilter) (ListPage, error) {
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	conds := []string{"true"}
	args := []any{}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	if f.TenantID != "" {
		conds = append(conds, "tenant_id = "+arg(f.TenantID)+"::uuid")
	}
	if f.Status != "" {
		conds = append(conds, "status = "+arg(f.Status))
	}
	if f.TaskType != "" {
		conds = append(conds, "task_type = "+arg(f.TaskType))
	}
	if f.Cursor != "" {
		createdAt, id, err := decodeCursor(f.Cursor)
		if err != nil {
			return ListPage{}, fmt.Errorf("decode cursor: %w", err)
		}
		conds = append(conds, "(created_at, id) < ("+arg(createdAt)+", "+arg(id)+"::uuid)")
	}

	q := fmt.Sprintf(`
SELECT id, status, task_type, payload, attempts, max_attempts, priority,
       scheduled_at, COALESCE(last_error, ''), COALESCE(tenant_id::text, ''), created_at, updated_at
FROM tasks WHERE %s
ORDER BY created_at DESC, id DESC
LIMIT %d`, joinAnd(conds), limit+1)

	rows, err := db.Query(ctx, q, args...)
	if err != nil {
		return ListPage{}, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	var page ListPage
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Status, &t.TaskType, &t.Payload, &t.Attempts, &t.MaxAttempts,
			&t.Priority, &t.ScheduledAt, &t.LastError, &t.TenantID, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return ListPage{}, fmt.Errorf("scan task: %w", err)
		}
		page.Tasks = append(page.Tasks, t)
	}
	if err := rows.Err(); err != nil {
		return ListPage{}, fmt.Errorf("iterate tasks: %w", err)
	}

	if len(page.Tasks) > limit {
		last := page.Tasks[limit-1]
		page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
		page.Tasks = page.Tasks[:limit]
	}
	return page, nil
}

func joinAnd(conds []string) string {
	out := conds[0]
	for _, c := range conds[1:] {
		out += " AND " + c
	}
	return out
}

func encodeCursor(t time.Time, id string) string {
	return t.Format(time.RFC3339Nano) + "|" + id
}

func decodeCursor(cursor string) (string, string, error) {
	for i := len(cursor) - 1; i >= 0; i-- {
		if cursor[i] == '|' {
			return cursor[:i], cursor[i+1:], nil
		}
	}
	return "", "", fmt.Errorf("malformed cursor %q", cursor)
}

func scanClaimed(rows pgx.Rows) ([]Claimed, error) {
	var out []Claimed
	for rows.Next() {
		var c Claimed
		if err := rows.Scan(&c.ID, &c.TaskType, &c.Payload, &c.Priority, &c.Attempt, &c.MaxAttempts, &c.ScheduledAt); err != nil {
			return nil, fmt.Errorf("scan claimed task: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate claimed tasks: %w", err)
	}
	return out, nil
}

func scanClaimedRow(row pgx.Row) (Claimed, error) {
	var c Claimed
	err := row.Scan(&c.ID, &c.TaskType, &c.Payload, &c.Priority, &c.Attempt, &c.MaxAttempts, &c.ScheduledAt)
	return c, err
}

func countReaped(rows pgx.Rows) (requeued, dead int, err error) {
	defer rows.Close()
	for rows.Next() {
		var wasDead bool
		if err := rows.Scan(&wasDead); err != nil {
			return requeued, dead, fmt.Errorf("scan reaped task: %w", err)
		}
		if wasDead {
			dead++
		} else {
			requeued++
		}
	}
	if err := rows.Err(); err != nil {
		return requeued, dead, fmt.Errorf("iterate reaped tasks: %w", err)
	}
	return requeued, dead, nil
}
