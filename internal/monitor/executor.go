package monitor

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
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
	DeferWalletObservation(context.Context, int64, time.Duration) error
	RecordWalletObservation(context.Context, store.RecordWalletObservationParams) (store.WalletObservationResult, error)
	ClaimWalletNotifications(context.Context, int64, int) ([]store.WalletNotificationOutbox, error)
	StartWalletNotificationAttempt(context.Context, int64, string) (store.WalletNotificationOutbox, error)
	WalletNotificationRule(context.Context, store.WalletNotificationOutbox) (store.WatchRule, bool, error)
	FinishWalletNotification(context.Context, store.FinishWalletNotificationParams) (store.WalletNotificationOutbox, error)
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
	observationStarted bool
	OutboxID           int64                             `json:"outbox_id,omitempty"`
	RuleID             int64                             `json:"rule_id"`
	Status             string                            `json:"status"`
	Reason             string                            `json:"reason,omitempty"`
	Plan               string                            `json:"plan,omitempty"`
	RuleType           string                            `json:"rule_type,omitempty"`
	Limit              int64                             `json:"limit,omitempty"`
	Used               int64                             `json:"used,omitempty"`
	Value              string                            `json:"value,omitempty"`
	Event              *store.AlertEvent                 `json:"event,omitempty"`
	Aggregate          *store.AggregateNotification      `json:"aggregate,omitempty"`
	TriggerCount       int64                             `json:"trigger_count,omitempty"`
	TriggerThreshold   int64                             `json:"trigger_threshold,omitempty"`
	CombinationID      int64                             `json:"combination_id,omitempty"`
	MemberProgress     []store.CombinationMemberProgress `json:"member_progress,omitempty"`
	Error              string                            `json:"error,omitempty"`
	ReadObservedAt     *time.Time                        `json:"read_observed_at,omitempty"`
}

type CycleResult struct {
	Selected            int `json:"selected"`
	lastAttempted       *store.WalletObservationCursor
	through             *store.WalletObservationCursor
	Checked             int               `json:"checked"`
	Alerted             int               `json:"alerted"`
	Queued              int               `json:"queued"`
	DeliveriesAttempted int               `json:"deliveries_attempted"`
	DeliveriesSent      int               `json:"deliveries_sent"`
	DeliveriesFailed    int               `json:"deliveries_failed"`
	Errors              []RuleResult      `json:"errors"`
	Results             []RuleResult      `json:"results"`
	Reads               WalletReadMetrics `json:"reads"`
	HasMoreDue          bool              `json:"has_more_due"`
	NextObservationAt   *time.Time        `json:"next_observation_at,omitempty"`
	NextDeliveryAt      *time.Time        `json:"next_delivery_at,omitempty"`
}

func (e *Executor) CheckAll(ctx context.Context, limit int) (CycleResult, error) {
	return e.checkAllWithDeliveryBudget(ctx, limit, 5*time.Second)
}

func (e *Executor) checkAllWithDeliveryBudget(ctx context.Context, limit int, budget time.Duration) (CycleResult, error) {
	rules, err := e.deps.Repository.ListEnabledWatchRules(ctx, limit)
	if err != nil {
		return CycleResult{}, err
	}
	return e.checkWalletRules(ctx, rules, budget, walletReadMaxAge), nil
}

type dueWalletRepository interface {
	ListDueWatchRules(context.Context, int, time.Duration) ([]store.WatchRule, error)
	NextWalletObservationAt(context.Context, time.Duration) (*time.Time, error)
}

type pagedDueWalletRepository interface {
	dueWalletRepository
	WalletObservationDueFence(context.Context, time.Duration) (*store.WalletObservationCursor, error)
	ListDueWatchRulesPage(context.Context, int, time.Duration, *store.WalletObservationCursor, *store.WalletObservationCursor) ([]store.WatchRule, error)
}

// CheckDue consumes one bounded page without forcing unrelated overdue rules
// to wait a whole observation period after each page.
func (e *Executor) CheckDue(ctx context.Context, limit int, interval time.Duration) (CycleResult, error) {
	return e.checkDuePage(ctx, limit, interval, nil, nil)
}

func (e *Executor) checkDuePage(ctx context.Context, limit int, interval time.Duration, after, through *store.WalletObservationCursor) (CycleResult, error) {
	if limit < 1 || limit > walletReadCacheKeys {
		limit = walletReadCacheKeys
	}
	if interval <= 0 {
		interval = DefaultInterval
	}
	repository, ok := e.deps.Repository.(dueWalletRepository)
	if !ok {
		// Older embedders retain their existing manually driven list contract.
		return e.CheckAll(ctx, limit)
	}
	var rules []store.WatchRule
	var err error
	if paged, ok := repository.(pagedDueWalletRepository); ok {
		if through == nil {
			through, err = paged.WalletObservationDueFence(ctx, interval)
			if err != nil {
				return CycleResult{}, err
			}
		}
		if through != nil {
			rules, err = paged.ListDueWatchRulesPage(ctx, limit, interval, after, through)
		}
	} else {
		rules, err = repository.ListDueWatchRules(ctx, limit, interval)
	}
	if err != nil {
		return CycleResult{}, err
	}
	result := e.checkWalletRules(ctx, rules, 5*time.Second, interval)
	result.through = through
	result.HasMoreDue = len(rules) == limit || result.Checked < len(rules)
	if sameWalletObservationKey(result.lastAttempted, through) {
		result.HasMoreDue = false
	}
	if !result.HasMoreDue && ctx.Err() == nil {
		result.NextObservationAt, err = repository.NextWalletObservationAt(ctx, interval)
	}
	return result, err
}

