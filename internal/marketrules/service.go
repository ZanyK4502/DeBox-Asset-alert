package marketrules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketwindow"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdetail"
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
	CreateMarketRuleEventAndEnqueue(context.Context, store.CreateMarketRuleEventParams) (store.MarketRuleEvent, bool, error)
	UpdateMarketRuleState(context.Context, int64, json.RawMessage, bool) (store.MarketRule, error)
	RecordMarketStageTrigger(context.Context, int64) (store.MarketStageWindow, bool, error)
	RecordMarketCombinationTrigger(context.Context, store.RecordMarketCombinationTriggerParams) (int64, error)
	ProcessPendingWatchCombinationTriggers(context.Context, int) (int64, error)
	ReconcileMarketNotificationSources(context.Context) error
	RecoverStaleMarketDeliveries(context.Context, time.Duration) (int64, error)
	ClaimMarketDeliveries(context.Context, int) ([]store.MarketDeliveryClaim, error)
	LoadMarketDelivery(context.Context, store.MarketDeliveryClaim) (store.MarketNotificationDelivery, error)
	CompleteMarketDelivery(context.Context, store.MarketDeliveryClaim, *string, string) error
	GetMarketDeliveryPayload(context.Context, store.MarketDeliveryClaim) (*store.MarketDeliveryPayload, error)
	PrepareMarketDelivery(context.Context, store.MarketDeliveryClaim, store.MarketDeliveryPayload) (store.MarketDeliveryPayload, error)
	BeginMarketDeliverySend(context.Context, store.MarketDeliveryClaim) error
	CompleteMarketDeliveryResult(context.Context, store.MarketDeliveryClaim, *string, *notificationdelivery.Failure) error
	CreateNotificationDetailSnapshot(
		context.Context,
		store.CreateNotificationDetailSnapshotParams,
	) (store.NotificationDetailSnapshot, error)
	ListMarketProjectsDueHolderRefresh(context.Context, int64, time.Time, int) ([]store.MarketProject, error)
	ApplyExpiredEntitlementFallbacks(context.Context) (int64, error)
	UpsertMarketHolder(context.Context, store.UpsertMarketHolderParams) (store.MarketHolder, error)
	ClearMarketHolderRanksOutsideSnapshot(context.Context, int64, string, []string, time.Time) (int64, error)
	ListMarketProjectPools(context.Context, int64, string) ([]store.MarketPool, error)
}

type multiChainRuleRepository interface {
	ListMarketRuleTargets(
		context.Context,
		store.MarketRule,
		store.MarketProject,
	) ([]store.MarketRuleTarget, error)
	LoadMarketRuleTargetState(
		context.Context,
		int64,
		store.MarketRuleTarget,
	) (store.MarketRuleTarget, bool, error)
	UpdateMarketRuleTargetState(
		context.Context,
		int64,
		store.MarketRuleTarget,
		json.RawMessage,
		bool,
	) error
	ListMarketSnapshotsForTarget(
		context.Context,
		store.MarketRuleTarget,
		int,
	) ([]store.MarketSnapshot, error)
	ListMarketEventsForTarget(
		context.Context,
		store.MarketRuleTarget,
		int64,
		bool,
		int,
	) ([]store.MarketEvent, error)
	ListMarketHoldersForTarget(
		context.Context,
		store.MarketRuleTarget,
		int,
	) ([]store.MarketHolder, error)
	ListMarketAddressLabelsForTarget(
		context.Context,
		int64,
		string,
		store.MarketRuleTarget,
	) ([]store.MarketAddressLabel, error)
}

type NotificationService interface {
	SendNotification(chatID, chatType, text string) (string, error)
}

type marketRuleEvaluationDeferrer interface {
	DeferMarketRuleEvaluation(context.Context, int64, time.Duration) error
}

type ActionNotificationService interface {
	SendNotificationWithAction(
		chatID, chatType, text, actionText, actionURL string,
	) (string, error)
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
	PublicAppURL  string
}

type Settings struct {
	// nil retains legacy adapters; production supplies an explicit product scope.
	MonitoringChainKeys   []string
	Enabled               bool
	TradeFactsEnabled     bool
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
	publicAppURL         string
}

func New(dependencies Dependencies, settings Settings) *Service {
	if settings.MonitoringChainKeys != nil {
		settings.MonitoringChainKeys = append([]string{}, settings.MonitoringChainKeys...)
	}
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
		publicAppURL: strings.TrimRight(
			strings.TrimSpace(dependencies.PublicAppURL),
			"/",
		),
	}
}

