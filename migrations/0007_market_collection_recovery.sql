CREATE TABLE IF NOT EXISTS market_scanned_blocks (
    id BIGSERIAL PRIMARY KEY,
    chain_key TEXT NOT NULL,
    chain_id BIGINT NOT NULL CHECK (chain_id > 0),
    cursor_key TEXT NOT NULL,
    block_number BIGINT NOT NULL CHECK (block_number >= 0),
    block_hash TEXT NOT NULL,
    parent_hash TEXT NOT NULL,
    block_timestamp TIMESTAMPTZ,
    canonical INTEGER NOT NULL DEFAULT 1 CHECK (canonical IN (0, 1)),
    scanned_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (chain_key <> ''),
    CHECK (cursor_key <> ''),
    CHECK (block_hash ~ '^0x[0-9a-f]{64}$'),
    CHECK (parent_hash ~ '^0x[0-9a-f]{64}$'),
    UNIQUE (chain_id, cursor_key, block_number, block_hash)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_market_scanned_blocks_canonical
    ON market_scanned_blocks (chain_id, cursor_key, block_number)
    WHERE canonical = 1;

CREATE INDEX IF NOT EXISTS idx_market_scanned_blocks_recovery
    ON market_scanned_blocks (chain_id, cursor_key, canonical, block_number DESC);

CREATE TABLE IF NOT EXISTS market_provider_health (
    id BIGSERIAL PRIMARY KEY,
    provider TEXT NOT NULL,
    component TEXT NOT NULL,
    chain_key TEXT NOT NULL DEFAULT '',
    chain_id BIGINT NOT NULL DEFAULT 0 CHECK (chain_id >= 0),
    status TEXT NOT NULL DEFAULT 'unknown'
        CHECK (status IN ('unknown', 'healthy', 'degraded', 'unavailable')),
    consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    latency_ms BIGINT CHECK (latency_ms IS NULL OR latency_ms >= 0),
    last_success_at TIMESTAMPTZ,
    last_failure_at TIMESTAMPTZ,
    last_checked_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (provider <> ''),
    CHECK (component <> ''),
    UNIQUE (provider, component, chain_id)
);

CREATE INDEX IF NOT EXISTS idx_market_provider_health_status
    ON market_provider_health (status, consecutive_failures DESC, last_checked_at);

CREATE TABLE IF NOT EXISTS market_provider_usage (
    id BIGSERIAL PRIMARY KEY,
    provider TEXT NOT NULL,
    metric TEXT NOT NULL,
    period_start TIMESTAMPTZ NOT NULL,
    period_end TIMESTAMPTZ NOT NULL,
    used_units NUMERIC NOT NULL DEFAULT 0 CHECK (used_units >= 0),
    limit_units NUMERIC CHECK (limit_units IS NULL OR limit_units > 0),
    usage_percent NUMERIC(7, 3)
        CHECK (usage_percent IS NULL OR (usage_percent >= 0 AND usage_percent <= 100)),
    alert_level INTEGER NOT NULL DEFAULT 0 CHECK (alert_level IN (0, 70, 85, 95)),
    last_alert_at TIMESTAMPTZ,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    checked_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (provider <> ''),
    CHECK (metric <> ''),
    CHECK (period_end > period_start),
    UNIQUE (provider, metric, period_start, period_end)
);

CREATE INDEX IF NOT EXISTS idx_market_provider_usage_alert
    ON market_provider_usage (provider, metric, alert_level DESC, checked_at DESC);

CREATE INDEX IF NOT EXISTS idx_webhook_inbox_processing_lock
    ON webhook_inbox (processing_status, locked_at)
    WHERE processing_status = 'processing';

CREATE INDEX IF NOT EXISTS idx_market_events_active_block_hash
    ON market_events (chain_id, block_number, block_hash)
    WHERE reorged = 0 AND block_number IS NOT NULL;
