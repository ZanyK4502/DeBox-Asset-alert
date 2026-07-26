package marketrules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const (
	DefaultInterval = 5 * time.Second
	defaultBatch    = 200
)

type Repository interface {
	ListEnabledMarketRules(context.Context, int) ([]store.MarketRule, error)
	GetMarketProject(context.Context, int64, string) (*store.MarketProject, error)
	ListMarketSnapshots(context.Context, int64, string, int) ([]store.MarketSnapshot, error)
	ListMarketEvents(context.Context, int64, string, int64, int) ([]store.MarketEvent, error)
	ListMarketEventsAfter(context.Context, int64, string, int64, int) ([]store.MarketEvent, error)
	ListMarketHolders(context.Context, int64, string, bool, int) ([]store.MarketHolder, error)
	ListMarketAddressLabels(context.Context, int64, string) ([]store.MarketAddressLabel, error)
	CreateMarketEvent(context.Context, store.CreateMarketEventParams) (store.MarketEvent, bool, error)
	CreateMarketRuleEvent(context.Context, store.CreateMarketRuleEventParams) (store.MarketRuleEvent, bool, error)
	UpdateMarketRuleState(context.Context, int64, json.RawMessage, bool) (store.MarketRule, error)
	RecordMarketStageTrigger(context.Context, int64) (store.MarketStageWindow, bool, error)
	RecordMarketCombinationTrigger(context.Context, store.RecordMarketCombinationTriggerParams) (int64, error)
	ProcessPendingWatchCombinationTriggers(context.Context, int) (int64, error)
	ReconcileMarketNotificationSources(context.Context) error
	RecoverStaleMarketDeliveries(context.Context, time.Duration) (int64, error)
	ClaimMarketDeliveries(context.Context, int) ([]store.MarketDeliveryClaim, error)
	LoadMarketDelivery(context.Context, store.MarketDeliveryClaim) (store.MarketNotificationDelivery, error)
	CompleteMarketDelivery(context.Context, store.MarketDeliveryClaim, *string, string) error
	ListMarketProjectsDueHolderRefresh(context.Context, int64, time.Time, int) ([]store.MarketProject, error)
	ApplyExpiredEntitlementFallbacks(context.Context) (int64, error)
	UpsertMarketHolder(context.Context, store.UpsertMarketHolderParams) (store.MarketHolder, error)
	ClearMarketHolderRanksOutsideSnapshot(context.Context, int64, string, []string, time.Time) (int64, error)
	ListMarketProjectPools(context.Context, int64, string) ([]store.MarketPool, error)
}

type NotificationService interface {
	SendNotification(chatID, chatType, text string) (string, error)
}

type HolderProvider interface {
	TokenHoldersByContract(
		context.Context,
		string,
		string,
		string,
		chain.TokenHoldersOptions,
	) (chain.TokenHoldersPage, error)
}

type Lock interface {
	Unlock(context.Context) error
}

type TryLock func(context.Context, string) (Lock, bool, error)

type Dependencies struct {
	Repository    Repository
	Notifications NotificationService
	Holders       HolderProvider
	TryLock       TryLock
}

type Settings struct {
	Enabled               bool
	Interval              time.Duration
	Batch                 int
	HolderRefreshInterval time.Duration
}

type Service struct {
	repository           Repository
	notifications        NotificationService
	holders              HolderProvider
	tryLock              TryLock
	settings             Settings
	now                  func() time.Time
	lastEntitlementCheck time.Time
	lastHolderCheck      time.Time
}

func New(dependencies Dependencies, settings Settings) *Service {
	if settings.Interval <= 0 {
		settings.Interval = DefaultInterval
	}
	if settings.Batch <= 0 {
		settings.Batch = defaultBatch
	}
	if settings.HolderRefreshInterval <= 0 {
		settings.HolderRefreshInterval = 15 * time.Minute
	}
	return &Service{
		repository:    dependencies.Repository,
		notifications: dependencies.Notifications,
		holders:       dependencies.Holders,
		tryLock:       dependencies.TryLock,
		settings:      settings,
		now:           func() time.Time { return time.Now().UTC() },
	}
}

func (service *Service) Enabled() bool {
	return service != nil && service.settings.Enabled
}

type CycleResult struct {
	RulesEvaluated          int64 `json:"rules_evaluated"`
	TriggersCreated         int64 `json:"triggers_created"`
	CombinationInputs       int64 `json:"combination_inputs"`
	DeliveriesAttempted     int64 `json:"deliveries_attempted"`
	DeliveriesSent          int64 `json:"deliveries_sent"`
	DeliveriesFailed        int64 `json:"deliveries_failed"`
	StaleDeliveriesReset    int64 `json:"stale_deliveries_reset"`
	HolderProjectsRefreshed int64 `json:"holder_projects_refreshed"`
	ExpiredUsersPaused      int64 `json:"expired_users_paused"`
}

