package monitor

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdetail"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationfmt"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const freeAlertTimezone = "Asia/Shanghai"

var ruleTypeLabels = map[string]map[string]string{
	"zh": {
		plans.BalanceChange:        "余额变化",
		plans.Incoming:             "转入提醒",
		plans.Outgoing:             "转出提醒",
		plans.BalanceThreshold:     "低余额阈值",
		plans.HighBalanceThreshold: "高余额阈值",
		plans.ApprovalChange:       "授权 / Approve 监控",
		plans.AddressInteraction:   "指定地址交互提醒",
	},
	"en": {
		plans.BalanceChange:        "Balance change",
		plans.Incoming:             "Incoming transfer",
		plans.Outgoing:             "Outgoing transfer",
		plans.BalanceThreshold:     "Low balance threshold",
		plans.HighBalanceThreshold: "High balance threshold",
		plans.ApprovalChange:       "Approval change",
		plans.AddressInteraction:   "Specified address interaction",
	},
}

type Repository interface {
	ApplyExpiredEntitlementFallbacks(context.Context) (int64, error)
	ListEnabledWatchRules(context.Context, int) ([]store.WatchRule, error)
	CleanupAggregationHistory(context.Context) (store.AggregationCleanupResult, error)
	UpdateWatchRuleValue(context.Context, int64, string) error
	CountDailyAlertEvents(context.Context, string, string) (int64, error)
	CreateAlertEvent(context.Context, store.CreateAlertEventParams) (store.AlertEvent, error)
	UpdateAlertEventNotification(context.Context, int64, string, *string, string) (store.AlertEvent, error)
	RecordStageTrigger(
		context.Context,
		store.RecordStageTriggerParams,
	) (store.StageTriggerResult, error)
	RecordCombinationTrigger(
		context.Context,
		store.RecordCombinationTriggerParams,
	) (store.CombinationTriggerResult, error)
	UpdateAggregateNotification(
		context.Context,
		int64,
		string,
		*string,
		string,
	) (store.AggregateNotification, error)
	CreateNotificationDetailSnapshot(
		context.Context,
		store.CreateNotificationDetailSnapshotParams,
	) (store.NotificationDetailSnapshot, error)
}

type ChainService interface {
	Balance(context.Context, string, string, string, string) (chain.BalanceResult, error)
	TokenAllowance(context.Context, string, string, string, string, string) (chain.AllowanceResult, error)
	LatestInteraction(context.Context, string, string, string, string) (chain.InteractionResult, error)
}

type NotificationService interface {
	SendNotification(string, string, string) (string, error)
}

type ActionNotificationService interface {
	SendNotificationWithAction(
		chatID, chatType, text, actionText, actionURL string,
	) (string, error)
}

type Dependencies struct {
	Repository       Repository
	Chain            ChainService
	Notifications    NotificationService
	Catalog          *plans.Catalog
	TryExecutionLock TryLockFunc
	DefaultChainKey  string
	PublicAppURL     string
}

type Executor struct {
	deps Dependencies
}

func New(dependencies Dependencies) *Executor {
	dependencies.DefaultChainKey = strings.ToLower(strings.TrimSpace(dependencies.DefaultChainKey))
	if dependencies.DefaultChainKey == "" {
		dependencies.DefaultChainKey = "bsc"
	}
	dependencies.PublicAppURL = strings.TrimRight(
		strings.TrimSpace(dependencies.PublicAppURL),
		"/",
	)
	return &Executor{deps: dependencies}
}

func (e *Executor) CleanupAggregationHistory(
	ctx context.Context,
) (store.AggregationCleanupResult, error) {
	return e.deps.Repository.CleanupAggregationHistory(ctx)
}

func (e *Executor) ApplyExpiredEntitlementFallbacks(ctx context.Context) (int64, error) {
	return e.deps.Repository.ApplyExpiredEntitlementFallbacks(ctx)
}

