package subscription

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type marketRepository interface {
	CreateMarketProjectWithinQuota(
		context.Context,
		store.CreateMarketProjectParams,
		store.QuotaPolicy,
	) (store.MarketProject, error)
	RestoreMarketProjectWithinQuota(
		context.Context,
		int64,
		string,
		store.QuotaPolicy,
	) (store.MarketProject, error)
	LinkMarketProjectPoolWithinQuota(
		context.Context,
		store.LinkMarketProjectPoolParams,
		store.QuotaPolicy,
	) (store.MarketProjectPool, error)
	CreateMarketRuleWithinQuota(
		context.Context,
		store.CreateMarketRuleParams,
		store.QuotaPolicy,
	) (store.MarketRule, error)
	RestoreMarketRuleWithinQuota(
		context.Context,
		int64,
		string,
		store.QuotaPolicy,
	) (store.MarketRule, error)
	CreateMarketCombinationWithinQuota(
		context.Context,
		store.CreateMarketCombinationParams,
		store.QuotaPolicy,
	) (store.MarketCombinationRule, error)
	RestoreMarketCombinationWithinQuota(
		context.Context,
		int64,
		string,
		store.QuotaPolicy,
	) (store.MarketCombinationRule, error)
}

func (s *Service) CreateMarketProject(
	ctx context.Context,
	params store.CreateMarketProjectParams,
) (store.MarketProject, error) {
	repository, ok := s.repository.(marketRepository)
	if !ok {
		return store.MarketProject{}, errors.New("market project repository is unavailable")
	}
	for attempt := 0; attempt < 2; attempt++ {
		plan, err := s.ActivePlan(ctx, params.DeBoxUserID)
		if err != nil {
			return store.MarketProject{}, err
		}
		project, err := repository.CreateMarketProjectWithinQuota(
			ctx,
			params,
			quotaPolicy(plan),
		)
		if errors.Is(err, store.ErrSubscriptionChanged) {
			continue
		}
		if err != nil {
			return store.MarketProject{}, quotaError(err, plan, false)
		}
		return project, nil
	}
	return store.MarketProject{}, store.ErrSubscriptionChanged
}

func (s *Service) RestoreMarketProject(
	ctx context.Context,
	deboxUserID string,
	projectID int64,
) (store.MarketProject, error) {
	repository, ok := s.repository.(marketRepository)
	if !ok {
		return store.MarketProject{}, errors.New("market project repository is unavailable")
	}
	for attempt := 0; attempt < 2; attempt++ {
		plan, err := s.ActivePlan(ctx, deboxUserID)
		if err != nil {
			return store.MarketProject{}, err
		}
		project, err := repository.RestoreMarketProjectWithinQuota(
			ctx,
			projectID,
			deboxUserID,
			quotaPolicy(plan),
		)
		if errors.Is(err, store.ErrSubscriptionChanged) {
			continue
		}
		if err != nil {
			return store.MarketProject{}, quotaError(err, plan, true)
		}
		return project, nil
	}
	return store.MarketProject{}, store.ErrSubscriptionChanged
}

func (s *Service) LinkMarketProjectPool(
	ctx context.Context,
	params store.LinkMarketProjectPoolParams,
) (store.MarketProjectPool, error) {
	repository, ok := s.repository.(marketRepository)
	if !ok {
		return store.MarketProjectPool{}, errors.New("market project repository is unavailable")
	}
	for attempt := 0; attempt < 2; attempt++ {
		plan, err := s.ActivePlan(ctx, params.DeBoxUserID)
		if err != nil {
			return store.MarketProjectPool{}, err
		}
		link, err := repository.LinkMarketProjectPoolWithinQuota(
			ctx,
			params,
			quotaPolicy(plan),
		)
		if errors.Is(err, store.ErrSubscriptionChanged) {
			continue
		}
		if err != nil {
			return store.MarketProjectPool{}, quotaError(err, plan, false)
		}
		return link, nil
	}
	return store.MarketProjectPool{}, store.ErrSubscriptionChanged
}

type entitlementReconcileRepository interface {
	ReconcileUserEntitlements(
		context.Context,
		string,
		store.QuotaPolicy,
	) (store.EntitlementReconcileResult, error)
}

