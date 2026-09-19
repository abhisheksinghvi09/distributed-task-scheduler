-- Light multi-tenancy: tenant_id is additive and nullable so existing rows
-- and the legacy unauthenticated /schedule path keep working. API keys are
-- the entire auth surface -- no JWT, no external IdP, because there isn't
-- one to federate with.

ALTER TABLE tasks ADD COLUMN IF NOT EXISTS tenant_id UUID;
CREATE INDEX IF NOT EXISTS idx_tasks_tenant ON tasks (tenant_id, created_at);

ALTER TABLE schedules ADD COLUMN IF NOT EXISTS tenant_id UUID;
CREATE INDEX IF NOT EXISTS idx_schedules_tenant ON schedules (tenant_id);

CREATE TABLE IF NOT EXISTS api_keys (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  UUID NOT NULL,
    key_hash   BYTEA NOT NULL,
    prefix     TEXT NOT NULL,
    name       TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_prefix ON api_keys (prefix) WHERE revoked_at IS NULL;
