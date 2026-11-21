-- Add provenance without changing any historical quote or sent notification.
-- Existing rows deliberately remain unknown until a new observed quote exists.
ALTER TABLE market_snapshots
    ADD COLUMN IF NOT EXISTS quote_quality JSONB NOT NULL DEFAULT '{}';
