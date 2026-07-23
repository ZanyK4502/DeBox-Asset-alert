package monitor

import (
	"context"
	"fmt"
	"html"
	"math/big"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const freeAlertTimezone = "Asia/Shanghai"

var ruleTypeLabels = map[string]map[string]string{
	"zh": {
		plans.BalanceChange:      "余额变化",
		plans.Incoming:           "转入提醒",
		plans.Outgoing:           "转出提醒",
		plans.BalanceThreshold:   "余额阈值",
		plans.ApprovalChange:     "授权 / Approve 监控",
		plans.AddressInteraction: "指定地址交互提醒",
	},
	"en": {
		plans.BalanceChange:      "Balance change",
		plans.Incoming:           "Incoming transfer",
		plans.Outgoing:           "Outgoing transfer",
		plans.BalanceThreshold:   "Balance threshold",
		plans.ApprovalChange:     "Approval change",
		plans.AddressInteraction: "Specified address interaction",
	},
}

type Repository interface {
	ListEnabledWatchRules(context.Context, int) ([]store.WatchRule, error)
	UpdateWatchRuleValue(context.Context, int64, string) error
	CountDailyAlertEvents(context.Context, string, string) (int64, error)
	CreateAlertEvent(context.Context, store.CreateAlertEventParams) (store.AlertEvent, error)
	UpdateAlertEventNotification(context.Context, int64, string, *string, string) (store.AlertEvent, error)
}

type ChainService interface {
	Balance(context.Context, string, string, string, string) (chain.BalanceResult, error)
	TokenAllowance(context.Context, string, string, string, string, string) (chain.AllowanceResult, error)
	LatestInteraction(context.Context, string, string, string, string) (chain.InteractionResult, error)
}

type NotificationService interface {
	SendNotification(string, string, string) (string, error)
}

type Dependencies struct {
	Repository       Repository
	Chain            ChainService
	Notifications    NotificationService
	Catalog          *plans.Catalog
	TryExecutionLock TryLockFunc
	DefaultChainKey  string
}

type Executor struct {
	deps Dependencies
}

func New(dependencies Dependencies) *Executor {
	dependencies.DefaultChainKey = strings.ToLower(strings.TrimSpace(dependencies.DefaultChainKey))
	if dependencies.DefaultChainKey == "" {
		dependencies.DefaultChainKey = "bsc"
	}
	return &Executor{deps: dependencies}
}

type RuleResult struct {
	RuleID   int64             `json:"rule_id"`
	Status   string            `json:"status"`
	Reason   string            `json:"reason,omitempty"`
	Plan     string            `json:"plan,omitempty"`
	RuleType string            `json:"rule_type,omitempty"`
	Limit    int64             `json:"limit,omitempty"`
	Used     int64             `json:"used,omitempty"`
	Value    string            `json:"value,omitempty"`
	Event    *store.AlertEvent `json:"event,omitempty"`
	Error    string            `json:"error,omitempty"`
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
	case plans.BalanceChange, plans.Incoming, plans.Outgoing, plans.BalanceThreshold:
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
		rule.RuleType == plans.BalanceThreshold &&
		decimalCompare(currentValue, rule.Threshold) <= 0
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
	if rule.RuleType == plans.BalanceThreshold {
		note = fmt.Sprintf("%s 余额达到或低于阈值 %s。", current.Symbol, rule.Threshold)
		if language == "en" {
			note = fmt.Sprintf(
				"%s balance reached or fell below the threshold %s.",
				current.Symbol,
				rule.Threshold,
			)
		}
	}
	if limited, err := e.freeDailyLimit(ctx, rule, plan); err != nil {
		return RuleResult{}, err
	} else if limited != nil {
		limited.Value = currentValue
		return *limited, nil
	}
	event, err := e.recordAndSend(ctx, rule, previousValue, currentValue, note)
	if err != nil {
		return RuleResult{}, err
	}
	return RuleResult{RuleID: rule.ID, Status: "alerted", Event: &event}, nil
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

	note := fmt.Sprintf("授权对象：%s。", shortAddress(rule.TargetAddress))
	if normalizeLanguage(rule.NotificationLanguage) == "en" {
		note = fmt.Sprintf("Approved spender: %s.", shortAddress(rule.TargetAddress))
	}
	if limited, err := e.freeDailyLimit(ctx, rule, plan); err != nil {
		return RuleResult{}, err
	} else if limited != nil {
		limited.Value = currentValue
		return *limited, nil
	}
	event, err := e.recordAndSend(ctx, rule, previousValue, currentValue, note)
	if err != nil {
		return RuleResult{}, err
	}
	return RuleResult{RuleID: rule.ID, Status: "alerted", Event: &event}, nil
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

	note := fmt.Sprintf("目标地址：%s。", shortAddress(rule.TargetAddress))
	if normalizeLanguage(rule.NotificationLanguage) == "en" {
		note = fmt.Sprintf("Target address: %s.", shortAddress(rule.TargetAddress))
	}
	if limited, err := e.freeDailyLimit(ctx, rule, plan); err != nil {
		return RuleResult{}, err
	} else if limited != nil {
		limited.Value = cursor
		return *limited, nil
	}
	event, err := e.recordAndSend(ctx, rule, previousCursor, cursor, note)
	if err != nil {
		return RuleResult{}, err
	}
	return RuleResult{RuleID: rule.ID, Status: "alerted", Event: &event}, nil
}

func (e *Executor) recordAndSend(
	ctx context.Context,
	rule store.WatchRule,
	previousValue *string,
	currentValue string,
	note string,
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
	messageID, sendErr := e.deps.Notifications.SendNotification(
		rule.NotificationChatID,
		rule.NotificationChatType,
		alertText(rule, previousValue, currentValue, note),
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

func alertText(
	rule store.WatchRule,
	previousValue *string,
	currentValue string,
	note string,
) string {
	language := normalizeLanguage(rule.NotificationLanguage)
	previous := "-"
	if previousValue != nil {
		previous = *previousValue
	}
	label := ruleTypeLabels[language][rule.RuleType]
	if label == "" {
		label = rule.RuleType
	}
	if language == "en" {
		return "<b>DeBox Asset Alert</b><br/>" +
			"Rule: " + html.EscapeString(label) + "<br/>" +
			"Network: " + html.EscapeString(rule.ChainKey) + "<br/>" +
			"Wallet: " + html.EscapeString(shortAddress(stringPointer(rule.WalletAddress))) + "<br/>" +
			"Change: " + html.EscapeString(previous) + " -&gt; " + html.EscapeString(currentValue) + "<br/>" +
			html.EscapeString(note)
	}
	return "<b>DeBox Asset Alert</b><br/>" +
		"规则：" + html.EscapeString(label) + "<br/>" +
		"网络：" + html.EscapeString(rule.ChainKey) + "<br/>" +
		"钱包：" + html.EscapeString(shortAddress(stringPointer(rule.WalletAddress))) + "<br/>" +
		"变化：" + html.EscapeString(previous) + " -&gt; " + html.EscapeString(currentValue) + "<br/>" +
		html.EscapeString(note)
}

func normalizeLanguage(language string) string {
	if strings.ToLower(strings.TrimSpace(language)) == "en" {
		return "en"
	}
	return "zh"
}

func shortAddress(address *string) string {
	if address == nil || *address == "" {
		return "-"
	}
	value := *address
	if len(value) <= 16 {
		return value
	}
	return value[:8] + "..." + value[len(value)-6:]
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
