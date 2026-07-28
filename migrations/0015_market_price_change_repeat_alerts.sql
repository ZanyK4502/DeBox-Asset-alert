-- Optional repeated notifications for price-change rules while the condition
-- remains satisfied. Existing and newly created rules default to crossing-only
-- notifications until the user explicitly enables this option.

ALTER TABLE market_rules
    ADD COLUMN IF NOT EXISTS repeat_while_active BOOLEAN NOT NULL DEFAULT FALSE;
