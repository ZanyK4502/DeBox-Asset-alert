package management

import (
	"context"
	"errors"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/subscription"
)

type CombinationMemberInput struct {
	Rule                 WatchRuleInput `json:"rule"`
	RequiredTriggerCount int64          `json:"required_trigger_count"`
}

type CombinationRuleInput struct {
	Note                 string                   `json:"note"`
	CycleType            string                   `json:"cycle_type"`
	CycleMinutes         int32                    `json:"cycle_minutes"`
	NotificationChatID   string                   `json:"notification_chat_id"`
	NotificationChatType string                   `json:"notification_chat_type"`
	NotificationLabel    string                   `json:"notification_label"`
	NotificationLanguage string                   `json:"notification_language"`
	Members              []CombinationMemberInput `json:"members"`
}

func DefaultCombinationRuleInput() CombinationRuleInput {
	return CombinationRuleInput{
		CycleType:            "fixed",
		CycleMinutes:         60,
		NotificationChatType: "private",
		NotificationLanguage: "zh",
		Members:              []CombinationMemberInput{},
	}
}

type CombinationRuleCreation struct {
	Combination store.CombinationRule    `json:"combination"`
	Baselines   []any                    `json:"baselines"`
	Entitlement subscription.Entitlement `json:"entitlement"`
}

type CombinationRuleUpdate struct {
	Combination store.CombinationRule    `json:"combination"`
	Entitlement subscription.Entitlement `json:"entitlement"`
}

func (s *Service) ListCombinationRules(
	ctx context.Context,
	deboxUserID string,
) ([]store.CombinationRule, error) {
	return s.deps.Repository.ListUserCombinationRules(ctx, deboxUserID)
}

func (s *Service) CreateCombinationRule(
	ctx context.Context,
	deboxUserID string,
	input CombinationRuleInput,
) (CombinationRuleCreation, error) {
	input = normalizeCombinationRuleInput(input)
	if err := validateCombinationRuleInput(input); err != nil {
		return CombinationRuleCreation{}, err
	}
	plan, err := s.deps.Entitlements.ActivePlan(ctx, deboxUserID)
	if err != nil {
		return CombinationRuleCreation{}, err
	}
	if !plan.AllowsCombinationRules() {
		return CombinationRuleCreation{}, errors.New("组合规则仅支持专业版。")
	}
	if input.NotificationChatType == "group" && !plan.GroupNotification {
		return CombinationRuleCreation{}, errors.New("当前套餐不支持群通知，请升级专业版。")
	}
	chatID, notificationLabel, err := s.notificationTarget(ctx, deboxUserID, WatchRuleInput{
		NotificationChatID:   input.NotificationChatID,
		NotificationChatType: input.NotificationChatType,
		NotificationLabel:    input.NotificationLabel,
	})
	if err != nil {
		return CombinationRuleCreation{}, err
	}

	members := make([]store.CreateCombinationMemberParams, 0, len(input.Members))
	baselines := make([]any, 0, len(input.Members))
	for _, memberInput := range input.Members {
		memberInput.Rule = normalizeWatchRuleInput(memberInput.Rule, s.deps.DefaultChainKey)
		if memberInput.Rule.DeliveryMode != "realtime" {
			return CombinationRuleCreation{}, errors.New("组合成员只能使用实时检测模式。")
		}
		threshold, err := validateWatchRuleInput(memberInput.Rule)
		if err != nil {
			return CombinationRuleCreation{}, err
		}
		if !plan.AllowsRuleType(memberInput.Rule.RuleType) {
			return CombinationRuleCreation{}, errors.New("专业版不支持其中一条成员规则的类型。")
		}
		prepared, baseline, err := s.prepareCombinationMember(
			ctx,
			deboxUserID,
			memberInput.Rule,
			threshold,
		)
		if err != nil {
			return CombinationRuleCreation{}, err
		}
		members = append(members, store.CreateCombinationMemberParams{
			Rule:                 prepared,
			RequiredTriggerCount: memberInput.RequiredTriggerCount,
		})
		baselines = append(baselines, baseline)
	}

	combination, err := s.deps.Entitlements.CreateCombinationRule(
		ctx,
		store.CreateCombinationRuleParams{
			DeBoxUserID:          deboxUserID,
			Note:                 input.Note,
			CycleType:            input.CycleType,
			CycleMinutes:         input.CycleMinutes,
			NotificationChatID:   chatID,
			NotificationChatType: input.NotificationChatType,
			NotificationLabel:    notificationLabel,
			NotificationLanguage: input.NotificationLanguage,
			Members:              members,
		},
	)
	if err != nil {
		return CombinationRuleCreation{}, err
	}
	entitlement, err := s.deps.Entitlements.Entitlement(ctx, deboxUserID)
	if err != nil {
		return CombinationRuleCreation{}, err
	}
	return CombinationRuleCreation{
		Combination: combination,
		Baselines:   baselines,
		Entitlement: entitlement,
	}, nil
}