type RuleResult struct {
	RuleID           int64                             `json:"rule_id"`
	Status           string                            `json:"status"`
	Reason           string                            `json:"reason,omitempty"`
	Plan             string                            `json:"plan,omitempty"`
	RuleType         string                            `json:"rule_type,omitempty"`
	Limit            int64                             `json:"limit,omitempty"`
	Used             int64                             `json:"used,omitempty"`
	Value            string                            `json:"value,omitempty"`
	Event            *store.AlertEvent                 `json:"event,omitempty"`
	Aggregate        *store.AggregateNotification      `json:"aggregate,omitempty"`
	TriggerCount     int64                             `json:"trigger_count,omitempty"`
	TriggerThreshold int64                             `json:"trigger_threshold,omitempty"`
	CombinationID    int64                             `json:"combination_id,omitempty"`
	MemberProgress   []store.CombinationMemberProgress `json:"member_progress,omitempty"`
	Error            string                            `json:"error,omitempty"`
}

type CycleResult struct {
	Checked int          `json:"checked"`
	Alerted int          `json:"alerted"`
	Errors  []RuleResult `json:"errors"`
	Results []RuleResult `json:"results"`
}

func (e *Executor) CheckAll(ctx context.Context, limit int) (CycleResult, error) {
	rules, err := e.deps.Repository.ListEnabledWatchRules(ctx, limit)
	if err != nil {
		return CycleResult{}, err
	}
	result := CycleResult{
		Checked: len(rules),
		Errors:  make([]RuleResult, 0),
		Results: make([]RuleResult, 0, len(rules)),
	}
	for _, rule := range rules {
		item := e.checkRule(ctx, rule, rule.EffectivePlanCode)
		result.Results = append(result.Results, item)
		switch item.Status {
		case "alerted":
			result.Alerted++
		case "error":
			result.Errors = append(result.Errors, item)
		}
	}
	return result, nil
}

// CheckRule implements the immediate check used after a balance-threshold rule is created.
func (e *Executor) CheckRule(
	ctx context.Context,
	rule store.WatchRule,
	effectivePlanCode string,
) (any, error) {
	if e.deps.TryExecutionLock != nil {
		lock, acquired, err := e.deps.TryExecutionLock(ctx)
		if err != nil {
			return nil, err
		}
		if !acquired {
			return RuleResult{RuleID: rule.ID, Status: "locked"}, nil
		}
		result := e.checkRule(ctx, rule, effectivePlanCode)
		if err := lock.Unlock(ctx); err != nil {
			return nil, err
		}
		return result, nil
	}
	return e.checkRule(ctx, rule, effectivePlanCode), nil
}

func (e *Executor) checkRule(
	ctx context.Context,
	rule store.WatchRule,
	effectivePlanCode string,
) (result RuleResult) {
	result = RuleResult{RuleID: rule.ID}
	defer func() {
		if recovered := recover(); recovered != nil {
			result = RuleResult{
				RuleID: rule.ID,
				Status: "error",
				Error:  fmt.Sprint(recovered),
			}
		}
	}()

	plan, err := e.plan(effectivePlanCode)
	if err != nil {
		return errorResult(rule.ID, err)
	}
	if limited := ruleAllowedByPlan(rule, plan); limited != nil {
		return *limited
	}

	switch rule.RuleType {
	case plans.BalanceChange, plans.Incoming, plans.Outgoing,
		plans.BalanceThreshold, plans.HighBalanceThreshold:
		result, err = e.checkAssetRule(ctx, rule, plan)
	case plans.ApprovalChange:
		result, err = e.checkApprovalRule(ctx, rule, plan)
	case plans.AddressInteraction:
		result, err = e.checkInteractionRule(ctx, rule, plan)
	default:
		return RuleResult{
			RuleID:   rule.ID,
			Status:   "unsupported",
			RuleType: rule.RuleType,
		}
	}
	if err != nil {
		return errorResult(rule.ID, err)
	}
	return result
}

