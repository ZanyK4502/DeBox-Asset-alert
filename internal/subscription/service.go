package subscription

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type Repository interface {
	GetActiveSubscription(context.Context, string) (*store.Subscription, error)
	BindPermanentPlan(context.Context, string, string) (store.PermanentPlanBinding, error)
	HasPaidSubscriptionHistory(context.Context, string) (bool, error)
	GetUserPreferences(context.Context, string) (store.UserPreference, error)
	ApplyPaidExpiryFallback(context.Context, string, *int64) (bool, error)
	ListUserWatchRules(context.Context, string) ([]store.WatchRule, error)
	ListNotificationGroups(context.Context, string) ([]store.NotificationGroup, error)
	CountUserWatchRules(context.Context, string) (int64, error)
	CountUserWallets(context.Context, string) (int64, error)
	CountNotificationGroups(context.Context, string) (int64, error)
	ListDailySummaryTargets(context.Context, int64) ([]store.DailySummaryTarget, error)
	SetFreeWatchRule(context.Context, string, int64) (store.UserPreference, error)
	CreateWatchRuleWithinQuota(context.Context, store.CreateWatchRuleParams, store.QuotaPolicy) (store.WatchRule, error)
	RestoreWatchRuleWithinQuota(context.Context, int64, string, store.QuotaPolicy) (store.WatchRule, error)
	CreateCombinationRuleWithinQuota(context.Context, store.CreateCombinationRuleParams, store.QuotaPolicy) (store.CombinationRule, error)
	RestoreCombinationRuleWithinQuota(context.Context, int64, string, store.QuotaPolicy) (store.CombinationRule, error)
	CreateNotificationGroupWithinQuota(context.Context, string, string, string, store.QuotaPolicy) (store.NotificationGroup, error)
	ActivateSubscription(context.Context, string, string, int) (store.Subscription, error)
}

type Service struct {
	repository Repository
	catalog    *plans.Catalog
	now        func() time.Time
}

func New(repository Repository, catalog *plans.Catalog) *Service {
	return &Service{
		repository: repository,
		catalog:    catalog,
		now:        func() time.Time { return time.Now().UTC() },
	}
}

type RuleView struct {
	store.WatchRule
	Status        string `json:"status"`
	PauseReason   string `json:"pause_reason"`
	CanSelectFree bool   `json:"can_select_free"`
}

type SummarySettings struct {
	Enabled  bool                       `json:"enabled"`
	Time     string                     `json:"time"`
	Timezone string                     `json:"timezone"`
	ChatType string                     `json:"chat_type"`
	ChatID   string                     `json:"chat_id"`
	Label    string                     `json:"label"`
	Language string                     `json:"language"`
	Targets  []store.DailySummaryTarget `json:"targets"`
}

type Entitlement struct {
	DeBoxUserID     string                    `json:"debox_user_id"`
	Subscription    *store.Subscription       `json:"subscription"`
	Plan            plans.Plan                `json:"plan"`
	Permanent       bool                      `json:"permanent"`
	PaidHistory     bool                      `json:"paid_history"`
	FallbackFree    bool                      `json:"fallback_free"`
	Preferences     store.UserPreference      `json:"preferences"`
	DaysRemaining   int                       `json:"days_remaining"`
	RuleCount       int64                     `json:"rule_count"`
	WalletCount     int64                     `json:"wallet_count"`
	GroupCount      int64                     `json:"group_count"`
	Rules           []store.WatchRule         `json:"rules"`
	ActiveRules     []RuleView                `json:"active_rules"`
	PausedRules     []RuleView                `json:"paused_rules"`
	Groups          []store.NotificationGroup `json:"groups"`
	SummarySettings SummarySettings           `json:"summary_settings"`
}