func (s *Service) ReconcileActivePlan(
	ctx context.Context,
	deboxUserID string,
	expectedPlanCode string,
) error {
	repository, ok := s.repository.(entitlementReconcileRepository)
	if !ok {
		return nil
	}
	plan, err := s.ActivePlan(ctx, deboxUserID)
	if err != nil {
		return err
	}
	if expectedPlanCode != "" && plan.Code != expectedPlanCode {
		return store.ErrSubscriptionChanged
	}
	_, err = repository.ReconcileUserEntitlements(
		ctx,
		deboxUserID,
		quotaPolicy(plan),
	)
	return err
}

func (s *Service) CreateMarketRule(
	ctx context.Context,
	params store.CreateMarketRuleParams,
) (store.MarketRule, error) {
	repository, ok := s.repository.(marketRepository)
	if !ok {
		return store.MarketRule{}, errors.New("market rule repository is unavailable")
	}
	if err := validateMarketRule(params); err != nil {
		return store.MarketRule{}, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		plan, err := s.ActivePlan(ctx, params.DeBoxUserID)
		if err != nil {
			return store.MarketRule{}, err
		}
		if !plan.AllowsMarketRuleType(params.RuleType) {
			return store.MarketRule{}, errors.New("当前套餐不支持该市场规则类型。")
		}
		rule, err := repository.CreateMarketRuleWithinQuota(
			ctx,
			params,
			quotaPolicy(plan),
		)
		if errors.Is(err, store.ErrSubscriptionChanged) {
			continue
		}
		if err != nil {
			return store.MarketRule{}, quotaError(err, plan, false)
		}
		return rule, nil
	}
	return store.MarketRule{}, store.ErrSubscriptionChanged
}

func (s *Service) RestoreMarketRule(
	ctx context.Context,
	deboxUserID string,
	ruleID int64,
) (store.MarketRule, error) {
	repository, ok := s.repository.(marketRepository)
	if !ok {
		return store.MarketRule{}, errors.New("market rule repository is unavailable")
	}
	for attempt := 0; attempt < 2; attempt++ {
		plan, err := s.ActivePlan(ctx, deboxUserID)
		if err != nil {
			return store.MarketRule{}, err
		}
		rule, err := repository.RestoreMarketRuleWithinQuota(
			ctx,
			ruleID,
			deboxUserID,
			quotaPolicy(plan),
		)
		if errors.Is(err, store.ErrSubscriptionChanged) {
			continue
		}
		if err != nil {
			return store.MarketRule{}, quotaError(err, plan, true)
		}
		return rule, nil
	}
	return store.MarketRule{}, store.ErrSubscriptionChanged
}

func (s *Service) CreateMarketCombination(
	ctx context.Context,
	params store.CreateMarketCombinationParams,
) (store.MarketCombinationRule, error) {
	repository, ok := s.repository.(marketRepository)
	if !ok {
		return store.MarketCombinationRule{}, errors.New(
			"market combination repository is unavailable",
		)
	}
	for attempt := 0; attempt < 2; attempt++ {
		plan, err := s.ActivePlan(ctx, params.DeBoxUserID)
		if err != nil {
			return store.MarketCombinationRule{}, err
		}
		if !plan.MarketCombination {
			return store.MarketCombinationRule{}, errors.New(
				"市场组合规则仅支持专业版。",
			)
		}
		combination, err := repository.CreateMarketCombinationWithinQuota(
			ctx,
			params,
			quotaPolicy(plan),
		)
		if errors.Is(err, store.ErrSubscriptionChanged) {
			continue
		}
		if err != nil {
			return store.MarketCombinationRule{}, quotaError(err, plan, false)
		}
		return combination, nil
	}
	return store.MarketCombinationRule{}, store.ErrSubscriptionChanged
}

func (s *Service) RestoreMarketCombination(
	ctx context.Context,
	deboxUserID string,
	combinationID int64,
) (store.MarketCombinationRule, error) {
	repository, ok := s.repository.(marketRepository)
	if !ok {
		return store.MarketCombinationRule{}, errors.New(
			"market combination repository is unavailable",
		)
	}
	for attempt := 0; attempt < 2; attempt++ {
		plan, err := s.ActivePlan(ctx, deboxUserID)
		if err != nil {
			return store.MarketCombinationRule{}, err
		}
		if !plan.MarketCombination {
			return store.MarketCombinationRule{}, errors.New(
				"市场组合规则仅支持专业版。",
			)
		}
		value, err := repository.RestoreMarketCombinationWithinQuota(
			ctx,
			combinationID,
			deboxUserID,
			quotaPolicy(plan),
		)
		if errors.Is(err, store.ErrSubscriptionChanged) {
			continue
		}
		if err != nil {
			return store.MarketCombinationRule{}, quotaError(err, plan, true)
		}
		return value, nil
	}
	return store.MarketCombinationRule{}, store.ErrSubscriptionChanged
}