func (e *Executor) checkAssetRule(
	ctx context.Context,
	rule store.WatchRule,
	plan plans.Plan,
) (RuleResult, error) {
	current, err := e.deps.Chain.Balance(
		ctx,
		rule.WalletAddress,
		stringValue(rule.TokenAddress),
		rule.ChainKey,
		e.deps.DefaultChainKey,
	)
	if err != nil {
		return RuleResult{}, err
	}
	currentValue := current.Value
	previousValue := rule.LastValue
	if err := e.deps.Repository.UpdateWatchRuleValue(ctx, rule.ID, currentValue); err != nil {
		return RuleResult{}, err
	}

	initialThresholdMatch := previousValue == nil &&
		initialBalanceThresholdMatch(rule.RuleType, currentValue, rule.Threshold)
	if previousValue == nil && !initialThresholdMatch {
		return RuleResult{RuleID: rule.ID, Status: "baseline", Value: currentValue}, nil
	}
	if !initialThresholdMatch &&
		!shouldAlertAsset(rule.RuleType, stringValue(previousValue), currentValue, rule.Threshold) {
		return RuleResult{RuleID: rule.ID, Status: "no_change", Value: currentValue}, nil
	}

	language := normalizeLanguage(rule.NotificationLanguage)
	note := current.Symbol + " 余额触发监控条件。"
	if language == "en" {
		note = current.Symbol + " balance matched the monitoring condition."
	}
	threshold := notificationfmt.TokenAmount(rule.Threshold)
	if rule.RuleType == plans.BalanceThreshold {
		note = fmt.Sprintf("%s 余额达到或低于阈值 %s。", current.Symbol, threshold)
		if language == "en" {
			note = fmt.Sprintf(
				"%s balance reached or fell below the threshold %s.",
				current.Symbol,
				threshold,
			)
		}
	} else if rule.RuleType == plans.HighBalanceThreshold {
		note = fmt.Sprintf("%s 余额达到或高于阈值 %s。", current.Symbol, threshold)
		if language == "en" {
			note = fmt.Sprintf(
				"%s balance reached or rose above the threshold %s.",
				current.Symbol,
				threshold,
			)
		}
	}
	if limited, err := e.freeDailyLimit(ctx, rule, plan); err != nil {
		return RuleResult{}, err
	} else if limited != nil {
		limited.Value = currentValue
		return *limited, nil
	}
	return e.recordTrigger(ctx, rule, previousValue, currentValue, note, current.Symbol)
}

func (e *Executor) checkApprovalRule(
	ctx context.Context,
	rule store.WatchRule,
	plan plans.Plan,
) (RuleResult, error) {
	if rule.TokenAddress == nil || rule.TargetAddress == nil {
		return RuleResult{
			RuleID: rule.ID,
			Status: "invalid",
			Reason: "missing token or spender",
		}, nil
	}
	current, err := e.deps.Chain.TokenAllowance(
		ctx,
		rule.WalletAddress,
		*rule.TokenAddress,
		*rule.TargetAddress,
		rule.ChainKey,
		e.deps.DefaultChainKey,
	)
	if err != nil {
		return RuleResult{}, err
	}
	currentValue := current.Value
	previousValue := rule.LastValue
	if err := e.deps.Repository.UpdateWatchRuleValue(ctx, rule.ID, currentValue); err != nil {
		return RuleResult{}, err
	}
	if previousValue == nil {
		return RuleResult{RuleID: rule.ID, Status: "baseline", Value: currentValue}, nil
	}
	if decimalCompare(*previousValue, currentValue) == 0 {
		return RuleResult{RuleID: rule.ID, Status: "no_change", Value: currentValue}, nil
	}

	note := targetLabelPrefix(rule, "zh") +
		fmt.Sprintf("授权对象：%s。", shortAddress(rule.TargetAddress))
	if normalizeLanguage(rule.NotificationLanguage) == "en" {
		note = targetLabelPrefix(rule, "en") +
			fmt.Sprintf("Approved spender: %s.", shortAddress(rule.TargetAddress))
	}
	if limited, err := e.freeDailyLimit(ctx, rule, plan); err != nil {
		return RuleResult{}, err
	} else if limited != nil {
		limited.Value = currentValue
		return *limited, nil
	}
	return e.recordTrigger(ctx, rule, previousValue, currentValue, note, current.Symbol)
}

