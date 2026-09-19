-- Recurring schedules (cron), task dependencies (a lightweight DAG), and
-- per-task-type concurrency caps.

CREATE TABLE IF NOT EXISTS schedules (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name           TEXT NOT NULL,
    cron_expr      TEXT NOT NULL,
    timezone       TEXT NOT NULL DEFAULT 'UTC',
    task_type      TEXT NOT NULL,
    payload        JSONB NOT NULL DEFAULT '{}'::jsonb,
    priority       SMALLINT NOT NULL DEFAULT 0,
    max_attempts   INT NOT NULL DEFAULT 3,
    catchup_policy TEXT NOT NULL DEFAULT 'skip' CHECK (catchup_policy IN ('skip', 'one', 'all')),
    next_run_at    TIMESTAMPTZ NOT NULL,
    last_run_at    TIMESTAMPTZ,
    paused         BOOLEAN NOT NULL DEFAULT false,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (name)
);

CREATE INDEX IF NOT EXISTS idx_schedules_due ON schedules (next_run_at) WHERE NOT paused;

ALTER TABLE tasks ADD COLUMN IF NOT EXISTS schedule_id UUID REFERENCES schedules(id);

-- Overlap prevention for a schedule's own runs, enforced by the database
-- rather than app-level locking: a second concurrent run for the same
-- schedule is a unique-constraint violation, not a race to check-then-act.
CREATE UNIQUE INDEX IF NOT EXISTS idx_one_active_run_per_schedule ON tasks (schedule_id)
    WHERE schedule_id IS NOT NULL AND status IN ('pending', 'queued', 'running');

-- Task dependencies: a task becomes eligible only once every id in
-- depends_on has succeeded. Deliberately not a full DAG engine -- no
-- cycle detection (a cycle just means the tasks stay 'pending' forever,
-- which is detectable, not prevented), no per-run identity, no reruns.
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS depends_on UUID[] NOT NULL DEFAULT '{}';
CREATE INDEX IF NOT EXISTS idx_tasks_depends_on ON tasks USING GIN (depends_on);

-- Per-task-type concurrency caps, enforced in the relay so an over-limit
-- task is never even published -- one query change instead of a
-- distributed coordination problem.
CREATE TABLE IF NOT EXISTS task_type_limits (
    task_type   TEXT PRIMARY KEY,
    max_running INT NOT NULL
);
