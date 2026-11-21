package marketrules

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketwindow"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type tradeWindowFenwick []int64

func (tree tradeWindowFenwick) add(index int) {
	for index++; index < len(tree); index += index & -index {
		tree[index]++
	}
}

func validateCompleteTradeCountKeys(rule store.MarketRule, project store.MarketProject, snapshots []store.MarketSnapshot, events []store.MarketEvent, cursor int64, counts map[int64]int64) error {
	if !isConsecutiveTradeFactsRule(rule.RuleType) {
		return fmt.Errorf("%w: count_rule_mismatch", ErrTradeWindowContextIncomplete)
	}
	direction := marketparse.EventBuy
	if rule.RuleType == plans.MarketConsecutiveLargeSell {
		direction = marketparse.EventSell
	}
	for _, event := range events {
		if event.ID <= cursor || event.EventType != direction || event.Confirmed != 1 || event.Reorged != 0 {
			continue
		}
		count, present := counts[event.ID]
		if !present || count < 0 || (count == 0 && eventThresholdMatch(rule, project, snapshots, event)) {
			return fmt.Errorf("%w: complete_count_missing", ErrTradeWindowContextIncomplete)
		}
	}
	return nil
}

func completeTradeWindowDetails(rule store.MarketRule, event store.MarketEvent, count int64) json.RawMessage {
	var details map[string]json.RawMessage
	if json.Unmarshal(event.Metadata, &details) != nil || details == nil {
		details = make(map[string]json.RawMessage)
	}
	minutes, _ := marketwindow.Effective(rule.RuleType, rule.WindowMinutes)
	details["trade_window"], _ = json.Marshal(map[string]any{"schema_version": 1, "count": count, "window_minutes": minutes,
		"from": event.OccurredAt.Add(-time.Duration(minutes) * time.Minute), "through": event.OccurredAt, "through_ready_id": event.TradeReadyID})
	encoded, _ := json.Marshal(details)
	return encoded
}
func (tree tradeWindowFenwick) prefix(end int) int64 {
	var count int64
	for ; end > 0; end -= end & -end {
		count += tree[end]
	}
	return count
}

// Publication-ready order and event-time order are independent. Sweep the
// former, and query a pair of time-coordinate trees for closed [t-W,t] ranges.
// This retains cumulative semantics without O(current*history) JSON scans.
func completeTradeWindowCountsReadiness(rule store.MarketRule, current []store.MarketEvent, candidates []store.MarketTradeWindowCandidate, cursor int64) (map[int64]int64, map[int64]string, error) {
	warmup := make(map[int64]string)
	minutes, ok := marketwindow.Effective(rule.RuleType, rule.WindowMinutes)
	if !ok || !isConsecutiveTradeFactsRule(rule.RuleType) {
		return nil, nil, fmt.Errorf("%w: invalid_period", ErrTradeWindowContextIncomplete)
	}
	if len(candidates) > store.MarketTradeWindowCandidateLimit {
		return nil, nil, fmt.Errorf("%w: candidate_row_budget", ErrTradeWindowContextIncomplete)
	}
	ordered := append([]store.MarketTradeWindowCandidate(nil), candidates...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].TradeReadyID < ordered[j].TradeReadyID })
	coordinates := make([]time.Time, 0, len(ordered))
	parents := make(map[int64]bool, len(ordered))
	byReady := make(map[int64]store.MarketTradeWindowCandidate, len(ordered))
	for _, candidate := range ordered {
		if candidate.TradeReadyID < 1 || candidate.ParentID < 1 || candidate.OccurredAt.IsZero() || parents[candidate.ParentID] ||
			(candidate.Unknown && candidate.Qualified) {
			return nil, nil, fmt.Errorf("%w: invalid_compact_candidate", ErrTradeWindowContextIncomplete)
		}
		if _, duplicate := byReady[candidate.TradeReadyID]; duplicate {
			return nil, nil, fmt.Errorf("%w: duplicate_ready", ErrTradeWindowContextIncomplete)
		}
		parents[candidate.ParentID] = true
		byReady[candidate.TradeReadyID] = candidate
		coordinates = append(coordinates, candidate.OccurredAt)
	}
	sort.Slice(coordinates, func(i, j int) bool { return coordinates[i].Before(coordinates[j]) })
	unique := coordinates[:0]
	for _, at := range coordinates {
		if len(unique) == 0 || !unique[len(unique)-1].Equal(at) {
			unique = append(unique, at)
		}
	}
	coordinates = unique
	qualified, unknown := make(tradeWindowFenwick, len(coordinates)+1), make(tradeWindowFenwick, len(coordinates)+1)
	current = append([]store.MarketEvent(nil), current...)
	sort.Slice(current, func(i, j int) bool { return current[i].TradeReadyID < current[j].TradeReadyID })
	direction := marketparse.EventBuy
	if rule.RuleType == plans.MarketConsecutiveLargeSell {
		direction = marketparse.EventSell
	}
	counts := make(map[int64]int64, len(current))
	index := 0
	lastReady := cursor
	window := time.Duration(minutes) * time.Minute
	for _, event := range current {
		if event.TradeReadyID <= lastReady || event.ID < 1 || event.OccurredAt.IsZero() {
			return nil, nil, fmt.Errorf("%w: invalid_current_order", ErrTradeWindowContextIncomplete)
		}
		lastReady = event.TradeReadyID
		for index < len(ordered) && ordered[index].TradeReadyID <= event.TradeReadyID {
			candidate := ordered[index]
			coordinate := sort.Search(len(coordinates), func(i int) bool { return !coordinates[i].Before(candidate.OccurredAt) })
			if candidate.Qualified {
				qualified.add(coordinate)
			}
			if candidate.Unknown {
				unknown.add(coordinate)
			}
			index++
		}
		if event.EventType != direction {
			continue
		}
		candidate, present := byReady[event.TradeReadyID]
		if !present || candidate.ParentID != event.ID || !candidate.OccurredAt.Equal(event.OccurredAt) || event.Confirmed != 1 || event.Reorged != 0 {
			return nil, nil, fmt.Errorf("%w: current_candidate_missing", ErrTradeWindowContextIncomplete)
		}
		from := event.OccurredAt.Add(-window)
		lower := sort.Search(len(coordinates), func(i int) bool { return !coordinates[i].Before(from) })
		upper := sort.Search(len(coordinates), func(i int) bool { return coordinates[i].After(event.OccurredAt) })
		if candidate.Unknown {
			return nil, nil, fmt.Errorf("%w: current_valuation_or_fact_unavailable", ErrTradeWindowContextIncomplete)
		}
		if unknown.prefix(upper)-unknown.prefix(lower) > 0 {
			warmup[event.ID] = "historical_valuation_unavailable"
			continue
		}
		counts[event.TradeReadyID] = qualified.prefix(upper) - qualified.prefix(lower)
	}
	return counts, warmup, nil
}

func completeTradeWindowCounts(rule store.MarketRule, current []store.MarketEvent, candidates []store.MarketTradeWindowCandidate, cursor int64) (map[int64]int64, error) {
	counts, warmup, err := completeTradeWindowCountsReadiness(rule, current, candidates, cursor)
	if err != nil {
		return nil, err
	}
	if len(warmup) > 0 {
		return nil, fmt.Errorf("%w: valuation_or_fact_unavailable", ErrTradeWindowContextIncomplete)
	}
	return counts, nil
}
