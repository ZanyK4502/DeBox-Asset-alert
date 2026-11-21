package marketrules

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type tradeProjection struct {
	SchemaVersion     int     `json:"schema_version"`
	ReadyID           int64   `json:"ready_id"`
	PoolIDs           []int64 `json:"pool_ids"`
	SelectedPoolIDs   []int64 `json:"selected_pool_ids"`
	SourceLegIDs      []int64 `json:"source_leg_ids"`
	LiquidityUSD      *string `json:"liquidity_usd"`
	LiquidityComplete bool    `json:"liquidity_complete"`
	SnapshotIDs       []int64 `json:"snapshot_ids"`
}

func tradeProjectionFromEvent(event store.MarketEvent) (tradeProjection, bool) {
	var envelope struct {
		Projection json.RawMessage `json:"trade_projection"`
	}
	if json.Unmarshal(event.Metadata, &envelope) != nil || len(envelope.Projection) == 0 || string(envelope.Projection) == "null" {
		return tradeProjection{}, false
	}
	var projection tradeProjection
	if json.Unmarshal(envelope.Projection, &projection) != nil {
		return tradeProjection{}, true
	}
	return projection, true
}

func tradeProjectionUnavailable(rule store.MarketRule, event store.MarketEvent, now time.Time) string {
	if reason := metadataString(event.Metadata, "trade_projection_unavailable"); reason != "" {
		return reason
	}
	projection, present := tradeProjectionFromEvent(event)
	if !present || projection.SchemaVersion != 1 || projection.ReadyID != event.TradeReadyID ||
		len(projection.SelectedPoolIDs) == 0 || len(projection.PoolIDs) == 0 || len(projection.SourceLegIDs) == 0 {
		return "trade_projection_invalid"
	}
	selected := make(map[int64]struct{}, len(projection.SelectedPoolIDs))
	for _, id := range projection.SelectedPoolIDs {
		selected[id] = struct{}{}
	}
	for _, id := range projection.PoolIDs {
		if _, ok := selected[id]; !ok {
			return "trade_projection_scope_invalid"
		}
	}
	switch rule.ThresholdUnit {
	case "token":
		if _, valid := pointerRat(event.TokenAmount); !valid {
			return "trade_token_amount_unavailable"
		}
	case "percent":
		if _, valid := pointerRat(event.USDValue); !valid {
			return "trade_usd_value_unavailable"
		}
		liquidity, valid := pointerRat(projection.LiquidityUSD)
		if !projection.LiquidityComplete || !valid || liquidity.Sign() <= 0 || len(projection.SnapshotIDs) != len(selected) {
			return "trade_liquidity_unavailable"
		}
	default:
		if _, valid := pointerRat(event.USDValue); !valid {
			return "trade_usd_value_unavailable"
		}
	}
	if ruleNeedsUSDValuation(rule) {
		return eventQuoteUnavailable(rule, nil, event, now)
	}
	return ""
}

// evaluateTradeFacts reuses the established threshold, cooldown and cumulative
// window logic with a private ready-sequence view. It restores the real parent
// IDs before persistence and never stores that temporary sequence as LastEventID.
func evaluateTradeFacts(rule store.MarketRule, project store.MarketProject, events []store.MarketEvent, now time.Time) (Evaluation, error) {
	return evaluateTradeFactsWithCounts(rule, project, events, nil, now)
}

func evaluateTradeFactsWithCounts(rule store.MarketRule, project store.MarketProject, events []store.MarketEvent, counts map[int64]int64, now time.Time) (Evaluation, error) {
	return evaluateTradeFactsWithCountsAfter(rule, project, events, counts, now, nil)
}

func evaluateTradeFactsWithCountsAfter(rule store.MarketRule, project store.MarketProject, events []store.MarketEvent, counts map[int64]int64, now time.Time, sourceAfter *time.Time, existing ...map[int64]bool) (Evaluation, error) {
	var decisions map[int64]bool
	if len(existing) > 0 {
		decisions = existing[0]
	}
	return evaluateTradeFactsWithCountsReadiness(rule, project, events, counts, now, sourceAfter, decisions, nil)
}

