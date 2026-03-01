-- 064_add_billing_usage_entry_retry_fields.sql
-- Add retry-state columns for billing_usage_entries compensation worker.

ALTER TABLE billing_usage_entries
    ADD COLUMN IF NOT EXISTS status SMALLINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS attempt_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS next_retry_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ADD COLUMN IF NOT EXISTS last_error TEXT;

-- Keep legacy rows aligned with applied flag.
UPDATE billing_usage_entries
SET status = CASE WHEN applied THEN 2 ELSE 0 END
WHERE status NOT IN (0, 1, 2)
   OR (applied = TRUE AND status <> 2)
   OR (applied = FALSE AND status = 2);

ALTER TABLE billing_usage_entries
    DROP CONSTRAINT IF EXISTS chk_billing_usage_entries_status;

ALTER TABLE billing_usage_entries
    ADD CONSTRAINT chk_billing_usage_entries_status
    CHECK (status IN (0, 1, 2));

CREATE INDEX IF NOT EXISTS idx_billing_usage_entries_retry
    ON billing_usage_entries (status, next_retry_at, updated_at)
    WHERE applied = FALSE;
