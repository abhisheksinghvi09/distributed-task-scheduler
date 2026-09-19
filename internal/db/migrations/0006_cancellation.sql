-- Cancellation for pending/queued tasks is immediate (one UPDATE). For a
-- running task there is no cheap way to interrupt a worker process from
-- here, so cancel_requested is a flag the worker's lease-renewal loop
-- checks on its own cadence -- cancelling a running task therefore takes
-- up to one lease-renewal interval, not less. That ceiling is documented,
-- not hidden.
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS cancel_requested BOOLEAN NOT NULL DEFAULT false;
