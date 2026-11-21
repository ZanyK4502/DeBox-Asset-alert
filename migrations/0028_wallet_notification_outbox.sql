CREATE TABLE IF NOT EXISTS wallet_notification_outbox (
    id BIGSERIAL PRIMARY KEY,
    source_key TEXT NOT NULL UNIQUE,
    source_type TEXT NOT NULL CHECK (source_type IN ('alert_event', 'aggregate_notification')),
    source_id BIGINT NOT NULL CHECK (source_id > 0),
    watch_rule_id BIGINT NOT NULL CHECK (watch_rule_id > 0),
    source_generation BIGINT NOT NULL DEFAULT 0 CHECK (source_generation >= 0),
    combination_rule_id BIGINT,
    debox_user_id TEXT NOT NULL,
    snapshot_id BIGINT NOT NULL CHECK (snapshot_id > 0),
    payload JSONB NOT NULL CHECK (jsonb_typeof(payload) = 'object' AND octet_length(payload::text) <= 65536),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sending', 'retry', 'sent', 'failed', 'unknown', 'skipped')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lease_token TEXT,
    lease_expires_at TIMESTAMPTZ,
    message_id TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    attempted_at TIMESTAMPTZ,
    send_started_at TIMESTAMPTZ,
    closed_at TIMESTAMPTZ,
    sent_at TIMESTAMPTZ,
    UNIQUE(source_type,source_id)
);

ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS next_observation_at TIMESTAMPTZ;
ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS notification_generation BIGINT NOT NULL DEFAULT 0 CHECK (notification_generation >= 0);
CREATE INDEX IF NOT EXISTS idx_watch_rules_observation_due
ON watch_rules (next_observation_at, last_checked_at, id)
WHERE enabled=1 AND run_status='active';

CREATE INDEX IF NOT EXISTS idx_wallet_notification_due
ON wallet_notification_outbox (next_attempt_at, id)
WHERE status IN ('pending', 'retry');

CREATE INDEX IF NOT EXISTS idx_wallet_notification_lease
ON wallet_notification_outbox (lease_expires_at, id)
WHERE status = 'sending';

CREATE INDEX IF NOT EXISTS idx_wallet_notification_retention
ON wallet_notification_outbox (COALESCE(sent_at, closed_at, created_at), id)
WHERE status IN ('sent','failed','skipped');

CREATE INDEX IF NOT EXISTS idx_wallet_notification_snapshot_active
ON wallet_notification_outbox (snapshot_id)
WHERE status IN ('pending','sending','retry','unknown');

CREATE INDEX IF NOT EXISTS idx_wallet_notification_source_active
ON wallet_notification_outbox (source_type, source_id)
WHERE status IN ('pending','sending','retry');

CREATE INDEX IF NOT EXISTS idx_wallet_notification_owner_active
ON wallet_notification_outbox (debox_user_id,id)
WHERE status IN ('pending','sending','retry');

ALTER TABLE aggregation_windows ADD COLUMN IF NOT EXISTS recovery_skipped_at TIMESTAMPTZ;
ALTER TABLE aggregation_windows ADD COLUMN IF NOT EXISTS recovery_reason TEXT;

CREATE TABLE IF NOT EXISTS wallet_trigger_recovery_decisions (
    id BIGSERIAL PRIMARY KEY,
    watch_rule_id BIGINT NOT NULL,
    debox_user_id TEXT NOT NULL,
    source_key TEXT NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    cutoff_at TIMESTAMPTZ NOT NULL,
    reason TEXT NOT NULL,
    previous_value TEXT,
    current_value TEXT,
    observed_from_at TIMESTAMPTZ,
    observed_through_at TIMESTAMPTZ,
    time_basis TEXT NOT NULL DEFAULT 'source_occurrence',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(watch_rule_id, source_key, cutoff_at)
);

CREATE TABLE IF NOT EXISTS wallet_notification_unknown_resolutions (
    id BIGSERIAL PRIMARY KEY,
    outbox_id BIGINT NOT NULL,
    delivered BOOLEAN NOT NULL,
    message_id TEXT,
    explanation TEXT NOT NULL CHECK (length(btrim(explanation)) BETWEEN 1 AND 1000),
    previous_error TEXT NOT NULL DEFAULT '' CHECK (octet_length(previous_error) <= 2000),
    attempts INTEGER NOT NULL,
    resolved_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_wallet_trigger_recovery_retention
ON wallet_trigger_recovery_decisions (created_at, id);

CREATE INDEX IF NOT EXISTS idx_wallet_unknown_resolution_retention
ON wallet_notification_unknown_resolutions (resolved_at, id);
