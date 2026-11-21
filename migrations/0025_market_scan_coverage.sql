-- Coverage is evidence from a completed scope-specific log scan, never from
-- cursor height or the presence/absence of a few hydrated transactions.
CREATE TABLE IF NOT EXISTS market_scan_coverage (
    id BIGSERIAL PRIMARY KEY,
    chain_id BIGINT NOT NULL CHECK (chain_id > 0),
    cursor_key TEXT NOT NULL CHECK (cursor_key <> ''),
    scope_id BIGINT NOT NULL REFERENCES market_trade_scopes(id),
    from_block BIGINT NOT NULL CHECK (from_block > 0),
    through_block BIGINT NOT NULL CHECK (through_block >= from_block),
    after_at TIMESTAMPTZ NOT NULL,
    through_at TIMESTAMPTZ NOT NULL CHECK (through_at >= after_at),
    after_hash TEXT NOT NULL CHECK (after_hash ~ '^0x[0-9a-f]{64}$'),
    through_hash TEXT NOT NULL CHECK (through_hash ~ '^0x[0-9a-f]{64}$'),
    facts_retained_after TIMESTAMPTZ NOT NULL DEFAULT '-infinity',
    verified_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_market_scan_coverage_scope_blocks
    ON market_scan_coverage (chain_id,cursor_key,scope_id,from_block,through_block);
CREATE INDEX IF NOT EXISTS idx_market_scan_coverage_time
    ON market_scan_coverage (chain_id,through_at,after_at);
CREATE INDEX IF NOT EXISTS idx_market_scan_coverage_scope
    ON market_scan_coverage (scope_id);

-- Verify captured scope within the window without reading receipt JSON.
CREATE INDEX IF NOT EXISTS idx_market_receipts_window_scope
    ON market_transaction_receipts (chain_id,occurred_at,block_number)
    WHERE NOT reorged;
