package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type marketRuleDeploymentTarget struct {
	ID            int64  `db:"id"`
	ChainKey      string `db:"chain_key"`
	ChainID       int64  `db:"chain_id"`
	TokenAddress  string `db:"token_address"`
	TokenName     string `db:"token_name"`
	TokenSymbol   string `db:"token_symbol"`
	TokenDecimals int32  `db:"token_decimals"`
}

type marketRulePoolTarget struct {
	ProjectPoolID int64 `db:"project_pool_id"`
	MarketPoolID  int64 `db:"market_pool_id"`
}

// ListMarketRuleTargets resolves the current deployment and pool scope at
// evaluation time. "all" scopes therefore include a deployment or selected
// pool added after the rule was created, while "selected" scopes remain fixed.
func (s *Store) ListMarketRuleTargets(
	ctx context.Context,
	rule MarketRule,
	project MarketProject,
) ([]MarketRuleTarget, error) {
	deployments, err := collectMany[marketRuleDeploymentTarget](ctx, s.db, `
		SELECT
			mpd.id,
			mad.chain_key,
			mad.chain_id,
			mad.token_address,
			mad.token_name,
			mad.token_symbol,
			mad.token_decimals
		FROM market_project_deployments mpd
		JOIN market_asset_deployments mad
		  ON mad.id = mpd.market_asset_deployment_id
		WHERE mpd.market_project_id = $1
		  AND mpd.status = 'active'
		  AND (
			$2 = 'all'
			OR EXISTS (
				SELECT 1
				FROM market_rule_deployments mrd
				WHERE mrd.market_rule_id = $3
				  AND mrd.market_project_deployment_id = mpd.id
			)
		  )
		ORDER BY mad.chain_id, mpd.id
	`, project.ID, rule.DeploymentScope, rule.ID)
	if err != nil {
		return nil, fmt.Errorf("list market rule deployments: %w", err)
	}
	if len(deployments) == 0 {
		return s.legacyMarketRuleTargets(ctx, rule, project)
	}

	result := make([]MarketRuleTarget, 0, len(deployments))
	for _, deployment := range deployments {
		if !marketRuleNeedsPool(rule.RuleType) {
			target := deploymentTarget(deployment, nil)
			result = append(result, target)
			continue
		}
		pools, err := collectMany[marketRulePoolTarget](ctx, s.db, `
			SELECT
				mpp.id AS project_pool_id,
				mpp.market_pool_id
			FROM market_project_pools mpp
			JOIN market_pools pool ON pool.id = mpp.market_pool_id
			WHERE mpp.market_project_id = $1
			  AND mpp.selected = 1
			  AND pool.chain_id = $2
			  AND (
				mpp.market_project_deployment_id = $3
				OR (
					mpp.market_project_deployment_id IS NULL
					AND (pool.token0_address = $4 OR pool.token1_address = $4)
				)
			  )
			  AND (
				$5 = 'all'
				OR ($5 = 'primary' AND mpp.is_primary = 1)
				OR (
					$5 = 'selected'
					AND EXISTS (
						SELECT 1
						FROM market_rule_pools mrp
						WHERE mrp.market_rule_id = $6
						  AND mrp.market_project_pool_id = mpp.id
					)
				)
			  )
			ORDER BY mpp.is_primary DESC, pool.liquidity_usd DESC, mpp.id
		`,
			project.ID,
			deployment.ChainID,
			deployment.ID,
			deployment.TokenAddress,
			rule.PoolScope,
			rule.ID,
		)
		if err != nil {
			return nil, fmt.Errorf("list market rule pools: %w", err)
		}
		for _, pool := range pools {
			target := deploymentTarget(deployment, &pool)
			result = append(result, target)
		}
	}
	return result, nil
}

func deploymentTarget(
	deployment marketRuleDeploymentTarget,
	pool *marketRulePoolTarget,
) MarketRuleTarget {
	deploymentID := deployment.ID
	target := MarketRuleTarget{
		TargetKey:                 fmt.Sprintf("deployment:%d", deployment.ID),
		MarketProjectDeploymentID: &deploymentID,
		ChainKey:                  deployment.ChainKey,
		ChainID:                   deployment.ChainID,
		TokenAddress:              deployment.TokenAddress,
		TokenName:                 deployment.TokenName,
		TokenSymbol:               deployment.TokenSymbol,
		TokenDecimals:             deployment.TokenDecimals,
		State:                     json.RawMessage(`{}`),
	}
	if pool != nil {
		projectPoolID, marketPoolID := pool.ProjectPoolID, pool.MarketPoolID
		target.TargetKey += fmt.Sprintf("/pool:%d", projectPoolID)
		target.MarketProjectPoolID = &projectPoolID
		target.MarketPoolID = &marketPoolID
	}
	return target
}

