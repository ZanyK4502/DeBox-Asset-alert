-- No boundary is configured by migration. Production cutover must explicitly
-- record the reviewed time/ranges; no cursor, rule or pending queue is changed.
CREATE TABLE IF NOT EXISTS notification_recovery_boundaries (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    cutoff_at TIMESTAMPTZ NOT NULL,
    revision BIGINT NOT NULL CHECK (revision > 0),
    reason TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 1000),
    configured_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS notification_recovery_gaps (
    id BIGSERIAL PRIMARY KEY,
    chain_id BIGINT NOT NULL CHECK (chain_id > 0),
    chain_key TEXT NOT NULL CHECK (length(chain_key) BETWEEN 1 AND 32),
    from_block BIGINT NOT NULL CHECK (from_block > 0),
    through_block BIGINT NOT NULL CHECK (through_block >= from_block),
    from_at TIMESTAMPTZ,
    through_at TIMESTAMPTZ NOT NULL,
    cutoff_at TIMESTAMPTZ NOT NULL,
    reason TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 1000),
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (from_at IS NULL OR through_at >= from_at),
    UNIQUE (chain_id, from_block, through_block, cutoff_at)
);
CREATE INDEX IF NOT EXISTS idx_notification_recovery_gaps_chain_time
    ON notification_recovery_gaps (chain_id,through_at,from_at);

CREATE TABLE IF NOT EXISTS notification_recovery_boundary_history (
    revision BIGINT PRIMARY KEY CHECK (revision > 0),
    cutoff_at TIMESTAMPTZ NOT NULL,
    reason TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 1000),
    configured_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_wallet_outbox_snapshot_evidence
    ON wallet_notification_outbox(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_wallet_outbox_source_evidence
    ON wallet_notification_outbox(source_type,source_id);
CREATE INDEX IF NOT EXISTS idx_wallet_resolution_retention
    ON wallet_notification_unknown_resolutions(resolved_at,id);
CREATE INDEX IF NOT EXISTS idx_wallet_recovery_decision_retention
    ON wallet_trigger_recovery_decisions(created_at,id);
CREATE INDEX IF NOT EXISTS idx_market_mre_snapshot_active
    ON market_rule_events(notification_snapshot_id)
    WHERE notification_snapshot_id IS NOT NULL AND (notification_status IN ('pending','failed','sending') OR delivery_outcome='unknown');
CREATE INDEX IF NOT EXISTS idx_market_stage_snapshot_active
    ON market_stage_windows(notification_snapshot_id)
    WHERE notification_snapshot_id IS NOT NULL AND (notification_status IN ('pending','failed','sending') OR delivery_outcome='unknown');
CREATE INDEX IF NOT EXISTS idx_market_combination_snapshot_active
    ON market_combination_windows(notification_snapshot_id)
    WHERE notification_snapshot_id IS NOT NULL AND (notification_status IN ('pending','failed','sending') OR delivery_outcome='unknown');
