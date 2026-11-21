package marketrules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

var ErrTradeRuleStateUninitialized = errors.New("market trade rule lane requires an explicit activation fence")
var ErrTradeWindowContextIncomplete = errors.New("market trade window context is incomplete")

type tradeFactsRepository interface {
	multiChainRuleRepository
	ListMarketTradeEventsForTarget(context.Context, store.MarketRuleTarget, int64, bool, int) ([]store.MarketEvent, error)
	InitializeTradeRuleState(context.Context, int64, store.MarketRuleTarget, json.RawMessage) (bool, error)
	LoadMarketTradeEvaluationBatch(context.Context, store.MarketRule, store.MarketRuleTarget, int64, time.Time) (store.MarketTradeEvaluationBatch, error)
	LoadMarketTradeEvaluationBatchAfter(context.Context, store.MarketRule, store.MarketRuleTarget, int64, time.Time, *time.Time) (store.MarketTradeEvaluationBatch, error)
}

type tradeReadinessRepository interface {
	EnsureMarketTradeReadiness(context.Context, store.MarketRule, store.MarketProject, store.MarketRuleTarget, *time.Time) (store.MarketRuleTarget, store.MarketTradeReadiness, error)
	LoadMarketTradeReadinessBatch(context.Context, store.MarketRule, store.MarketRuleTarget, int64, time.Time, *time.Time, store.MarketTradeReadiness) (store.MarketTradeEvaluationBatch, error)
	CommitMarketTradeReadiness(context.Context, store.MarketRule, store.MarketProject, store.MarketRuleTarget, store.MarketTradeReadiness, json.RawMessage, []store.MarketTradeReadinessDecision, string, string) error
	MarkMarketTradeReadinessBlocked(context.Context, store.MarketTradeReadiness, string) error
}

type tradeLaneState struct {
	ActivationFence  *int64 `json:"activation_fence"`
	LastTradeReadyID int64  `json:"last_trade_ready_id"`
}

func isTradeFactsRule(ruleType string) bool {
	switch ruleType {
	case plans.MarketLargeBuy, plans.MarketLargeSell,
		plans.MarketConsecutiveLargeBuy, plans.MarketConsecutiveLargeSell:
		return true
	default:
		return false
	}
}

// MergeTradeRuleTargets preserves the existing resolver's selected-pool scope,
// but evaluates one logical trade per deployment. It does not merge old states.
func MergeTradeRuleTargets(targets []store.MarketRuleTarget) []store.MarketRuleTarget {
	var result []store.MarketRuleTarget
	indices := make(map[string]int)
	for _, target := range targets {
		if target.MarketPoolID == nil {
			continue
		}
		poolID := *target.MarketPoolID
		key := strings.Split(target.TargetKey, "/pool:")[0] + "/trades:v2"
		index, exists := indices[key]
		if !exists {
			index = len(result)
			indices[key] = index
			target.TargetKey = key
			target.MarketPoolID, target.MarketProjectPoolID = nil, nil
			target.State = nil
			target.TradePoolIDs = nil
			target.LastEvaluatedAt = nil
			target.LastTriggeredAt = nil
			result = append(result, target)
		}
		result[index].TradePoolIDs = append(result[index].TradePoolIDs, poolID)
	}
	for index := range result {
		pools := result[index].TradePoolIDs
		sort.Slice(pools, func(i, j int) bool { return pools[i] < pools[j] })
		unique := pools[:0]
		for _, pool := range pools {
			if len(unique) == 0 || unique[len(unique)-1] != pool {
				unique = append(unique, pool)
			}
		}
		result[index].TradePoolIDs = unique
	}
	return result
}

// InitializeTradeRuleState is an explicit administrative/local activation step.
// Runtime uses lifecycle provenance separately; it never guesses MAX(ready_id).
func (service *Service) InitializeTradeRuleState(ctx context.Context, ruleID int64, target store.MarketRuleTarget, fence int64) (bool, error) {
	repository, ok := service.repository.(tradeFactsRepository)
	if !ok || ruleID < 1 || fence < 0 || len(target.TradePoolIDs) == 0 || !strings.HasSuffix(target.TargetKey, "/trades:v2") {
		return false, errors.New("invalid explicit market trade activation")
	}
	state, err := json.Marshal(tradeLaneState{ActivationFence: &fence, LastTradeReadyID: fence})
	if err != nil {
		return false, err
	}
	return repository.InitializeTradeRuleState(ctx, ruleID, target, state)
}