func (e *Executor) checkInteractionRule(
	ctx context.Context,
	rule store.WatchRule,
	plan plans.Plan,
) (RuleResult, error) {
	if rule.TargetAddress == nil {
		return RuleResult{
			RuleID: rule.ID,
			Status: "invalid",
			Reason: "missing target address",
		}, nil
	}
	current, err := e.deps.Chain.LatestInteraction(
		ctx,
		rule.WalletAddress,
		*rule.TargetAddress,
		rule.ChainKey,
		e.deps.DefaultChainKey,
	)
	if err != nil {
		return RuleResult{}, err
	}
	cursor := current.Cursor
	previousCursor := rule.LastValue
	if err := e.deps.Repository.UpdateWatchRuleValue(ctx, rule.ID, cursor); err != nil {
		return RuleResult{}, err
	}
	if previousCursor == nil {
		return RuleResult{RuleID: rule.ID, Status: "baseline", Value: cursor}, nil
	}
	if cursor == *previousCursor || !current.Matched {
		return RuleResult{RuleID: rule.ID, Status: "no_change", Value: cursor}, nil
	}

	note := targetLabelPrefix(rule, "zh") +
		fmt.Sprintf("目标地址：%s。", shortAddress(rule.TargetAddress))
	if normalizeLanguage(rule.NotificationLanguage) == "en" {
		note = targetLabelPrefix(rule, "en") +
			fmt.Sprintf("Target address: %s.", shortAddress(rule.TargetAddress))
	}
	if limited, err := e.freeDailyLimit(ctx, rule, plan); err != nil {
		return RuleResult{}, err
	} else if limited != nil {
		limited.Value = cursor
		return *limited, nil
	}
	return e.recordTrigger(ctx, rule, previousCursor, cursor, note, "")
}

func (e *Executor) recordTrigger(
	ctx context.Context,
	rule store.WatchRule,
	previousValue *string,
	currentValue string,
	note string,
	tokenSymbol string,
) (RuleResult, error) {
	if rule.RuleScope == "combination" {
		return e.recordCombinationTrigger(
			ctx,
			rule,
			previousValue,
			currentValue,
			note,
			tokenSymbol,
		)
	}
	if rule.DeliveryMode != "stage" {
		event, err := e.recordAndSend(
			ctx,
			rule,
			previousValue,
			currentValue,
			note,
			tokenSymbol,
		)
		if err != nil {
			return RuleResult{}, err
		}
		return RuleResult{
			RuleID: rule.ID,
			Status: "alerted",
			Value:  currentValue,
			Event:  &event,
		}, nil
	}

	stage, err := e.deps.Repository.RecordStageTrigger(ctx, store.RecordStageTriggerParams{
		WatchRuleID:   rule.ID,
		DeBoxUserID:   rule.DeBoxUserID,
		PreviousValue: previousValue,
		CurrentValue:  stringPointer(currentValue),
		Note:          note,
		TokenSymbol:   tokenSymbol,
	})
	if err != nil {
		return RuleResult{}, err
	}
	result := RuleResult{
		RuleID:           rule.ID,
		Status:           "counted",
		Value:            currentValue,
		TriggerCount:     stage.TotalTriggerCount,
		TriggerThreshold: stage.TriggerCountThreshold,
	}
	if !stage.NotificationDue {
		return result, nil
	}
	if stage.Notification == nil {
		return RuleResult{}, errors.New("stage notification was claimed without a record")
	}

	text := stageAlertText(rule, stage)
	snapshot, err := e.createAddressStageSnapshot(ctx, rule, stage, text)
	if err != nil {
		_, updateErr := e.deps.Repository.UpdateAggregateNotification(
			ctx,
			stage.Notification.ID,
			"failed",
			nil,
			err.Error(),
		)
		if updateErr != nil {
			return RuleResult{}, errors.Join(err, updateErr)
		}
		return RuleResult{}, err
	}
	messageID, sendErr := e.sendAggregateNotification(
		stage.Notification.NotificationChatID,
		stage.Notification.NotificationChatType,
		text,
		rule.NotificationLanguage,
		snapshot.PublicID,
	)
	if sendErr != nil {
		_, updateErr := e.deps.Repository.UpdateAggregateNotification(
			ctx,
			stage.Notification.ID,
			"failed",
			nil,
			sendErr.Error(),
		)
		if updateErr != nil {
			return RuleResult{}, fmt.Errorf(
				"send stage notification: %v; record failure: %w",
				sendErr,
				updateErr,
			)
		}
		return RuleResult{}, sendErr
	}
	notification, err := e.deps.Repository.UpdateAggregateNotification(
		ctx,
		stage.Notification.ID,
		"sent",
		stringPointer(messageID),
		"",
	)
	if err != nil {
		return RuleResult{}, err
	}
	result.Status = "alerted"
	result.Aggregate = &notification
	return result, nil
}

