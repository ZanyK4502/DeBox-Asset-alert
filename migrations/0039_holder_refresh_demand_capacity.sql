-- A complete round now admits at most 2,000 source holders / twenty pages.
-- Widen only the prior safety constraints; successful observations, leases,
-- rule parameters and schedules are not rewritten by this migration.
ALTER TABLE market_holder_refresh_states
    DROP CONSTRAINT IF EXISTS market_holder_refresh_states_source_item_count_check;
ALTER TABLE market_holder_refresh_states
    ADD CONSTRAINT market_holder_refresh_states_source_item_count_check
    CHECK (source_item_count BETWEEN 0 AND 2000);
ALTER TABLE market_holder_refresh_states
    DROP CONSTRAINT IF EXISTS market_holder_refresh_states_refresh_pages_reserved_check;
ALTER TABLE market_holder_refresh_states
    ADD CONSTRAINT market_holder_refresh_states_refresh_pages_reserved_check
    CHECK (refresh_pages_reserved BETWEEN 0 AND 20);

-- Demand checks use actual enabled holder rules rather than every project.
CREATE INDEX IF NOT EXISTS idx_market_rules_active_holder_demand
    ON market_rules (market_project_id, debox_user_id, id)
    INCLUDE (deployment_scope, rule_scope)
    WHERE enabled = 1 AND run_status = 'active'
      AND rule_type IN (
          'market_holder_increase', 'market_holder_decrease',
          'market_holder_rank_entered', 'market_holder_rank_exited'
      );
