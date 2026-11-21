package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
)

var (
	ErrMarketChainNotAllowed  = errors.New("market monitoring is not allowed on this chain")
	ErrMarketPoolLimitReached = errors.New("selected market pool limit reached")
)

// WithMarketScopePolicies installs only the catalog's market scope. The copy
// prevents later caller mutation, and an absent option keeps legacy adapters.
func WithMarketScopePolicies(policies map[string]QuotaPolicy) OpenOption {
	return func(settings *openSettings) {
		if policies == nil {
			return
		}
		settings.marketScopePolicies = make(map[string]QuotaPolicy, len(policies))
		for code, p := range policies {
			keys := make([]string, len(p.AllowedMarketChainKeys))
			copy(keys, p.AllowedMarketChainKeys)
			settings.marketScopePolicies[code] = QuotaPolicy{PlanCode: code, MarketProjectLimit: p.MarketProjectLimit, MarketPoolLimit: p.MarketPoolLimit, AllowedMarketChainKeys: keys}
		}
	}
}

func validateMarketScopePolicies(policies map[string]QuotaPolicy) error {
	if policies == nil {
		return nil
	}
	for _, code := range []string{"free", "standard", "professional"} {
		p, ok := policies[code]
		if !ok || p.MarketProjectLimit < 0 || p.MarketPoolLimit < 0 {
			return fmt.Errorf("invalid market scope policy for %s", code)
		}
		if p.MarketProjectLimit > 0 && (p.MarketPoolLimit < 1 || len(p.AllowedMarketChainKeys) == 0) {
			return fmt.Errorf("market monitoring scope must be explicit for %s", code)
		}
		for _, key := range p.AllowedMarketChainKeys {
			if strings.TrimSpace(key) == "" {
				return fmt.Errorf("market scope chain must not be empty")
			}
			if _, err := chain.ChainProfile(key, ""); err != nil {
				return err
			}
		}
	}
	return nil
}

// Call after requirePolicyPlan for a supplied policy, or under the user lock
// with an empty policy for a raw Store entry point. Only scope is overridden.
func (s *Store) marketScopePolicy(ctx context.Context, db DBTX, owner string, policy QuotaPolicy) (QuotaPolicy, error) {
	if s.marketScopePolicies == nil {
		return policy, nil
	}
	code := policy.PlanCode
	if code == "" {
		var err error
		code, err = effectivePlanCode(ctx, db, owner)
		if err != nil {
			return policy, err
		}
	}
	p, ok := s.marketScopePolicies[code]
	if !ok {
		return policy, ErrMarketMonitoringDenied
	}
	policy.MarketProjectLimit = p.MarketProjectLimit
	policy.MarketPoolLimit = p.MarketPoolLimit
	policy.AllowedMarketChainKeys = p.AllowedMarketChainKeys
	return policy, nil
}

func marketScopeEnabled(p QuotaPolicy) bool {
	return p.MarketPoolLimit > 0 || p.AllowedMarketChainKeys != nil
}

func marketScopeChains(p QuotaPolicy) ([]string, []int64, error) {
	keys, ids := make([]string, 0, len(p.AllowedMarketChainKeys)), make([]int64, 0, len(p.AllowedMarketChainKeys))
	for _, key := range p.AllowedMarketChainKeys {
		profile, err := chain.ChainProfile(key, "")
		if err != nil {
			return nil, nil, ErrMarketChainNotAllowed
		}
		keys = append(keys, profile.Key)
		ids = append(ids, profile.ChainID)
	}
	return keys, ids, nil
}

func validateMarketScopeChain(p QuotaPolicy, key string, id int64) error {
	if !marketScopeEnabled(p) {
		return nil
	}
	if p.MarketProjectLimit < 1 {
		return ErrMarketMonitoringDenied
	}
	if p.AllowedMarketChainKeys == nil {
		return nil
	}
	profile, err := chain.ChainProfile(key, "")
	if err != nil || profile.ChainID != id {
		return ErrMarketChainNotAllowed
	}
	for _, allowed := range p.AllowedMarketChainKeys {
		candidate, err := chain.ChainProfile(allowed, "")
		if err == nil && candidate.Key == profile.Key {
			return nil
		}
	}
	return ErrMarketChainNotAllowed
}