func (e *Executor) recordCombinationTrigger(
	ctx context.Context,
	rule store.WatchRule,
	previousValue *string,
	currentValue string,
	note string,
	tokenSymbol string,
) (RuleResult, error) {
	combination, err := e.deps.Repository.RecordCombinationTrigger(
		ctx,
		store.RecordCombinationTriggerParams{
			WatchRuleID:   rule.ID,
			DeBoxUserID:   rule.DeBoxUserID,
			PreviousValue: previousValue,
			CurrentValue:  stringPointer(currentValue),
			Note:          note,
			TokenSymbol:   tokenSymbol,
		},
	)
	if err != nil {
		return RuleResult{}, err
	}
	result := RuleResult{
		RuleID:         rule.ID,
		Status:         "counted",
		Value:          currentValue,
		CombinationID:  combination.CombinationRuleID,
		MemberProgress: combination.MemberProgress,
	}
	if !combination.NotificationDue {
		return result, nil
	}
	if combination.Notification == nil {
		return RuleResult{}, errors.New("combination notification was claimed without a record")
	}
	text := combinationAlertText(combination)
	snapshot, err := e.createAddressCombinationSnapshot(
		ctx,
		rule,
		combination,
		text,
	)
	if err != nil {
		_, updateErr := e.deps.Repository.UpdateAggregateNotification(
			ctx,
			combination.Notification.ID,
			"failed",
			nil,
			err.Error(),
		)
		if updateErr != nil {
			return RuleResult{}, errors.Join(err, updateErr)
		}
		return RuleResult{}, err
	}
	messageID, sendErr := e.sendCombinationNotification(
		combination.Notification.NotificationChatID,
		combination.Notification.NotificationChatType,
		text,
		combination.Notification.NotificationLanguage,
		snapshot.PublicID,
	)
	if sendErr != nil {
		_, updateErr := e.deps.Repository.UpdateAggregateNotification(
			ctx,
			combination.Notification.ID,
			"failed",
			nil,
			sendErr.Error(),
		)
		if updateErr != nil {
			return RuleResult{}, fmt.Errorf(
				"send combination notification: %v; record failure: %w",
				sendErr,
				updateErr,
			)
		}
		return RuleResult{}, sendErr
	}
	notification, err := e.deps.Repository.UpdateAggregateNotification(
		ctx,
		combination.Notification.ID,
		"sent",
		stringPointer(messageID),
		"",
	)
	if err != nil {
		return RuleResult{}, err
	}
	result.Status = "alerted"
	result.Aggregate = &notification
	return result, nil
}

