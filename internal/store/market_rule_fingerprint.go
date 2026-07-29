package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
)

type marketRuleScopeFingerprintRow struct {
	MarketRuleID int64 `db:"market_rule_id"`
	ScopeID      int64 `db:"scope_id"`
}

type marketProjectPoolFingerprintRow struct {
	ID           int64 `db:"id"`
	MarketPoolID int64 `db:"market_pool_id"`
}

type marketRuleFingerprintValue struct {
	MarketProjectID      int64   `json:"market_project_id"`
	RuleType             string  `json:"rule_type"`
	ThresholdValue       string  `json:"threshold_value"`
	ThresholdUnit        string  `json:"threshold_unit"`
	WindowMinutes        *int32  `json:"window_minutes"`
	CooldownSeconds      int32   `json:"cooldown_seconds"`
	RepeatWhileActive    bool    `json:"repeat_while_active"`
	RuleScope            string  `json:"rule_scope"`
	DeliveryMode         string  `json:"delivery_mode"`
	CycleType            string  `json:"cycle_type,omitempty"`
	CycleMinutes         int32   `json:"cycle_minutes,omitempty"`
	TriggerCount         int64   `json:"trigger_count,omitempty"`
	DeploymentScope      string  `json:"deployment_scope"`
	DeploymentIDs        []int64 `json:"deployment_ids,omitempty"`
	PoolScope            string  `json:"pool_scope"`
	ProjectPoolIDs       []int64 `json:"project_pool_ids,omitempty"`
	CooldownScope        string  `json:"cooldown_scope"`
	NotificationChatID   string  `json:"notification_chat_id"`
	NotificationChatType string  `json:"notification_chat_type"`
}

type marketCombinationMemberFingerprintValue struct {
	SourceType           string `json:"source_type"`
	RuleID               int64  `json:"rule_id"`
	RequiredTriggerCount int64  `json:"required_trigger_count"`
}

type marketCombinationFingerprintValue struct {
	CycleType            string                                    `json:"cycle_type"`
	CycleMinutes         int32                                     `json:"cycle_minutes"`
	NotificationChatID   string                                    `json:"notification_chat_id"`
	NotificationChatType string                                    `json:"notification_chat_type"`
	Members              []marketCombinationMemberFingerprintValue `json:"members"`
}

func marketRuleConfigurationFingerprint(
	params CreateMarketRuleParams,
	deploymentIDs []int64,
	projectPoolIDs []int64,
) string {
	normalizeMarketRuleParams(&params)
	if params.DeploymentScope != "selected" {
		deploymentIDs = nil
	}
	if params.PoolScope != "selected" {
		projectPoolIDs = nil
	}
	value := marketRuleFingerprintValue{
		MarketProjectID:      params.MarketProjectID,
		RuleType:             params.RuleType,
		ThresholdValue:       canonicalMarketDecimal(params.ThresholdValue),
		ThresholdUnit:        params.ThresholdUnit,
		WindowMinutes:        params.WindowMinutes,
		CooldownSeconds:      params.CooldownSeconds,
		RepeatWhileActive:    params.RepeatWhileActive,
		RuleScope:            params.RuleScope,
		DeliveryMode:         params.DeliveryMode,
		DeploymentScope:      params.DeploymentScope,
		DeploymentIDs:        sortedUniquePositiveIDs(deploymentIDs),
		PoolScope:            params.PoolScope,
		ProjectPoolIDs:       sortedUniquePositiveIDs(projectPoolIDs),
		CooldownScope:        params.CooldownScope,
		NotificationChatID:   params.NotificationChatID,
		NotificationChatType: params.NotificationChatType,
	}
	if params.DeliveryMode == "stage" {
		value.CycleType = params.CycleType
		value.CycleMinutes = params.CycleMinutes
		value.TriggerCount = params.TriggerCountThreshold
	}
	return fingerprintJSON(value)
}

