-- Migration 0010 intentionally removed the empty complimentary grant table,
-- but its schema-qualified guard skipped isolated non-public test schemas.
-- Repeat the same guarded cleanup without changing the already-applied file.
DO $$
BEGIN
    IF to_regclass('complimentary_grants') IS NOT NULL THEN
        IF EXISTS (SELECT 1 FROM complimentary_grants) THEN
            RAISE EXCEPTION 'complimentary_grants must be empty before removal';
        END IF;
        DROP TABLE complimentary_grants;
    END IF;
END
$$;

CREATE TABLE IF NOT EXISTS market_assets (
    id BIGSERIAL PRIMARY KEY,
    canonical_name TEXT NOT NULL DEFAULT '',
    symbol TEXT NOT NULL DEFAULT '',
    logo_url TEXT NOT NULL DEFAULT '',
    identity_source TEXT NOT NULL,
    canonical_asset_id TEXT NOT NULL,
    verification_status TEXT NOT NULL DEFAULT 'unverified'
        CHECK (
            verification_status IN (
                'unverified', 'single_chain', 'verified', 'conflicted', 'rejected'
            )
        ),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (identity_source <> ''),
    CHECK (canonical_asset_id <> ''),
    UNIQUE (identity_source, canonical_asset_id)
);

CREATE INDEX IF NOT EXISTS idx_market_assets_identity
    ON market_assets (identity_source, canonical_asset_id);

CREATE INDEX IF NOT EXISTS idx_market_assets_search
    ON market_assets (LOWER(symbol), LOWER(canonical_name));

CREATE TABLE IF NOT EXISTS market_asset_deployments (
    id BIGSERIAL PRIMARY KEY,
    market_asset_id BIGINT NOT NULL REFERENCES market_assets(id) ON DELETE CASCADE,
    chain_key TEXT NOT NULL,
    chain_id BIGINT NOT NULL CHECK (chain_id > 0),
    token_address TEXT NOT NULL,
    token_name TEXT NOT NULL DEFAULT '',
    token_symbol TEXT NOT NULL DEFAULT '',
    token_decimals INTEGER NOT NULL DEFAULT 18 CHECK (token_decimals BETWEEN 0 AND 255),
    total_supply_raw NUMERIC(78, 0) CHECK (total_supply_raw IS NULL OR total_supply_raw >= 0),
    verification_status TEXT NOT NULL DEFAULT 'unverified'
        CHECK (
            verification_status IN (
                'unverified', 'single_chain', 'verified', 'conflicted', 'rejected'
            )
        ),
    verification_source TEXT NOT NULL DEFAULT '',
    verification_evidence JSONB NOT NULL DEFAULT '{}'::jsonb,
    default_market_pool_id BIGINT REFERENCES market_pools(id) ON DELETE SET NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    verified_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (chain_key <> ''),
    CHECK (token_address ~ '^0x[0-9a-f]{40}$'),
    UNIQUE (market_asset_id, chain_id),
    UNIQUE (chain_id, token_address),
    UNIQUE (id, market_asset_id)
);

CREATE INDEX IF NOT EXISTS idx_market_asset_deployments_asset
    ON market_asset_deployments (market_asset_id, chain_id);

CREATE INDEX IF NOT EXISTS idx_market_asset_deployments_token
    ON market_asset_deployments (chain_id, token_address);

CREATE TABLE IF NOT EXISTS market_asset_identity_evidence (
    id BIGSERIAL PRIMARY KEY,
    market_asset_id BIGINT NOT NULL REFERENCES market_assets(id) ON DELETE CASCADE,
    market_asset_deployment_id BIGINT
        REFERENCES market_asset_deployments(id) ON DELETE CASCADE,
    evidence_key TEXT NOT NULL,
    source TEXT NOT NULL,
    evidence_type TEXT NOT NULL,
    external_asset_id TEXT NOT NULL DEFAULT '',
    verdict TEXT NOT NULL
        CHECK (verdict IN ('supports', 'conflicts', 'inconclusive')),
    confidence NUMERIC(5, 4) NOT NULL DEFAULT 0
        CHECK (confidence >= 0 AND confidence <= 1),
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (evidence_key <> ''),
    CHECK (source <> ''),
    CHECK (evidence_type <> ''),
    UNIQUE (market_asset_id, evidence_key)
);