func validateMarketRule(params store.CreateMarketRuleParams) error {
	if strings.TrimSpace(params.DeBoxUserID) == "" || params.MarketProjectID <= 0 {
		return errors.New("市场规则缺少用户或项目币。")
	}
	threshold, ok := new(big.Rat).SetString(strings.TrimSpace(params.ThresholdValue))
	if !ok || threshold.Sign() < 0 {
		return errors.New("市场规则阈值必须是大于或等于 0 的数字。")
	}
	unit := strings.ToLower(strings.TrimSpace(params.ThresholdUnit))
	allowedUnits := map[string]bool{
		"usd": true, "token": true, "percent": true,
		"ratio": true, "count": true, "progress": true,
	}
	if !allowedUnits[unit] {
		return errors.New("市场规则阈值单位无效。")
	}
	allowedRuleUnits := map[string]map[string]bool{
		plans.MarketPriceAbove:           {"usd": true},
		plans.MarketPriceBelow:           {"usd": true},
		plans.MarketPriceIncrease:        {"percent": true},
		plans.MarketPriceDecrease:        {"percent": true},
		plans.MarketLiquidityBelow:       {"usd": true},
		plans.MarketLiquidityDecrease:    {"percent": true},
		plans.MarketVolumeAbove:          {"usd": true},
		plans.MarketVolumeSpike:          {"ratio": true},
		plans.MarketTradeImbalance:       {"percent": true},
		plans.MarketLargeBuy:             {"usd": true, "token": true, "percent": true},
		plans.MarketLargeSell:            {"usd": true, "token": true, "percent": true},
		plans.MarketConsecutiveLargeBuy:  {"usd": true, "token": true, "percent": true},
		plans.MarketConsecutiveLargeSell: {"usd": true, "token": true, "percent": true},
		plans.MarketLiquidityAdded:       {"usd": true, "percent": true},
		plans.MarketLiquidityRemoved:     {"usd": true, "percent": true},
		plans.MarketNewPool:              {"count": true},
		plans.MarketHolderIncrease:       {"usd": true, "token": true, "percent": true},
		plans.MarketHolderDecrease:       {"usd": true, "token": true, "percent": true},
		plans.MarketHolderRankEntered:    {"count": true},
		plans.MarketHolderRankExited:     {"count": true},
		plans.MarketFourMemeLargeTrade:   {"usd": true, "token": true, "percent": true},
		plans.MarketFourMemeProgress:     {"progress": true, "percent": true},
		plans.MarketFourMemeMigration:    {"count": true},
	}
	if units, exists := allowedRuleUnits[params.RuleType]; exists && !units[unit] {
		return errors.New("该市场规则不支持所选阈值单位。")
	}
	windowRequired := map[string]bool{
		plans.MarketPriceIncrease:        true,
		plans.MarketPriceDecrease:        true,
		plans.MarketLiquidityDecrease:    true,
		plans.MarketVolumeAbove:          true,
		plans.MarketVolumeSpike:          true,
		plans.MarketTradeImbalance:       true,
		plans.MarketConsecutiveLargeBuy:  true,
		plans.MarketConsecutiveLargeSell: true,
	}
	if windowRequired[params.RuleType] &&
		(params.WindowMinutes == nil || *params.WindowMinutes <= 0) {
		return errors.New("该市场规则必须设置大于 0 的统计窗口。")
	}
	if params.DeliveryMode == "stage" {
		if params.CycleMinutes <= 0 || params.TriggerCountThreshold <= 0 {
			return errors.New("阶段提醒周期和触发次数必须大于 0。")
		}
	}
	if params.RuleScope != "" &&
		params.RuleScope != "standalone" &&
		params.RuleScope != "combination" {
		return fmt.Errorf("无效的市场规则作用域：%s", params.RuleScope)
	}
	return nil
}
