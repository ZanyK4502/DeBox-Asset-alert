CREATE TABLE IF NOT EXISTS notification_send_admission (
    bot_id TEXT PRIMARY KEY CHECK (length(bot_id) BETWEEN 1 AND 128),
    reservations JSONB NOT NULL DEFAULT '[]'::jsonb
        CHECK (jsonb_typeof(reservations) = 'array' AND jsonb_array_length(reservations) <= 10),
    blocked_until TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