func (s *Store) legacyMarketRuleTargets(
	ctx context.Context,
	rule MarketRule,
	project MarketProject,
) ([]MarketRuleTarget, error) {
	var pools []marketRulePoolTarget
	if marketRuleNeedsPool(rule.RuleType) {
		err := error(nil)
		pools, err = collectMany[marketRulePoolTarget](ctx, s.db, `
			SELECT id AS project_pool_id, market_pool_id
			FROM market_project_pools
			WHERE market_project_id = $1 AND selected = 1
			  AND (
				$2 = 'all'
				OR ($2 = 'primary' AND is_primary = 1)
				OR (
					$2 = 'selected'
					AND (
						market_pool_id = $3
						OR EXISTS (
							SELECT 1 FROM market_rule_pools mrp
							WHERE mrp.market_rule_id = $4
							  AND mrp.market_project_pool_id = market_project_pools.id
						)
					)
				)
			  )
			ORDER BY is_primary DESC, id
		`, project.ID, rule.PoolScope, rule.MarketPoolID, rule.ID)
		if err != nil {
			return nil, fmt.Errorf("list legacy market rule pools: %w", err)
		}
	}
	base := MarketRuleTarget{
		TargetKey:       fmt.Sprintf("legacy:%d", project.ID),
		ChainKey:        project.ChainKey,
		ChainID:         project.ChainID,
		TokenAddress:    project.TokenAddress,
		TokenName:       project.TokenName,
		TokenSymbol:     project.TokenSymbol,
		TokenDecimals:   project.TokenDecimals,
		State:           rule.State,
		LastEvaluatedAt: rule.LastEvaluatedAt,
		LastTriggeredAt: rule.LastTriggeredAt,
	}
	if !marketRuleNeedsPool(rule.RuleType) {
		return []MarketRuleTarget{base}, nil
	}
	result := make([]MarketRuleTarget, 0, len(pools))
	for _, pool := range pools {
		target := base
		projectPoolID, marketPoolID := pool.ProjectPoolID, pool.MarketPoolID
		target.TargetKey += fmt.Sprintf("/pool:%d", projectPoolID)
		target.MarketProjectPoolID = &projectPoolID
		target.MarketPoolID = &marketPoolID
		result = append(result, target)
	}
	return result, nil
}

func (s *Store) LoadMarketRuleTargetState(
	ctx context.Context,
	ruleID int64,
	target MarketRuleTarget,
) (MarketRuleTarget, bool, error) {
	value, err := collectOptional[MarketRuleTarget](ctx, s.db, `
		SELECT
			target_key,
			market_project_deployment_id,
			market_project_pool_id,
			state,
			last_evaluated_at,
			last_triggered_at
		FROM market_rule_target_states
		WHERE market_rule_id = $1 AND target_key = $2
	`, ruleID, target.TargetKey)
	if err != nil {
		return target, false, fmt.Errorf("load market rule target state: %w", err)
	}
	if value == nil {
		return target, false, nil
	}
	target.State = value.State
	target.LastEvaluatedAt = value.LastEvaluatedAt
	target.LastTriggeredAt = value.LastTriggeredAt
	return target, true, nil
}