CREATE INDEX IF NOT EXISTS idx_market_asset_identity_evidence_deployment
    ON market_asset_identity_evidence (
        market_asset_deployment_id, verdict, observed_at DESC
    );

ALTER TABLE market_projects
    ADD COLUMN IF NOT EXISTS market_asset_id BIGINT
        REFERENCES market_assets(id) ON DELETE RESTRICT;

CREATE UNIQUE INDEX IF NOT EXISTS idx_market_projects_id_asset
    ON market_projects (id, market_asset_id);

CREATE INDEX IF NOT EXISTS idx_market_projects_asset
    ON market_projects (market_asset_id, debox_user_id, status);

CREATE TABLE IF NOT EXISTS market_project_deployments (
    id BIGSERIAL PRIMARY KEY,
    market_project_id BIGINT NOT NULL,
    market_asset_id BIGINT NOT NULL,
    market_asset_deployment_id BIGINT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'paused', 'removed')),
    pause_reason TEXT NOT NULL DEFAULT '',
    default_market_pool_id BIGINT REFERENCES market_pools(id) ON DELETE SET NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    FOREIGN KEY (market_project_id, market_asset_id)
        REFERENCES market_projects(id, market_asset_id) ON DELETE CASCADE,
    FOREIGN KEY (market_asset_deployment_id, market_asset_id)
        REFERENCES market_asset_deployments(id, market_asset_id) ON DELETE RESTRICT,
    UNIQUE (market_project_id, market_asset_deployment_id),
    UNIQUE (id, market_project_id)
);

CREATE INDEX IF NOT EXISTS idx_market_project_deployments_project
    ON market_project_deployments (market_project_id, status, id);

CREATE INDEX IF NOT EXISTS idx_market_project_deployments_asset_deployment
    ON market_project_deployments (market_asset_deployment_id, status);

-- Convert every legacy single-chain project into one logical asset and one
-- project deployment. The old chain/token columns remain as compatibility
-- mirrors until every reader has moved to the multi-chain model.
INSERT INTO market_assets (
    canonical_name,
    symbol,
    logo_url,
    identity_source,
    canonical_asset_id,
    verification_status,
    metadata,
    created_at,
    updated_at
)
SELECT
    CASE
        WHEN BTRIM(mp.token_name) <> '' THEN mp.token_name
        WHEN BTRIM(mp.token_symbol) <> '' THEN mp.token_symbol
        ELSE mp.token_address
    END,
    mp.token_symbol,
    '',
    'legacy',
    'eip155:' || mp.chain_id::text || '/erc20:' || mp.token_address,
    'single_chain',
    jsonb_build_object(
        'migration', '0011_multichain_market_domain',
        'legacy_chain_key', mp.chain_key
    ),
    MIN(mp.created_at),
    MAX(mp.updated_at)
FROM market_projects mp
WHERE mp.market_asset_id IS NULL
GROUP BY
    mp.chain_id,
    mp.chain_key,
    mp.token_address,
    mp.token_name,
    mp.token_symbol
ON CONFLICT (identity_source, canonical_asset_id) DO NOTHING;

UPDATE market_projects mp
SET market_asset_id = ma.id
FROM market_assets ma
WHERE mp.market_asset_id IS NULL
  AND ma.identity_source = 'legacy'
  AND ma.canonical_asset_id =
      'eip155:' || mp.chain_id::text || '/erc20:' || mp.token_address;

