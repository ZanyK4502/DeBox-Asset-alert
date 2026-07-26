ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS is_permanent INTEGER NOT NULL DEFAULT 0 CHECK (is_permanent IN (0, 1));

ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS entitlement_source TEXT NOT NULL DEFAULT 'paid';

ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS entitlement_wallet_address TEXT;

ALTER TABLE subscriptions ADD CONSTRAINT subscriptions_entitlement_wallet_address_check
    CHECK (
        entitlement_wallet_address IS NULL
        OR entitlement_wallet_address ~ '^0x[0-9a-f]{40}$'
    );

CREATE TABLE IF NOT EXISTS permanent_plan_allowlist (
    wallet_address TEXT PRIMARY KEY,
    plan_code TEXT NOT NULL CHECK (plan_code IN ('standard', 'professional')),
    debox_user_id TEXT,
    subscription_id BIGINT REFERENCES subscriptions(id) ON DELETE SET NULL,
    bound_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (wallet_address ~ '^0x[0-9a-f]{40}$')
);

CREATE INDEX IF NOT EXISTS idx_permanent_plan_allowlist_user
    ON permanent_plan_allowlist (debox_user_id)
    WHERE debox_user_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_subscriptions_permanent_wallet
    ON subscriptions (entitlement_wallet_address)
    WHERE is_permanent = 1 AND status = 'active';

INSERT INTO permanent_plan_allowlist (wallet_address, plan_code)
VALUES
    ('0xcba3fce9d49ce5d7870443f324a8dd56a5788bfc', 'professional'),
    ('0xe4f1f421d116ed75822c4527bfaf332566043b2d', 'professional'),
    ('0x50d593be2c06d7b13c5deb3b9565b4b54ebda3a1', 'professional'),
    ('0xdd7e931d86c1ae7d38453e2c261e048f323497c4', 'professional'),
    ('0xcd44ffeb623bdc62a821a0301fad91e1c44c3643', 'standard')
ON CONFLICT (wallet_address) DO UPDATE
SET plan_code = EXCLUDED.plan_code,
    updated_at = NOW();