func (service *Service) RunCycle(ctx context.Context) (CycleResult, error) {
	if !service.Enabled() {
		return CycleResult{}, nil
	}
	if service.repository == nil || service.notifications == nil {
		return CycleResult{}, errors.New("market rule engine dependencies are incomplete")
	}
	if service.tryLock != nil {
		lock, acquired, err := service.tryLock(ctx, "rule-engine")
		if err != nil {
			return CycleResult{}, err
		}
		if !acquired {
			return CycleResult{}, nil
		}
		defer func() { _ = lock.Unlock(context.WithoutCancel(ctx)) }()
	}

	result := CycleResult{}
	now := service.now()
	if service.lastEntitlementCheck.IsZero() ||
		now.Sub(service.lastEntitlementCheck) >= time.Minute {
		paused, err := service.repository.ApplyExpiredEntitlementFallbacks(ctx)
		if err != nil {
			return result, err
		}
		result.ExpiredUsersPaused = paused
		service.lastEntitlementCheck = now
	}
	var holderRefreshErr error
	if service.holders != nil &&
		(service.lastHolderCheck.IsZero() ||
			now.Sub(service.lastHolderCheck) >= time.Minute) {
		service.lastHolderCheck = now
		refreshed, err := service.RefreshDueHolders(ctx)
		result.HolderProjectsRefreshed = refreshed
		if err != nil {
			holderRefreshErr = fmt.Errorf("refresh market holders: %w", err)
		}
	}
	if err := service.repository.ReconcileMarketNotificationSources(ctx); err != nil {
		return result, err
	}
	recovered, err := service.repository.RecoverStaleMarketDeliveries(ctx, 5*time.Minute)
	if err != nil {
		return result, err
	}
	result.StaleDeliveriesReset = recovered
	combinationInputs, err := service.repository.ProcessPendingWatchCombinationTriggers(
		ctx,
		service.settings.Batch,
	)
	if err != nil {
		return result, err
	}
	result.CombinationInputs += combinationInputs

	rules, err := service.repository.ListEnabledMarketRules(ctx, service.settings.Batch)
	if err != nil {
		return result, err
	}
	for _, rule := range rules {
		created, err := service.evaluateRule(ctx, rule)
		if err != nil {
			return result, fmt.Errorf("evaluate market rule %d: %w", rule.ID, err)
		}
		result.RulesEvaluated++
		result.TriggersCreated += created
	}

	combinationInputs, err = service.repository.ProcessPendingWatchCombinationTriggers(
		ctx,
		service.settings.Batch,
	)
	if err != nil {
		return result, err
	}
	result.CombinationInputs += combinationInputs
	claims, err := service.repository.ClaimMarketDeliveries(ctx, service.settings.Batch)
	if err != nil {
		return result, err
	}
	for _, claim := range claims {
		result.DeliveriesAttempted++
		sent, err := service.deliver(ctx, claim)
		if err != nil {
			result.DeliveriesFailed++
			continue
		}
		if sent {
			result.DeliveriesSent++
		}
	}
	return result, holderRefreshErr
}

