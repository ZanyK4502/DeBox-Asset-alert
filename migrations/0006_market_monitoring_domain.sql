CREATE TABLE IF NOT EXISTS market_pools (
    id BIGSERIAL PRIMARY KEY,
    chain_key TEXT NOT NULL,
    chain_id BIGINT NOT NULL CHECK (chain_id > 0),
    protocol TEXT NOT NULL,
    protocol_version TEXT NOT NULL DEFAULT '',
    pool_key TEXT NOT NULL,
    pool_address TEXT,
    factory_address TEXT,
    factory_verified INTEGER NOT NULL DEFAULT 0 CHECK (factory_verified IN (0, 1)),
    token0_address TEXT NOT NULL,
    token0_symbol TEXT NOT NULL DEFAULT '',
    token0_decimals INTEGER NOT NULL DEFAULT 18 CHECK (token0_decimals BETWEEN 0 AND 255),
    token1_address TEXT NOT NULL,
    token1_symbol TEXT NOT NULL DEFAULT '',
    token1_decimals INTEGER NOT NULL DEFAULT 18 CHECK (token1_decimals BETWEEN 0 AND 255),
    liquidity_usd NUMERIC NOT NULL DEFAULT 0 CHECK (liquidity_usd >= 0),
    supports_event_parsing INTEGER NOT NULL DEFAULT 0 CHECK (supports_event_parsing IN (0, 1)),
    parser_adapter TEXT NOT NULL DEFAULT '',
    verification_status TEXT NOT NULL DEFAULT 'unverified'
        CHECK (verification_status IN ('unverified', 'verified', 'unsupported', 'invalid')),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (chain_key <> ''),
    CHECK (protocol <> ''),
    CHECK (pool_key <> ''),
    CHECK (token0_address ~ '^0x[0-9a-f]{40}$'),
    CHECK (token1_address ~ '^0x[0-9a-f]{40}$'),
    CHECK (pool_address IS NULL OR pool_address ~ '^0x[0-9a-f]{40}$'),
    CHECK (factory_address IS NULL OR factory_address ~ '^0x[0-9a-f]{40}$'),
    UNIQUE (chain_id, protocol, pool_key)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_market_pools_chain_address
    ON market_pools (chain_id, LOWER(pool_address))
    WHERE pool_address IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_market_pools_tokens
    ON market_pools (chain_id, token0_address, token1_address);

CREATE INDEX IF NOT EXISTS idx_market_pools_last_seen
    ON market_pools (last_seen_at DESC);

CREATE TABLE IF NOT EXISTS market_projects (
    id BIGSERIAL PRIMARY KEY,
    debox_user_id TEXT NOT NULL,
    chain_key TEXT NOT NULL DEFAULT 'bsc',
    chain_id BIGINT NOT NULL DEFAULT 56 CHECK (chain_id > 0),
    token_address TEXT NOT NULL,
    token_name TEXT NOT NULL DEFAULT '',
    token_symbol TEXT NOT NULL DEFAULT '',
    token_decimals INTEGER NOT NULL DEFAULT 18 CHECK (token_decimals BETWEEN 0 AND 255),
    total_supply_raw NUMERIC(78, 0) CHECK (total_supply_raw IS NULL OR total_supply_raw >= 0),
    status TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'paused', 'archived')),
    pause_reason TEXT NOT NULL DEFAULT '',
    four_meme_status TEXT NOT NULL DEFAULT 'unknown'
        CHECK (
            four_meme_status IN (
                'unknown', 'not_applicable', 'bonding', 'graduating', 'graduated', 'migrated'
            )
        ),
    main_pool_id BIGINT REFERENCES market_pools(id) ON DELETE SET NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_discovered_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (debox_user_id <> ''),
    CHECK (chain_key <> ''),
    CHECK (token_address ~ '^0x[0-9a-f]{40}$'),
    UNIQUE (debox_user_id, chain_id, token_address),
    UNIQUE (id, debox_user_id)
);