func (s *Service) Entitlement(ctx context.Context, deboxUserID string) (Entitlement, error) {
	preferences, err := s.repository.GetUserPreferences(ctx, deboxUserID)
	if err != nil {
		return Entitlement{}, err
	}
	if _, err := s.repository.ApplyPaidExpiryFallback(
		ctx,
		deboxUserID,
		preferences.FreeWatchRuleID,
	); err != nil {
		return Entitlement{}, err
	}

	activeSubscription, err := s.repository.GetActiveSubscription(ctx, deboxUserID)
	if err != nil {
		return Entitlement{}, err
	}
	paidHistory, err := s.repository.HasPaidSubscriptionHistory(ctx, deboxUserID)
	if err != nil {
		return Entitlement{}, err
	}
	fallbackFree := activeSubscription == nil
	planCode := plans.Free
	if activeSubscription != nil {
		planCode = activeSubscription.PlanCode
	}
	plan, err := s.catalog.Get(planCode)
	if err != nil {
		return Entitlement{}, err
	}

	rules, err := s.repository.ListUserWatchRules(ctx, deboxUserID)
	if err != nil {
		return Entitlement{}, err
	}
	activeRules, pausedRules := classifyRules(
		rules,
		plan,
		fallbackFree,
		paidHistory,
		preferences.FreeWatchRuleID,
	)
	groups, err := s.repository.ListNotificationGroups(ctx, deboxUserID)
	if err != nil {
		return Entitlement{}, err
	}
	ruleCount, err := s.repository.CountUserWatchRules(ctx, deboxUserID)
	if err != nil {
		return Entitlement{}, err
	}
	walletCount, err := s.repository.CountUserWallets(ctx, deboxUserID)
	if err != nil {
		return Entitlement{}, err
	}
	groupCount, err := s.repository.CountNotificationGroups(ctx, deboxUserID)
	if err != nil {
		return Entitlement{}, err
	}
	summaryTargets := []store.DailySummaryTarget{}
	if activeSubscription != nil {
		summaryTargets, err = s.repository.ListDailySummaryTargets(ctx, activeSubscription.ID)
		if err != nil {
			return Entitlement{}, err
		}
	}

	return Entitlement{
		DeBoxUserID:     deboxUserID,
		Subscription:    activeSubscription,
		Plan:            plan,
		Permanent:       activeSubscription != nil && activeSubscription.IsPermanent == 1,
		PaidHistory:     paidHistory,
		FallbackFree:    fallbackFree,
		Preferences:     preferences,
		DaysRemaining:   daysRemaining(activeSubscription, s.now()),
		RuleCount:       ruleCount,
		WalletCount:     walletCount,
		GroupCount:      groupCount,
		Rules:           rules,
		ActiveRules:     activeRules,
		PausedRules:     pausedRules,
		Groups:          groups,
		SummarySettings: summarySettings(activeSubscription, summaryTargets),
	}, nil
}

func (s *Service) BindPermanentWallet(
	ctx context.Context,
	deboxUserID string,
	walletAddress string,
) (*store.Subscription, error) {
	wallet, err := normalizeAddress(walletAddress)
	if err != nil {
		return nil, err
	}
	binding, err := s.repository.BindPermanentPlan(ctx, deboxUserID, wallet)
	if err != nil {
		return nil, err
	}
	if binding.Subscription != nil && binding.Changed {
		if binding.PreviousDeBoxUserID != "" {
			if err := s.ReconcileActivePlan(
				ctx,
				binding.PreviousDeBoxUserID,
				"",
			); err != nil {
				return nil, err
			}
		}
		if err := s.ReconcileActivePlan(
			ctx,
			deboxUserID,
			"",
		); err != nil {
			return nil, err
		}
	}
	return binding.Subscription, nil
}

func (s *Service) ActivePlan(ctx context.Context, deboxUserID string) (plans.Plan, error) {
	activeSubscription, err := s.repository.GetActiveSubscription(ctx, deboxUserID)
	if err != nil {
		return plans.Plan{}, err
	}
	if activeSubscription == nil {
		return s.catalog.Get(plans.Free)
	}
	return s.catalog.Get(activeSubscription.PlanCode)
}

func (s *Service) EnableFreePlan(
	ctx context.Context,
	deboxUserID string,
) (*store.Subscription, error) {
	return s.repository.GetActiveSubscription(ctx, deboxUserID)
}

func (s *Service) ChooseFreeWatchRule(
	ctx context.Context,
	deboxUserID string,
	ruleID int64,
) (Entitlement, error) {
	for attempt := 0; attempt < 2; attempt++ {
		plan, err := s.ActivePlan(ctx, deboxUserID)
		if err != nil {
			return Entitlement{}, err
		}
		if plan.Code != plans.Free {
			return Entitlement{}, errors.New("当前不是免费版，无需设置免费版监控规则。")
		}
		if _, err := s.repository.SetFreeWatchRule(ctx, deboxUserID, ruleID); err != nil {
			if errors.Is(err, store.ErrSubscriptionChanged) {
				continue
			}
			if errors.Is(err, store.ErrInvalidFreeWatchRule) {
				return Entitlement{}, errors.New("这条规则不符合免费版监控条件。")
			}
			return Entitlement{}, err
		}
		return s.Entitlement(ctx, deboxUserID)
	}
	return Entitlement{}, store.ErrSubscriptionChanged
}