INSERT INTO market_asset_deployments (
    market_asset_id,
    chain_key,
    chain_id,
    token_address,
    token_name,
    token_symbol,
    token_decimals,
    total_supply_raw,
    verification_status,
    verification_source,
    verification_evidence,
    default_market_pool_id,
    metadata,
    verified_at,
    created_at,
    updated_at
)
SELECT DISTINCT ON (mp.market_asset_id, mp.chain_id)
    mp.market_asset_id,
    mp.chain_key,
    mp.chain_id,
    mp.token_address,
    mp.token_name,
    mp.token_symbol,
    mp.token_decimals,
    mp.total_supply_raw,
    'single_chain',
    'legacy_migration',
    jsonb_build_object(
        'kind', 'legacy_single_chain_project',
        'market_project_id', mp.id
    ),
    mp.main_pool_id,
    mp.metadata,
    mp.last_discovered_at,
    mp.created_at,
    mp.updated_at
FROM market_projects mp
WHERE mp.market_asset_id IS NOT NULL
ORDER BY
    mp.market_asset_id,
    mp.chain_id,
    (mp.main_pool_id IS NOT NULL) DESC,
    (mp.status = 'active') DESC,
    mp.updated_at DESC,
    mp.id
ON CONFLICT (chain_id, token_address) DO NOTHING;

INSERT INTO market_project_deployments (
    market_project_id,
    market_asset_id,
    market_asset_deployment_id,
    status,
    pause_reason,
    default_market_pool_id,
    metadata,
    created_at,
    updated_at
)
SELECT
    mp.id,
    mp.market_asset_id,
    mad.id,
    CASE WHEN mp.status = 'archived' THEN 'removed' ELSE mp.status END,
    mp.pause_reason,
    mp.main_pool_id,
    jsonb_build_object('migration', '0011_multichain_market_domain'),
    mp.created_at,
    mp.updated_at
FROM market_projects mp
JOIN market_asset_deployments mad
  ON mad.market_asset_id = mp.market_asset_id
 AND mad.chain_id = mp.chain_id
 AND mad.token_address = mp.token_address
WHERE mp.market_asset_id IS NOT NULL
ON CONFLICT (market_project_id, market_asset_deployment_id) DO NOTHING;

INSERT INTO market_asset_identity_evidence (
    market_asset_id,
    market_asset_deployment_id,
    evidence_key,
    source,
    evidence_type,
    external_asset_id,
    verdict,
    confidence,
    payload,
    observed_at
)
SELECT
    mad.market_asset_id,
    mad.id,
    'legacy:' || mad.chain_id::text || ':' || mad.token_address,
    'legacy_migration',
    'onchain_contract',
    'eip155:' || mad.chain_id::text || '/erc20:' || mad.token_address,
    'supports',
    1,
    jsonb_build_object(
        'chain_key', mad.chain_key,
        'token_name', mad.token_name,
        'token_symbol', mad.token_symbol,
        'verification_scope', 'single_chain_only'
    ),
    COALESCE(mad.verified_at, mad.created_at)
FROM market_asset_deployments mad
ON CONFLICT (market_asset_id, evidence_key) DO NOTHING;

ALTER TABLE market_project_pools
    ADD COLUMN IF NOT EXISTS market_project_deployment_id BIGINT
        REFERENCES market_project_deployments(id) ON DELETE CASCADE;

UPDATE market_project_pools mpp
SET market_project_deployment_id = mpd.id
FROM market_project_deployments mpd
JOIN market_asset_deployments mad
  ON mad.id = mpd.market_asset_deployment_id
JOIN market_pools pool
  ON pool.chain_id = mad.chain_id
WHERE mpp.market_project_deployment_id IS NULL
  AND mpp.market_project_id = mpd.market_project_id
  AND mpp.market_pool_id = pool.id;

CREATE INDEX IF NOT EXISTS idx_market_project_pools_deployment
    ON market_project_pools (
        market_project_deployment_id, selected, is_primary DESC, market_pool_id
    );

DROP INDEX IF EXISTS idx_market_project_pools_one_primary;

CREATE UNIQUE INDEX IF NOT EXISTS idx_market_project_pools_one_primary_per_deployment
    ON market_project_pools (market_project_deployment_id)
    WHERE is_primary = 1 AND market_project_deployment_id IS NOT NULL;

