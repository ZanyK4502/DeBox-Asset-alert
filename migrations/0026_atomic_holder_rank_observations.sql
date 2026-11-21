-- Current rank coverage is independent of the legacy Top20 balance tracker.
-- No legacy holder, snapshot, rule parameter or notification is rewritten.
CREATE TABLE IF NOT EXISTS market_holder_refresh_states (
    chain_id BIGINT NOT NULL CHECK (chain_id > 0),
    token_address TEXT NOT NULL CHECK (token_address ~ '^0x[0-9a-f]{40}$'),
    chain_key TEXT NOT NULL CHECK (chain_key <> ''),
    last_success_at TIMESTAMPTZ,
    last_attempt_at TIMESTAMPTZ NOT NULL,
    covered_top_n INTEGER NOT NULL DEFAULT 0 CHECK (covered_top_n BETWEEN 0 AND 100),
    source_item_count INTEGER NOT NULL DEFAULT 0 CHECK (source_item_count BETWEEN 0 AND 1000),
    rank_baseline JSONB NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(rank_baseline) = 'array' AND jsonb_array_length(rank_baseline) <= 100 AND octet_length(rank_baseline::text) <= 65536),
    exclusion_hash TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'unknown' CHECK (status IN ('unknown', 'complete', 'source_coverage_unknown')),
    PRIMARY KEY (chain_id, token_address),
    CHECK ((last_success_at IS NULL AND covered_top_n = 0) OR (last_success_at IS NOT NULL AND covered_top_n > 0))
);

ALTER TABLE market_holder_snapshots
    ADD COLUMN IF NOT EXISTS managed_observation BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS balance_observed BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS balance_observed_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_holder_managed_snapshot_retention
    ON market_holder_snapshots (captured_at, id) WHERE managed_observation;

CREATE TABLE IF NOT EXISTS market_holder_snapshot_refs (
    snapshot_id BIGINT NOT NULL REFERENCES market_holder_snapshots(id) ON DELETE RESTRICT,
    market_event_id BIGINT NOT NULL REFERENCES market_events(id) ON DELETE CASCADE,
    PRIMARY KEY (snapshot_id, market_event_id)
);
CREATE INDEX IF NOT EXISTS idx_holder_snapshot_refs_event
    ON market_holder_snapshot_refs (market_event_id);

CREATE INDEX IF NOT EXISTS idx_market_pools_holder_token1
    ON market_pools (chain_id, token1_address) WHERE pool_address IS NOT NULL;

CREATE TABLE IF NOT EXISTS market_holder_history_cleanup_cursor (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    after_at TIMESTAMPTZ,
    after_id BIGINT NOT NULL DEFAULT 0 CHECK (after_id >= 0)
);