func marketCombinationConfigurationFingerprint(
	params CreateMarketCombinationParams,
) string {
	normalizeMarketCombinationParams(&params)
	members := make(
		[]marketCombinationMemberFingerprintValue,
		0,
		len(params.Members),
	)
	for _, member := range params.Members {
		var ruleID int64
		if member.SourceType == "watch" && member.WatchRuleID != nil {
			ruleID = *member.WatchRuleID
		} else if member.SourceType == "market" && member.MarketRuleID != nil {
			ruleID = *member.MarketRuleID
		}
		members = append(members, marketCombinationMemberFingerprintValue{
			SourceType:           member.SourceType,
			RuleID:               ruleID,
			RequiredTriggerCount: member.RequiredTriggerCount,
		})
	}
	sort.Slice(members, func(i, j int) bool {
		if members[i].SourceType != members[j].SourceType {
			return members[i].SourceType < members[j].SourceType
		}
		if members[i].RuleID != members[j].RuleID {
			return members[i].RuleID < members[j].RuleID
		}
		return members[i].RequiredTriggerCount < members[j].RequiredTriggerCount
	})
	return fingerprintJSON(marketCombinationFingerprintValue{
		CycleType:            params.CycleType,
		CycleMinutes:         params.CycleMinutes,
		NotificationChatID:   params.NotificationChatID,
		NotificationChatType: params.NotificationChatType,
		Members:              members,
	})
}

func hasDuplicateMarketRule(
	ctx context.Context,
	db DBTX,
	params CreateMarketRuleParams,
) (bool, error) {
	rules, err := collectMany[MarketRule](ctx, db, `
		SELECT `+marketRuleColumns+`
		FROM market_rules
		WHERE debox_user_id = $1
		  AND market_project_id = $2
		  AND enabled = 1
		ORDER BY id
	`, params.DeBoxUserID, params.MarketProjectID)
	if err != nil {
		return false, fmt.Errorf("list market rules for duplicate check: %w", err)
	}
	if len(rules) == 0 {
		return false, nil
	}

	deploymentRows, err := collectMany[marketRuleScopeFingerprintRow](ctx, db, `
		SELECT mrd.market_rule_id, mrd.market_project_deployment_id AS scope_id
		FROM market_rule_deployments mrd
		JOIN market_rules mr ON mr.id = mrd.market_rule_id
		WHERE mr.debox_user_id = $1
		  AND mr.market_project_id = $2
		  AND mr.enabled = 1
	`, params.DeBoxUserID, params.MarketProjectID)
	if err != nil {
		return false, fmt.Errorf("list market rule deployments for duplicate check: %w", err)
	}
	poolRows, err := collectMany[marketRuleScopeFingerprintRow](ctx, db, `
		SELECT mrp.market_rule_id, mrp.market_project_pool_id AS scope_id
		FROM market_rule_pools mrp
		JOIN market_rules mr ON mr.id = mrp.market_rule_id
		WHERE mr.debox_user_id = $1
		  AND mr.market_project_id = $2
		  AND mr.enabled = 1
	`, params.DeBoxUserID, params.MarketProjectID)
	if err != nil {
		return false, fmt.Errorf("list market rule pools for duplicate check: %w", err)
	}
	projectPools, err := collectMany[marketProjectPoolFingerprintRow](ctx, db, `
		SELECT id, market_pool_id
		FROM market_project_pools
		WHERE market_project_id = $1
	`, params.MarketProjectID)
	if err != nil {
		return false, fmt.Errorf("list project pools for duplicate check: %w", err)
	}

	deploymentsByRule := groupMarketRuleScopeRows(deploymentRows)
	poolsByRule := groupMarketRuleScopeRows(poolRows)
	projectPoolByPool := make(map[int64]int64, len(projectPools))
	for _, projectPool := range projectPools {
		projectPoolByPool[projectPool.MarketPoolID] = projectPool.ID
	}
	incomingPoolIDs := append([]int64(nil), params.MarketProjectPoolIDs...)
	if params.PoolScope == "selected" && len(incomingPoolIDs) == 0 &&
		params.MarketPoolID != nil {
		if projectPoolID, exists := projectPoolByPool[*params.MarketPoolID]; exists {
			incomingPoolIDs = append(incomingPoolIDs, projectPoolID)
		}
	}
	incoming := marketRuleConfigurationFingerprint(
		params,
		params.MarketProjectDeploymentIDs,
		incomingPoolIDs,
	)
	for _, rule := range rules {
		existingPoolIDs := append([]int64(nil), poolsByRule[rule.ID]...)
		if rule.PoolScope == "selected" && len(existingPoolIDs) == 0 &&
			rule.MarketPoolID != nil {
			if projectPoolID, exists := projectPoolByPool[*rule.MarketPoolID]; exists {
				existingPoolIDs = append(existingPoolIDs, projectPoolID)
			}
		}
		if incoming == marketRuleConfigurationFingerprint(
			createMarketRuleParamsFromRule(rule),
			deploymentsByRule[rule.ID],
			existingPoolIDs,
		) {
			return true, nil
		}
	}
	return false, nil
}

