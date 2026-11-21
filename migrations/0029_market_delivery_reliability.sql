-- Keep the legacy terminal status for uncertain delivery.  Older claimers must
-- never mistake an unknown outcome for a retryable failure.
ALTER TABLE market_rules ADD COLUMN IF NOT EXISTS notification_source_after TIMESTAMPTZ;
ALTER TABLE market_combination_rules ADD COLUMN IF NOT EXISTS notification_source_after TIMESTAMPTZ;

ALTER TABLE market_rule_events
    ADD COLUMN IF NOT EXISTS delivery_outcome TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS notification_source_reconciled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS notification_source_audit JSONB NOT NULL DEFAULT '[]'::jsonb
        CHECK (jsonb_typeof(notification_source_audit)='array' AND jsonb_array_length(notification_source_audit)<=16),
    ADD COLUMN IF NOT EXISTS notification_route JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK(jsonb_typeof(notification_route)='object' AND octet_length(notification_route::text)<=4096),
    ADD COLUMN IF NOT EXISTS notification_lease_token BIGINT NOT NULL DEFAULT 0 CHECK (notification_lease_token >= 0),
    ADD COLUMN IF NOT EXISTS notification_lease_expires_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS notification_send_started_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS notification_snapshot_id BIGINT REFERENCES notification_detail_snapshots(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS notification_payload JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(notification_payload) = 'object' AND octet_length(notification_payload::text) <= 65536);

ALTER TABLE market_stage_windows
    ADD COLUMN IF NOT EXISTS delivery_outcome TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS notification_route JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK(jsonb_typeof(notification_route)='object' AND octet_length(notification_route::text)<=4096),
    ADD COLUMN IF NOT EXISTS notification_lease_token BIGINT NOT NULL DEFAULT 0 CHECK (notification_lease_token >= 0),
    ADD COLUMN IF NOT EXISTS notification_lease_expires_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS notification_send_started_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS notification_snapshot_id BIGINT REFERENCES notification_detail_snapshots(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS notification_payload JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(notification_payload) = 'object' AND octet_length(notification_payload::text) <= 65536);

ALTER TABLE market_combination_windows
    ADD COLUMN IF NOT EXISTS delivery_outcome TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS notification_route JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK(jsonb_typeof(notification_route)='object' AND octet_length(notification_route::text)<=4096),
    ADD COLUMN IF NOT EXISTS notification_lease_token BIGINT NOT NULL DEFAULT 0 CHECK (notification_lease_token >= 0),
    ADD COLUMN IF NOT EXISTS notification_lease_expires_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS notification_send_started_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS notification_snapshot_id BIGINT REFERENCES notification_detail_snapshots(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS notification_payload JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(notification_payload) = 'object' AND octet_length(notification_payload::text) <= 65536);

CREATE TABLE IF NOT EXISTS market_combination_source_decisions (
    id BIGSERIAL PRIMARY KEY,
    market_combination_member_id BIGINT NOT NULL REFERENCES market_combination_members(id) ON DELETE CASCADE,
    source_type TEXT NOT NULL CHECK (source_type IN ('watch', 'market')),
    watch_trigger_event_id BIGINT REFERENCES rule_trigger_events(id) ON DELETE CASCADE,
    market_rule_event_id BIGINT REFERENCES market_rule_events(id) ON DELETE CASCADE,
    disposition TEXT NOT NULL CHECK (disposition = 'skipped'),
    reason TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 100),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((source_type='watch' AND watch_trigger_event_id IS NOT NULL AND market_rule_event_id IS NULL)
        OR (source_type='market' AND market_rule_event_id IS NOT NULL AND watch_trigger_event_id IS NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_market_combination_decision_watch
    ON market_combination_source_decisions(market_combination_member_id,watch_trigger_event_id)
    WHERE source_type='watch';
CREATE UNIQUE INDEX IF NOT EXISTS idx_market_combination_decision_market
    ON market_combination_source_decisions(market_combination_member_id,market_rule_event_id)
    WHERE source_type='market';

CREATE INDEX IF NOT EXISTS idx_market_rule_events_staged_repair ON market_rule_events(id)
    WHERE notification_status IN ('staged','combined','pending','failed');
CREATE INDEX IF NOT EXISTS idx_market_rule_delivery_lease ON market_rule_events(notification_attempted_at,id)
    WHERE notification_status='sending';
CREATE INDEX IF NOT EXISTS idx_market_stage_delivery_lease ON market_stage_windows(notification_attempted_at,id)
    WHERE notification_status='sending';
CREATE INDEX IF NOT EXISTS idx_market_combination_delivery_lease ON market_combination_windows(notification_attempted_at,id)
    WHERE notification_status='sending';

CREATE TABLE IF NOT EXISTS market_delivery_unknown_resolutions (
    id BIGSERIAL PRIMARY KEY,
    kind TEXT NOT NULL CHECK(kind IN('realtime','stage','combination')),
    source_id BIGINT NOT NULL CHECK(source_id>0),
    lease_token BIGINT NOT NULL CHECK(lease_token>=0),
    attempts INTEGER NOT NULL CHECK(attempts>=0),
    delivered BOOLEAN NOT NULL,
    message_id TEXT,
    explanation TEXT NOT NULL CHECK(length(explanation) BETWEEN 1 AND 1000),
    previous_error TEXT NOT NULL DEFAULT '' CHECK(length(previous_error)<=2000),
    resolved_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK((delivered AND message_id IS NOT NULL AND BTRIM(message_id)<>'') OR(NOT delivered AND message_id IS NULL)),
    UNIQUE(kind,source_id,lease_token)
);
CREATE INDEX IF NOT EXISTS idx_market_delivery_unknown_resolved_at ON market_delivery_unknown_resolutions(resolved_at,id);