func (service *Service) evaluateRule(
	ctx context.Context,
	rule store.MarketRule,
) (int64, error) {
	project, err := service.repository.GetMarketProject(
		ctx,
		rule.MarketProjectID,
		rule.DeBoxUserID,
	)
	if err != nil {
		return 0, err
	}
	if project == nil || project.Status != "active" {
		return 0, nil
	}
	snapshots, err := service.repository.ListMarketSnapshots(
		ctx,
		project.ID,
		rule.DeBoxUserID,
		1000,
	)
	if err != nil {
		return 0, err
	}
	var events []store.MarketEvent
	if !isSnapshotRule(rule.RuleType) {
		historicalEvents, err := service.repository.ListMarketEvents(
			ctx,
			project.ID,
			rule.DeBoxUserID,
			0,
			500,
		)
		if err != nil {
			return 0, err
		}
		state := ruleState{}
		if len(rule.State) > 0 {
			_ = json.Unmarshal(rule.State, &state)
		}
		newEvents, err := service.repository.ListMarketEventsAfter(
			ctx,
			project.ID,
			rule.DeBoxUserID,
			state.LastEventID,
			2000,
		)
		if err != nil {
			return 0, err
		}
		events = mergeMarketEvents(state.LastEventID, historicalEvents, newEvents)
	}
	var holders []store.MarketHolder
	var labels []store.MarketAddressLabel
	if isHolderRule(rule.RuleType) {
		holders, err = service.repository.ListMarketHolders(
			ctx,
			project.ID,
			rule.DeBoxUserID,
			false,
			100,
		)
		if err != nil {
			return 0, err
		}
		labels, err = service.repository.ListMarketAddressLabels(
			ctx,
			project.ID,
			rule.DeBoxUserID,
		)
		if err != nil {
			return 0, err
		}
	}
	evaluation, err := Evaluate(EvaluationInput{
		Rule:      rule,
		Project:   *project,
		Snapshots: snapshots,
		Events:    events,
		Holders:   holders,
		Labels:    labels,
		Now:       service.now(),
	})
	if err != nil {
		return 0, err
	}

	var createdCount int64
	for _, trigger := range evaluation.Triggers {
		event := trigger.Event
		if event == nil {
			poolID := rule.MarketPoolID
			if poolID == nil {
				poolID = project.MainPoolID
			}
			rawPayload, _ := json.Marshal(map[string]any{
				"rule_type": rule.RuleType,
				"current":   trigger.CurrentValue,
				"previous":  trigger.PreviousValue,
			})
			value, _, createErr := service.repository.CreateMarketEvent(
				ctx,
				store.CreateMarketEventParams{
					MarketPoolID: poolID,
					ChainKey:     project.ChainKey,
					ChainID:      project.ChainID,
					TokenAddress: project.TokenAddress,
					EventType:    trigger.EventType,
					EventKey:     trigger.EventKey,
					PriceUSD:     snapshotPrice(snapshots),
					Source:       "market_rule_engine",
					Confidence:   "1.0000",
					Confirmed:    true,
					OccurredAt:   trigger.OccurredAt,
					RawPayload:   rawPayload,
					Metadata:     trigger.Details,
				},
			)
			if createErr != nil {
				return createdCount, createErr
			}
			event = &value
		}
		status := "pending"
		if rule.RuleScope == "combination" {
			status = "combined"
		} else if rule.DeliveryMode == "stage" {
			status = "staged"
		}
		ruleEvent, inserted, err := service.repository.CreateMarketRuleEvent(
			ctx,
			store.CreateMarketRuleEventParams{
				MarketRuleID:       rule.ID,
				MarketEventID:      event.ID,
				TriggerKey:         trigger.EventKey,
				PreviousValue:      trigger.PreviousValue,
				CurrentValue:       trigger.CurrentValue,
				Note:               trigger.Note,
				Details:            trigger.Details,
				NotificationStatus: status,
			},
		)
		if err != nil {
			return createdCount, err
		}
		if !inserted {
			continue
		}
		createdCount++
		if status == "staged" {
			if _, _, err := service.repository.RecordMarketStageTrigger(ctx, ruleEvent.ID); err != nil {
				return createdCount, err
			}
		}
		if _, err := service.repository.RecordMarketCombinationTrigger(
			ctx,
			store.RecordMarketCombinationTriggerParams{
				SourceType:        "market",
				MarketRuleEventID: &ruleEvent.ID,
				OccurredAt:        trigger.OccurredAt,
				Note:              trigger.Note,
			},
		); err != nil {
			return createdCount, err
		}
	}
	_, err = service.repository.UpdateMarketRuleState(
		ctx,
		rule.ID,
		evaluation.State,
		createdCount > 0,
	)
	return createdCount, err
}

func isHolderRule(ruleType string) bool {
	switch ruleType {
	case plans.MarketHolderIncrease,
		plans.MarketHolderDecrease,
		plans.MarketHolderRankEntered,
		plans.MarketHolderRankExited:
		return true
	default:
		return false
	}
}

func mergeMarketEvents(
	cursor int64,
	historical []store.MarketEvent,
	current []store.MarketEvent,
) []store.MarketEvent {
	byID := make(map[int64]store.MarketEvent)
	for _, event := range historical {
		if event.ID <= cursor {
			byID[event.ID] = event
		}
	}
	for _, event := range current {
		byID[event.ID] = event
	}
	result := make([]store.MarketEvent, 0, len(byID))
	for _, event := range byID {
		result = append(result, event)
	}
	return result
}

func snapshotPrice(snapshots []store.MarketSnapshot) *string {
	if len(snapshots) == 0 {
		return nil
	}
	return snapshots[0].PriceUSD
}

func (service *Service) deliver(
	ctx context.Context,
	claim store.MarketDeliveryClaim,
) (bool, error) {
	delivery, err := service.repository.LoadMarketDelivery(ctx, claim)
	if err != nil {
		_ = service.repository.CompleteMarketDelivery(ctx, claim, nil, err.Error())
		return false, err
	}
	messageID, sendErr := service.notifications.SendNotification(
		delivery.NotificationChatID,
		delivery.NotificationChatType,
		MarketNotificationText(delivery),
	)
	if sendErr != nil {
		_ = service.repository.CompleteMarketDelivery(ctx, claim, nil, sendErr.Error())
		return false, sendErr
	}
	if err := service.repository.CompleteMarketDelivery(
		ctx,
		claim,
		&messageID,
		"",
	); err != nil {
		return false, err
	}
	return true, nil
}

type Runner struct {
	service *Service
}

func NewRunner(service *Service) *Runner {
	return &Runner{service: service}
}

func (runner *Runner) Run(ctx context.Context, logger *slog.Logger) {
	if runner == nil || runner.service == nil || !runner.service.Enabled() {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	run := func() {
		result, err := runner.service.RunCycle(ctx)
		if err != nil {
			logger.Error("market rule cycle failed", "error", err)
			return
		}
		if result.TriggersCreated > 0 || result.DeliveriesAttempted > 0 {
			logger.Info(
				"market rule cycle completed",
				"rules", result.RulesEvaluated,
				"triggers", result.TriggersCreated,
				"sent", result.DeliveriesSent,
				"failed", result.DeliveriesFailed,
			)
		}
	}
	run()
	ticker := time.NewTicker(runner.service.settings.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
