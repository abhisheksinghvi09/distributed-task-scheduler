-- Token/cost accounting for LLM tasks, and per-tenant monthly budgets
-- enforced at enqueue time -- rejecting before a claim/dispatch/retry
-- cycle is wasted is cheaper than rejecting after.

ALTER TABLE tasks ADD COLUMN IF NOT EXISTS input_tokens       INT;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS output_tokens      INT;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS cache_read_tokens  INT;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS cost_microcents    BIGINT;

CREATE TABLE IF NOT EXISTS tenant_budgets (
    tenant_id        UUID NOT NULL,
    month            DATE NOT NULL, -- first-of-month, truncated in application code
    limit_microcents BIGINT NOT NULL,
    spent_microcents BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id, month)
);