func (s *Service) prepareCombinationMember(
	ctx context.Context,
	deboxUserID string,
	input WatchRuleInput,
	threshold string,
) (store.CreateWatchRuleParams, any, error) {
	profile, err := chain.ChainProfile(input.ChainKey, s.deps.DefaultChainKey)
	if err != nil {
		return store.CreateWatchRuleParams{}, nil, err
	}
	walletAddress := input.WalletAddress
	tokenAddress := optionalString(input.TokenAddress)
	targetAddress := optionalString(input.TargetAddress)
	var lastValue *string
	var baseline any

	switch input.RuleType {
	case plans.ApprovalChange:
		value, err := s.deps.Chain.TokenAllowance(
			ctx,
			walletAddress,
			input.TokenAddress,
			input.TargetAddress,
			profile.Key,
			s.deps.DefaultChainKey,
		)
		if err != nil {
			return store.CreateWatchRuleParams{}, nil, err
		}
		walletAddress = value.WalletAddress
		tokenAddress = stringPointer(value.TokenAddress)
		targetAddress = stringPointer(value.SpenderAddress)
		lastValue = stringPointer(value.Value)
		baseline = value
	case plans.AddressInteraction:
		value, err := s.deps.Chain.LatestInteraction(
			ctx,
			walletAddress,
			input.TargetAddress,
			profile.Key,
			s.deps.DefaultChainKey,
		)
		if err != nil {
			return store.CreateWatchRuleParams{}, nil, err
		}
		walletAddress = value.WalletAddress
		targetAddress = stringPointer(value.TargetAddress)
		lastValue = stringPointer(value.Cursor)
		baseline = value
	default:
		value, err := s.deps.Chain.Balance(
			ctx,
			walletAddress,
			input.TokenAddress,
			profile.Key,
			s.deps.DefaultChainKey,
		)
		if err != nil {
			return store.CreateWatchRuleParams{}, nil, err
		}
		walletAddress = value.WalletAddress
		tokenAddress = value.TokenAddress
		if !plans.IsBalanceThreshold(input.RuleType) {
			lastValue = stringPointer(value.Value)
		}
		baseline = value
	}

	return store.CreateWatchRuleParams{
		DeBoxUserID:          deboxUserID,
		ChainKey:             profile.Key,
		ChainID:              int32(profile.ChainID),
		WalletAddress:        walletAddress,
		TokenAddress:         tokenAddress,
		TargetAddress:        targetAddress,
		TargetLabel:          strings.TrimSpace(input.TargetLabel),
		RuleType:             input.RuleType,
		Threshold:            threshold,
		NotificationLanguage: input.NotificationLanguage,
		LastValue:            lastValue,
	}, baseline, nil
}

func (s *Service) DeleteCombinationRule(
	ctx context.Context,
	deboxUserID string,
	combinationRuleID int64,
) (EntitlementResult, error) {
	if _, err := s.deps.Repository.DeleteCombinationRule(
		ctx,
		combinationRuleID,
		deboxUserID,
	); err != nil {
		return EntitlementResult{}, err
	}
	entitlement, err := s.deps.Entitlements.Entitlement(ctx, deboxUserID)
	if err != nil {
		return EntitlementResult{}, err
	}
	return EntitlementResult{OK: true, Entitlement: entitlement}, nil
}

func (s *Service) RestoreCombinationRule(
	ctx context.Context,
	deboxUserID string,
	combinationRuleID int64,
) (CombinationRuleUpdate, error) {
	combination, err := s.deps.Entitlements.RestoreCombinationRule(
		ctx,
		deboxUserID,
		combinationRuleID,
	)
	if err != nil {
		return CombinationRuleUpdate{}, err
	}
	entitlement, err := s.deps.Entitlements.Entitlement(ctx, deboxUserID)
	if err != nil {
		return CombinationRuleUpdate{}, err
	}
	return CombinationRuleUpdate{Combination: combination, Entitlement: entitlement}, nil
}

func (s *Service) UpdateCombinationRuleLanguage(
	ctx context.Context,
	deboxUserID string,
	combinationRuleID int64,
	language string,
) (CombinationRuleUpdate, error) {
	language, err := requireLanguage(language)
	if err != nil {
		return CombinationRuleUpdate{}, err
	}
	combination, err := s.deps.Repository.UpdateCombinationRuleNotificationLanguage(
		ctx,
		combinationRuleID,
		deboxUserID,
		language,
	)
	if err != nil {
		return CombinationRuleUpdate{}, err
	}
	entitlement, err := s.deps.Entitlements.Entitlement(ctx, deboxUserID)
	if err != nil {
		return CombinationRuleUpdate{}, err
	}
	return CombinationRuleUpdate{Combination: combination, Entitlement: entitlement}, nil
}

func normalizeCombinationRuleInput(input CombinationRuleInput) CombinationRuleInput {
	input.Note = strings.TrimSpace(input.Note)
	input.CycleType = strings.ToLower(strings.TrimSpace(input.CycleType))
	if input.CycleType == "" {
		input.CycleType = "fixed"
	}
	input.NotificationChatType = strings.ToLower(strings.TrimSpace(input.NotificationChatType))
	if input.NotificationChatType == "" {
		input.NotificationChatType = "private"
	}
	input.NotificationLanguage = strings.ToLower(strings.TrimSpace(input.NotificationLanguage))
	if input.NotificationLanguage == "" {
		input.NotificationLanguage = "zh"
	}
	input.NotificationChatID = strings.TrimSpace(input.NotificationChatID)
	input.NotificationLabel = strings.TrimSpace(input.NotificationLabel)
	return input
}

func validateCombinationRuleInput(input CombinationRuleInput) error {
	if _, err := requireLanguage(input.NotificationLanguage); err != nil {
		return err
	}
	if input.CycleType != "fixed" && input.CycleType != "follow" {
		return errors.New("组合规则周期只能是 fixed 或 follow。")
	}
	if input.CycleMinutes <= 0 {
		return errors.New("组合规则周期分钟数必须大于 0。")
	}
	if len(input.Members) < 2 {
		return errors.New("组合规则至少需要两条成员规则。")
	}
	for _, member := range input.Members {
		if member.RequiredTriggerCount <= 0 {
			return errors.New("每条组合成员的触发次数必须大于 0。")
		}
	}
	return nil
}