CREATE INDEX IF NOT EXISTS idx_market_projects_user_status
    ON market_projects (debox_user_id, status, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_market_projects_token
    ON market_projects (chain_id, token_address);

CREATE TABLE IF NOT EXISTS market_project_pools (
    id BIGSERIAL PRIMARY KEY,
    market_project_id BIGINT NOT NULL REFERENCES market_projects(id) ON DELETE CASCADE,
    market_pool_id BIGINT NOT NULL REFERENCES market_pools(id) ON DELETE CASCADE,
    selected INTEGER NOT NULL DEFAULT 1 CHECK (selected IN (0, 1)),
    is_primary INTEGER NOT NULL DEFAULT 0 CHECK (is_primary IN (0, 1)),
    discovery_source TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (market_project_id, market_pool_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_market_project_pools_one_primary
    ON market_project_pools (market_project_id)
    WHERE is_primary = 1;

CREATE INDEX IF NOT EXISTS idx_market_project_pools_selected
    ON market_project_pools (market_project_id, selected, is_primary DESC);

CREATE TABLE IF NOT EXISTS market_snapshots (
    id BIGSERIAL PRIMARY KEY,
    chain_key TEXT NOT NULL,
    chain_id BIGINT NOT NULL CHECK (chain_id > 0),
    token_address TEXT NOT NULL,
    market_pool_id BIGINT NOT NULL REFERENCES market_pools(id) ON DELETE CASCADE,
    price_usd NUMERIC CHECK (price_usd IS NULL OR price_usd >= 0),
    liquidity_usd NUMERIC CHECK (liquidity_usd IS NULL OR liquidity_usd >= 0),
    fdv_usd NUMERIC CHECK (fdv_usd IS NULL OR fdv_usd >= 0),
    market_cap_usd NUMERIC CHECK (market_cap_usd IS NULL OR market_cap_usd >= 0),
    volume_5m_usd NUMERIC CHECK (volume_5m_usd IS NULL OR volume_5m_usd >= 0),
    volume_15m_usd NUMERIC CHECK (volume_15m_usd IS NULL OR volume_15m_usd >= 0),
    volume_1h_usd NUMERIC CHECK (volume_1h_usd IS NULL OR volume_1h_usd >= 0),
    volume_6h_usd NUMERIC CHECK (volume_6h_usd IS NULL OR volume_6h_usd >= 0),
    volume_24h_usd NUMERIC CHECK (volume_24h_usd IS NULL OR volume_24h_usd >= 0),
    buys_5m BIGINT CHECK (buys_5m IS NULL OR buys_5m >= 0),
    sells_5m BIGINT CHECK (sells_5m IS NULL OR sells_5m >= 0),
    buys_1h BIGINT CHECK (buys_1h IS NULL OR buys_1h >= 0),
    sells_1h BIGINT CHECK (sells_1h IS NULL OR sells_1h >= 0),
    buys_24h BIGINT CHECK (buys_24h IS NULL OR buys_24h >= 0),
    sells_24h BIGINT CHECK (sells_24h IS NULL OR sells_24h >= 0),
    source TEXT NOT NULL,
    source_timestamp TIMESTAMPTZ,
    captured_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    raw_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    CHECK (chain_key <> ''),
    CHECK (token_address ~ '^0x[0-9a-f]{40}$'),
    CHECK (source <> ''),
    UNIQUE (chain_id, token_address, market_pool_id, source, captured_at)
);

CREATE INDEX IF NOT EXISTS idx_market_snapshots_token_time
    ON market_snapshots (chain_id, token_address, captured_at DESC);

CREATE INDEX IF NOT EXISTS idx_market_snapshots_pool_time
    ON market_snapshots (market_pool_id, captured_at DESC);

CREATE TABLE IF NOT EXISTS market_rules (
    id BIGSERIAL PRIMARY KEY,
    debox_user_id TEXT NOT NULL,
    market_project_id BIGINT NOT NULL REFERENCES market_projects(id) ON DELETE CASCADE,
    market_pool_id BIGINT REFERENCES market_pools(id) ON DELETE SET NULL,
    rule_type TEXT NOT NULL,
    threshold_value NUMERIC NOT NULL CHECK (threshold_value >= 0),
    threshold_unit TEXT NOT NULL
        CHECK (threshold_unit IN ('usd', 'token', 'percent', 'ratio', 'count', 'progress')),
    window_minutes INTEGER CHECK (window_minutes IS NULL OR window_minutes > 0),
    sensitivity TEXT NOT NULL DEFAULT 'balanced'
        CHECK (sensitivity IN ('sensitive', 'balanced', 'stable', 'custom')),
    cooldown_seconds INTEGER NOT NULL DEFAULT 300 CHECK (cooldown_seconds >= 0),
    rule_scope TEXT NOT NULL DEFAULT 'standalone'
        CHECK (rule_scope IN ('standalone', 'combination')),
    delivery_mode TEXT NOT NULL DEFAULT 'realtime'
        CHECK (delivery_mode IN ('realtime', 'stage')),
    cycle_type TEXT NOT NULL DEFAULT 'fixed'
        CHECK (cycle_type IN ('fixed', 'follow')),
    cycle_minutes INTEGER NOT NULL DEFAULT 60 CHECK (cycle_minutes > 0),
    trigger_count_threshold BIGINT NOT NULL DEFAULT 1 CHECK (trigger_count_threshold > 0),
    notification_chat_id TEXT NOT NULL,
    notification_chat_type TEXT NOT NULL DEFAULT 'private'
        CHECK (notification_chat_type IN ('private', 'group')),
    notification_label TEXT NOT NULL DEFAULT '',
    notification_language TEXT NOT NULL DEFAULT 'zh'
        CHECK (notification_language IN ('zh', 'en')),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    run_status TEXT NOT NULL DEFAULT 'active'
        CHECK (run_status IN ('active', 'paused')),
    state JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_evaluated_at TIMESTAMPTZ,
    last_triggered_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (debox_user_id <> ''),
    CHECK (rule_type <> ''),
    CHECK (notification_chat_id <> ''),
    FOREIGN KEY (market_project_id, debox_user_id)
        REFERENCES market_projects(id, debox_user_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_market_rules_user
    ON market_rules (debox_user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_market_rules_project
    ON market_rules (market_project_id, enabled, run_status);

CREATE INDEX IF NOT EXISTS idx_market_rules_active
    ON market_rules (enabled, run_status, last_evaluated_at)
    WHERE enabled = 1 AND run_status = 'active';

CREATE TABLE IF NOT EXISTS market_events (
    id BIGSERIAL PRIMARY KEY,
    market_pool_id BIGINT REFERENCES market_pools(id) ON DELETE SET NULL,
    chain_key TEXT NOT NULL,
    chain_id BIGINT NOT NULL CHECK (chain_id > 0),
    token_address TEXT NOT NULL,
    event_type TEXT NOT NULL,
    event_key TEXT NOT NULL,
    transaction_hash TEXT,
    transaction_index INTEGER CHECK (transaction_index IS NULL OR transaction_index >= 0),
    log_index INTEGER CHECK (log_index IS NULL OR log_index >= 0),
    block_number BIGINT CHECK (block_number IS NULL OR block_number >= 0),
    block_hash TEXT,
    wallet_address TEXT,
    token_amount_raw NUMERIC(78, 0),
    quote_amount_raw NUMERIC(78, 0),
    token_amount NUMERIC,
    quote_amount NUMERIC,
    usd_value NUMERIC CHECK (usd_value IS NULL OR usd_value >= 0),
    price_usd NUMERIC CHECK (price_usd IS NULL OR price_usd >= 0),
    source TEXT NOT NULL,
    confidence NUMERIC(5, 4) NOT NULL DEFAULT 1
        CHECK (confidence >= 0 AND confidence <= 1),
    confirmed INTEGER NOT NULL DEFAULT 0 CHECK (confirmed IN (0, 1)),
    reorged INTEGER NOT NULL DEFAULT 0 CHECK (reorged IN (0, 1)),
    occurred_at TIMESTAMPTZ NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    raw_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    CHECK (chain_key <> ''),
    CHECK (token_address ~ '^0x[0-9a-f]{40}$'),
    CHECK (event_type <> ''),
    CHECK (event_key <> ''),
    CHECK (source <> ''),
    CHECK (transaction_hash IS NULL OR transaction_hash ~ '^0x[0-9a-f]{64}$'),
    CHECK (block_hash IS NULL OR block_hash ~ '^0x[0-9a-f]{64}$'),
    CHECK (wallet_address IS NULL OR wallet_address ~ '^0x[0-9a-f]{40}$'),
    UNIQUE (chain_id, token_address, event_key)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_market_events_chain_log
    ON market_events (chain_id, token_address, transaction_hash, log_index)
    WHERE transaction_hash IS NOT NULL AND log_index IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_market_events_token_time
    ON market_events (chain_id, token_address, occurred_at DESC);

CREATE INDEX IF NOT EXISTS idx_market_events_block
    ON market_events (chain_id, block_number, log_index);

CREATE INDEX IF NOT EXISTS idx_market_events_reconciliation
    ON market_events (chain_id, confirmed, reorged, block_number);

CREATE TABLE IF NOT EXISTS market_rule_events (
    id BIGSERIAL PRIMARY KEY,
    market_rule_id BIGINT NOT NULL REFERENCES market_rules(id) ON DELETE CASCADE,
    market_event_id BIGINT NOT NULL REFERENCES market_events(id) ON DELETE CASCADE,
    trigger_key TEXT NOT NULL,
    notification_message_id TEXT,
    notification_status TEXT NOT NULL DEFAULT 'pending'
        CHECK (notification_status IN ('pending', 'sending', 'sent', 'failed', 'skipped')),
    notification_error TEXT NOT NULL DEFAULT '',
    notification_attempts INTEGER NOT NULL DEFAULT 0 CHECK (notification_attempts >= 0),
    notification_attempted_at TIMESTAMPTZ,
    notification_sent_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (market_rule_id, market_event_id),
    UNIQUE (market_rule_id, trigger_key)
);

CREATE INDEX IF NOT EXISTS idx_market_rule_events_status
    ON market_rule_events (notification_status, created_at);

CREATE TABLE IF NOT EXISTS market_holders (
    id BIGSERIAL PRIMARY KEY,
    chain_key TEXT NOT NULL,
    chain_id BIGINT NOT NULL CHECK (chain_id > 0),
    token_address TEXT NOT NULL,
    holder_address TEXT NOT NULL,
    balance_raw NUMERIC(78, 0) NOT NULL DEFAULT 0 CHECK (balance_raw >= 0),
    balance NUMERIC NOT NULL DEFAULT 0 CHECK (balance >= 0),
    supply_percent NUMERIC(9, 6)
        CHECK (supply_percent IS NULL OR (supply_percent >= 0 AND supply_percent <= 100)),
    rank INTEGER CHECK (rank IS NULL OR rank > 0),
    address_kind TEXT NOT NULL DEFAULT 'wallet',
    excluded INTEGER NOT NULL DEFAULT 0 CHECK (excluded IN (0, 1)),
    exclusion_reason TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (chain_key <> ''),
    CHECK (token_address ~ '^0x[0-9a-f]{40}$'),
    CHECK (holder_address ~ '^0x[0-9a-f]{40}$'),
    UNIQUE (chain_id, token_address, holder_address)
);

CREATE INDEX IF NOT EXISTS idx_market_holders_ranking
    ON market_holders (chain_id, token_address, excluded, rank, balance_raw DESC);

CREATE TABLE IF NOT EXISTS market_holder_snapshots (
    id BIGSERIAL PRIMARY KEY,
    chain_key TEXT NOT NULL,
    chain_id BIGINT NOT NULL CHECK (chain_id > 0),
    token_address TEXT NOT NULL,
    holder_address TEXT NOT NULL,
    balance_raw NUMERIC(78, 0) NOT NULL CHECK (balance_raw >= 0),
    balance NUMERIC NOT NULL CHECK (balance >= 0),
    supply_percent NUMERIC(9, 6)
        CHECK (supply_percent IS NULL OR (supply_percent >= 0 AND supply_percent <= 100)),
    rank INTEGER CHECK (rank IS NULL OR rank > 0),
    source TEXT NOT NULL DEFAULT '',
    captured_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (chain_key <> ''),
    CHECK (token_address ~ '^0x[0-9a-f]{40}$'),
    CHECK (holder_address ~ '^0x[0-9a-f]{40}$'),
    UNIQUE (chain_id, token_address, holder_address, captured_at)
);

CREATE INDEX IF NOT EXISTS idx_market_holder_snapshots_history
    ON market_holder_snapshots (
        chain_id, token_address, holder_address, captured_at DESC
    );

CREATE TABLE IF NOT EXISTS market_address_labels (
    id BIGSERIAL PRIMARY KEY,
    debox_user_id TEXT NOT NULL,
    market_project_id BIGINT NOT NULL REFERENCES market_projects(id) ON DELETE CASCADE,
    chain_key TEXT NOT NULL,
    chain_id BIGINT NOT NULL CHECK (chain_id > 0),
    address TEXT NOT NULL,
    label_type TEXT NOT NULL DEFAULT 'custom',
    label TEXT NOT NULL DEFAULT '',
    excluded INTEGER NOT NULL DEFAULT 0 CHECK (excluded IN (0, 1)),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (debox_user_id <> ''),
    CHECK (chain_key <> ''),
    CHECK (address ~ '^0x[0-9a-f]{40}$'),
    UNIQUE (market_project_id, address),
    FOREIGN KEY (market_project_id, debox_user_id)
        REFERENCES market_projects(id, debox_user_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_market_address_labels_user
    ON market_address_labels (debox_user_id, market_project_id, label_type);

CREATE TABLE IF NOT EXISTS market_chain_cursors (
    id BIGSERIAL PRIMARY KEY,
    chain_key TEXT NOT NULL,
    chain_id BIGINT NOT NULL CHECK (chain_id > 0),
    cursor_key TEXT NOT NULL,
    next_block_number BIGINT NOT NULL DEFAULT 0 CHECK (next_block_number >= 0),
    safe_block_number BIGINT NOT NULL DEFAULT 0 CHECK (safe_block_number >= 0),
    last_block_hash TEXT,
    status TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'paused', 'error')),
    last_error TEXT NOT NULL DEFAULT '',
    last_scanned_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (chain_key <> ''),
    CHECK (cursor_key <> ''),
    CHECK (last_block_hash IS NULL OR last_block_hash ~ '^0x[0-9a-f]{64}$'),
    UNIQUE (chain_id, cursor_key)
);

CREATE TABLE IF NOT EXISTS nodit_webhook_subscriptions (
    id BIGSERIAL PRIMARY KEY,
    provider TEXT NOT NULL DEFAULT 'nodit',
    external_id TEXT,
    chain_key TEXT NOT NULL,
    chain_id BIGINT NOT NULL CHECK (chain_id > 0),
    event_category TEXT NOT NULL,
    callback_url_hash TEXT NOT NULL DEFAULT '',
    secret_reference TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'active', 'paused', 'error', 'deleted')),
    configuration JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_synced_at TIMESTAMPTZ,
    last_checked_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (provider <> ''),
    CHECK (chain_key <> ''),
    CHECK (event_category <> ''),
    UNIQUE (provider, chain_id, event_category)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_nodit_webhook_external_id
    ON nodit_webhook_subscriptions (provider, external_id)
    WHERE external_id IS NOT NULL AND external_id <> '';

CREATE TABLE IF NOT EXISTS webhook_inbox (
    id BIGSERIAL PRIMARY KEY,
    webhook_subscription_id BIGINT
        REFERENCES nodit_webhook_subscriptions(id) ON DELETE SET NULL,
    provider TEXT NOT NULL,
    delivery_id TEXT NOT NULL DEFAULT '',
    dedupe_key TEXT NOT NULL,
    signature_valid INTEGER NOT NULL DEFAULT 0 CHECK (signature_valid IN (0, 1)),
    headers JSONB NOT NULL DEFAULT '{}'::jsonb,
    raw_body BYTEA NOT NULL,
    payload JSONB,
    processing_status TEXT NOT NULL DEFAULT 'pending'
        CHECK (processing_status IN ('pending', 'processing', 'processed', 'failed', 'dead')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    locked_at TIMESTAMPTZ,
    processed_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (provider <> ''),
    CHECK (dedupe_key <> ''),
    UNIQUE (provider, dedupe_key)
);

CREATE INDEX IF NOT EXISTS idx_webhook_inbox_pending
    ON webhook_inbox (processing_status, next_attempt_at, id);

CREATE INDEX IF NOT EXISTS idx_webhook_inbox_received
    ON webhook_inbox (received_at DESC);