func (service *Service) Enabled() bool {
	return service != nil && service.settings.Enabled
}

type CycleResult struct {
	LockSkipped                     bool                                 `json:"lock_skipped"`
	RulesEvaluated                  int64                                `json:"rules_evaluated"`
	RulesUnsupportedParameters      int64                                `json:"rules_unsupported_parameters"`
	TradeRulesAwaitingActivation    int64                                `json:"trade_rules_awaiting_activation"`
	TradeRulesIncompleteWindow      int64                                `json:"trade_rules_incomplete_window"`
	TriggersCreated                 int64                                `json:"triggers_created"`
	CombinationInputs               int64                                `json:"combination_inputs"`
	DeliveriesClaimed               int64                                `json:"deliveries_claimed"`
	DeliveriesAttempted             int64                                `json:"deliveries_attempted"`
	DeliveriesSent                  int64                                `json:"deliveries_sent"`
	DeliveriesFailed                int64                                `json:"deliveries_failed"`
	StaleDeliveriesReset            int64                                `json:"stale_deliveries_reset"`
	HolderProjectsRefreshed         int64                                `json:"holder_projects_refreshed"`
	ExpiredUsersPaused              int64                                `json:"expired_users_paused"`
	UnacknowledgedDeliveries        []MarketDeliveryAcknowledgementError `json:"unacknowledged_deliveries,omitempty"`
	NotificationMaintenanceDeferred bool                                 `json:"notification_maintenance_deferred"`
	NextDeliveryAt                  *time.Time                           `json:"next_delivery_at,omitempty"`
}

type MarketDeliveryAcknowledgementError struct {
	Kind              string `json:"kind"`
	SourceID          int64  `json:"source_id"`
	LeaseToken        int64  `json:"lease_token"`
	AcceptedMessageID string `json:"accepted_message_id"`
	Cause             error  `json:"-"`
}

func (e *MarketDeliveryAcknowledgementError) Error() string {
	return fmt.Sprintf("market delivery acknowledgement failed: kind=%s source=%d token=%d accepted_message_id=%s: %v", e.Kind, e.SourceID, e.LeaseToken, e.AcceptedMessageID, e.Cause)
}
func (e *MarketDeliveryAcknowledgementError) Unwrap() error { return e.Cause }

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
			return CycleResult{LockSkipped: true}, nil
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
	if err := service.repository.ReconcileMarketNotificationSources(ctx); err != nil {
		if marketDeliveryMaintenanceDeferred(err) {
			result.NotificationMaintenanceDeferred = true
		} else {
			return result, err
		}
	}
	recovered, err := service.repository.RecoverStaleMarketDeliveries(ctx, 5*time.Minute)
	if err != nil {
		if marketDeliveryMaintenanceDeferred(err) {
			result.NotificationMaintenanceDeferred = true
		} else {
			return result, err
		}
	}
	result.StaleDeliveriesReset = recovered
	combinationInputs, err := service.repository.ProcessPendingWatchCombinationTriggers(
		ctx,
		service.settings.Batch,
	)
	if err != nil {
		if marketDeliveryMaintenanceDeferred(err) {
			result.NotificationMaintenanceDeferred = true
		} else {
			return result, err
		}
	}
	result.CombinationInputs += combinationInputs

	rules, err := service.repository.ListEnabledMarketRules(ctx, service.settings.Batch)
	if err != nil {
		return result, err
	}
	var evaluationErr error
	for _, rule := range rules {
		created, err := service.evaluateRule(ctx, rule)
		if err != nil {
			if errors.Is(err, store.ErrMarketRuleEvaluationInactive) || errors.Is(err, store.ErrMarketRuleEvaluationStale) {
				continue
			}
			if errors.Is(err, marketwindow.ErrUnsupported) {
				result.RulesUnsupportedParameters++
				continue
			}
			if errors.Is(err, ErrTradeRuleStateUninitialized) {
				if deferErr := service.deferIncompleteRule(ctx, rule.ID); deferErr != nil {
					return result, deferErr
				}
				result.TradeRulesAwaitingActivation++
				continue
			}
			if errors.Is(err, ErrTradeWindowContextIncomplete) {
				if deferErr := service.deferIncompleteRule(ctx, rule.ID); deferErr != nil {
					return result, deferErr
				}
				result.TradeRulesIncompleteWindow++
				continue
			}
			evaluationErr = errors.Join(evaluationErr, fmt.Errorf("evaluate market rule %d: %w", rule.ID, err))
			continue
		}
		result.RulesEvaluated++
		result.TriggersCreated += created
	}

	combinationInputs, err = service.repository.ProcessPendingWatchCombinationTriggers(
		ctx,
		service.settings.Batch,
	)
	if err != nil {
		if marketDeliveryMaintenanceDeferred(err) {
			result.NotificationMaintenanceDeferred = true
		} else {
			return result, err
		}
	}
	result.CombinationInputs += combinationInputs
	return service.drainDeliveries(ctx, result, evaluationErr)
}