func (service *Service) evaluateTradeFactsRule(ctx context.Context, repository tradeFactsRepository, rule store.MarketRule, project store.MarketProject, sourceAfter *time.Time) (int64, error) {
	legacyTargets, err := repository.ListMarketRuleTargets(ctx, rule, project)
	if err != nil {
		return 0, err
	}
	var total int64
	evaluationAt := service.now()
	projectLastTriggeredAt := rule.LastTriggeredAt
	// Validate every deployment before doing any work for this rule, so a
	// partially initialized rule cannot consume one chain before another fails.
	var activeTargets []store.MarketRuleTarget
	readinessRepository, hasReadiness := service.repository.(tradeReadinessRepository)
	readinessByTarget := make(map[string]store.MarketTradeReadiness)
	for _, unresolved := range MergeTradeRuleTargets(legacyTargets) {
		if !service.marketChainAllowed(unresolved.ChainKey, unresolved.ChainID) {
			continue
		}
		var target store.MarketRuleTarget
		var exists bool
		if hasReadiness {
			var readiness store.MarketTradeReadiness
			target, readiness, err = readinessRepository.EnsureMarketTradeReadiness(ctx, rule, project, unresolved, sourceAfter)
			exists = err == nil
			if err == nil {
				readinessByTarget[target.TargetKey] = readiness
			}
		} else {
			target, exists, err = repository.LoadMarketRuleTargetState(ctx, rule.ID, unresolved)
		}
		if err != nil {
			return total, err
		}
		state := tradeLaneState{}
		if !exists || json.Unmarshal(target.State, &state) != nil || state.ActivationFence == nil || state.LastTradeReadyID < *state.ActivationFence || *state.ActivationFence < 0 {
			return total, fmt.Errorf("%w: rule %d, target %s", ErrTradeRuleStateUninitialized, rule.ID, target.TargetKey)
		}
		activeTargets = append(activeTargets, target)
	}
	type targetBatch struct {
		target             store.MarketRuleTarget
		events             []store.MarketEvent
		counts             map[int64]int64
		decisions          map[int64]bool
		warmup             map[int64]string
		sourceAfter        *time.Time
		readiness          store.MarketTradeReadiness
		readinessDecisions []store.MarketTradeReadinessDecision
		status             string
		reason             string
	}
	var batches []targetBatch
	for _, target := range activeTargets {
		state := tradeLaneState{}
		_ = json.Unmarshal(target.State, &state)
		readiness := readinessByTarget[target.TargetKey]
		targetSourceAfter := sourceAfter
		if hasReadiness && (targetSourceAfter == nil || (readiness.EligibilityAfter != nil && readiness.EligibilityAfter.After(*targetSourceAfter))) {
			targetSourceAfter = readiness.EligibilityAfter
		}
		var events []store.MarketEvent
		var counts map[int64]int64
		var decisions map[int64]bool
		warmup := make(map[int64]string)
		blocked := func(cause error) (int64, error) {
			if hasReadiness {
				_ = readinessRepository.MarkMarketTradeReadinessBlocked(ctx, readiness, cause.Error())
			}
			return total, cause
		}
		if isConsecutiveTradeFactsRule(rule.RuleType) {
			var batch store.MarketTradeEvaluationBatch
			if hasReadiness {
				batch, err = readinessRepository.LoadMarketTradeReadinessBatch(ctx, rule, target, state.LastTradeReadyID, evaluationAt, targetSourceAfter, readiness)
			} else {
				batch, err = repository.LoadMarketTradeEvaluationBatchAfter(ctx, rule, target, state.LastTradeReadyID, evaluationAt, targetSourceAfter)
			}
			if err != nil {
				if errors.Is(err, store.ErrMarketTradeWindowUnavailable) {
					return blocked(fmt.Errorf("%w: %v", ErrTradeWindowContextIncomplete, err))
				}
				return blocked(err)
			}
			events, decisions = batch.Current, batch.NotificationEventDecisions
			if hasReadiness {
				for id, reason := range batch.HistoryUnavailable {
					warmup[id] = reason
				}
			}
			countEvents := notificationCurrentRuleEvents(rule, events, targetSourceAfter, evaluationAt, decisions)
			if hasReadiness {
				var fresh []store.MarketEvent
				for _, event := range countEvents {
					if warmup[event.ID] == "recorded_warmup" {
						continue
					}
					if notificationEventFamily(rule, event) {
						if reason := tradeProjectionUnavailable(rule, event, evaluationAt); reason != "" {
							return blocked(fmt.Errorf("%w: current_%s", ErrTradeWindowContextIncomplete, reason))
						}
					}
					fresh = append(fresh, event)
				}
				var historical map[int64]string
				counts, historical, err = completeTradeWindowCountsReadiness(rule, fresh, batch.Candidates, state.LastTradeReadyID)
				for id, reason := range historical {
					warmup[id] = reason
				}
			} else {
				counts, err = completeTradeWindowCounts(rule, countEvents, batch.Candidates, state.LastTradeReadyID)
			}
			if err != nil {
				return blocked(err)
			}
		} else {
			events, err = repository.ListMarketTradeEventsForTarget(ctx, target, state.LastTradeReadyID, true, store.MarketRuleCurrentEventLimit)
			if err != nil {
				return blocked(err)
			}
			decisions, err = service.marketRuleNotificationEventDecisions(ctx, rule.ID, events)
			if err != nil {
				return blocked(err)
			}
			if hasReadiness {
				for _, event := range notificationCurrentRuleEvents(rule, events, targetSourceAfter, evaluationAt, decisions) {
					if notificationEventFamily(rule, event) {
						if reason := tradeProjectionUnavailable(rule, event, evaluationAt); reason != "" {
							return blocked(fmt.Errorf("%w: current_%s", ErrTradeWindowContextIncomplete, reason))
						}
					}
				}
			}
		}
		status, reason := readiness.Status, readiness.Reason
		var readinessDecisions []store.MarketTradeReadinessDecision
		current := notificationCurrentRuleEvents(rule, events, targetSourceAfter, evaluationAt, decisions)
		sort.Slice(current, func(i, j int) bool { return current[i].OccurredAt.Before(current[j].OccurredAt) })
		for _, event := range current {
			if !notificationEventFamily(rule, event) || warmup[event.ID] == "recorded_warmup" {
				continue
			}
			readinessDecisions = append(readinessDecisions, store.MarketTradeReadinessDecision{OccurredAt: event.OccurredAt, WarmupReason: warmup[event.ID]})
			if readiness.LatestEventAt == nil || !event.OccurredAt.Before(*readiness.LatestEventAt) {
				status, reason = "ready", ""
				if warmup[event.ID] != "" {
					status, reason = "warming", warmup[event.ID]
				}
			}
		}
		batches = append(batches, targetBatch{target: target, events: events, counts: counts, decisions: decisions, warmup: warmup, sourceAfter: targetSourceAfter,
			readiness: readiness, readinessDecisions: readinessDecisions, status: status, reason: reason})
	}
	for _, batch := range batches {
		target, events := batch.target, batch.events
		targetRule := rule
		targetRule.State = target.State
		targetRule.MarketPoolID = nil
		if rule.CooldownScope == "chain" {
			targetRule.LastTriggeredAt = target.LastTriggeredAt
		} else {
			targetRule.LastTriggeredAt = projectLastTriggeredAt
		}
		targetProject := project
		targetProject.ChainKey, targetProject.ChainID = target.ChainKey, target.ChainID
		targetProject.TokenAddress, targetProject.TokenName = target.TokenAddress, target.TokenName
		targetProject.TokenSymbol, targetProject.TokenDecimals = target.TokenSymbol, target.TokenDecimals
		targetProject.TotalSupplyRaw = target.TotalSupplyRaw
		targetProject.MainPoolID = nil
		evaluation, err := evaluateTradeFactsWithCountsReadiness(targetRule, targetProject, events, batch.counts, evaluationAt, batch.sourceAfter, batch.decisions, batch.warmup)
		if err != nil {
			return total, err
		}
		var created int64
		err = service.withEvaluationWrite(ctx, rule.ID, sourceAfter, func(writer *Service) error {
			if hasReadiness {
				readinessWriter, ok := writer.repository.(tradeReadinessRepository)
				if !ok {
					return errors.New("market trade readiness persistence unavailable")
				}
				if err := readinessWriter.CommitMarketTradeReadiness(ctx, rule, project, target, batch.readiness, target.State, batch.readinessDecisions, batch.status, batch.reason); err != nil {
					return err
				}
			}
			count, err := writer.persistEvaluation(ctx, targetRule, targetProject, nil, evaluation)
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
			now := service.now()
			projectLastTriggeredAt = &now
		}
	}
	_, err = service.repository.UpdateMarketRuleState(ctx, rule.ID, rule.State, false)
	return total, err
}

func isConsecutiveTradeFactsRule(ruleType string) bool {
	return ruleType == plans.MarketConsecutiveLargeBuy || ruleType == plans.MarketConsecutiveLargeSell
}
