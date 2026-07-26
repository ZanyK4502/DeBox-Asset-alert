ALTER TABLE market_rules ADD COLUMN IF NOT EXISTS pause_reason TEXT NOT NULL DEFAULT '';

ALTER TABLE market_rules ADD COLUMN IF NOT EXISTS aggregation_anchor_at TIMESTAMPTZ;

ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS pause_reason TEXT NOT NULL DEFAULT '';

ALTER TABLE combination_rules ADD COLUMN IF NOT EXISTS pause_reason TEXT NOT NULL DEFAULT '';

ALTER TABLE market_rule_events ADD COLUMN IF NOT EXISTS previous_value TEXT;

ALTER TABLE market_rule_events ADD COLUMN IF NOT EXISTS current_value TEXT;

ALTER TABLE market_rule_events ADD COLUMN IF NOT EXISTS note TEXT NOT NULL DEFAULT '';

ALTER TABLE market_rule_events ADD COLUMN IF NOT EXISTS details JSONB NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE market_rule_events ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

ALTER TABLE market_rule_events DROP CONSTRAINT IF EXISTS market_rule_events_notification_status_check;

ALTER TABLE market_rule_events ADD CONSTRAINT market_rule_events_notification_status_check
    CHECK (
        notification_status IN (
            'pending', 'sending', 'sent', 'failed', 'skipped', 'staged', 'combined'
        )
    );