// RunDeliveryCycle only drains already-created notifications. It deliberately
// does not evaluate rules, advance their progress, or run entitlement sweeps.
func (service *Service) RunDeliveryCycle(ctx context.Context) (CycleResult, error) {
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
			return CycleResult{LockSkipped: true}, nil
		}
		defer func() { _ = lock.Unlock(context.WithoutCancel(ctx)) }()
	}
	return service.drainDeliveries(ctx, CycleResult{}, nil)
}

func (service *Service) drainDeliveries(ctx context.Context, result CycleResult, evaluationErr error) (CycleResult, error) {
	if at := notificationdelivery.NextAdmissionAt(service.notifications); at.After(service.now()) {
		result.NextDeliveryAt = &at
		return result, evaluationErr
	}
	drainCtx, cancelDrain := context.WithTimeout(ctx, 5*time.Second)
	defer cancelDrain()
	for remaining := service.settings.Batch; remaining > 0 && drainCtx.Err() == nil; remaining-- {
		claims, err := service.repository.ClaimMarketDeliveries(drainCtx, 1)
		if err != nil {
			if marketDeliveryMaintenanceDeferred(err) || drainCtx.Err() != nil {
				result.NotificationMaintenanceDeferred = true
				break
			}
			return result, err
		}
		if len(claims) == 0 {
			break
		}
		claim := claims[0]
		result.DeliveriesClaimed++
		var retryAt time.Time
		sent, attempted, err := service.deliverWithAttemptNext(drainCtx, claim, &retryAt)
		if !retryAt.IsZero() && err == nil {
			result.NextDeliveryAt = &retryAt
			break
		}
		if attempted {
			result.DeliveriesAttempted++
		}
		if err != nil {
			var acknowledgement *MarketDeliveryAcknowledgementError
			if errors.As(err, &acknowledgement) {
				result.UnacknowledgedDeliveries = append(result.UnacknowledgedDeliveries, *acknowledgement)
			}
			result.DeliveriesFailed++
			continue
		}
		if sent {
			result.DeliveriesSent++
		}
	}
	return result, evaluationErr
}

func marketDeliveryMaintenanceDeferred(err error) bool {
	return errors.Is(err, store.ErrMarketDeliveryBudget) || errors.Is(err, context.DeadlineExceeded)
}

func (service *Service) marketRuleNotificationSourceAfter(ctx context.Context, ruleID int64) (*time.Time, error) {
	if repo, ok := service.repository.(interface {
		GetMarketRuleNotificationSourceAfter(context.Context, int64) (*time.Time, error)
	}); ok {
		return repo.GetMarketRuleNotificationSourceAfter(ctx, ruleID)
	}
	return nil, nil
}

func (service *Service) marketRuleNotificationEventDecisions(ctx context.Context, ruleID int64, events []store.MarketEvent) (map[int64]bool, error) {
	if repo, ok := service.repository.(interface {
		ListMarketRuleNotificationEventDecisions(context.Context, int64, []int64) (map[int64]bool, error)
	}); ok {
		ids := make([]int64, 0, len(events))
		for _, event := range events {
			ids = append(ids, event.ID)
		}
		return repo.ListMarketRuleNotificationEventDecisions(ctx, ruleID, ids)
	}
	return nil, nil
}

