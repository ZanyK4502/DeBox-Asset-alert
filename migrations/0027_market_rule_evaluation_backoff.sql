-- Scheduling metadata only. Existing business state and watermarks are intact.
ALTER TABLE market_rules
    ADD COLUMN IF NOT EXISTS next_evaluation_at TIMESTAMPTZ;
