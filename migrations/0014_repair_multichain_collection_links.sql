-- Bind discovery rows created after the multi-chain domain migration to the
-- matching project deployment. This is idempotent and preserves selections.
UPDATE market_project_pools mpp
SET market_project_deployment_id = mpd.id,
    updated_at = NOW()
FROM market_project_deployments mpd
JOIN market_asset_deployments mad
  ON mad.id = mpd.market_asset_deployment_id
WHERE mpp.market_project_deployment_id IS NULL
  AND mpd.market_project_id = mpp.market_project_id
  AND mpd.status <> 'removed'
  AND EXISTS (
    SELECT 1
    FROM market_pools pool
    WHERE pool.id = mpp.market_pool_id
      AND pool.chain_id = mad.chain_id
      AND (
        pool.token0_address = mad.token_address
        OR pool.token1_address = mad.token_address
      )
  );

UPDATE market_project_deployments mpd
SET default_market_pool_id = mpp.market_pool_id,
    updated_at = NOW()
FROM market_project_pools mpp
WHERE mpp.market_project_deployment_id = mpd.id
  AND mpp.selected = 1
  AND mpp.is_primary = 1
  AND mpd.default_market_pool_id IS DISTINCT FROM mpp.market_pool_id;