func (service *Service) evaluateRule(
	ctx context.Context,
	rule store.MarketRule,
) (int64, error) {
	if issues := marketwindow.RuntimeIssues(rule.RuleType, rule.WindowMinutes, rule.ThresholdValue); len(issues) > 0 {
		return 0, fmt.Errorf("%w: %s", marketwindow.ErrUnsupported, issues[0].Message)
	}
	sourceAfter, err := service.marketRuleNotificationSourceAfter(ctx, rule.ID)
	if err != nil {
		return 0, err
	}
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
	if service.settings.TradeFactsEnabled && isTradeFactsRule(rule.RuleType) {
		tradeRepository, ok := service.repository.(tradeFactsRepository)
		if !ok {
			return 0, errors.New("market trade facts repository is unavailable")
		}
		return service.evaluateTradeFactsRule(ctx, tradeRepository, rule, *project, sourceAfter)
	}
	if repository, ok := service.repository.(multiChainRuleRepository); ok {
		return service.evaluateMultiChainRule(ctx, repository, rule, *project, sourceAfter)
	}
	if !service.marketChainAllowed(project.ChainKey, project.ChainID) {
		return 0, nil
	}
	legacyPoolID := rule.MarketPoolID
	if legacyPoolID == nil {
		legacyPoolID = project.MainPoolID
	}
	snapshots, err := service.loadRuleSnapshots(ctx, rule, store.MarketRuleTarget{
		ChainID: project.ChainID, TokenAddress: project.TokenAddress, MarketPoolID: legacyPoolID,
	}, func(limit int) ([]store.MarketSnapshot, error) {
		// Legacy adapters cannot filter a pool in SQL. Retain their original
		// bounded read so another pool's latest row cannot hide this target.
		// Store uses the indexed multi-chain target path below.
		if legacyPoolID != nil {
			limit = 1000
		}
		return service.repository.ListMarketSnapshots(ctx, project.ID, rule.DeBoxUserID, limit)
	})
	if err != nil {
		return 0, err
	}
	var events []store.MarketEvent
	var decisions map[int64]bool
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
			store.MarketRuleCurrentEventLimit,
		)
		if err != nil {
			return 0, err
		}
		decisions, err = service.marketRuleNotificationEventDecisions(ctx, rule.ID, newEvents)
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
		Rule:                       rule,
		Project:                    *project,
		Snapshots:                  snapshots,
		Events:                     events,
		Holders:                    holders,
		Labels:                     labels,
		Now:                        service.now(),
		NotificationSourceAfter:    sourceAfter,
		NotificationEventDecisions: decisions,
	})
	if err != nil {
		return 0, err
	}

	var createdCount int64
	writeRule := rule
	if writeRule.MarketPoolID == nil {
		writeRule.MarketPoolID = project.MainPoolID
	}
	err = service.withEvaluationWrite(ctx, rule.ID, sourceAfter, func(writer *Service) error {
		created, err := writer.persistEvaluation(ctx, writeRule, *project, snapshots, evaluation)
		if err != nil {
			return err
		}
		createdCount = created
		_, err = writer.repository.UpdateMarketRuleState(ctx, rule.ID, evaluation.State, created > 0)
		return err
	})
	if err != nil {
		return 0, err
	}
	return createdCount, nil
}

// Real Store adapters use a short database transaction. In-memory repositories
// used by pure service tests retain their existing deterministic behavior.
func (service *Service) withEvaluationWrite(ctx context.Context, ruleID int64, expectedSourceAfter *time.Time, write func(*Service) error) error {
	if repo, ok := service.repository.(interface {
		WithMarketRuleEvaluationWrite(context.Context, int64, func(*store.Store) error) error
	}); ok {
		return repo.WithMarketRuleEvaluationWrite(ctx, ruleID, func(txStore *store.Store) error {
			actual, err := txStore.GetMarketRuleNotificationSourceAfter(ctx, ruleID)
			if err != nil {
				return err
			}
			if (actual == nil) != (expectedSourceAfter == nil) || (actual != nil && !actual.Equal(*expectedSourceAfter)) {
				return store.ErrMarketRuleEvaluationStale
			}
			writer := *service
			writer.repository = txStore
			return write(&writer)
		})
	}
	return write(service)
}