func (e *Executor) recordAndSend(
	ctx context.Context,
	rule store.WatchRule,
	previousValue *string,
	currentValue string,
	note string,
	tokenSymbol string,
) (store.AlertEvent, error) {
	event, err := e.deps.Repository.CreateAlertEvent(ctx, store.CreateAlertEventParams{
		WatchRuleID:        rule.ID,
		EventType:          rule.RuleType,
		PreviousValue:      previousValue,
		CurrentValue:       stringPointer(currentValue),
		NotificationStatus: "pending",
	})
	if err != nil {
		return store.AlertEvent{}, err
	}
	occurredAt := event.CreatedAt
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}
	text := alertText(
		rule,
		previousValue,
		currentValue,
		tokenSymbol,
		note,
		occurredAt,
	)
	snapshot, err := e.createAddressRealtimeSnapshot(
		ctx,
		rule,
		event,
		tokenSymbol,
		note,
		occurredAt,
		text,
	)
	if err != nil {
		_, updateErr := e.deps.Repository.UpdateAlertEventNotification(
			ctx,
			event.ID,
			"failed",
			nil,
			err.Error(),
		)
		if updateErr != nil {
			return store.AlertEvent{}, errors.Join(err, updateErr)
		}
		return store.AlertEvent{}, err
	}
	messageID, sendErr := e.sendRealtimeNotification(
		rule,
		text,
		snapshot.PublicID,
	)
	if sendErr != nil {
		_, updateErr := e.deps.Repository.UpdateAlertEventNotification(
			ctx,
			event.ID,
			"failed",
			nil,
			sendErr.Error(),
		)
		if updateErr != nil {
			return store.AlertEvent{}, fmt.Errorf(
				"send notification: %v; record failure: %w",
				sendErr,
				updateErr,
			)
		}
		return store.AlertEvent{}, sendErr
	}
	return e.deps.Repository.UpdateAlertEventNotification(
		ctx,
		event.ID,
		"sent",
		stringPointer(messageID),
		"",
	)
}

func (e *Executor) sendRealtimeNotification(
	rule store.WatchRule,
	text string,
	notificationID string,
) (string, error) {
	actionSender, supportsAction := e.deps.Notifications.(ActionNotificationService)
	actionURL := notificationdetail.NotificationURL(e.deps.PublicAppURL, notificationID)
	if !supportsAction || actionURL == "" {
		return e.deps.Notifications.SendNotification(
			rule.NotificationChatID,
			rule.NotificationChatType,
			text,
		)
	}
	actionText := "查看详情"
	if normalizeLanguage(rule.NotificationLanguage) == "en" {
		actionText = "View details"
	}
	return actionSender.SendNotificationWithAction(
		rule.NotificationChatID,
		rule.NotificationChatType,
		text,
		actionText,
		actionURL,
	)
}

func (e *Executor) sendAggregateNotification(
	chatID, chatType, text, language, notificationID string,
) (string, error) {
	actionSender, supportsAction := e.deps.Notifications.(ActionNotificationService)
	actionURL := notificationdetail.NotificationURL(e.deps.PublicAppURL, notificationID)
	if !supportsAction || actionURL == "" {
		return e.deps.Notifications.SendNotification(chatID, chatType, text)
	}
	actionText := "查看全部事件"
	if normalizeLanguage(language) == "en" {
		actionText = "View all events"
	}
	return actionSender.SendNotificationWithAction(
		chatID,
		chatType,
		text,
		actionText,
		actionURL,
	)
}

func (e *Executor) sendCombinationNotification(
	chatID, chatType, text, language, notificationID string,
) (string, error) {
	actionSender, supportsAction := e.deps.Notifications.(ActionNotificationService)
	actionURL := notificationdetail.NotificationURL(e.deps.PublicAppURL, notificationID)
	if !supportsAction || actionURL == "" {
		return e.deps.Notifications.SendNotification(chatID, chatType, text)
	}
	actionText := "查看完整分析"
	if normalizeLanguage(language) == "en" {
		actionText = "View full analysis"
	}
	return actionSender.SendNotificationWithAction(
		chatID,
		chatType,
		text,
		actionText,
		actionURL,
	)
}

func (e *Executor) freeDailyLimit(
	ctx context.Context,
	rule store.WatchRule,
	plan plans.Plan,
) (*RuleResult, error) {
	if plan.Code != plans.Free || plan.DailyAlertLimit == nil || *plan.DailyAlertLimit <= 0 {
		return nil, nil
	}
	used, err := e.deps.Repository.CountDailyAlertEvents(
		ctx,
		rule.DeBoxUserID,
		freeAlertTimezone,
	)
	if err != nil {
		return nil, err
	}
	limit := int64(*plan.DailyAlertLimit)
	if used < limit {
		return nil, nil
	}
	return &RuleResult{
		RuleID: rule.ID,
		Status: "daily_limit",
		Limit:  limit,
		Used:   used,
	}, nil
}