func evaluateTradeFactsWithCountsReadiness(rule store.MarketRule, project store.MarketProject, events []store.MarketEvent, counts map[int64]int64, now time.Time, sourceAfter *time.Time, decisions map[int64]bool, warmup map[int64]string) (Evaluation, error) {
	if !isTradeFactsRule(rule.RuleType) {
		return Evaluation{}, fmt.Errorf("rule %s is not a DEX trade-facts rule", rule.RuleType)
	}
	state := tradeLaneState{}
	if json.Unmarshal(rule.State, &state) != nil || state.ActivationFence == nil ||
		*state.ActivationFence < 0 || state.LastTradeReadyID < *state.ActivationFence {
		return Evaluation{}, ErrTradeRuleStateUninitialized
	}
	if counts == nil {
		if err := validateTradeWindowValuationsAfterDecisions(rule, nil, events, state.LastTradeReadyID, true, now, sourceAfter, decisions); err != nil {
			return Evaluation{}, err
		}
	}
	ordered := append([]store.MarketEvent(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].TradeReadyID < ordered[j].TradeReadyID })
	progress := state.LastTradeReadyID
	parents := make(map[int64]store.MarketEvent, len(ordered))
	seenParents := make(map[int64]struct{}, len(ordered))
	proxies := make([]store.MarketEvent, 0, len(ordered))
	proxyDecisions := make(map[int64]bool, len(decisions))
	var unavailable []SuppressedTrigger
	for _, event := range ordered {
		if event.ID < 1 || event.TradeReadyID < 1 || event.ChainID != project.ChainID ||
			!strings.EqualFold(event.TokenAddress, project.TokenAddress) {
			return Evaluation{}, fmt.Errorf("invalid parent scope in market trade-facts reader")
		}
		isNew := event.TradeReadyID > state.LastTradeReadyID
		if event.Reorged == 0 && event.Confirmed != 1 {
			if isNew {
				break
			}
			continue
		}
		if isNew && event.TradeReadyID > progress {
			progress = event.TradeReadyID
		}
		if event.Reorged != 0 {
			continue
		}
		if _, duplicate := seenParents[event.ID]; duplicate {
			continue
		}
		seenParents[event.ID] = struct{}{}
		if isNew && decisions[event.ID] && tradeProjectionUnavailable(rule, event, now) != "" {
			// The persisted decision is already authoritative. Its ready replay
			// has no new notification or window obligation of its own.
			continue
		}
		if isNew && (notificationSourceOld(event, sourceAfter) || notificationStageSourceExpired(rule, event, now)) {
			// Valid old facts can still contribute to a fresh cumulative window.
			// They are not new notification candidates or cooldown consumers.
			if !isConsecutiveTradeFactsRule(rule.RuleType) || tradeProjectionUnavailable(rule, event, now) != "" {
				if notificationEventFamily(rule, event) {
					decision := notificationBoundarySuppression(event)
					if !notificationSourceOld(event, sourceAfter) {
						decision = notificationStageExpiredSuppression(event)
					}
					unavailable = append(unavailable, decision)
				}
				continue
			}
		}
		if isNew && warmup[event.ID] != "" {
			copy := event
			unavailable = append(unavailable, SuppressedTrigger{Trigger: Trigger{Event: &copy, EventType: event.EventType, EventKey: event.EventKey,
				Note: "历史窗口数据不足，正在预热；本段不补发提醒。", Details: event.Metadata, OccurredAt: event.OccurredAt}, Reason: "trade_warmup_" + warmup[event.ID]})
			continue
		}
		reason := tradeProjectionUnavailable(rule, event, now)
		if event.EventType != marketparse.EventBuy && event.EventType != marketparse.EventSell {
			reason = "trade_parent_type_invalid"
		}
		if reason != "" {
			if isNew {
				copy := event
				unavailable = append(unavailable, SuppressedTrigger{
					Trigger: Trigger{Event: &copy, EventType: event.EventType, EventKey: event.EventKey,
						Note: "所选交易池的数据不完整，本次未评估。", Details: event.Metadata, OccurredAt: event.OccurredAt},
					Reason: reason,
				})
			}
			continue
		}
		parents[event.TradeReadyID] = event
		if terminal, present := decisions[event.ID]; present {
			proxyDecisions[event.TradeReadyID] = terminal
		}
		proxy := event
		proxy.ID = event.TradeReadyID
		proxies = append(proxies, proxy)
	}
	privateRule := rule
	privateRule.MarketPoolID = nil
	privateRule.State, _ = json.Marshal(ruleState{LastEventID: state.LastTradeReadyID})
	privateProject := project
	privateProject.MainPoolID = nil
	evaluation, err := Evaluate(EvaluationInput{Rule: privateRule, Project: privateProject, Events: proxies, Now: now, ConsecutiveCounts: counts, NotificationSourceAfter: sourceAfter, NotificationEventDecisions: proxyDecisions})
	if err != nil {
		return Evaluation{}, err
	}
	restore := func(trigger *Trigger) {
		if trigger.Event != nil {
			parent := parents[trigger.Event.ID]
			trigger.Event = &parent
		}
	}
	for index := range evaluation.Triggers {
		restore(&evaluation.Triggers[index])
	}
	for index := range evaluation.Suppressed {
		restore(&evaluation.Suppressed[index].Trigger)
	}
	evaluation.Suppressed = append(evaluation.Suppressed, unavailable...)
	state.LastTradeReadyID = progress
	evaluation.State, err = json.Marshal(state)
	return evaluation, err
}
