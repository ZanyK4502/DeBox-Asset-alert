CREATE TABLE IF NOT EXISTS notification_detail_snapshots (
    id BIGSERIAL PRIMARY KEY,
    public_id TEXT NOT NULL UNIQUE,
    source_key TEXT NOT NULL UNIQUE,
    notification_kind TEXT NOT NULL,
    source_type TEXT NOT NULL,
    source_id BIGINT,
    debox_user_id TEXT NOT NULL,
    rule_id BIGINT,
    rule_type TEXT NOT NULL DEFAULT '',
    rule_name TEXT NOT NULL DEFAULT '',
    rule_threshold TEXT NOT NULL DEFAULT '',
    actual_value TEXT NOT NULL DEFAULT '',
    notification_chat_id TEXT NOT NULL,
    notification_chat_type TEXT NOT NULL DEFAULT 'private',
    notification_language TEXT NOT NULL DEFAULT 'zh',
    notification_label TEXT NOT NULL DEFAULT '',
    notification_text TEXT NOT NULL,
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT (NOW() + INTERVAL '30 days'),
    CHECK (notification_kind IN (
        'address_realtime',
        'address_stage',
        'address_combination',
        'market_realtime',
        'market_stage',
        'market_combination',
        'daily_summary'
    )),
    CHECK (notification_chat_type IN ('private', 'group')),
    CHECK (notification_language IN ('zh', 'en')),
    CHECK (jsonb_typeof(details) = 'object'),
    CHECK (expires_at > created_at)
);

CREATE INDEX IF NOT EXISTS idx_notification_detail_snapshots_user_created
    ON notification_detail_snapshots(debox_user_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_notification_detail_snapshots_expires
    ON notification_detail_snapshots(expires_at);

CREATE INDEX IF NOT EXISTS idx_notification_detail_snapshots_source
    ON notification_detail_snapshots(source_type, source_id)
    WHERE source_id IS NOT NULL;