func (s *Service) CreateWatchRule(
	ctx context.Context,
	params store.CreateWatchRuleParams,
) (store.WatchRule, error) {
	for attempt := 0; attempt < 2; attempt++ {
		plan, err := s.ActivePlan(ctx, params.DeBoxUserID)
		if err != nil {
			return store.WatchRule{}, err
		}
		rule, err := s.repository.CreateWatchRuleWithinQuota(ctx, params, quotaPolicy(plan))
		if errors.Is(err, store.ErrSubscriptionChanged) {
			continue
		}
		if err != nil {
			return store.WatchRule{}, quotaError(err, plan, false)
		}
		return rule, nil
	}
	return store.WatchRule{}, store.ErrSubscriptionChanged
}

func (s *Service) RestorePausedWatchRule(
	ctx context.Context,
	deboxUserID string,
	ruleID int64,
) (Entitlement, error) {
	for attempt := 0; attempt < 2; attempt++ {
		plan, err := s.ActivePlan(ctx, deboxUserID)
		if err != nil {
			return Entitlement{}, err
		}
		if plan.Code == plans.Free {
			return s.ChooseFreeWatchRule(ctx, deboxUserID, ruleID)
		}
		_, err = s.repository.RestoreWatchRuleWithinQuota(
			ctx,
			ruleID,
			deboxUserID,
			quotaPolicy(plan),
		)
		if errors.Is(err, store.ErrSubscriptionChanged) {
			continue
		}
		if err != nil {
			return Entitlement{}, quotaError(err, plan, true)
		}
		return s.Entitlement(ctx, deboxUserID)
	}
	return Entitlement{}, store.ErrSubscriptionChanged
}

func (s *Service) CreateCombinationRule(
	ctx context.Context,
	params store.CreateCombinationRuleParams,
) (store.CombinationRule, error) {
	for attempt := 0; attempt < 2; attempt++ {
		plan, err := s.ActivePlan(ctx, params.DeBoxUserID)
		if err != nil {
			return store.CombinationRule{}, err
		}
		combination, err := s.repository.CreateCombinationRuleWithinQuota(
			ctx,
			params,
			quotaPolicy(plan),
		)
		if errors.Is(err, store.ErrSubscriptionChanged) {
			continue
		}
		if err != nil {
			return store.CombinationRule{}, quotaError(err, plan, false)
		}
		return combination, nil
	}
	return store.CombinationRule{}, store.ErrSubscriptionChanged
}

func (s *Service) RestoreCombinationRule(
	ctx context.Context,
	deboxUserID string,
	combinationRuleID int64,
) (store.CombinationRule, error) {
	for attempt := 0; attempt < 2; attempt++ {
		plan, err := s.ActivePlan(ctx, deboxUserID)
		if err != nil {
			return store.CombinationRule{}, err
		}
		combination, err := s.repository.RestoreCombinationRuleWithinQuota(
			ctx,
			combinationRuleID,
			deboxUserID,
			quotaPolicy(plan),
		)
		if errors.Is(err, store.ErrSubscriptionChanged) {
			continue
		}
		if err != nil {
			return store.CombinationRule{}, quotaError(err, plan, true)
		}
		return combination, nil
	}
	return store.CombinationRule{}, store.ErrSubscriptionChanged
}

func (s *Service) CreateNotificationGroup(
	ctx context.Context,
	deboxUserID string,
	gid string,
	name string,
) (store.NotificationGroup, error) {
	for attempt := 0; attempt < 2; attempt++ {
		plan, err := s.ActivePlan(ctx, deboxUserID)
		if err != nil {
			return store.NotificationGroup{}, err
		}
		group, err := s.repository.CreateNotificationGroupWithinQuota(
			ctx,
			deboxUserID,
			gid,
			name,
			quotaPolicy(plan),
		)
		if errors.Is(err, store.ErrSubscriptionChanged) {
			continue
		}
		if err != nil {
			return store.NotificationGroup{}, quotaError(err, plan, false)
		}
		return group, nil
	}
	return store.NotificationGroup{}, store.ErrSubscriptionChanged
}

