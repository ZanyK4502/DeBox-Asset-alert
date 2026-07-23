CREATE TABLE IF NOT EXISTS subscriptions (
    id SERIAL PRIMARY KEY,
    debox_user_id TEXT NOT NULL,
    plan_code TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    starts_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    daily_summary_enabled INTEGER NOT NULL DEFAULT 0,
    daily_summary_time TEXT NOT NULL DEFAULT '20:00',
    daily_summary_timezone TEXT NOT NULL DEFAULT 'Asia/Shanghai',
    daily_summary_chat_type TEXT NOT NULL DEFAULT 'private',
    daily_summary_chat_id TEXT NOT NULL DEFAULT '',
    daily_summary_label TEXT NOT NULL DEFAULT '',
    daily_summary_language TEXT NOT NULL DEFAULT 'zh',
    daily_summary_last_sent_date TEXT NOT NULL DEFAULT '',
    scheduled_push_last_sent_at TIMESTAMPTZ,
    daily_summary_last_period_end_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS watch_rules (
    id SERIAL PRIMARY KEY,
    debox_user_id TEXT NOT NULL,
    chain_key TEXT NOT NULL DEFAULT 'bsc',
    chain_id INTEGER NOT NULL DEFAULT 56,
    wallet_address TEXT NOT NULL,
    token_address TEXT,
    target_address TEXT,
    target_label TEXT NOT NULL DEFAULT '',
    rule_type TEXT NOT NULL,
    threshold NUMERIC NOT NULL DEFAULT 0,
    notification_chat_id TEXT NOT NULL,
    notification_chat_type TEXT NOT NULL DEFAULT 'private',
    notification_label TEXT NOT NULL DEFAULT '',
    notification_language TEXT NOT NULL DEFAULT 'zh',
    enabled INTEGER NOT NULL DEFAULT 1,
    run_status TEXT NOT NULL DEFAULT 'active',
    last_value TEXT,
    last_checked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS orders (
    id SERIAL PRIMARY KEY,
    debox_user_id TEXT NOT NULL,
    payer_address TEXT NOT NULL,
    plan_code TEXT NOT NULL DEFAULT 'standard',
    chain_key TEXT NOT NULL DEFAULT 'bsc',
    chain_id INTEGER NOT NULL DEFAULT 56,
    token_address TEXT,
    token_symbol TEXT NOT NULL DEFAULT 'USDT',
    token_decimals INTEGER NOT NULL DEFAULT 18,
    total_amount NUMERIC NOT NULL,
    recipient_address TEXT NOT NULL,
    tx_hash TEXT,
    status TEXT NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    tx_block_number BIGINT,
    tx_confirmations INTEGER NOT NULL DEFAULT 0,
    verified_at TIMESTAMPTZ,
    verification_error TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS alert_events (
    id SERIAL PRIMARY KEY,
    watch_rule_id INTEGER NOT NULL REFERENCES watch_rules(id) ON DELETE CASCADE,
    event_type TEXT NOT NULL,
    previous_value TEXT,
    current_value TEXT,
    notification_message_id TEXT,
    notification_status TEXT NOT NULL DEFAULT 'sent',
    notification_error TEXT NOT NULL DEFAULT '',
    notification_attempts INTEGER NOT NULL DEFAULT 0,
    notification_attempted_at TIMESTAMPTZ,
    notification_sent_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS notification_groups (
    id SERIAL PRIMARY KEY,
    debox_user_id TEXT NOT NULL,
    gid TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (debox_user_id, gid)
);

CREATE TABLE IF NOT EXISTS user_preferences (
    debox_user_id TEXT PRIMARY KEY,
    free_watch_rule_id INTEGER REFERENCES watch_rules(id) ON DELETE SET NULL,
    bot_language TEXT NOT NULL DEFAULT 'zh',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS auth_challenges (
    challenge_id TEXT PRIMARY KEY,
    wallet_address TEXT NOT NULL,
    nonce_hash TEXT NOT NULL UNIQUE,
    message TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS auth_sessions (
    token_hash TEXT PRIMARY KEY,
    debox_user_id TEXT NOT NULL,
    wallet_address TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS complimentary_grants (
    wallet_address TEXT PRIMARY KEY,
    debox_user_id TEXT NOT NULL,
    plan_code TEXT NOT NULL,
    starts_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS chain_key TEXT NOT NULL DEFAULT 'bsc';
ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS chain_id INTEGER NOT NULL DEFAULT 56;
ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS target_address TEXT;
ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS target_label TEXT NOT NULL DEFAULT '';
ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS notification_label TEXT NOT NULL DEFAULT '';
ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS notification_language TEXT NOT NULL DEFAULT 'zh';
ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS run_status TEXT NOT NULL DEFAULT 'active';
ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS last_value TEXT;
ALTER TABLE watch_rules ADD COLUMN IF NOT EXISTS last_checked_at TIMESTAMPTZ;

ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS daily_summary_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS daily_summary_time TEXT NOT NULL DEFAULT '20:00';
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS daily_summary_timezone TEXT NOT NULL DEFAULT 'Asia/Shanghai';
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS daily_summary_chat_type TEXT NOT NULL DEFAULT 'private';
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS daily_summary_chat_id TEXT NOT NULL DEFAULT '';
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS daily_summary_label TEXT NOT NULL DEFAULT '';
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS daily_summary_language TEXT NOT NULL DEFAULT 'zh';
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS daily_summary_last_sent_date TEXT NOT NULL DEFAULT '';
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS scheduled_push_last_sent_at TIMESTAMPTZ;
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS daily_summary_last_period_end_at TIMESTAMPTZ;

ALTER TABLE user_preferences ADD COLUMN IF NOT EXISTS bot_language TEXT NOT NULL DEFAULT 'zh';

ALTER TABLE orders ADD COLUMN IF NOT EXISTS plan_code TEXT NOT NULL DEFAULT 'standard';
ALTER TABLE orders ADD COLUMN IF NOT EXISTS chain_key TEXT NOT NULL DEFAULT 'bsc';
ALTER TABLE orders ADD COLUMN IF NOT EXISTS chain_id INTEGER NOT NULL DEFAULT 56;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS token_address TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS token_symbol TEXT NOT NULL DEFAULT 'USDT';
ALTER TABLE orders ADD COLUMN IF NOT EXISTS token_decimals INTEGER NOT NULL DEFAULT 18;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS recipient_address TEXT NOT NULL DEFAULT '';
ALTER TABLE orders ADD COLUMN IF NOT EXISTS tx_hash TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE orders ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE orders ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ NOT NULL DEFAULT (NOW() + INTERVAL '20 minutes');
ALTER TABLE orders ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS tx_block_number BIGINT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS tx_confirmations INTEGER NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS verified_at TIMESTAMPTZ;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS verification_error TEXT NOT NULL DEFAULT '';

ALTER TABLE alert_events ADD COLUMN IF NOT EXISTS notification_status TEXT NOT NULL DEFAULT 'sent';
ALTER TABLE alert_events ADD COLUMN IF NOT EXISTS notification_error TEXT NOT NULL DEFAULT '';
ALTER TABLE alert_events ADD COLUMN IF NOT EXISTS notification_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE alert_events ADD COLUMN IF NOT EXISTS notification_attempted_at TIMESTAMPTZ;
ALTER TABLE alert_events ADD COLUMN IF NOT EXISTS notification_sent_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_watch_rules_user ON watch_rules (debox_user_id);
CREATE INDEX IF NOT EXISTS idx_watch_rules_enabled ON watch_rules (enabled);
CREATE INDEX IF NOT EXISTS idx_watch_rules_run_status ON watch_rules (run_status);
CREATE INDEX IF NOT EXISTS idx_subscriptions_user ON subscriptions (debox_user_id);
CREATE INDEX IF NOT EXISTS idx_orders_user ON orders (debox_user_id);
CREATE INDEX IF NOT EXISTS idx_events_rule ON alert_events (watch_rule_id);
CREATE INDEX IF NOT EXISTS idx_events_created ON alert_events (created_at);
CREATE INDEX IF NOT EXISTS idx_groups_user ON notification_groups (debox_user_id);
CREATE INDEX IF NOT EXISTS idx_auth_challenges_expiry ON auth_challenges (expires_at);
CREATE INDEX IF NOT EXISTS idx_auth_sessions_user ON auth_sessions (debox_user_id);
CREATE INDEX IF NOT EXISTS idx_auth_sessions_expiry ON auth_sessions (expires_at);
CREATE INDEX IF NOT EXISTS idx_complimentary_grants_user ON complimentary_grants (debox_user_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_orders_tx_hash_unique
ON orders (LOWER(tx_hash))
WHERE tx_hash IS NOT NULL AND tx_hash <> '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_orders_one_open_per_user
ON orders (debox_user_id)
WHERE status IN ('pending', 'confirming');
