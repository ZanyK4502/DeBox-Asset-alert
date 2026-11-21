package marketview

import (
	"fmt"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

// Reject disallowed creation before any provider fetch, pool metadata write or
// project insert. Store checks the current entitlement again in its transaction.
func validateCreateProjectMonitoringScope(plan plans.Plan, input CreateProjectInput) error {
	if !plan.MarketQuery {
		return ErrPaidMarketAccessRequired
	}
	if plan.MarketProjectLimit < 1 || plan.MarketPoolLimit < 1 {
		return store.ErrMarketMonitoringDenied
	}
	keys, err := plans.NormalizeMarketMonitoringChainKeys(plan.AllowedMarketChainKeys)
	if err != nil {
		return store.ErrMarketChainNotAllowed
	}
	allowed := make(map[string]bool, len(keys))
	for _, key := range keys {
		allowed[key] = true
	}
	deployments := input.Deployments
	if len(deployments) == 0 {
		deployments = []CreateProjectDeploymentInput{{ChainKey: input.ChainKey, PoolAddresses: input.PoolAddresses}}
	}
	if len(deployments) > len(chain.SupportedChains()) {
		return store.ErrMarketChainNotAllowed
	}
	seen := make(map[string]bool, len(deployments))
	total := 0
	for _, deployment := range deployments {
		profile, e := chain.ChainProfile(deployment.ChainKey, defaultChainKey)
		if e != nil || !allowed[profile.Key] {
			return store.ErrMarketChainNotAllowed
		}
		if seen[profile.Key] {
			return fmt.Errorf("同一条链不能重复提交：%s", profile.Name)
		}
		seen[profile.Key] = true
		selected := make(map[string]bool, len(deployment.PoolAddresses))
		for _, raw := range deployment.PoolAddresses {
			address, e := chain.ValidateAddress(raw)
			if e != nil {
				return fmt.Errorf("交易池地址无效：%w", e)
			}
			selected[address] = true
		}
		total += len(selected)
	}
	if total > plan.MarketPoolLimit {
		return store.ErrMarketPoolLimitReached
	}
	return nil
}
