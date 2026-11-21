-- Existing daily_summary_deliveries rows remain the confirmed-success ledger.
-- These new rows describe attempts and retain their original frozen snapshot.
CREATE TABLE IF NOT EXISTS daily_summary_dispatches (
    id BIGSERIAL PRIMARY KEY,
    subscription_id BIGINT NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
    target_id BIGINT NOT NULL,
    chat_type TEXT NOT NULL CHECK (chat_type IN ('private','group')),
    chat_id TEXT NOT NULL,
    period_start_at TIMESTAMPTZ NOT NULL,
    period_end_at TIMESTAMPTZ NOT NULL,
    local_date TEXT NOT NULL,
    snapshot_id BIGINT REFERENCES notification_detail_snapshots(id) ON DELETE SET NULL,
    payload JSONB NOT NULL CHECK (jsonb_typeof(payload)='object' AND octet_length(payload::text)<=65536),
    status TEXT NOT NULL CHECK (status IN ('pending','sending','sent','failed','unknown','cancelled','superseded')),
    claim_token TEXT,
    last_claim_token TEXT,
    lease_until TIMESTAMPTZ,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    retry_at TIMESTAMPTZ,
    message_id TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    resolution TEXT NOT NULL DEFAULT '',
    resolution_note TEXT NOT NULL DEFAULT '',
    resolved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    attempted_at TIMESTAMPTZ,
    sent_at TIMESTAMPTZ,
    closed_at TIMESTAMPTZ,
    CHECK (period_start_at < period_end_at),
    CHECK ((status='sending' AND claim_token IS NOT NULL AND lease_until IS NOT NULL)
        OR (status<>'sending' AND claim_token IS NULL AND lease_until IS NULL)),
    UNIQUE(subscription_id,chat_type,chat_id,period_end_at)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_daily_summary_dispatches_open_target
    ON daily_summary_dispatches(subscription_id,chat_type,chat_id)
    WHERE status IN ('pending','sending','failed','unknown');
CREATE INDEX IF NOT EXISTS idx_daily_summary_dispatches_snapshot
    ON daily_summary_dispatches(snapshot_id) WHERE snapshot_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_daily_summary_dispatches_retention
    ON daily_summary_dispatches(COALESCE(sent_at,closed_at,created_at),id) WHERE status IN ('sent','cancelled','superseded');

CREATE INDEX IF NOT EXISTS idx_daily_summary_deliveries_retention
    ON daily_summary_deliveries(sent_at,id);
CREATE INDEX IF NOT EXISTS idx_daily_summary_deliveries_target_cutoff
    ON daily_summary_deliveries(subscription_id,chat_type,chat_id,period_end_at DESC);

CREATE TABLE IF NOT EXISTS daily_summary_unknown_resolutions (
    id BIGSERIAL PRIMARY KEY,
    dispatch_id BIGINT NOT NULL REFERENCES daily_summary_dispatches(id) ON DELETE CASCADE,
    delivered BOOLEAN NOT NULL,
    message_id TEXT,
    note TEXT NOT NULL,
    attempts INTEGER NOT NULL,
    last_claim_token TEXT,
    resolved_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_daily_summary_unknown_resolutions_parent
    ON daily_summary_unknown_resolutions(dispatch_id,id);

-- Pre-outbox snapshots prove that sending may have started, not that it failed.
-- Invalid/missing dates stay NULL and require review; no period is invented.
CREATE TABLE IF NOT EXISTS daily_summary_legacy_reviews (
    snapshot_id BIGINT PRIMARY KEY REFERENCES notification_detail_snapshots(id) ON DELETE CASCADE,
    subscription_id BIGINT NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
    debox_user_id TEXT NOT NULL,
    chat_type TEXT NOT NULL CHECK (chat_type IN ('private','group')),
    chat_id TEXT NOT NULL,
    source_subscription_id BIGINT,
    source_target_id BIGINT,
    period_start_at TIMESTAMPTZ,
    period_end_at TIMESTAMPTZ,
    proof_valid BOOLEAN NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('unknown','confirmed_sent','confirmed_not_sent')),
    message_id TEXT,
    note TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    closed_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_daily_summary_legacy_reviews_open_route
    ON daily_summary_legacy_reviews(debox_user_id,chat_type,chat_id) WHERE status='unknown';
CREATE INDEX IF NOT EXISTS idx_daily_summary_legacy_reviews_retention
    ON daily_summary_legacy_reviews(closed_at,snapshot_id) WHERE status<>'unknown';
CREATE INDEX IF NOT EXISTS idx_daily_summary_legacy_snapshot_route
    ON notification_detail_snapshots(debox_user_id,notification_chat_type,notification_chat_id,created_at DESC,id DESC)
    WHERE notification_kind='daily_summary' AND source_type='daily_summary_target';
