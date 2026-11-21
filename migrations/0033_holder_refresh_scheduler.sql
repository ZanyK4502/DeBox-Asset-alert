-- Scheduling is separate from the last successful complete holder observation.
-- Existing ranks, balances, history and coverage are deliberately not rewritten.
ALTER TABLE market_holder_refresh_states
    ADD COLUMN IF NOT EXISTS next_due_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS refresh_lease_token BIGINT NOT NULL DEFAULT 0 CHECK (refresh_lease_token >= 0),
    ADD COLUMN IF NOT EXISTS refresh_lease_until TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS refresh_started_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS refresh_pages_reserved INTEGER NOT NULL DEFAULT 0 CHECK (refresh_pages_reserved BETWEEN 0 AND 10),
    ADD COLUMN IF NOT EXISTS refresh_pages_used INTEGER NOT NULL DEFAULT 0 CHECK (refresh_pages_used BETWEEN 0 AND refresh_pages_reserved),
    ADD COLUMN IF NOT EXISTS consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures BETWEEN 0 AND 30),
    ADD COLUMN IF NOT EXISTS last_failure_code TEXT NOT NULL DEFAULT '' CHECK (length(last_failure_code) <= 32);

CREATE INDEX IF NOT EXISTS idx_holder_refresh_active_leases
    ON market_holder_refresh_states (refresh_lease_until)
    WHERE refresh_lease_until IS NOT NULL;

-- This single row is initialized lazily on actual work, not on idle polls.
CREATE TABLE IF NOT EXISTS market_holder_refresh_scheduler (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    last_chain_id BIGINT NOT NULL DEFAULT 0,
    window_started_at TIMESTAMPTZ NOT NULL,
    pages_used INTEGER NOT NULL DEFAULT 0 CHECK (pages_used BETWEEN 0 AND 60)
);

CREATE INDEX IF NOT EXISTS idx_watch_rules_active_checked_due
    ON watch_rules (last_checked_at NULLS FIRST, id)
    WHERE enabled = 1 AND run_status = 'active';
