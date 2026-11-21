-- Empty provenance means an existing balance has not been joined to the new
-- reader. No baseline, recovery boundary or notification is changed here.
CREATE TABLE IF NOT EXISTS wallet_balance_read_states (
    watch_rule_id BIGINT PRIMARY KEY REFERENCES watch_rules(id) ON DELETE CASCADE,
    chain_id BIGINT NOT NULL CHECK (chain_id > 0),
    chain_key TEXT NOT NULL CHECK (length(chain_key) BETWEEN 1 AND 32),
    wallet_address TEXT NOT NULL CHECK (wallet_address ~ '^0x[0-9a-f]{40}$'),
    token_address TEXT NOT NULL CHECK (token_address ~ '^0x[0-9a-f]{40}$'),
    read_source TEXT NOT NULL CHECK (length(read_source) BETWEEN 1 AND 64),
    decimals SMALLINT NOT NULL CHECK (decimals BETWEEN 0 AND 255),
    baseline_value TEXT NOT NULL CHECK (length(baseline_value) BETWEEN 1 AND 335),
    baseline_checked_at TIMESTAMPTZ NOT NULL,
    read_observed_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

-- Written only for an explicitly permitted source transition, never for every
-- successful poll. The previous value and source remain available for review.
CREATE TABLE IF NOT EXISTS wallet_balance_source_transitions (
    id BIGSERIAL PRIMARY KEY,
    watch_rule_id BIGINT NOT NULL REFERENCES watch_rules(id) ON DELETE CASCADE,
    debox_user_id TEXT NOT NULL,
    previous_source TEXT NOT NULL CHECK (length(previous_source) BETWEEN 1 AND 64),
    previous_decimals SMALLINT CHECK (previous_decimals BETWEEN 0 AND 255),
    previous_value TEXT NOT NULL,
    previous_checked_at TIMESTAMPTZ NOT NULL,
    next_source TEXT NOT NULL CHECK (length(next_source) BETWEEN 1 AND 64),
    next_decimals SMALLINT NOT NULL CHECK (next_decimals BETWEEN 0 AND 255),
    next_value TEXT NOT NULL CHECK (length(next_value) BETWEEN 1 AND 335),
    cutoff_at TIMESTAMPTZ NOT NULL,
    read_observed_at TIMESTAMPTZ NOT NULL,
    source_context JSONB NOT NULL CHECK (jsonb_typeof(source_context) = 'object' AND octet_length(source_context::text) <= 4096),
    reason TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 1000),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (watch_rule_id, previous_checked_at, cutoff_at)
);