// Validate the saved configuration as a whole. Removed deployments and
// unselected discovery metadata are history, not continuous monitoring scope.
// No rejected restore silently turns an old multi-chain project into BSC-only.
func validateMarketProjectScope(ctx context.Context, db DBTX, projectID int64, owner string, p QuotaPolicy) error {
	if !marketScopeEnabled(p) {
		return nil
	}
	if p.MarketProjectLimit < 1 {
		return ErrMarketMonitoringDenied
	}
	keys, ids, err := marketScopeChains(p)
	if err != nil {
		return err
	}
	var exists, denied bool
	var pools int64
	err = db.QueryRow(ctx, `WITH project AS (
 SELECT id,chain_key,chain_id FROM market_projects WHERE id=$1 AND debox_user_id=$2
), scope AS (
 SELECT chain_key,chain_id FROM project p WHERE NOT EXISTS(SELECT 1 FROM market_project_deployments mpd WHERE mpd.market_project_id=p.id)
 UNION ALL SELECT mad.chain_key,mad.chain_id FROM market_project_deployments mpd
 JOIN project p ON p.id=mpd.market_project_id JOIN market_asset_deployments mad ON mad.id=mpd.market_asset_deployment_id WHERE mpd.status<>'removed'
 UNION ALL SELECT pool.chain_key,pool.chain_id FROM market_project_pools mpp
 JOIN project p ON p.id=mpp.market_project_id JOIN market_pools pool ON pool.id=mpp.market_pool_id WHERE mpp.selected=1
)
 SELECT EXISTS(SELECT 1 FROM project),
 EXISTS(SELECT 1 FROM scope s WHERE $5::boolean AND NOT EXISTS(
 SELECT 1 FROM UNNEST($3::text[],$4::bigint[]) allowed(key,id) WHERE allowed.key=(CASE LOWER(TRIM(s.chain_key)) WHEN 'bnb' THEN 'bsc' WHEN 'bnbchain' THEN 'bsc' WHEN 'bnb_chain' THEN 'bsc' ELSE LOWER(TRIM(s.chain_key)) END) AND allowed.id=s.chain_id)),
 (SELECT COUNT(DISTINCT mpp.market_pool_id) FROM market_project_pools mpp JOIN project p ON p.id=mpp.market_project_id WHERE mpp.selected=1)`, projectID, owner, keys, ids, p.AllowedMarketChainKeys != nil).Scan(&exists, &denied, &pools)
	if err != nil {
		return fmt.Errorf("validate market project scope: %w", err)
	}
	if !exists {
		return ErrNotFound
	}
	if denied {
		return ErrMarketChainNotAllowed
	}
	if p.MarketPoolLimit > 0 && pools > int64(p.MarketPoolLimit) {
		return ErrMarketPoolLimitReached
	}
	return nil
}

func validateStoredMarketRuleScope(ctx context.Context, db DBTX, rule MarketRule, p QuotaPolicy) error {
	if !marketScopeEnabled(p) {
		return nil
	}
	if err := validateMarketProjectScope(ctx, db, rule.MarketProjectID, rule.DeBoxUserID, p); err != nil {
		return err
	}
	if !p.MultiPoolMonitoring && rule.PoolScope == "all" {
		return ErrMarketPoolMismatch
	}
	var deployments, pools int64
	var invalidDeployment, invalidPool bool
	selectedPools := rule.PoolScope == "selected" && marketRuleNeedsPool(rule.RuleType)
	// Aggregate saved selections in PostgreSQL. Even an oversized legacy
	// configuration returns a fixed-size result rather than an ID slice.
	// Dynamic "all"/"primary" scopes ignore stored explicit selectors. The
	// scalar legacy pool ID is used only by the legacy selected-pool resolver.
	err := db.QueryRow(ctx, `WITH effective_deployments AS (
 SELECT mpd.id FROM market_project_deployments mpd WHERE mpd.market_project_id=$2 AND mpd.status='active'
 AND($5::text='all' OR EXISTS(SELECT 1 FROM market_rule_deployments mrd WHERE mrd.market_rule_id=$1 AND mrd.market_project_deployment_id=mpd.id))
 ), deployments AS (
 SELECT mpd.id,mpd.market_project_id,mpd.status FROM market_rule_deployments mrd
 LEFT JOIN market_project_deployments mpd ON mpd.id=mrd.market_project_deployment_id WHERE mrd.market_rule_id=$1 AND $5::text='selected'
 ), pools AS (
 SELECT mpp.market_pool_id,mpp.market_project_id,mpp.selected,mpp.is_primary FROM market_rule_pools mrp
 LEFT JOIN market_project_pools mpp ON mpp.id=mrp.market_project_pool_id WHERE mrp.market_rule_id=$1 AND $6::boolean
 UNION SELECT mpp.market_pool_id,mpp.market_project_id,mpp.selected,mpp.is_primary FROM (SELECT $3::bigint AS id) requested
 LEFT JOIN market_project_pools mpp ON mpp.market_project_id=$2 AND mpp.market_pool_id=requested.id WHERE requested.id IS NOT NULL AND $6::boolean AND NOT EXISTS(SELECT 1 FROM effective_deployments)
 ) SELECT
 (SELECT COUNT(*) FROM deployments),
 EXISTS(SELECT 1 FROM deployments WHERE id IS NULL OR market_project_id<>$2 OR status<>'active'),
 (SELECT COUNT(DISTINCT market_pool_id) FROM pools),
 EXISTS(SELECT 1 FROM pools WHERE market_pool_id IS NULL OR market_project_id<>$2 OR selected<>1 OR ($4::boolean AND is_primary<>1))`, rule.ID, rule.MarketProjectID, rule.MarketPoolID, !p.MultiPoolMonitoring, rule.DeploymentScope, selectedPools).Scan(&deployments, &invalidDeployment, &pools, &invalidPool)
	if err != nil {
		return fmt.Errorf("validate saved market rule scope: %w", err)
	}
	if invalidDeployment || (rule.DeploymentScope == "selected" && deployments == 0) {
		return ErrInvalidMarketRule
	}
	if p.MarketPoolLimit > 0 && pools > int64(p.MarketPoolLimit) {
		return ErrMarketPoolLimitReached
	}
	if invalidPool || (selectedPools && pools == 0) {
		return ErrMarketPoolMismatch
	}
	return nil
}

func isMarketScopeConstraint(err error) bool {
	return errors.Is(err, ErrMarketChainNotAllowed) || errors.Is(err, ErrMarketPoolLimitReached) || errors.Is(err, ErrMarketPoolMismatch) || errors.Is(err, ErrInvalidMarketRule) || errors.Is(err, ErrMarketMonitoringDenied)
}
