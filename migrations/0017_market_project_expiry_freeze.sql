ALTER TABLE market_projects
    ADD COLUMN IF NOT EXISTS frozen_at TIMESTAMPTZ;

UPDATE market_projects
SET frozen_at = updated_at
WHERE status = 'paused'
  AND pause_reason = 'subscription_expired'
  AND frozen_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_market_projects_expiry_frozen
    ON market_projects (debox_user_id, frozen_at)
    WHERE pause_reason = 'subscription_expired';
