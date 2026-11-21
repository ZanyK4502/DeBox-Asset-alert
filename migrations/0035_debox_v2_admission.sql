-- Retain all old consumed reservations. V2 shares 100 admissions per rolling
-- minute across the bot's API keys; failures never refund a consumed slot.
ALTER TABLE notification_send_admission
    DROP CONSTRAINT IF EXISTS notification_send_admission_reservations_check;
ALTER TABLE notification_send_admission
    ADD CONSTRAINT notification_send_admission_reservations_check
    CHECK (jsonb_typeof(reservations) = 'array' AND jsonb_array_length(reservations) <= 100);

-- The digest covers API Key + the v2 path, never stores a credential or chat.
-- At most ten timestamps per credential/path are kept; no per-message rows.
CREATE TABLE IF NOT EXISTS notification_send_path_admission (
    scope_hash TEXT PRIMARY KEY CHECK (scope_hash ~ '^[0-9a-f]{64}$'),
    admissions JSONB NOT NULL DEFAULT '[]'::jsonb
        CHECK (jsonb_typeof(admissions) = 'array' AND jsonb_array_length(admissions) <= 10),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