func (service *Service) evaluateMultiChainRule(
	ctx context.Context,
	repository multiChainRuleRepository,
	rule store.MarketRule,
	project store.MarketProject,
	sourceAfter *time.Time,
) (int64, error) {
	targets, err := repository.ListMarketRuleTargets(ctx, rule, project)
	if err != nil {
		return 0, err
	}
	var total int64
	projectLastTriggeredAt := rule.LastTriggeredAt
	for _, unresolved := range targets {
		if !service.marketChainAllowed(unresolved.ChainKey, unresolved.ChainID) {
			continue
		}
		target, exists, err := repository.LoadMarketRuleTargetState(
			ctx, rule.ID, unresolved,
		)
		if err != nil {
			return total, err
		}
		targetRule := rule
		targetRule.State = target.State
		targetRule.MarketPoolID = target.MarketPoolID
		if rule.CooldownScope == "chain" {
			targetRule.LastTriggeredAt = target.LastTriggeredAt
		} else {
			targetRule.LastTriggeredAt = projectLastTriggeredAt
		}
		targetProject := project
		targetProject.ChainKey = target.ChainKey
		targetProject.ChainID = target.ChainID
		targetProject.TokenAddress = target.TokenAddress
		targetProject.TokenName = target.TokenName
		targetProject.TokenSymbol = target.TokenSymbol
		targetProject.TokenDecimals = target.TokenDecimals
		targetProject.TotalSupplyRaw = target.TotalSupplyRaw
		targetProject.MainPoolID = target.MarketPoolID

		snapshots, err := service.loadRuleSnapshots(ctx, targetRule, target, func(limit int) ([]store.MarketSnapshot, error) {
			return repository.ListMarketSnapshotsForTarget(ctx, target, limit)
		})
		if err != nil {
			return total, err
		}
		state := ruleState{}
		if len(targetRule.State) > 0 {
			_ = json.Unmarshal(targetRule.State, &state)
		}
		var events []store.MarketEvent
		var decisions map[int64]bool
		if !isSnapshotRule(targetRule.RuleType) {
			if !exists {
				latest, err := repository.ListMarketEventsForTarget(
					ctx, target, 0, false, 1,
				)
				if err != nil {
					return total, err
				}
				if len(latest) > 0 {
					state.LastEventID = latest[0].ID
				}
				targetRule.State, _ = json.Marshal(state)
			} else {
				historical, err := repository.ListMarketEventsForTarget(
					ctx, target, 0, false, 500,
				)
				if err != nil {
					return total, err
				}
				current, err := repository.ListMarketEventsForTarget(
					ctx, target, state.LastEventID, true, store.MarketRuleCurrentEventLimit,
				)
				if err != nil {
					return total, err
				}
				decisions, err = service.marketRuleNotificationEventDecisions(ctx, rule.ID, current)
				if err != nil {
					return total, err
				}
				events = mergeMarketEvents(state.LastEventID, historical, current)
			}
		}
		var holders []store.MarketHolder
		var labels []store.MarketAddressLabel
		if isHolderRule(targetRule.RuleType) {
			holders, err = repository.ListMarketHoldersForTarget(ctx, target, 100)
			if err != nil {
				return total, err
			}
			labels, err = repository.ListMarketAddressLabelsForTarget(
				ctx, project.ID, rule.DeBoxUserID, target,
			)
			if err != nil {
				return total, err
			}
		}
		evaluation, err := Evaluate(EvaluationInput{
			Rule:                       targetRule,
			Project:                    targetProject,
			Snapshots:                  snapshots,
			Events:                     events,
			Holders:                    holders,
			Labels:                     labels,
			Now:                        service.now(),
			NotificationSourceAfter:    sourceAfter,
			NotificationEventDecisions: decisions,
		})
		if err != nil {
			return total, err
		}
		var created int64
		err = service.withEvaluationWrite(ctx, rule.ID, sourceAfter, func(writer *Service) error {
			count, err := writer.persistEvaluation(ctx, targetRule, targetProject, snapshots, evaluation)
			if err != nil {
				return err
			}
			created = count
			targetWriter, ok := writer.repository.(multiChainRuleRepository)
			if !ok {
				return errors.New("market target persistence repository is unavailable")
			}
			if err := targetWriter.UpdateMarketRuleTargetState(ctx, rule.ID, target, evaluation.State, count > 0); err != nil {
				return err
			}
			_, err = writer.repository.UpdateMarketRuleState(ctx, rule.ID, rule.State, count > 0)
			return err
		})
		if err != nil {
			return total, err
		}
		total += created
		if created > 0 && rule.CooldownScope == "project" {
			triggeredAt := service.now()
			projectLastTriggeredAt = &triggeredAt
		}
	}
	_, err = service.repository.UpdateMarketRuleState(
		ctx, rule.ID, rule.State, false,
	)
	return total, err
}