func hasDuplicateMarketCombination(
	ctx context.Context,
	db DBTX,
	params CreateMarketCombinationParams,
) (bool, error) {
	combinations, err := collectMany[MarketCombinationRule](ctx, db, `
		SELECT `+marketCombinationRuleColumns+`
		FROM market_combination_rules
		WHERE debox_user_id = $1 AND enabled = 1
		ORDER BY id
	`, params.DeBoxUserID)
	if err != nil {
		return false, fmt.Errorf("list market combinations for duplicate check: %w", err)
	}
	if len(combinations) == 0 {
		return false, nil
	}
	members, err := collectMany[MarketCombinationMember](ctx, db, `
		SELECT `+marketCombinationMemberJoinedColumns+`
		FROM market_combination_members mcm
		JOIN market_combination_rules mcr
		  ON mcr.id = mcm.market_combination_rule_id
		WHERE mcr.debox_user_id = $1 AND mcr.enabled = 1
		ORDER BY mcm.market_combination_rule_id, mcm.id
	`, params.DeBoxUserID)
	if err != nil {
		return false, fmt.Errorf("list market combination members for duplicate check: %w", err)
	}
	membersByCombination := make(
		map[int64][]CreateMarketCombinationMemberParams,
		len(combinations),
	)
	for _, member := range members {
		membersByCombination[member.MarketCombinationRuleID] = append(
			membersByCombination[member.MarketCombinationRuleID],
			CreateMarketCombinationMemberParams{
				SourceType:           member.SourceType,
				WatchRuleID:          member.WatchRuleID,
				MarketRuleID:         member.MarketRuleID,
				RequiredTriggerCount: member.RequiredTriggerCount,
			},
		)
	}
	incoming := marketCombinationConfigurationFingerprint(params)
	for _, combination := range combinations {
		existing := CreateMarketCombinationParams{
			DeBoxUserID:          combination.DeBoxUserID,
			CycleType:            combination.CycleType,
			CycleMinutes:         combination.CycleMinutes,
			NotificationChatID:   combination.NotificationChatID,
			NotificationChatType: combination.NotificationChatType,
			Members:              membersByCombination[combination.ID],
		}
		if incoming == marketCombinationConfigurationFingerprint(existing) {
			return true, nil
		}
	}
	return false, nil
}

func createMarketRuleParamsFromRule(rule MarketRule) CreateMarketRuleParams {
	return CreateMarketRuleParams{
		DeBoxUserID:           rule.DeBoxUserID,
		MarketProjectID:       rule.MarketProjectID,
		MarketPoolID:          rule.MarketPoolID,
		DeploymentScope:       rule.DeploymentScope,
		PoolScope:             rule.PoolScope,
		CooldownScope:         rule.CooldownScope,
		RuleType:              rule.RuleType,
		ThresholdValue:        rule.ThresholdValue,
		ThresholdUnit:         rule.ThresholdUnit,
		WindowMinutes:         rule.WindowMinutes,
		Sensitivity:           rule.Sensitivity,
		CooldownSeconds:       rule.CooldownSeconds,
		RepeatWhileActive:     rule.RepeatWhileActive,
		RuleScope:             rule.RuleScope,
		DeliveryMode:          rule.DeliveryMode,
		CycleType:             rule.CycleType,
		CycleMinutes:          rule.CycleMinutes,
		TriggerCountThreshold: rule.TriggerCountThreshold,
		NotificationChatID:    rule.NotificationChatID,
		NotificationChatType:  rule.NotificationChatType,
		NotificationLabel:     rule.NotificationLabel,
		NotificationLanguage:  rule.NotificationLanguage,
	}
}

func groupMarketRuleScopeRows(
	rows []marketRuleScopeFingerprintRow,
) map[int64][]int64 {
	grouped := make(map[int64][]int64)
	for _, row := range rows {
		grouped[row.MarketRuleID] = append(grouped[row.MarketRuleID], row.ScopeID)
	}
	return grouped
}

func sortedUniquePositiveIDs(values []int64) []int64 {
	result := uniquePositiveIDs(values)
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) == 0 {
		return nil
	}
	return result
}

func canonicalMarketDecimal(value string) string {
	value = strings.TrimSpace(value)
	if parsed, ok := new(big.Rat).SetString(value); ok {
		return parsed.RatString()
	}
	return value
}

func fingerprintJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("encode market rule fingerprint: %v", err))
	}
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", sum)
}
