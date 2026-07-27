-- Multi-chain market rule execution state and stricter scope semantics.
--
-- Rules can span every verified deployment of one logical project while each
-- chain/pool keeps an independent evaluation cursor.  This prevents events on
-- a busy chain from advancing the cursor or cooldown of another chain.

ALTER TABLE market_rules
    DROP CONSTRAINT IF EXISTS market_rules_pool_scope_check;

ALTER TABLE market_rules
    ADD CONSTRAINT market_rules_pool_scope_check
        CHECK (pool_scope IN ('primary', 'all', 'selected'));

CREATE TABLE IF NOT EXISTS market_rule_target_states (
    id BIGSERIAL PRIMARY KEY,
    market_rule_id BIGINT NOT NULL
        REFERENCES market_rules(id) ON DELETE CASCADE,
    target_key TEXT NOT NULL,
    market_project_deployment_id BIGINT
        REFERENCES market_project_deployments(id) ON DELETE CASCADE,
    market_project_pool_id BIGINT
        REFERENCES market_project_pools(id) ON DELETE CASCADE,
    state JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_evaluated_at TIMESTAMPTZ,
    last_triggered_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (market_rule_id, target_key),
    CHECK (target_key <> '')
);

CREATE INDEX IF NOT EXISTS idx_market_rule_target_states_due
    ON market_rule_target_states (
        market_rule_id, last_evaluated_at, target_key
    );

-- Existing rules remain single-chain and keep their previous state/cooldown.
-- The target row is pre-seeded so the first deployment-aware cycle cannot
-- replay historical market events.
INSERT INTO market_rule_target_states (
    market_rule_id,
    target_key,
    market_project_deployment_id,
    market_project_pool_id,
    state,
    last_evaluated_at,
    last_triggered_at
)
SELECT
    mr.id,
    'deployment:' || mpd.id::text ||
        CASE
            WHEN mr.rule_type NOT IN (
                'market_price_above',
                'market_price_below',
                'market_price_increase',
                'market_price_decrease',
                'market_liquidity_below',
                'market_liquidity_decrease',
                'market_volume_above',
                'market_volume_spike',
                'market_trade_imbalance',
                'market_large_buy',
                'market_large_sell',
                'market_consecutive_large_buy',
                'market_consecutive_large_sell',
                'market_liquidity_added',
                'market_liquidity_removed',
                'market_four_meme_large_trade'
            ) OR mpp.id IS NULL THEN ''
            ELSE '/pool:' || mpp.id::text
        END,
    mpd.id,
    CASE
        WHEN mr.rule_type IN (
            'market_price_above',
            'market_price_below',
            'market_price_increase',
            'market_price_decrease',
            'market_liquidity_below',
            'market_liquidity_decrease',
            'market_volume_above',
            'market_volume_spike',
            'market_trade_imbalance',
            'market_large_buy',
            'market_large_sell',
            'market_consecutive_large_buy',
            'market_consecutive_large_sell',
            'market_liquidity_added',
            'market_liquidity_removed',
            'market_four_meme_large_trade'
        ) THEN mpp.id
        ELSE NULL
    END,
    mr.state,
    mr.last_evaluated_at,
    mr.last_triggered_at
FROM market_rules mr
JOIN market_project_deployments mpd
  ON mpd.market_project_id = mr.market_project_id
LEFT JOIN market_project_pools mpp
  ON mpp.market_project_id = mr.market_project_id
 AND mpp.market_pool_id = COALESCE(
        mr.market_pool_id,
        CASE
            WHEN mr.pool_scope = 'selected' THEN NULL
            ELSE (
                SELECT selected_pool.market_pool_id
                FROM market_project_pools selected_pool
                WHERE selected_pool.market_project_id = mr.market_project_id
                  AND selected_pool.market_project_deployment_id = mpd.id
                  AND selected_pool.selected = 1
                ORDER BY selected_pool.is_primary DESC, selected_pool.id
                LIMIT 1
            )
        END
    )
ON CONFLICT (market_rule_id, target_key) DO NOTHING;
