-- Additive receipt facts. Existing events, notification references and rule
-- watermarks remain untouched; no legacy row is declared complete by migration.
CREATE TABLE IF NOT EXISTS market_trade_scopes (
    id BIGSERIAL PRIMARY KEY,
    scope_key TEXT NOT NULL UNIQUE CHECK (scope_key ~ '^[0-9a-f]{64}$'),
    parser_version INTEGER NOT NULL CHECK (parser_version > 0),
    pool_ids BIGINT[] NOT NULL,
    token_addresses TEXT[] NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS market_transaction_receipts (
    id BIGSERIAL PRIMARY KEY,
    chain_key TEXT NOT NULL CHECK (chain_key <> ''),
    chain_id BIGINT NOT NULL CHECK (chain_id > 0),
    transaction_hash TEXT NOT NULL CHECK (transaction_hash ~ '^0x[0-9a-f]{64}$'),
    block_number BIGINT NOT NULL CHECK (block_number >= 0),
    block_hash TEXT NOT NULL CHECK (block_hash ~ '^0x[0-9a-f]{64}$'),
    transaction_index INTEGER NOT NULL CHECK (transaction_index >= 0),
    occurred_at TIMESTAMPTZ NOT NULL,
    scope_id BIGINT NOT NULL REFERENCES market_trade_scopes(id),
    receipt_payload JSONB NOT NULL CHECK (jsonb_typeof(receipt_payload) = 'object'),
    complete BOOLEAN NOT NULL DEFAULT FALSE,
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    confirmed BOOLEAN NOT NULL DEFAULT FALSE,
    reorged BOOLEAN NOT NULL DEFAULT FALSE,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (chain_id, transaction_hash, block_hash),
    CHECK (NOT complete OR completed_at IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS idx_market_transaction_receipts_confirmation
    ON market_transaction_receipts (chain_id, block_number, id)
    WHERE complete AND NOT confirmed AND NOT reorged;
CREATE INDEX IF NOT EXISTS idx_market_transaction_receipts_reorg
    ON market_transaction_receipts (chain_id, block_number, block_hash)
    WHERE NOT reorged;
CREATE INDEX IF NOT EXISTS idx_market_transaction_receipts_retention
    ON market_transaction_receipts (occurred_at, id);
CREATE INDEX IF NOT EXISTS idx_market_transaction_receipts_scope
    ON market_transaction_receipts (scope_id);

CREATE TABLE IF NOT EXISTS market_transaction_events (
    receipt_id BIGINT NOT NULL REFERENCES market_transaction_receipts(id) ON DELETE CASCADE,
    market_event_id BIGINT NOT NULL REFERENCES market_events(id) ON DELETE CASCADE,
    PRIMARY KEY (receipt_id, market_event_id)
);
CREATE INDEX IF NOT EXISTS idx_market_transaction_events_parent
    ON market_transaction_events (market_event_id, receipt_id);

CREATE TABLE IF NOT EXISTS market_trade_parents (
    parent_event_id BIGINT PRIMARY KEY REFERENCES market_events(id) ON DELETE CASCADE,
    chain_id BIGINT NOT NULL CHECK (chain_id > 0),
    transaction_hash TEXT NOT NULL CHECK (transaction_hash ~ '^0x[0-9a-f]{64}$'),
    token_address TEXT NOT NULL CHECK (token_address ~ '^0x[0-9a-f]{40}$'),
    direction TEXT NOT NULL CHECK (direction IN ('buy', 'sell')),
    UNIQUE (chain_id, transaction_hash, token_address, direction)
);

CREATE TABLE IF NOT EXISTS market_trade_legs (
    id BIGSERIAL PRIMARY KEY,
    receipt_id BIGINT NOT NULL,
    parent_event_id BIGINT NOT NULL,
    pool_id BIGINT NOT NULL REFERENCES market_pools(id),
    log_index BIGINT NOT NULL CHECK (log_index >= 0),
    quote_address TEXT CHECK (quote_address IS NULL OR quote_address ~ '^0x[0-9a-f]{40}$'),
    token_amount_raw NUMERIC(78,0) NOT NULL CHECK (token_amount_raw >= 0),
    quote_amount_raw NUMERIC(78,0) CHECK (quote_amount_raw >= 0),
    token_amount NUMERIC CHECK (token_amount >= 0),
    quote_amount NUMERIC CHECK (quote_amount >= 0),
    usd_value NUMERIC CHECK (usd_value >= 0),
    price_usd NUMERIC CHECK (price_usd >= 0),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(metadata) = 'object' AND octet_length(metadata::text) <= 2048),
    FOREIGN KEY (receipt_id, parent_event_id)
        REFERENCES market_transaction_events(receipt_id, market_event_id) ON DELETE CASCADE,
    UNIQUE (receipt_id, parent_event_id, log_index)
);
CREATE INDEX IF NOT EXISTS idx_market_trade_legs_projection
    ON market_trade_legs (receipt_id, parent_event_id, pool_id, log_index);

-- IDs are allocated only while holding the chain transaction lock. Publishing
-- after complete+canonical verification, rather than event insertion IDs, keeps
-- a later confirmation or a slow concurrent receipt from falling behind a rule.
CREATE TABLE IF NOT EXISTS market_trade_ready_events (
    id BIGSERIAL PRIMARY KEY,
    receipt_id BIGINT NOT NULL REFERENCES market_transaction_receipts(id) ON DELETE CASCADE,
    receipt_revision INTEGER NOT NULL CHECK (receipt_revision > 0),
    parent_event_id BIGINT NOT NULL REFERENCES market_trade_parents(parent_event_id) ON DELETE CASCADE,
    chain_id BIGINT NOT NULL,
    token_address TEXT NOT NULL,
    valid BOOLEAN NOT NULL DEFAULT TRUE,
    published_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (receipt_id, receipt_revision, parent_event_id)
);
CREATE INDEX IF NOT EXISTS idx_market_trade_ready_target
    ON market_trade_ready_events (chain_id, token_address, id) WHERE valid;