func (service *Service) persistEvaluation(
	ctx context.Context,
	rule store.MarketRule,
	project store.MarketProject,
	snapshots []store.MarketSnapshot,
	evaluation Evaluation,
) (int64, error) {
	var createdCount int64
	for _, trigger := range evaluation.Triggers {
		event := trigger.Event
		if event == nil {
			rawPayload, _ := json.Marshal(map[string]any{
				"rule_type": rule.RuleType,
				"current":   trigger.CurrentValue,
				"previous":  trigger.PreviousValue,
			})
			value, _, err := service.repository.CreateMarketEvent(
				ctx,
				store.CreateMarketEventParams{
					MarketPoolID: rule.MarketPoolID,
					ChainKey:     project.ChainKey, ChainID: project.ChainID,
					TokenAddress: project.TokenAddress,
					EventType:    trigger.EventType, EventKey: trigger.EventKey,
					PriceUSD: snapshotPrice(snapshots),
					Source:   "market_rule_engine", Confidence: "1.0000",
					Confirmed: true, OccurredAt: trigger.OccurredAt,
					RawPayload: rawPayload, Metadata: trigger.Details,
				},
			)
			if err != nil {
				return createdCount, err
			}
			event = &value
		}
		status := "pending"
		if rule.RuleScope == "combination" {
			status = "combined"
		} else if rule.DeliveryMode == "stage" {
			status = "staged"
		}
		ruleEvent, inserted, err := service.repository.CreateMarketRuleEventAndEnqueue(
			ctx,
			store.CreateMarketRuleEventParams{
				MarketRuleID: rule.ID, MarketEventID: event.ID,
				TriggerKey:    trigger.EventKey,
				PreviousValue: trigger.PreviousValue,
				CurrentValue:  trigger.CurrentValue,
				Note:          trigger.Note, Details: trigger.Details,
				NotificationStatus: status,
			},
		)
		if err != nil {
			return createdCount, err
		}
		if inserted && ruleEvent.NotificationStatus == "skipped" && ruleEvent.NotificationError == "source window expired or precedes recorded recovery cutoff" {
			// The stage-age boundary may pass between pure evaluation and the
			// short write transaction. Re-evaluate the entire batch rather than
			// commit a cursor after its discarded event consumed private cooldown.
			return 0, store.ErrMarketRuleEvaluationStale
		}
		if inserted && ruleEvent.NotificationStatus != "skipped" {
			createdCount++
		}
	}
	if err := service.persistSuppressedTriggers(ctx, rule, evaluation.Suppressed); err != nil {
		return createdCount, err
	}
	return createdCount, nil
}

