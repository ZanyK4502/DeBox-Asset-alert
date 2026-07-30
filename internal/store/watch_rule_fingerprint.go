package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

type watchRuleFingerprintValue struct {
	ChainKey              string `json:"chain_key"`
	WalletAddress         string `json:"wallet_address"`
	TokenAddress          string `json:"token_address,omitempty"`
	TargetAddress         string `json:"target_address,omitempty"`
	RuleType              string `json:"rule_type"`
	Threshold             string `json:"threshold"`
	DeliveryMode          string `json:"delivery_mode"`
	CycleType             string `json:"cycle_type,omitempty"`
	CycleMinutes          int32  `json:"cycle_minutes,omitempty"`
	TriggerCountThreshold int64  `json:"trigger_count_threshold,omitempty"`
	NotificationChatID    string `json:"notification_chat_id,omitempty"`
	NotificationChatType  string `json:"notification_chat_type,omitempty"`
}

type combinationMemberFingerprintValue struct {
	RuleFingerprint      string `json:"rule_fingerprint"`
	RequiredTriggerCount int64  `json:"required_trigger_count"`
}

type combinationRuleFingerprintValue struct {
	CycleType            string                              `json:"cycle_type"`
	CycleMinutes         int32                               `json:"cycle_minutes"`
	NotificationChatID   string                              `json:"notification_chat_id"`
	NotificationChatType string                              `json:"notification_chat_type"`
	Members              []combinationMemberFingerprintValue `json:"members"`
}

func watchRuleConfigurationFingerprint(
	params CreateWatchRuleParams,
	includeNotification bool,
) string {
	deliveryMode := normalizeDeliveryMode(params.DeliveryMode)
	value := watchRuleFingerprintValue{
		ChainKey:      strings.ToLower(strings.TrimSpace(params.ChainKey)),
		WalletAddress: normalizeFingerprintAddress(params.WalletAddress),
		TokenAddress:  normalizeFingerprintOptionalAddress(params.TokenAddress),
		TargetAddress: normalizeFingerprintOptionalAddress(params.TargetAddress),
		RuleType:      strings.ToLower(strings.TrimSpace(params.RuleType)),
		Threshold:     canonicalMarketDecimal(params.Threshold),
		DeliveryMode:  deliveryMode,
	}
	if deliveryMode == "stage" {
		value.CycleType = normalizeCycleType(params.CycleType)
		value.CycleMinutes = params.CycleMinutes
		value.TriggerCountThreshold = params.TriggerCountThreshold
	}
	if includeNotification {
		value.NotificationChatID = strings.TrimSpace(params.NotificationChatID)
		value.NotificationChatType = strings.ToLower(
			strings.TrimSpace(params.NotificationChatType),
		)
	}
	return fingerprintJSON(value)
}

func combinationRuleConfigurationFingerprint(
	params CreateCombinationRuleParams,
) string {
	members := make(
		[]combinationMemberFingerprintValue,
		0,
		len(params.Members),
	)
	for _, member := range params.Members {
		members = append(members, combinationMemberFingerprintValue{
			RuleFingerprint:      watchRuleConfigurationFingerprint(member.Rule, false),
			RequiredTriggerCount: member.RequiredTriggerCount,
		})
	}
	sort.Slice(members, func(i, j int) bool {
		if members[i].RuleFingerprint != members[j].RuleFingerprint {
			return members[i].RuleFingerprint < members[j].RuleFingerprint
		}
		return members[i].RequiredTriggerCount < members[j].RequiredTriggerCount
	})
	return fingerprintJSON(combinationRuleFingerprintValue{
		CycleType:          normalizeCycleType(params.CycleType),
		CycleMinutes:       params.CycleMinutes,
		NotificationChatID: strings.TrimSpace(params.NotificationChatID),
		NotificationChatType: strings.ToLower(
			strings.TrimSpace(params.NotificationChatType),
		),
		Members: members,
	})
}

func hasDuplicateWatchRule(
	ctx context.Context,
	db DBTX,
	params CreateWatchRuleParams,
) (bool, error) {
	rules, err := collectMany[WatchRule](ctx, db, `
		SELECT `+watchRuleColumns+`
		FROM watch_rules
		WHERE debox_user_id = $1
		  AND rule_scope = 'standalone'
		  AND enabled = 1
		ORDER BY id
	`, params.DeBoxUserID)
	if err != nil {
		return false, fmt.Errorf("list watch rules for duplicate check: %w", err)
	}
	incoming := watchRuleConfigurationFingerprint(params, true)
	for _, rule := range rules {
		if incoming == watchRuleConfigurationFingerprint(
			createWatchRuleParamsFromRule(rule),
			true,
		) {
			return true, nil
		}
	}
	return false, nil
}

func hasDuplicateCombinationRule(
	ctx context.Context,
	db DBTX,
	params CreateCombinationRuleParams,
) (bool, error) {
	combinations, err := collectMany[CombinationRule](ctx, db, `
		SELECT `+combinationRuleColumns+`
		FROM combination_rules
		WHERE debox_user_id = $1 AND enabled = 1
		ORDER BY id
	`, params.DeBoxUserID)
	if err != nil {
		return false, fmt.Errorf("list combination rules for duplicate check: %w", err)
	}
	incoming := combinationRuleConfigurationFingerprint(params)
	for _, combination := range combinations {
		members, err := listCombinationMembers(ctx, db, combination.ID)
		if err != nil {
			return false, err
		}
		existing := CreateCombinationRuleParams{
			DeBoxUserID:          combination.DeBoxUserID,
			CycleType:            combination.CycleType,
			CycleMinutes:         combination.CycleMinutes,
			NotificationChatID:   combination.NotificationChatID,
			NotificationChatType: combination.NotificationChatType,
			Members: make(
				[]CreateCombinationMemberParams,
				0,
				len(members),
			),
		}
		for _, member := range members {
			existing.Members = append(existing.Members, CreateCombinationMemberParams{
				Rule:                 createWatchRuleParamsFromRule(member.Rule),
				RequiredTriggerCount: member.RequiredTriggerCount,
			})
		}
		if incoming == combinationRuleConfigurationFingerprint(existing) {
			return true, nil
		}
	}
	return false, nil
}

func createWatchRuleParamsFromRule(rule WatchRule) CreateWatchRuleParams {
	return CreateWatchRuleParams{
		DeBoxUserID:           rule.DeBoxUserID,
		ChainKey:              rule.ChainKey,
		ChainID:               rule.ChainID,
		WalletAddress:         rule.WalletAddress,
		TokenAddress:          rule.TokenAddress,
		TargetAddress:         rule.TargetAddress,
		TargetLabel:           rule.TargetLabel,
		RuleType:              rule.RuleType,
		Threshold:             rule.Threshold,
		NotificationChatID:    rule.NotificationChatID,
		NotificationChatType:  rule.NotificationChatType,
		NotificationLabel:     rule.NotificationLabel,
		NotificationLanguage:  rule.NotificationLanguage,
		RuleScope:             rule.RuleScope,
		DeliveryMode:          rule.DeliveryMode,
		CycleType:             rule.CycleType,
		CycleMinutes:          rule.CycleMinutes,
		TriggerCountThreshold: rule.TriggerCountThreshold,
		LastValue:             rule.LastValue,
	}
}

func normalizeFingerprintAddress(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeFingerprintOptionalAddress(value *string) string {
	if value == nil {
		return ""
	}
	return normalizeFingerprintAddress(*value)
}