func (s *Store) UpdateMarketRuleTargetState(
	ctx context.Context,
	ruleID int64,
	target MarketRuleTarget,
	state json.RawMessage,
	triggered bool,
) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO market_rule_target_states (
			market_rule_id,
			target_key,
			market_project_deployment_id,
			market_project_pool_id,
			state,
			last_evaluated_at,
			last_triggered_at
		)
		VALUES ($1, $2, $3, $4, $5, NOW(), CASE WHEN $6 THEN NOW() ELSE NULL END)
		ON CONFLICT (market_rule_id, target_key) DO UPDATE
		SET state = EXCLUDED.state,
		    last_evaluated_at = EXCLUDED.last_evaluated_at,
		    last_triggered_at = CASE
		      WHEN $6 THEN EXCLUDED.last_triggered_at
		      ELSE market_rule_target_states.last_triggered_at
		    END,
		    updated_at = NOW()
	`,
		ruleID,
		target.TargetKey,
		target.MarketProjectDeploymentID,
		target.MarketProjectPoolID,
		normalizedJSON(state),
		triggered,
	)
	if err != nil {
		return fmt.Errorf("update market rule target state: %w", err)
	}
	return nil
}

func (s *Store) ListMarketSnapshotsForTarget(
	ctx context.Context,
	target MarketRuleTarget,
	limit int,
) ([]MarketSnapshot, error) {
	limit = clamp(limit, 1, 1000)
	values, err := collectMany[MarketSnapshot](ctx, s.db, `
		SELECT `+marketSnapshotColumns+`
		FROM market_snapshots
		WHERE chain_id = $1 AND token_address = $2
		  AND ($3::bigint IS NULL OR market_pool_id = $3)
		ORDER BY captured_at DESC, id DESC
		LIMIT $4
	`, target.ChainID, target.TokenAddress, target.MarketPoolID, limit)
	if err != nil {
		return nil, fmt.Errorf("list target market snapshots: %w", err)
	}
	return values, nil
}

func (s *Store) ListMarketEventsForTarget(
	ctx context.Context,
	target MarketRuleTarget,
	afterID int64,
	ascending bool,
	limit int,
) ([]MarketEvent, error) {
	limit = clamp(limit, 1, 2000)
	order, comparison := "DESC", "<"
	if ascending {
		order, comparison = "ASC", ">"
	}
	query := `
		SELECT ` + marketEventColumns + `
		FROM market_events
		WHERE chain_id = $1 AND token_address = $2
		  AND ($3::bigint IS NULL OR market_pool_id = $3)
		  AND ($4::bigint = 0 OR id ` + comparison + ` $4)
		ORDER BY id ` + order + `
		LIMIT $5
	`
	values, err := collectMany[MarketEvent](
		ctx, s.db, query,
		target.ChainID, target.TokenAddress, target.MarketPoolID, afterID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list target market events: %w", err)
	}
	return values, nil
}

func (s *Store) ListMarketHoldersForTarget(
	ctx context.Context,
	target MarketRuleTarget,
	limit int,
) ([]MarketHolder, error) {
	limit = clamp(limit, 1, 500)
	values, err := collectMany[MarketHolder](ctx, s.db, `
		SELECT `+marketHolderColumns+`
		FROM market_holders
		WHERE chain_id = $1 AND token_address = $2 AND excluded = 0
		ORDER BY rank NULLS LAST, balance_raw DESC, holder_address
		LIMIT $3
	`, target.ChainID, target.TokenAddress, limit)
	if err != nil {
		return nil, fmt.Errorf("list target market holders: %w", err)
	}
	return values, nil
}

func (s *Store) ListMarketAddressLabelsForTarget(
	ctx context.Context,
	projectID int64,
	deboxUserID string,
	target MarketRuleTarget,
) ([]MarketAddressLabel, error) {
	values, err := collectMany[MarketAddressLabel](ctx, s.db, `
		SELECT `+marketAddressLabelColumns+`
		FROM market_address_labels
		WHERE market_project_id = $1 AND debox_user_id = $2
		  AND (
			(chain_id IS NULL AND market_project_deployment_id IS NULL)
			OR chain_id = $3
			OR market_project_deployment_id = $4
		  )
		ORDER BY created_at, id
	`, projectID, deboxUserID, target.ChainID, target.MarketProjectDeploymentID)
	if err != nil {
		return nil, fmt.Errorf("list target market address labels: %w", err)
	}
	return values, nil
}

func marketRuleNeedsPool(ruleType string) bool {
	switch strings.ToLower(strings.TrimSpace(ruleType)) {
	case "market_price_above", "market_price_below",
		"market_price_increase", "market_price_decrease",
		"market_liquidity_below", "market_liquidity_decrease",
		"market_volume_above", "market_volume_spike",
		"market_trade_imbalance", "market_large_buy", "market_large_sell",
		"market_consecutive_large_buy", "market_consecutive_large_sell",
		"market_liquidity_added", "market_liquidity_removed",
		"market_four_meme_large_trade":
		return true
	default:
		return false
	}
}