func (service *Service) persistSuppressedTriggers(
	ctx context.Context,
	rule store.MarketRule,
	suppressed []SuppressedTrigger,
) error {
	for _, item := range suppressed {
		if item.Event == nil {
			continue
		}
		_, _, err := service.repository.CreateMarketRuleEvent(
			ctx,
			store.CreateMarketRuleEventParams{
				MarketRuleID:       rule.ID,
				MarketEventID:      item.Event.ID,
				TriggerKey:         item.EventKey,
				PreviousValue:      item.PreviousValue,
				CurrentValue:       item.CurrentValue,
				Note:               item.Note,
				Details:            item.Details,
				NotificationStatus: "skipped",
				NotificationError:  item.Reason,
			},
		)
		if err != nil {
			return err
		}
	}
	return nil
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

func (service *Service) deliver(ctx context.Context, claim store.MarketDeliveryClaim) (bool, error) {
	sent, _, err := service.deliverWithAttempt(ctx, claim)
	return sent, err
}

func (service *Service) deliverWithAttempt(ctx context.Context, claim store.MarketDeliveryClaim) (bool, bool, error) {
	return service.deliverWithAttemptNext(ctx, claim, nil)
}

func (service *Service) deliverWithAttemptNext(ctx context.Context, claim store.MarketDeliveryClaim, next *time.Time) (bool, bool, error) {
	failBeforeSend := func(err error) error {
		kind := notificationdelivery.Retryable
		if errors.Is(err, notificationdelivery.ErrCapacity) {
			kind = notificationdelivery.Rejected
		}
		failure := &notificationdelivery.Failure{Kind: kind, Code: "market_delivery_preparation_failed", Cause: err}
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return errors.Join(err, service.repository.CompleteMarketDeliveryResult(saveCtx, claim, nil, failure))
	}
	frozen, err := service.repository.GetMarketDeliveryPayload(ctx, claim)
	if err != nil {
		if errors.Is(err, store.ErrMarketDeliveryLeaseLost) {
			return false, false, err
		}
		return false, false, failBeforeSend(err)
	}
	if frozen == nil {
		delivery, err := service.repository.LoadMarketDelivery(ctx, claim)
		if err != nil {
			return false, false, failBeforeSend(err)
		}
		text := marketNotificationTextAt(delivery, service.now())
		snapshot, err := service.createMarketNotificationSnapshot(ctx, delivery, text)
		if err != nil {
			return false, false, failBeforeSend(err)
		}
		payload := store.MarketDeliveryPayload{SnapshotID: snapshot.ID, SnapshotPublicID: snapshot.PublicID,
			Payload: notificationdelivery.Payload{ChatID: snapshot.NotificationChatID, ChatType: snapshot.NotificationChatType, Text: snapshot.NotificationText}}
		actionURL := notificationdetail.NotificationURL(service.publicAppURL, snapshot.PublicID)
		if _, ok := service.notifications.(ActionNotificationService); ok && actionURL != "" {
			payload.Payload.ActionURL = actionURL
			english := notificationLanguage(snapshot.NotificationLanguage) == "en"
			switch claim.Kind {
			case "stage":
				payload.Payload.ActionText = "查看全部事件"
				if english {
					payload.Payload.ActionText = "View all events"
				}
			case "combination":
				payload.Payload.ActionText = "查看完整分析"
				if english {
					payload.Payload.ActionText = "View full analysis"
				}
			default:
				payload.Payload.ActionText = "查看详情"
				if english {
					payload.Payload.ActionText = "View details"
				}
			}
		}
		value, err := service.repository.PrepareMarketDelivery(ctx, claim, payload)
		if err != nil {
			return false, false, failBeforeSend(err)
		}
		frozen = &value
	}
	if err := ctx.Err(); err != nil {
		return false, false, failBeforeSend(err)
	}
	sendCtx, reservation, err := notificationdelivery.Acquire(ctx, service.notifications)
	if err != nil {
		if retryAt, waiting := notificationdelivery.AdmissionRetryAt(err); waiting {
			if next != nil {
				*next = retryAt
			}
			return false, false, service.deferMarketAdmission(ctx, claim, retryAt, false)
		}
		return false, false, failBeforeSend(err)
	}
	if reservation != nil {
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			_ = reservation.Release(cleanupCtx)
		}()
	}
	if err := service.repository.BeginMarketDeliverySend(ctx, claim); err != nil {
		if errors.Is(err, store.ErrMarketDeliveryLeaseLost) {
			return false, false, err
		}
		return false, false, failBeforeSend(err)
	}
	messageID, sendErr := notificationdelivery.Send(sendCtx, service.notifications, frozen.Payload)
	if retryAt, waiting := notificationdelivery.AdmissionRetryAt(sendErr); waiting {
		if next != nil {
			*next = retryAt
		}
		// The innermost transport gate proves that Do was never called. Only
		// this typed result may refund the just-started durable attempt.
		return false, false, service.deferMarketAdmission(ctx, claim, retryAt, true)
	}
	attempted := true
	var failure *notificationdelivery.Failure
	if sendErr != nil {
		value := notificationdelivery.Classify(sendErr)
		failure = &value
		if value.Code == "cancelled_before_send" || value.Code == "invalid_frozen_payload" {
			attempted = false
		}
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := service.repository.CompleteMarketDeliveryResult(saveCtx, claim, &messageID, failure); err != nil {
		if sendErr == nil && strings.TrimSpace(messageID) != "" {
			return false, attempted, &MarketDeliveryAcknowledgementError{Kind: claim.Kind, SourceID: claim.ID, LeaseToken: claim.LeaseToken, AcceptedMessageID: messageID, Cause: err}
		}
		return false, attempted, errors.Join(sendErr, err)
	}
	if sendErr != nil {
		return false, attempted, sendErr
	}
	return true, attempted, nil
}

func (service *Service) deferMarketAdmission(ctx context.Context, claim store.MarketDeliveryClaim, retryAt time.Time, sendStarted bool) error {
	repository, ok := service.repository.(interface {
		DeferMarketDeliveryAdmission(context.Context, store.MarketDeliveryClaim, time.Time, bool) error
	})
	if !ok {
		return errors.New("market admission deferral repository is unavailable")
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	return repository.DeferMarketDeliveryAdmission(cleanupCtx, claim, retryAt, sendStarted)
}

func (service *Service) sendMarketNotification(
	delivery store.MarketNotificationDelivery,
	text string,
	notificationID string,
) (string, error) {
	actionSender, supportsAction := service.notifications.(ActionNotificationService)
	actionURL := notificationdetail.NotificationURL(service.publicAppURL, notificationID)
	if (delivery.Kind != "realtime" && delivery.Kind != "stage" && delivery.Kind != "combination") ||
		!supportsAction || actionURL == "" {
		return service.notifications.SendNotification(
			delivery.NotificationChatID,
			delivery.NotificationChatType,
			text,
		)
	}
	actionText := "查看详情"
	if notificationLanguage(delivery.NotificationLanguage) == "en" {
		actionText = "View details"
	}
	if delivery.Kind == "stage" {
		actionText = "查看全部事件"
		if notificationLanguage(delivery.NotificationLanguage) == "en" {
			actionText = "View all events"
		}
	} else if delivery.Kind == "combination" {
		actionText = "查看完整分析"
		if notificationLanguage(delivery.NotificationLanguage) == "en" {
			actionText = "View full analysis"
		}
	}
	return actionSender.SendNotificationWithAction(
		delivery.NotificationChatID,
		delivery.NotificationChatType,
		text,
		actionText,
		actionURL,
	)
}

type Runner struct {
	service                        *Service
	nextNormalDue                  time.Time
	lastActivationWarning          time.Time
	lastActivationCount            int64
	lastWindowWarning              time.Time
	lastWindowCount                int64
	lastDeliveryMaintenanceWarning time.Time
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
	run := func() time.Duration {
		now := time.Now()
		normal := runner.nextNormalDue.IsZero() || !now.Before(runner.nextNormalDue)
		var result CycleResult
		var err error
		if normal {
			runner.nextNormalDue = now.Add(runner.service.settings.Interval)
			result, err = runner.service.RunCycle(ctx)
		} else {
			result, err = runner.service.RunDeliveryCycle(ctx)
		}
		for _, accepted := range result.UnacknowledgedDeliveries {
			logger.Warn("market notification accepted but database acknowledgement failed", "kind", accepted.Kind, "source_id", accepted.SourceID, "claim_token", accepted.LeaseToken, "accepted_message_id", accepted.AcceptedMessageID)
		}
		if result.NotificationMaintenanceDeferred && (runner.lastDeliveryMaintenanceWarning.IsZero() || time.Since(runner.lastDeliveryMaintenanceWarning) >= time.Minute) {
			logger.Warn("market notification maintenance deferred after database budget; remaining sources will resume next cycle")
			runner.lastDeliveryMaintenanceWarning = time.Now()
		}
		if err != nil {
			logger.Error("market rule cycle failed", "error", err)
		}
		runner.reportTradeActivation(logger, result, time.Now())
		runner.reportTradeWindow(logger, result, time.Now())
		if result.TriggersCreated > 0 || result.DeliveriesAttempted > 0 {
			logger.Info(
				"market rule cycle completed",
				"rules", result.RulesEvaluated,
				"triggers", result.TriggersCreated,
				"sent", result.DeliveriesSent,
				"failed", result.DeliveriesFailed,
			)
		}
		return runner.nextDelay(ctx, result, err, time.Now())
	}
	for {
		if ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(run())
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (runner *Runner) nextDelay(ctx context.Context, result CycleResult, cycleErr error, now time.Time) time.Duration {
	at := runner.nextNormalDue
	if cycleErr == nil && !result.LockSkipped {
		if repository, ok := runner.service.repository.(interface {
			NextMarketDeliveryAt(context.Context) (*time.Time, error)
		}); ok {
			next, err := repository.NextMarketDeliveryAt(ctx)
			if err == nil && next != nil {
				candidate := *next
				if result.NextDeliveryAt != nil && result.NextDeliveryAt.After(candidate) {
					candidate = *result.NextDeliveryAt
				}
				if hint := notificationdelivery.NextAdmissionAt(runner.service.notifications); hint.After(candidate) {
					candidate = hint
				}
				if !candidate.After(now) && (result.NotificationMaintenanceDeferred || result.DeliveriesSent == 0 && result.DeliveriesFailed > 0) {
					candidate = now.Add(min(5*time.Second, runner.service.settings.Interval))
				}
				if candidate.Before(at) {
					at = candidate
				}
			}
		}
	}
	minimum := min(time.Second, runner.service.settings.Interval)
	return max(minimum, at.Sub(now))
}

func (runner *Runner) reportTradeActivation(logger *slog.Logger, result CycleResult, now time.Time) {
	if result.LockSkipped {
		return
	}
	count := result.TradeRulesAwaitingActivation
	changed := count != runner.lastActivationCount
	if count > 0 && (changed || runner.lastActivationWarning.IsZero() || now.Sub(runner.lastActivationWarning) >= time.Minute) {
		logger.Warn("market trade rules await explicit activation", "awaiting_activation", count)
		runner.lastActivationWarning = now
	} else if changed && count == 0 {
		logger.Info("market trade activation backlog cleared")
	}
	runner.lastActivationCount = count
}

func (runner *Runner) reportTradeWindow(logger *slog.Logger, result CycleResult, now time.Time) {
	if result.LockSkipped {
		return
	}
	count := result.TradeRulesIncompleteWindow
	changed := count != runner.lastWindowCount
	if count > 0 && (changed || runner.lastWindowWarning.IsZero() || now.Sub(runner.lastWindowWarning) >= time.Minute) {
		logger.Warn("market trade rules require complete window context", "incomplete_window", count)
		runner.lastWindowWarning = now
	} else if changed && count == 0 {
		logger.Info("market trade window context backlog cleared")
	}
	runner.lastWindowCount = count
}