ALTER TABLE market_rules
    ADD COLUMN IF NOT EXISTS deployment_scope TEXT NOT NULL DEFAULT 'all'
        CHECK (deployment_scope IN ('all', 'selected'));

ALTER TABLE market_rules
    ADD COLUMN IF NOT EXISTS pool_scope TEXT NOT NULL DEFAULT 'all'
        CHECK (pool_scope IN ('all', 'selected'));

ALTER TABLE market_rules
    ADD COLUMN IF NOT EXISTS cooldown_scope TEXT NOT NULL DEFAULT 'chain'
        CHECK (cooldown_scope IN ('chain', 'project'));

CREATE TABLE IF NOT EXISTS market_rule_deployments (
    market_rule_id BIGINT NOT NULL REFERENCES market_rules(id) ON DELETE CASCADE,
    market_project_deployment_id BIGINT NOT NULL
        REFERENCES market_project_deployments(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (market_rule_id, market_project_deployment_id)
);

CREATE INDEX IF NOT EXISTS idx_market_rule_deployments_target
    ON market_rule_deployments (market_project_deployment_id, market_rule_id);

CREATE TABLE IF NOT EXISTS market_rule_pools (
    market_rule_id BIGINT NOT NULL REFERENCES market_rules(id) ON DELETE CASCADE,
    market_project_pool_id BIGINT NOT NULL
        REFERENCES market_project_pools(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (market_rule_id, market_project_pool_id)
);

CREATE INDEX IF NOT EXISTS idx_market_rule_pools_target
    ON market_rule_pools (market_project_pool_id, market_rule_id);

-- Existing rules were single-chain. Recording their deployment and pool scope
-- preserves that behavior while future rules can explicitly target many chains.
UPDATE market_rules
SET deployment_scope = 'selected',
    pool_scope = CASE WHEN market_pool_id IS NULL THEN 'all' ELSE 'selected' END
WHERE deployment_scope = 'all'
  AND NOT EXISTS (
      SELECT 1
      FROM market_rule_deployments existing_scope
      WHERE existing_scope.market_rule_id = market_rules.id
  );

INSERT INTO market_rule_deployments (
    market_rule_id,
    market_project_deployment_id
)
SELECT mr.id, mpd.id
FROM market_rules mr
JOIN market_project_deployments mpd
  ON mpd.market_project_id = mr.market_project_id
ON CONFLICT (market_rule_id, market_project_deployment_id) DO NOTHING;

INSERT INTO market_rule_pools (
    market_rule_id,
    market_project_pool_id
)
SELECT mr.id, mpp.id
FROM market_rules mr
JOIN market_project_pools mpp
  ON mpp.market_project_id = mr.market_project_id
 AND mpp.market_pool_id = mr.market_pool_id
WHERE mr.market_pool_id IS NOT NULL
ON CONFLICT (market_rule_id, market_project_pool_id) DO NOTHING;

CREATE TABLE IF NOT EXISTS market_combination_rule_projects (
    market_combination_rule_id BIGINT NOT NULL
        REFERENCES market_combination_rules(id) ON DELETE CASCADE,
    market_project_id BIGINT NOT NULL REFERENCES market_projects(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (market_combination_rule_id, market_project_id)
);

CREATE INDEX IF NOT EXISTS idx_market_combination_rule_projects_project
    ON market_combination_rule_projects (
        market_project_id, market_combination_rule_id
    );

INSERT INTO market_combination_rule_projects (
    market_combination_rule_id,
    market_project_id
)
SELECT DISTINCT
    mcm.market_combination_rule_id,
    mr.market_project_id
FROM market_combination_members mcm
JOIN market_rules mr ON mr.id = mcm.market_rule_id
WHERE mcm.source_type = 'market'
ON CONFLICT (market_combination_rule_id, market_project_id) DO NOTHING;

ALTER TABLE market_snapshots
    ADD COLUMN IF NOT EXISTS market_asset_deployment_id BIGINT
        REFERENCES market_asset_deployments(id) ON DELETE SET NULL;

UPDATE market_snapshots value
SET market_asset_deployment_id = mad.id
FROM market_asset_deployments mad
WHERE value.market_asset_deployment_id IS NULL
  AND value.chain_id = mad.chain_id
  AND value.token_address = mad.token_address;

CREATE INDEX IF NOT EXISTS idx_market_snapshots_deployment_time
    ON market_snapshots (market_asset_deployment_id, captured_at DESC);

ALTER TABLE market_events
    ADD COLUMN IF NOT EXISTS market_asset_deployment_id BIGINT
        REFERENCES market_asset_deployments(id) ON DELETE SET NULL;

UPDATE market_events value
SET market_asset_deployment_id = mad.id
FROM market_asset_deployments mad
WHERE value.market_asset_deployment_id IS NULL
  AND value.chain_id = mad.chain_id
  AND value.token_address = mad.token_address;

CREATE INDEX IF NOT EXISTS idx_market_events_deployment_time
    ON market_events (market_asset_deployment_id, occurred_at DESC);

ALTER TABLE market_holders
    ADD COLUMN IF NOT EXISTS market_asset_deployment_id BIGINT
        REFERENCES market_asset_deployments(id) ON DELETE CASCADE;

UPDATE market_holders value
SET market_asset_deployment_id = mad.id
FROM market_asset_deployments mad
WHERE value.market_asset_deployment_id IS NULL
  AND value.chain_id = mad.chain_id
  AND value.token_address = mad.token_address;

CREATE INDEX IF NOT EXISTS idx_market_holders_deployment_ranking
    ON market_holders (
        market_asset_deployment_id, excluded, rank, balance_raw DESC
    );

ALTER TABLE market_holder_snapshots
    ADD COLUMN IF NOT EXISTS market_asset_deployment_id BIGINT
        REFERENCES market_asset_deployments(id) ON DELETE CASCADE;

UPDATE market_holder_snapshots value
SET market_asset_deployment_id = mad.id
FROM market_asset_deployments mad
WHERE value.market_asset_deployment_id IS NULL
  AND value.chain_id = mad.chain_id
  AND value.token_address = mad.token_address;

CREATE INDEX IF NOT EXISTS idx_market_holder_snapshots_deployment
    ON market_holder_snapshots (
        market_asset_deployment_id, holder_address, captured_at DESC
    );

ALTER TABLE market_address_labels
    ADD COLUMN IF NOT EXISTS market_project_deployment_id BIGINT
        REFERENCES market_project_deployments(id) ON DELETE CASCADE;

UPDATE market_address_labels value
SET market_project_deployment_id = mpd.id
FROM market_project_deployments mpd
JOIN market_asset_deployments mad
  ON mad.id = mpd.market_asset_deployment_id
WHERE value.market_project_deployment_id IS NULL
  AND value.market_project_id = mpd.market_project_id
  AND value.chain_id = mad.chain_id;

CREATE INDEX IF NOT EXISTS idx_market_address_labels_deployment
    ON market_address_labels (
        market_project_deployment_id, label_type, address
    );

ALTER TABLE market_chain_cursors
    ADD COLUMN IF NOT EXISTS market_asset_deployment_id BIGINT
        REFERENCES market_asset_deployments(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_market_chain_cursors_deployment
    ON market_chain_cursors (
        market_asset_deployment_id, status, next_block_number
    );

ALTER TABLE market_scanned_blocks
    ADD COLUMN IF NOT EXISTS market_asset_deployment_id BIGINT
        REFERENCES market_asset_deployments(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_market_scanned_blocks_deployment_recovery
    ON market_scanned_blocks (
        market_asset_deployment_id, canonical, block_number DESC
    );

ALTER TABLE market_provider_health
    ADD COLUMN IF NOT EXISTS market_asset_deployment_id BIGINT
        REFERENCES market_asset_deployments(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_market_provider_health_deployment
    ON market_provider_health (
        market_asset_deployment_id, status, last_checked_at DESC
    );
