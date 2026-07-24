ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS rule_scope TEXT NOT NULL DEFAULT 'standalone' CHECK (rule_scope IN ('standalone', 'combination'));
ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS delivery_mode TEXT NOT NULL DEFAULT 'realtime' CHECK (delivery_mode IN ('realtime', 'stage'));
ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS cycle_type TEXT NOT NULL DEFAULT 'fixed' CHECK (cycle_type IN ('fixed', 'follow'));
ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS cycle_minutes INTEGER NOT NULL DEFAULT 60 CHECK (cycle_minutes > 0);
ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS trigger_count_threshold BIGINT NOT NULL DEFAULT 1 CHECK (trigger_count_threshold > 0);
ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS aggregation_anchor_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS combination_rules (
    id BIGSERIAL PRIMARY KEY,
    debox_user_id TEXT NOT NULL,
    note TEXT NOT NULL DEFAULT '',
    cycle_type TEXT NOT NULL DEFAULT 'fixed',
    cycle_minutes INTEGER NOT NULL,
    notification_chat_id TEXT NOT NULL,
    notification_chat_type TEXT NOT NULL DEFAULT 'private',
    notification_label TEXT NOT NULL DEFAULT '',
    notification_language TEXT NOT NULL DEFAULT 'zh',
    enabled INTEGER NOT NULL DEFAULT 1,
    run_status TEXT NOT NULL DEFAULT 'active',
    aggregation_anchor_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (cycle_type IN ('fixed', 'follow')),
    CHECK (cycle_minutes > 0),
    CHECK (notification_chat_type IN ('private', 'group')),
    CHECK (notification_language IN ('zh', 'en')),
    CHECK (enabled IN (0, 1))
);

CREATE TABLE IF NOT EXISTS combination_rule_members (
    id BIGSERIAL PRIMARY KEY,
    combination_rule_id BIGINT NOT NULL REFERENCES combination_rules(id) ON DELETE CASCADE,
    watch_rule_id INTEGER NOT NULL REFERENCES watch_rules(id) ON DELETE CASCADE,
    required_trigger_count BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (required_trigger_count > 0),
    UNIQUE (combination_rule_id, watch_rule_id),
    UNIQUE (watch_rule_id)
);

CREATE TABLE IF NOT EXISTS aggregation_windows (
    id BIGSERIAL PRIMARY KEY,
    debox_user_id TEXT NOT NULL,
    source_type TEXT NOT NULL,
    watch_rule_id INTEGER REFERENCES watch_rules(id) ON DELETE CASCADE,
    combination_rule_id BIGINT REFERENCES combination_rules(id) ON DELETE CASCADE,
    starts_at TIMESTAMPTZ NOT NULL,
    ends_at TIMESTAMPTZ NOT NULL,
    total_trigger_count BIGINT NOT NULL DEFAULT 0,
    notification_sent INTEGER NOT NULL DEFAULT 0,
    notification_sent_at TIMESTAMPTZ,
    closed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (source_type IN ('rule', 'combination')),
    CHECK (ends_at > starts_at),
    CHECK (total_trigger_count >= 0),
    CHECK (notification_sent IN (0, 1)),
    CHECK (
        (source_type = 'rule' AND watch_rule_id IS NOT NULL AND combination_rule_id IS NULL)
        OR
        (source_type = 'combination' AND watch_rule_id IS NULL AND combination_rule_id IS NOT NULL)
    )
);

CREATE TABLE IF NOT EXISTS aggregation_window_members (
    aggregation_window_id BIGINT NOT NULL REFERENCES aggregation_windows(id) ON DELETE CASCADE,
    watch_rule_id INTEGER NOT NULL REFERENCES watch_rules(id) ON DELETE CASCADE,
    required_trigger_count BIGINT NOT NULL,
    trigger_count BIGINT NOT NULL DEFAULT 0,
    reached_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (aggregation_window_id, watch_rule_id),
    CHECK (required_trigger_count > 0),
    CHECK (trigger_count >= 0)
);

CREATE TABLE IF NOT EXISTS rule_trigger_events (
    id BIGSERIAL PRIMARY KEY,
    debox_user_id TEXT NOT NULL,
    watch_rule_id INTEGER NOT NULL REFERENCES watch_rules(id) ON DELETE CASCADE,
    aggregation_window_id BIGINT NOT NULL REFERENCES aggregation_windows(id) ON DELETE CASCADE,
    event_type TEXT NOT NULL,
    event_key TEXT NOT NULL DEFAULT '',
    previous_value TEXT,
    current_value TEXT,
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    occurred_at TIMESTAMPTZ,
    detected_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS aggregate_notifications (
    id BIGSERIAL PRIMARY KEY,
    debox_user_id TEXT NOT NULL,
    aggregation_window_id BIGINT NOT NULL UNIQUE REFERENCES aggregation_windows(id) ON DELETE CASCADE,
    notification_kind TEXT NOT NULL,
    notification_chat_id TEXT NOT NULL,
    notification_chat_type TEXT NOT NULL DEFAULT 'private',
    notification_label TEXT NOT NULL DEFAULT '',
    notification_language TEXT NOT NULL DEFAULT 'zh',
    note TEXT NOT NULL DEFAULT '',
    trigger_count_snapshot BIGINT NOT NULL,
    notification_message_id TEXT,
    notification_status TEXT NOT NULL DEFAULT 'pending',
    notification_error TEXT NOT NULL DEFAULT '',
    notification_attempts INTEGER NOT NULL DEFAULT 0,
    notification_attempted_at TIMESTAMPTZ,
    notification_sent_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (notification_kind IN ('stage', 'combination')),
    CHECK (notification_chat_type IN ('private', 'group')),
    CHECK (notification_language IN ('zh', 'en')),
    CHECK (trigger_count_snapshot > 0),
    CHECK (notification_attempts >= 0)
);

CREATE INDEX IF NOT EXISTS idx_watch_rules_scope
ON watch_rules (debox_user_id, rule_scope);

CREATE INDEX IF NOT EXISTS idx_watch_rules_delivery
ON watch_rules (delivery_mode);

CREATE INDEX IF NOT EXISTS idx_combination_rules_user
ON combination_rules (debox_user_id);

CREATE INDEX IF NOT EXISTS idx_combination_rules_status
ON combination_rules (enabled, run_status);

CREATE INDEX IF NOT EXISTS idx_combination_members_combination
ON combination_rule_members (combination_rule_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_aggregation_windows_open_rule
ON aggregation_windows (watch_rule_id)
WHERE closed_at IS NULL AND watch_rule_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_aggregation_windows_open_combination
ON aggregation_windows (combination_rule_id)
WHERE closed_at IS NULL AND combination_rule_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_aggregation_windows_expiry
ON aggregation_windows (ends_at)
WHERE closed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_aggregation_windows_history
ON aggregation_windows (debox_user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_rule_trigger_events_window
ON rule_trigger_events (aggregation_window_id, created_at);

CREATE INDEX IF NOT EXISTS idx_rule_trigger_events_history
ON rule_trigger_events (debox_user_id, created_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS idx_rule_trigger_events_key
ON rule_trigger_events (watch_rule_id, event_key)
WHERE event_key <> '';

CREATE INDEX IF NOT EXISTS idx_aggregate_notifications_history
ON aggregate_notifications (debox_user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_aggregate_notifications_status
ON aggregate_notifications (notification_status, created_at);
