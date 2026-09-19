CREATE TABLE IF NOT EXISTS triage_reports (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    summary    TEXT NOT NULL,
    task_count INT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