CREATE TABLE IF NOT EXISTS market_stage_windows (
    id BIGSERIAL PRIMARY KEY,
    debox_user_id TEXT NOT NULL,
    market_rule_id BIGINT NOT NULL REFERENCES market_rules(id) ON DELETE CASCADE,
    starts_at TIMESTAMPTZ NOT NULL,
    ends_at TIMESTAMPTZ NOT NULL,
    trigger_count BIGINT NOT NULL DEFAULT 0 CHECK (trigger_count >= 0),
    notification_status TEXT NOT NULL DEFAULT 'collecting'
        CHECK (
            notification_status IN (
                'collecting', 'pending', 'sending', 'sent', 'failed', 'skipped'
            )
        ),
    notification_message_id TEXT,
    notification_error TEXT NOT NULL DEFAULT '',
    notification_attempts INTEGER NOT NULL DEFAULT 0 CHECK (notification_attempts >= 0),
    notification_attempted_at TIMESTAMPTZ,
    notification_sent_at TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    closed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (debox_user_id <> ''),
    CHECK (ends_at > starts_at)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_market_stage_windows_open_rule
    ON market_stage_windows (market_rule_id)
    WHERE closed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_market_stage_windows_delivery
    ON market_stage_windows (notification_status, next_attempt_at, id);

CREATE TABLE IF NOT EXISTS market_stage_window_events (
    market_stage_window_id BIGINT NOT NULL
        REFERENCES market_stage_windows(id) ON DELETE CASCADE,
    market_rule_event_id BIGINT NOT NULL
        REFERENCES market_rule_events(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (market_stage_window_id, market_rule_event_id),
    UNIQUE (market_rule_event_id)
);

CREATE TABLE IF NOT EXISTS market_combination_rules (
    id BIGSERIAL PRIMARY KEY,
    debox_user_id TEXT NOT NULL,
    note TEXT NOT NULL DEFAULT '',
    cycle_type TEXT NOT NULL DEFAULT 'fixed'
        CHECK (cycle_type IN ('fixed', 'follow')),
    cycle_minutes INTEGER NOT NULL CHECK (cycle_minutes > 0),
    notification_chat_id TEXT NOT NULL,
    notification_chat_type TEXT NOT NULL DEFAULT 'private'
        CHECK (notification_chat_type IN ('private', 'group')),
    notification_label TEXT NOT NULL DEFAULT '',
    notification_language TEXT NOT NULL DEFAULT 'zh'
        CHECK (notification_language IN ('zh', 'en')),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    run_status TEXT NOT NULL DEFAULT 'active'
        CHECK (run_status IN ('active', 'paused')),
    pause_reason TEXT NOT NULL DEFAULT '',
    aggregation_anchor_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (debox_user_id <> ''),
    CHECK (notification_chat_id <> '')
);

CREATE INDEX IF NOT EXISTS idx_market_combination_rules_user
    ON market_combination_rules (debox_user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_market_combination_rules_active
    ON market_combination_rules (enabled, run_status)
    WHERE enabled = 1 AND run_status = 'active';

CREATE TABLE IF NOT EXISTS market_combination_members (
    id BIGSERIAL PRIMARY KEY,
    market_combination_rule_id BIGINT NOT NULL
        REFERENCES market_combination_rules(id) ON DELETE CASCADE,
    source_type TEXT NOT NULL CHECK (source_type IN ('watch', 'market')),
    watch_rule_id INTEGER REFERENCES watch_rules(id) ON DELETE CASCADE,
    market_rule_id BIGINT REFERENCES market_rules(id) ON DELETE CASCADE,
    required_trigger_count BIGINT NOT NULL DEFAULT 1
        CHECK (required_trigger_count > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (
        (source_type = 'watch' AND watch_rule_id IS NOT NULL AND market_rule_id IS NULL)
        OR
        (source_type = 'market' AND watch_rule_id IS NULL AND market_rule_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_market_combination_member_watch
    ON market_combination_members (market_combination_rule_id, watch_rule_id)
    WHERE source_type = 'watch';

CREATE UNIQUE INDEX IF NOT EXISTS idx_market_combination_member_market
    ON market_combination_members (market_combination_rule_id, market_rule_id)
    WHERE source_type = 'market';

CREATE INDEX IF NOT EXISTS idx_market_combination_members_watch_source
    ON market_combination_members (watch_rule_id)
    WHERE source_type = 'watch';

CREATE INDEX IF NOT EXISTS idx_market_combination_members_market_source
    ON market_combination_members (market_rule_id)
    WHERE source_type = 'market';

CREATE TABLE IF NOT EXISTS market_combination_windows (
    id BIGSERIAL PRIMARY KEY,
    debox_user_id TEXT NOT NULL,
    market_combination_rule_id BIGINT NOT NULL
        REFERENCES market_combination_rules(id) ON DELETE CASCADE,
    starts_at TIMESTAMPTZ NOT NULL,
    ends_at TIMESTAMPTZ NOT NULL,
    total_trigger_count BIGINT NOT NULL DEFAULT 0 CHECK (total_trigger_count >= 0),
    notification_status TEXT NOT NULL DEFAULT 'collecting'
        CHECK (
            notification_status IN (
                'collecting', 'pending', 'sending', 'sent', 'failed', 'skipped'
            )
        ),
    notification_message_id TEXT,
    notification_error TEXT NOT NULL DEFAULT '',
    notification_attempts INTEGER NOT NULL DEFAULT 0 CHECK (notification_attempts >= 0),
    notification_attempted_at TIMESTAMPTZ,
    notification_sent_at TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    closed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (debox_user_id <> ''),
    CHECK (ends_at > starts_at)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_market_combination_windows_open
    ON market_combination_windows (market_combination_rule_id)
    WHERE closed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_market_combination_windows_delivery
    ON market_combination_windows (notification_status, next_attempt_at, id);

CREATE TABLE IF NOT EXISTS market_combination_window_members (
    market_combination_window_id BIGINT NOT NULL
        REFERENCES market_combination_windows(id) ON DELETE CASCADE,
    market_combination_member_id BIGINT NOT NULL
        REFERENCES market_combination_members(id) ON DELETE CASCADE,
    required_trigger_count BIGINT NOT NULL CHECK (required_trigger_count > 0),
    trigger_count BIGINT NOT NULL DEFAULT 0 CHECK (trigger_count >= 0),
    reached_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (market_combination_window_id, market_combination_member_id)
);

CREATE TABLE IF NOT EXISTS market_combination_trigger_events (
    id BIGSERIAL PRIMARY KEY,
    market_combination_window_id BIGINT NOT NULL
        REFERENCES market_combination_windows(id) ON DELETE CASCADE,
    market_combination_member_id BIGINT NOT NULL
        REFERENCES market_combination_members(id) ON DELETE CASCADE,
    source_type TEXT NOT NULL CHECK (source_type IN ('watch', 'market')),
    watch_trigger_event_id BIGINT
        REFERENCES rule_trigger_events(id) ON DELETE CASCADE,
    market_rule_event_id BIGINT
        REFERENCES market_rule_events(id) ON DELETE CASCADE,
    note TEXT NOT NULL DEFAULT '',
    occurred_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (
        (
            source_type = 'watch'
            AND watch_trigger_event_id IS NOT NULL
            AND market_rule_event_id IS NULL
        )
        OR
        (
            source_type = 'market'
            AND watch_trigger_event_id IS NULL
            AND market_rule_event_id IS NOT NULL
        )
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_market_combination_trigger_watch
    ON market_combination_trigger_events (
        market_combination_member_id, watch_trigger_event_id
    )
    WHERE source_type = 'watch';

CREATE UNIQUE INDEX IF NOT EXISTS idx_market_combination_trigger_market
    ON market_combination_trigger_events (
        market_combination_member_id, market_rule_event_id
    )
    WHERE source_type = 'market';

CREATE INDEX IF NOT EXISTS idx_market_combination_trigger_window
    ON market_combination_trigger_events (
        market_combination_window_id, created_at DESC
    );

CREATE INDEX IF NOT EXISTS idx_market_rule_events_retry
    ON market_rule_events (notification_status, next_attempt_at, id)
    WHERE notification_status IN ('pending', 'failed', 'sending');
