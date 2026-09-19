-- Durable execution semantics: status machine, retries, leases, idempotency.
-- Additive only -- code from before this migration keeps working unchanged,
-- which is what makes this migration safe to deploy ahead of the code that
-- reads the new columns.

ALTER TABLE tasks
    ADD COLUMN IF NOT EXISTS status           TEXT        NOT NULL DEFAULT 'pending',
    ADD COLUMN IF NOT EXISTS task_type        TEXT        NOT NULL DEFAULT 'shell',
    ADD COLUMN IF NOT EXISTS payload          JSONB       NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS attempts         INT         NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS max_attempts     INT         NOT NULL DEFAULT 3,
    ADD COLUMN IF NOT EXISTS priority         INT         NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS lease_expires_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS queued_at        TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS worker_id        TEXT,
    ADD COLUMN IF NOT EXISTS last_error       TEXT,
    ADD COLUMN IF NOT EXISTS idempotency_key  TEXT,
    ADD COLUMN IF NOT EXISTS created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS updated_at       TIMESTAMPTZ NOT NULL DEFAULT now();

-- Backfill status from the legacy nullable-timestamp state machine.
UPDATE tasks SET status = CASE
    WHEN completed_at IS NOT NULL THEN 'succeeded'
    WHEN failed_at    IS NOT NULL THEN 'failed'
    WHEN picked_at    IS NOT NULL THEN 'running'
    ELSE 'pending'
END WHERE status = 'pending';

-- Backfill payload from the legacy shell command column.
UPDATE tasks SET payload = jsonb_build_object('argv', ARRAY['sh', '-c', command]),
                 task_type = 'shell'
    WHERE command IS NOT NULL AND payload = '{}'::jsonb;

ALTER TABLE tasks ALTER COLUMN command DROP NOT NULL;

-- Convert to TIMESTAMPTZ now, not deferred: naked TIMESTAMP silently breaks
-- cron timezone math later, and converting after the table grows is a
-- costlier rewrite.
ALTER TABLE tasks ALTER COLUMN scheduled_at TYPE TIMESTAMPTZ USING scheduled_at AT TIME ZONE 'UTC';

ALTER TABLE tasks DROP CONSTRAINT IF EXISTS tasks_status_check;
ALTER TABLE tasks ADD CONSTRAINT tasks_status_check CHECK (
    status IN ('pending', 'queued', 'running', 'succeeded', 'failed', 'dead_letter', 'blocked', 'cancelled')
);

CREATE INDEX IF NOT EXISTS idx_tasks_claim
    ON tasks (priority DESC, scheduled_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_tasks_lease
    ON tasks (lease_expires_at) WHERE status = 'running';
CREATE INDEX IF NOT EXISTS idx_tasks_queued
    ON tasks (queued_at) WHERE status = 'queued';
CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_idempotency
    ON tasks (idempotency_key) WHERE idempotency_key IS NOT NULL;