func (e *Executor) checkWalletRules(ctx context.Context, rules []store.WatchRule, budget, maxAge time.Duration) CycleResult {
	result := CycleResult{
		Selected: len(rules),
		Errors:   make([]RuleResult, 0),
		Results:  make([]RuleResult, 0, len(rules)),
	}
	if maxAge <= 0 || maxAge > walletReadMaxAge {
		maxAge = walletReadMaxAge
	}
	observationContext, observationCancel := context.WithTimeout(ctx, maxAge)
	defer observationCancel()
	reads := newWalletCycleReads(observationContext, e.deps.Chain, maxAge)
	observationContext = context.WithValue(observationContext, walletCycleReadsKey{}, reads)
	observationContext = context.WithValue(observationContext, walletEnqueueOnlyContextKey{}, true)
	stopPrefetch := reads.startBalancePrefetch(observationContext, e.walletPrefetchRequests(rules))
	defer stopPrefetch()
	for _, rule := range rules {
		if observationContext.Err() != nil {
			break
		}
		item := e.checkRule(observationContext, rule, rule.EffectivePlanCode)
		result.lastAttempted = walletObservationKey(rule)
		if !item.observationStarted && observationContext.Err() == nil &&
			(item.Status == "invalid" || item.Status == "unsupported" || item.Status == "plan_limited" || item.Status == "error") {
			// These paths return before attempting an observation transaction.
			// Keep malformed/ineligible active rows from monopolizing a due page.
			if deferErr := e.deps.Repository.DeferWalletObservation(observationContext, rule.ID, time.Minute); deferErr != nil {
				item = errorResult(rule.ID, deferErr)
			}
		}
		result.Checked++
		result.Results = append(result.Results, item)
		switch item.Status {
		case "queued":
			result.Queued++
		case "error":
			result.Errors = append(result.Errors, item)
		}
	}
	observationCancel()
	stopPrefetch()
	result.Reads = reads.metrics
	reads.entries = nil
	e.drainWalletDeliveries(ctx, &result, budget)
	return result
}