func (s *Service) RequireSummaryTarget(
	ctx context.Context,
	deboxUserID string,
	chatType string,
) (plans.Plan, error) {
	plan, err := s.ActivePlan(ctx, deboxUserID)
	if err != nil {
		return plans.Plan{}, err
	}
	if !plan.DailySummary {
		return plans.Plan{}, errors.New("当前套餐不支持每日摘要。")
	}
	if !plan.AllowsSummaryTarget(chatType) {
		return plans.Plan{}, errors.New("当前套餐不支持把每日摘要发送到这个目标。")
	}
	return plan, nil
}

func (s *Service) ActivatePaidSubscription(
	ctx context.Context,
	deboxUserID string,
	planCode string,
) (store.Subscription, error) {
	plan, err := s.catalog.Get(planCode)
	if err != nil {
		return store.Subscription{}, err
	}
	if plan.Code == plans.Free {
		return store.Subscription{}, errors.New("免费版无需支付。")
	}
	activated, err := s.repository.ActivateSubscription(
		ctx,
		deboxUserID,
		plan.Code,
		plan.Days,
	)
	if err != nil {
		return store.Subscription{}, err
	}
	if err := s.ReconcileActivePlan(ctx, deboxUserID, plan.Code); err != nil {
		return store.Subscription{}, err
	}
	return activated, nil
}

func classifyRules(
	rules []store.WatchRule,
	plan plans.Plan,
	fallbackFree bool,
	paidHistory bool,
	freeWatchRuleID *int64,
) ([]RuleView, []RuleView) {
	activeRules := make([]RuleView, 0)
	pausedRules := make([]RuleView, 0)
	activeWallets := make(map[string]struct{})

	for _, rule := range rules {
		reason := pauseReason(rule, plan, fallbackFree, paidHistory)
		walletKey := strings.ToLower(rule.WalletAddress)
		_, walletActive := activeWallets[walletKey]
		isNewWallet := walletKey != "" && !walletActive
		canSelectFree := plan.Code == plans.Free &&
			rule.Enabled == 1 &&
			plan.AllowsRuleType(rule.RuleType) &&
			rule.NotificationChatType == "private"

		selectedFreeRule := freeWatchRuleID != nil && rule.ID == *freeWatchRuleID
		if plan.Code == plans.Free && selectedFreeRule && canSelectFree {
			reason = ""
		} else if canSelectFree && !selectedFreeRule && reason == "" {
			reason = "请选择这条规则作为免费版监控后继续执行。"
		}
		if reason == "" && len(activeRules) >= plan.RuleLimit {
			reason = fmt.Sprintf("超出%s规则额度。", plan.Name)
		}
		if reason == "" && isNewWallet && len(activeWallets) >= plan.WalletLimit {
			reason = fmt.Sprintf("超出%s钱包额度。", plan.Name)
		}

		view := RuleView{
			WatchRule:     rule,
			Status:        "active",
			CanSelectFree: canSelectFree,
		}
		if reason != "" {
			view.Status = "paused"
			view.PauseReason = reason
			pausedRules = append(pausedRules, view)
			continue
		}
		if walletKey != "" {
			activeWallets[walletKey] = struct{}{}
		}
		activeRules = append(activeRules, view)
	}
	return activeRules, pausedRules
}

func pauseReason(
	rule store.WatchRule,
	plan plans.Plan,
	fallbackFree bool,
	paidHistory bool,
) string {
	switch {
	case rule.Enabled != 1:
		return "规则已关闭。"
	case rule.RunStatus == "paused":
		return "规则已暂停。"
	case fallbackFree && paidHistory:
		return "付费套餐已到期，规则已暂停。"
	case !plan.AllowsRuleType(rule.RuleType):
		return fmt.Sprintf("%s不支持该规则类型。", plan.Name)
	case rule.NotificationChatType == "group" && !plan.GroupNotification:
		return fmt.Sprintf("%s不支持群通知。", plan.Name)
	default:
		return ""
	}
}

func summarySettings(
	subscription *store.Subscription,
	targets []store.DailySummaryTarget,
) SummarySettings {
	if subscription == nil {
		return SummarySettings{
			Time:     "20:00",
			Timezone: "Asia/Shanghai",
			ChatType: "private",
			Language: "zh",
			Targets:  []store.DailySummaryTarget{},
		}
	}
	chatType := defaultString(subscription.DailySummaryChatType, "private")
	chatID := subscription.DailySummaryChatID
	if len(targets) > 0 {
		chatType = targets[0].ChatType
		chatID = targets[0].ChatID
	}
	return SummarySettings{
		Enabled:  subscription.DailySummaryEnabled == 1,
		Time:     defaultString(subscription.DailySummaryTime, "20:00"),
		Timezone: defaultString(subscription.DailySummaryTimezone, "Asia/Shanghai"),
		ChatType: chatType,
		ChatID:   chatID,
		Label:    subscription.DailySummaryLabel,
		Language: normalizeLanguage(subscription.DailySummaryLanguage),
		Targets:  targets,
	}
}

