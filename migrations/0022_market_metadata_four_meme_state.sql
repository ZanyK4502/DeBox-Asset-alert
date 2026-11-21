-- Existing pool decimals have no reliable provenance. Do not label legacy
-- defaults as verified, and do not guess a replacement decimal count.
ALTER TABLE market_pools
    ADD COLUMN IF NOT EXISTS token0_decimals_verified BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS token1_decimals_verified BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS market_four_meme_states (
    chain_id BIGINT NOT NULL CHECK (chain_id > 0),
    token_address TEXT NOT NULL CHECK (token_address ~ '^0x[0-9a-f]{40}$'),
    source_event_id BIGINT NOT NULL REFERENCES market_events(id) ON DELETE RESTRICT,
    source_block_hash TEXT NOT NULL CHECK (source_block_hash ~ '^0x[0-9a-f]{64}$'),
    source_block_number BIGINT NOT NULL CHECK (source_block_number >= 0),
    source_transaction_index INTEGER NOT NULL CHECK (source_transaction_index >= 0),
    source_log_index INTEGER NOT NULL CHECK (source_log_index >= 0),
    status TEXT NOT NULL CHECK (status IN ('bonding', 'graduating', 'migrated')),
    occurred_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (chain_id, token_address)
);

CREATE INDEX IF NOT EXISTS idx_market_four_meme_states_source
    ON market_four_meme_states (source_event_id);

-- Both normal advancement and reorg recovery read only the last effective
-- Four.meme event for a single token, rather than scanning transaction history.
CREATE INDEX IF NOT EXISTS idx_market_events_four_meme_position
    ON market_events (
        chain_id, token_address, block_number DESC,
        transaction_index DESC, log_index DESC, id DESC
    )
    WHERE reorged = 0
      AND block_number IS NOT NULL AND block_hash IS NOT NULL
      AND transaction_index IS NOT NULL AND log_index IS NOT NULL
      AND metadata->>'adapter' IN ('four_meme_v1', 'four_meme_v2')
      AND event_type IN ('token_created', 'buy', 'sell', 'trading_stopped', 'migrated');