func (e *Executor) plan(code string) (plans.Plan, error) {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		code = plans.Free
	}
	return e.deps.Catalog.Get(code)
}

func ruleAllowedByPlan(rule store.WatchRule, plan plans.Plan) *RuleResult {
	if rule.RuleScope == "combination" && !plan.AllowsCombinationRules() {
		return &RuleResult{
			RuleID: rule.ID,
			Status: "plan_limited",
			Reason: "combination_rule",
			Plan:   plan.Code,
		}
	}
	if !plan.AllowsRuleType(rule.RuleType) {
		return &RuleResult{
			RuleID: rule.ID,
			Status: "plan_limited",
			Reason: "rule_type",
			Plan:   plan.Code,
		}
	}
	if rule.NotificationChatType == "group" && !plan.GroupNotification {
		return &RuleResult{
			RuleID: rule.ID,
			Status: "plan_limited",
			Reason: "group_notification",
			Plan:   plan.Code,
		}
	}
	if rule.DeliveryMode == "stage" && !plan.AllowsStageNotifications() {
		return &RuleResult{
			RuleID: rule.ID,
			Status: "plan_limited",
			Reason: "stage_notification",
			Plan:   plan.Code,
		}
	}
	return nil
}

func shouldAlertAsset(ruleType, previousValue, currentValue, thresholdValue string) bool {
	previous := decimal(previousValue)
	current := decimal(currentValue)
	threshold := decimal(thresholdValue)
	delta := new(big.Float).Sub(current, previous)
	absoluteDelta := new(big.Float).Abs(new(big.Float).Set(delta))

	switch ruleType {
	case plans.BalanceChange:
		return delta.Sign() != 0 && (threshold.Sign() <= 0 || absoluteDelta.Cmp(threshold) >= 0)
	case plans.Incoming:
		return delta.Sign() > 0 && (threshold.Sign() <= 0 || delta.Cmp(threshold) >= 0)
	case plans.Outgoing:
		return delta.Sign() < 0 && (threshold.Sign() <= 0 || absoluteDelta.Cmp(threshold) >= 0)
	case plans.BalanceThreshold:
		return previous.Cmp(threshold) > 0 && current.Cmp(threshold) <= 0
	case plans.HighBalanceThreshold:
		return previous.Cmp(threshold) < 0 && current.Cmp(threshold) >= 0
	default:
		return false
	}
}

func initialBalanceThresholdMatch(ruleType, currentValue, thresholdValue string) bool {
	switch ruleType {
	case plans.BalanceThreshold:
		return decimalCompare(currentValue, thresholdValue) <= 0
	case plans.HighBalanceThreshold:
		return decimalCompare(currentValue, thresholdValue) >= 0
	default:
		return false
	}
}

func decimalCompare(left, right string) int {
	return decimal(left).Cmp(decimal(right))
}

func decimal(value string) *big.Float {
	number, _, err := big.ParseFloat(strings.TrimSpace(value), 10, 256, big.ToNearestEven)
	if err != nil {
		return new(big.Float).SetPrec(256)
	}
	return number
}

func normalizeLanguage(language string) string {
	if strings.ToLower(strings.TrimSpace(language)) == "en" {
		return "en"
	}
	return "zh"
}

func targetLabelPrefix(rule store.WatchRule, language string) string {
	label := strings.TrimSpace(rule.TargetLabel)
	if label == "" {
		return ""
	}
	if normalizeLanguage(language) == "en" {
		return "Target note: " + label + ". "
	}
	return "目标备注：" + label + "。"
}

func shortAddress(address *string) string {
	if address == nil {
		return "-"
	}
	value := notificationfmt.ShortIdentifier(*address)
	if value == "" {
		return "-"
	}
	return value
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func stringPointer(value string) *string {
	return &value
}

func errorResult(ruleID int64, err error) RuleResult {
	return RuleResult{RuleID: ruleID, Status: "error", Error: err.Error()}
}