func daysRemaining(subscription *store.Subscription, now time.Time) int {
	if subscription == nil || subscription.IsPermanent == 1 || subscription.ExpiresAt.IsZero() {
		return 0
	}
	days := math.Ceil(subscription.ExpiresAt.UTC().Sub(now.UTC()).Hours() / 24)
	if days < 0 {
		return 0
	}
	return int(days)
}

func quotaPolicy(plan plans.Plan) store.QuotaPolicy {
	return store.QuotaPolicy{
		PlanCode:               plan.Code,
		WalletLimit:            plan.WalletLimit,
		RuleLimit:              plan.RuleLimit,
		GroupLimit:             plan.GroupLimit,
		MarketProjectLimit:     plan.MarketProjectLimit,
		AllowedRuleTypes:       append([]string(nil), plan.AllowedRuleTypes...),
		AllowedMarketRuleTypes: append([]string(nil), plan.AllowedMarketRules...),
		GroupNotification:      plan.GroupNotification,
		StageNotifications:     plan.AllowsStageNotifications(),
		CombinationRules:       plan.AllowsCombinationRules(),
		MultiPoolMonitoring:    plan.MarketPoolMode == "multiple",
	}
}

func quotaError(err error, plan plans.Plan, restoring bool) error {
	switch {
	case errors.Is(err, store.ErrRuleTypeDenied):
		return errors.New("当前套餐不支持该规则类型。")
	case errors.Is(err, store.ErrGroupNotificationDenied):
		return errors.New("当前套餐不支持群通知，请升级专业版。")
	case errors.Is(err, store.ErrStageNotificationsDenied):
		return errors.New("当前套餐不支持阶段提醒。")
	case errors.Is(err, store.ErrMarketMonitoringDenied):
		return errors.New("当前套餐不支持持续项目币监控。")
	case errors.Is(err, store.ErrMarketRuleTypeDenied):
		return errors.New("当前套餐不支持该市场规则类型。")
	case errors.Is(err, store.ErrMarketProjectLimitReached):
		return fmt.Errorf("当前套餐最多支持 %d 个持续监控项目币。", plan.MarketProjectLimit)
	case errors.Is(err, store.ErrMarketProjectExists):
		return errors.New("已创建，请先删除此代币相关监控项目。")
	case errors.Is(err, store.ErrMarketPoolMismatch):
		return errors.New("当前套餐只能监控项目主池；多池监控需要专业版。")
	case errors.Is(err, store.ErrCombinationRulesDenied):
		return errors.New("组合规则仅支持专业版。")
	case errors.Is(err, store.ErrInvalidCombinationRule):
		return errors.New("组合规则至少需要两条成员规则，且周期和触发次数必须大于 0。")
	case errors.Is(err, store.ErrCombinationMemberManaged):
		return errors.New("组合成员只能通过所属组合规则进行管理。")
	case errors.Is(err, store.ErrRuleLimitReached):
		noun := "规则"
		if restoring {
			noun = "运行规则"
		}
		return fmt.Errorf("当前套餐最多支持 %d 条%s。", plan.RuleLimit, noun)
	case errors.Is(err, store.ErrWalletLimitReached):
		noun := "钱包"
		if restoring {
			noun = "运行钱包"
		}
		return fmt.Errorf("当前套餐最多支持 %d 个%s。", plan.WalletLimit, noun)
	case errors.Is(err, store.ErrGroupLimitReached):
		return fmt.Errorf("当前套餐最多绑定 %d 个群。", plan.GroupLimit)
	default:
		return err
	}
}

func normalizeAddress(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) != 42 || !strings.HasPrefix(value, "0x") {
		return "", errors.New("Invalid EVM address")
	}
	if _, err := hex.DecodeString(value[2:]); err != nil {
		return "", errors.New("Invalid EVM address")
	}
	return "0x" + strings.ToLower(value[2:]), nil
}

func normalizeLanguage(language string) string {
	if strings.EqualFold(strings.TrimSpace(language), "en") {
		return "en"
	}
	return "zh"
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