// CheckRule implements the immediate check used after a balance-threshold rule is created.
func (e *Executor) CheckRule(
	ctx context.Context,
	rule store.WatchRule,
	effectivePlanCode string,
) (any, error) {
	ctx = context.WithValue(ctx, walletCycleReadsKey{}, newWalletCycleReads(ctx, e.deps.Chain, walletReadMaxAge))
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
	phase := &walletObservationPhase{}
	ctx = context.WithValue(ctx, walletObservationPhaseKey{}, phase)
	ctx = context.WithValue(ctx, walletReadBaselineTimeKey{}, rule.LastCheckedAt)
	result = RuleResult{RuleID: rule.ID}
	defer func() {
		if recovered := recover(); recovered != nil {
			result = RuleResult{
				RuleID: rule.ID,
				Status: "error",
				Error:  fmt.Sprint(recovered),
			}
		}
		result.observationStarted = phase.started
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
	current, err := e.walletChain(ctx).Balance(
		ctx,
		rule.WalletAddress,
		stringValue(rule.TokenAddress),
		rule.ChainKey,
		e.deps.DefaultChainKey,
	)
	if err != nil {
		return RuleResult{}, err
	}
	ctx, err = walletBalanceReadContext(ctx, rule, current, e.deps.DefaultChainKey)
	if err != nil {
		return RuleResult{}, err
	}
	currentValue := current.Value
	previousValue := rule.LastValue
	initialThresholdMatch := previousValue == nil &&
		initialBalanceThresholdMatch(rule.RuleType, currentValue, rule.Threshold)
	if previousValue == nil && !initialThresholdMatch {
		return e.commitWalletObservation(ctx, rule, plan, currentValue, "", current.Symbol, false, "baseline")
	}
	if !initialThresholdMatch &&
		!shouldAlertAsset(rule.RuleType, stringValue(previousValue), currentValue, rule.Threshold) {
		return e.commitWalletObservation(ctx, rule, plan, currentValue, "", current.Symbol, false, "no_change")
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
	return e.commitWalletObservation(ctx, rule, plan, currentValue, note, current.Symbol, true, "")
}

type walletBalanceReadContextKey struct{}

type walletBalanceReadMetadata struct {
	source   string
	decimals int
}

// The source and precision travel with this particular observation, including
// memo hits. Missing or malformed ERC-20 evidence must never become a new zero
// baseline. Native balances retain their existing source contract.
func walletBalanceReadContext(ctx context.Context, rule store.WatchRule, current chain.BalanceResult, fallback string) (context.Context, error) {
	if strings.TrimSpace(stringValue(rule.TokenAddress)) == "" {
		return ctx, nil
	}
	profile, err := chain.ChainProfile(rule.ChainKey, fallback)
	if err != nil || int64(rule.ChainID) != profile.ChainID || current.ChainID != profile.ChainID || current.ChainKey != profile.Key {
		return ctx, fmt.Errorf("wallet ERC-20 balance chain identity does not match its rule")
	}
	wallet, err := chain.ValidateAddress(rule.WalletAddress)
	if err != nil {
		return ctx, fmt.Errorf("wallet ERC-20 rule wallet is invalid")
	}
	observedWallet, err := chain.ValidateAddress(current.WalletAddress)
	if err != nil || observedWallet != wallet {
		return ctx, fmt.Errorf("wallet ERC-20 balance wallet does not match its rule")
	}
	token, err := chain.ValidateAddress(*rule.TokenAddress)
	if err != nil || current.TokenAddress == nil {
		return ctx, fmt.Errorf("wallet ERC-20 balance token does not match its rule")
	}
	observedToken, err := chain.ValidateAddress(*current.TokenAddress)
	if err != nil || observedToken != token {
		return ctx, fmt.Errorf("wallet ERC-20 balance token does not match its rule")
	}
	if current.ReadSource != chain.WalletBalanceReadSource || current.ReadDecimals == nil ||
		*current.ReadDecimals < 0 || *current.ReadDecimals > 255 || current.Decimals != *current.ReadDecimals {
		return ctx, fmt.Errorf("wallet ERC-20 balance source or precision is unavailable")
	}
	value := current.Value
	if value == "" || len(value) > 258 || strings.TrimSpace(value) != value {
		return ctx, fmt.Errorf("wallet ERC-20 balance value is invalid")
	}
	point := -1
	for i, ch := range value {
		if ch == '.' && point < 0 {
			point = i
			continue
		}
		if ch < '0' || ch > '9' {
			return ctx, fmt.Errorf("wallet ERC-20 balance value is invalid")
		}
	}
	if point == 0 || point == len(value)-1 {
		return ctx, fmt.Errorf("wallet ERC-20 balance value is invalid")
	}
	units, err := chain.AmountToUnits(value, *current.ReadDecimals)
	if err != nil || units.Sign() < 0 || units.BitLen() > 256 {
		return ctx, fmt.Errorf("wallet ERC-20 balance value does not match its precision")
	}
	return context.WithValue(ctx, walletBalanceReadContextKey{}, walletBalanceReadMetadata{
		source: current.ReadSource, decimals: *current.ReadDecimals,
	}), nil
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
	current, err := e.walletChain(ctx).TokenAllowance(
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
	if previousValue == nil {
		return e.commitWalletObservation(ctx, rule, plan, currentValue, "", current.Symbol, false, "baseline")
	}
	if decimalCompare(*previousValue, currentValue) == 0 {
		return e.commitWalletObservation(ctx, rule, plan, currentValue, "", current.Symbol, false, "no_change")
	}

	note := targetLabelPrefix(rule, "zh") +
		fmt.Sprintf("授权对象：%s。", shortAddress(rule.TargetAddress))
	if normalizeLanguage(rule.NotificationLanguage) == "en" {
		note = targetLabelPrefix(rule, "en") +
			fmt.Sprintf("Approved spender: %s.", shortAddress(rule.TargetAddress))
	}
	return e.commitWalletObservation(ctx, rule, plan, currentValue, note, current.Symbol, true, "")
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
	current, err := e.walletChain(ctx).LatestInteraction(
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
	if previousCursor == nil {
		return e.commitWalletObservation(ctx, rule, plan, cursor, "", "", false, "baseline")
	}
	if cursor == *previousCursor || !current.Matched {
		return e.commitWalletObservation(ctx, rule, plan, cursor, "", "", false, "no_change")
	}

	note := targetLabelPrefix(rule, "zh") +
		fmt.Sprintf("目标地址：%s。", shortAddress(rule.TargetAddress))
	if normalizeLanguage(rule.NotificationLanguage) == "en" {
		note = targetLabelPrefix(rule, "en") +
			fmt.Sprintf("Target address: %s.", shortAddress(rule.TargetAddress))
	}
	return e.commitWalletObservation(ctx, rule, plan, cursor, note, "", true, "", walletObservationSource{SourceOccurredAt: current.SourceOccurredAt, SourceTimeBasis: current.SourceTimeBasis})
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
