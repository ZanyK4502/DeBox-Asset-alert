package marketrules

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type ruleState struct {
	LastSnapshotID  int64 `json:"last_snapshot_id"`
	LastEventID     int64 `json:"last_event_id"`
	ConditionActive bool  `json:"condition_active"`
	PendingCrossing bool  `json:"pending_crossing"`
}

type Trigger struct {
	Event         *store.MarketEvent
	EventType     string
	EventKey      string
	PreviousValue *string
	CurrentValue  *string
	Note          string
	Details       json.RawMessage
	OccurredAt    time.Time
}

type Evaluation struct {
	State    json.RawMessage
	Triggers []Trigger
}

type EvaluationInput struct {
	Rule      store.MarketRule
	Project   store.MarketProject
	Snapshots []store.MarketSnapshot
	Events    []store.MarketEvent
	Holders   []store.MarketHolder
	Labels    []store.MarketAddressLabel
	Now       time.Time
}

func Evaluate(input EvaluationInput) (Evaluation, error) {
	state := ruleState{}
	if len(input.Rule.State) > 0 {
		_ = json.Unmarshal(input.Rule.State, &state)
	}
	now := input.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	snapshots := eligibleSnapshots(input)
	events := eligibleEvents(input)
	holders := holderIndex(input.Holders, input.Labels)

	var triggers []Trigger
	if isSnapshotRule(input.Rule.RuleType) {
		trigger, active, pending, evaluated, err := evaluateSnapshotRule(
			input.Rule,
			input.Project,
			snapshots,
			state,
			now,
		)
		if err != nil {
			return Evaluation{}, err
		}
		if evaluated {
			state.LastSnapshotID = snapshots[0].ID
			state.ConditionActive = active
			state.PendingCrossing = pending
		}
		if trigger != nil {
			triggers = append(triggers, *trigger)
			state.PendingCrossing = false
		}
	} else if input.Rule.RuleType == plans.MarketFourMemeProgress {
		eventTriggers, lastEventID, active, pending, err := evaluateProgressRule(
			input.Rule,
			input.Project,
			events,
			state,
			now,
		)
		if err != nil {
			return Evaluation{}, err
		}
		triggers = append(triggers, eventTriggers...)
		state.LastEventID = lastEventID
		state.ConditionActive = active
		state.PendingCrossing = pending
	} else {
		eventTriggers, lastEventID, err := evaluateEventRule(
			input.Rule,
			input.Project,
			snapshots,
			events,
			holders,
			state.LastEventID,
			now,
		)
		if err != nil {
			return Evaluation{}, err
		}
		triggers = append(triggers, eventTriggers...)
		state.LastEventID = lastEventID
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return Evaluation{}, fmt.Errorf("encode market rule state: %w", err)
	}
	return Evaluation{State: encoded, Triggers: triggers}, nil
}

func evaluateProgressRule(
	rule store.MarketRule,
	project store.MarketProject,
	events []store.MarketEvent,
	state ruleState,
	now time.Time,
) ([]Trigger, int64, bool, bool, error) {
	threshold, ok := rat(rule.ThresholdValue)
	if !ok {
		return nil, state.LastEventID, state.ConditionActive, state.PendingCrossing,
			fmt.Errorf("invalid market threshold")
	}
	maxEventID := state.LastEventID
	active := state.ConditionActive
	pending := state.PendingCrossing
	var candidate *store.MarketEvent
	var candidateProgress *string
	for _, event := range events {
		if event.ID > maxEventID {
			maxEventID = event.ID
		}
		progress := metadataDecimal(event.Metadata, "progress_percent")
		if event.ID <= state.LastEventID {
			if event.Reorged == 0 && event.Confirmed == 1 &&
				progress != nil &&
				metadataContains(event.Metadata, "protocol", "four_meme") &&
				active && pending {
				copy := event
				candidate = &copy
				candidateProgress = progress
			}
			continue
		}
		if event.Reorged == 1 {
			continue
		}
		if event.Confirmed != 1 {
			maxEventID = event.ID - 1
			break
		}
		if progress == nil || !metadataContains(event.Metadata, "protocol", "four_meme") {
			continue
		}
		value, valid := pointerRat(progress)
		if !valid {
			continue
		}
		condition := value.Cmp(threshold) >= 0
		if condition && !active {
			pending = true
		}
		if !condition {
			pending = false
		}
		active = condition
		if active && pending {
			copy := event
			candidate = &copy
			candidateProgress = progress
		}
	}
	if candidate == nil || !active || !pending || cooldownActive(rule, now) {
		return nil, maxEventID, active, pending, nil
	}
	trigger := Trigger{
		Event:        candidate,
		EventType:    candidate.EventType,
		EventKey:     candidate.EventKey,
		CurrentValue: candidateProgress,
		Note:         marketEventNote(rule, project, *candidate, candidateProgress),
		Details:      candidate.Metadata,
		OccurredAt:   candidate.OccurredAt.UTC(),
	}
	return []Trigger{trigger}, maxEventID, active, false, nil
}

func eligibleSnapshots(input EvaluationInput) []store.MarketSnapshot {
	result := make([]store.MarketSnapshot, 0, len(input.Snapshots))
	poolID := input.Rule.MarketPoolID
	if poolID == nil {
		poolID = input.Project.MainPoolID
	}
	for _, snapshot := range input.Snapshots {
		if poolID != nil && snapshot.MarketPoolID != *poolID {
			continue
		}
		result = append(result, snapshot)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CapturedAt.Equal(result[j].CapturedAt) {
			return result[i].ID > result[j].ID
		}
		return result[i].CapturedAt.After(result[j].CapturedAt)
	})
	return result
}

func eligibleEvents(input EvaluationInput) []store.MarketEvent {
	result := make([]store.MarketEvent, 0, len(input.Events))
	poolID := input.Rule.MarketPoolID
	if poolID == nil && isPoolScopedEventRule(input.Rule.RuleType) {
		poolID = input.Project.MainPoolID
	}
	for _, event := range input.Events {
		if poolID != nil &&
			(event.MarketPoolID == nil || *event.MarketPoolID != *poolID) {
			continue
		}
		result = append(result, event)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func isPoolScopedEventRule(ruleType string) bool {
	switch ruleType {
	case plans.MarketLargeBuy,
		plans.MarketLargeSell,
		plans.MarketConsecutiveLargeBuy,
		plans.MarketConsecutiveLargeSell,
		plans.MarketLiquidityAdded,
		plans.MarketLiquidityRemoved,
		plans.MarketFourMemeLargeTrade:
		return true
	default:
		return false
	}
}

func isSnapshotRule(ruleType string) bool {
	switch ruleType {
	case plans.MarketPriceAbove,
		plans.MarketPriceBelow,
		plans.MarketPriceIncrease,
		plans.MarketPriceDecrease,
		plans.MarketLiquidityBelow,
		plans.MarketLiquidityDecrease,
		plans.MarketVolumeAbove,
		plans.MarketVolumeSpike,
		plans.MarketTradeImbalance:
		return true
	default:
		return false
	}
}

func evaluateSnapshotRule(
	rule store.MarketRule,
	project store.MarketProject,
	snapshots []store.MarketSnapshot,
	state ruleState,
	now time.Time,
) (*Trigger, bool, bool, bool, error) {
	if len(snapshots) == 0 {
		return nil, state.ConditionActive, state.PendingCrossing, false, nil
	}
	latest := snapshots[0]
	current, previous, condition, available, err := snapshotCondition(rule, snapshots)
	if err != nil {
		return nil, state.ConditionActive, state.PendingCrossing, true, err
	}
	if !available {
		return nil, false, false, true, nil
	}
	crossed := condition && !state.ConditionActive
	repeatReady := condition && state.ConditionActive &&
		repeatsWhileConditionActive(rule) &&
		latest.ID != state.LastSnapshotID &&
		!cooldownActive(rule, now)
	pending := state.PendingCrossing || crossed || repeatReady
	if !condition {
		pending = false
	}
	if !pending || cooldownActive(rule, now) {
		return nil, condition, pending, true, nil
	}
	note := snapshotNote(rule, project, current, previous)
	details, _ := json.Marshal(map[string]any{
		"snapshot_id": latest.ID,
		"rule_type":   rule.RuleType,
		"threshold":   rule.ThresholdValue,
		"unit":        rule.ThresholdUnit,
		"window":      rule.WindowMinutes,
	})
	return &Trigger{
		EventType:     rule.RuleType,
		EventKey:      fmt.Sprintf("snapshot:%d:%s", latest.ID, rule.RuleType),
		PreviousValue: previous,
		CurrentValue:  current,
		Note:          note,
		Details:       details,
		OccurredAt:    latest.CapturedAt.UTC(),
	}, condition, false, true, nil
}

func repeatsWhileConditionActive(rule store.MarketRule) bool {
	return rule.RepeatWhileActive &&
		(rule.RuleType == plans.MarketPriceIncrease ||
			rule.RuleType == plans.MarketPriceDecrease)
}

func snapshotCondition(
	rule store.MarketRule,
	snapshots []store.MarketSnapshot,
) (current *string, previous *string, condition bool, available bool, err error) {
	latest := snapshots[0]
	threshold, ok := rat(rule.ThresholdValue)
	if !ok {
		return nil, nil, false, false, fmt.Errorf("invalid market threshold")
	}
	switch rule.RuleType {
	case plans.MarketPriceAbove, plans.MarketPriceBelow:
		current = latest.PriceUSD
		if value, exists := pointerRat(current); exists {
			condition = value.Cmp(threshold) >= 0
			if rule.RuleType == plans.MarketPriceBelow {
				condition = value.Cmp(threshold) <= 0
			}
			return current, nil, condition, true, nil
		}
	case plans.MarketLiquidityBelow:
		current = latest.LiquidityUSD
		if value, exists := pointerRat(current); exists {
			return current, nil, value.Cmp(threshold) <= 0, true, nil
		}
	case plans.MarketPriceIncrease, plans.MarketPriceDecrease,
		plans.MarketLiquidityDecrease:
		baseline := snapshotBaseline(snapshots, latest.CapturedAt, rule.WindowMinutes)
		if baseline == nil {
			return nil, nil, false, false, nil
		}
		current = latest.PriceUSD
		previous = baseline.PriceUSD
		if rule.RuleType == plans.MarketLiquidityDecrease {
			current = latest.LiquidityUSD
			previous = baseline.LiquidityUSD
		}
		change, exists := percentChange(current, previous)
		if !exists {
			return nil, nil, false, false, nil
		}
		if rule.RuleType == plans.MarketPriceIncrease {
			return stringPointer(decimalString(change)), previous,
				change.Cmp(threshold) >= 0, true, nil
		}
		decrease := new(big.Rat).Neg(change)
		return stringPointer(decimalString(decrease)), previous,
			decrease.Cmp(threshold) >= 0, true, nil
	case plans.MarketVolumeAbove:
		current = snapshotVolume(latest, rule.WindowMinutes)
		if value, exists := pointerRat(current); exists {
			return current, nil, value.Cmp(threshold) >= 0, true, nil
		}
	case plans.MarketVolumeSpike:
		current = snapshotVolume(latest, rule.WindowMinutes)
		currentValue, exists := pointerRat(current)
		if !exists {
			return nil, nil, false, false, nil
		}
		average, exists := historicalVolumeAverage(snapshots[1:], rule.WindowMinutes)
		if !exists || average.Sign() <= 0 {
			return nil, nil, false, false, nil
		}
		ratio := new(big.Rat).Quo(currentValue, average)
		return stringPointer(decimalString(ratio)), stringPointer(decimalString(average)),
			ratio.Cmp(threshold) >= 0, true, nil
	case plans.MarketTradeImbalance:
		buys, sells, exists := snapshotTrades(latest, rule.WindowMinutes)
		if !exists || buys+sells == 0 {
			return nil, nil, false, false, nil
		}
		dominant := buys
		if sells > dominant {
			dominant = sells
		}
		percent := new(big.Rat).SetFrac(
			big.NewInt(dominant*100),
			big.NewInt(buys+sells),
		)
		return stringPointer(decimalString(percent)), nil,
			percent.Cmp(threshold) >= 0, true, nil
	}
	return nil, nil, false, false, nil
}

func evaluateEventRule(
	rule store.MarketRule,
	project store.MarketProject,
	snapshots []store.MarketSnapshot,
	events []store.MarketEvent,
	holders map[string]store.MarketHolder,
	lastEventID int64,
	now time.Time,
) ([]Trigger, int64, error) {
	result := make([]Trigger, 0)
	maxEventID := lastEventID
	cooldownAvailable := !cooldownActive(rule, now)
	for _, event := range events {
		if event.ID <= lastEventID {
			continue
		}
		if event.ID > maxEventID {
			maxEventID = event.ID
		}
		if event.Reorged == 1 {
			continue
		}
		if event.Confirmed != 1 {
			maxEventID = event.ID - 1
			break
		}
		matched, value, note, details := eventCondition(
			rule,
			project,
			snapshots,
			events,
			event,
			holders,
		)
		if !matched || !cooldownAvailable {
			continue
		}
		eventCopy := event
		result = append(result, Trigger{
			Event:        &eventCopy,
			EventType:    event.EventType,
			EventKey:     event.EventKey,
			CurrentValue: value,
			Note:         note,
			Details:      details,
			OccurredAt:   event.OccurredAt.UTC(),
		})
		if rule.CooldownSeconds > 0 {
			cooldownAvailable = false
		}
	}
	return result, maxEventID, nil
}

func eventCondition(
	rule store.MarketRule,
	project store.MarketProject,
	snapshots []store.MarketSnapshot,
	events []store.MarketEvent,
	event store.MarketEvent,
	holders map[string]store.MarketHolder,
) (bool, *string, string, json.RawMessage) {
	matchedType := ""
	switch rule.RuleType {
	case plans.MarketLargeBuy:
		matchedType = marketparse.EventBuy
	case plans.MarketLargeSell:
		matchedType = marketparse.EventSell
	case plans.MarketFourMemeLargeTrade:
		if event.EventType != marketparse.EventBuy && event.EventType != marketparse.EventSell {
			return false, nil, "", nil
		}
		if !metadataContains(event.Metadata, "protocol", "four_meme") {
			return false, nil, "", nil
		}
	case plans.MarketLiquidityAdded:
		matchedType = marketparse.EventLiquidityAdded
	case plans.MarketLiquidityRemoved:
		matchedType = marketparse.EventLiquidityRemoved
	case plans.MarketNewPool:
		matchedType = marketparse.EventPoolInitialized
	case plans.MarketFourMemeMigration:
		matchedType = marketparse.EventMigrated
	case plans.MarketFourMemeProgress:
		progress := metadataDecimal(event.Metadata, "progress_percent")
		if progress == nil ||
			!metadataContains(event.Metadata, "protocol", "four_meme") ||
			comparePointer(progress, rule.ThresholdValue) < 0 {
			return false, nil, "", nil
		}
		return true, progress, marketEventNote(rule, project, event, progress), event.Metadata
	case plans.MarketHolderIncrease, plans.MarketHolderDecrease,
		plans.MarketHolderRankEntered, plans.MarketHolderRankExited:
		return holderEventCondition(rule, project, event, holders)
	case plans.MarketConsecutiveLargeBuy, plans.MarketConsecutiveLargeSell:
		direction := marketparse.EventBuy
		if rule.RuleType == plans.MarketConsecutiveLargeSell {
			direction = marketparse.EventSell
		}
		if event.EventType != direction || !eventThresholdMatch(rule, project, snapshots, event) {
			return false, nil, "", nil
		}
		count := consecutiveCount(rule, project, snapshots, events, event, direction)
		if count < rule.TriggerCountThreshold {
			return false, nil, "", nil
		}
		value := strconv.FormatInt(count, 10)
		return true, &value, marketEventNote(rule, project, event, &value), event.Metadata
	}
	if matchedType != "" && event.EventType != matchedType {
		return false, nil, "", nil
	}
	if (rule.RuleType == plans.MarketLargeBuy ||
		rule.RuleType == plans.MarketLargeSell ||
		rule.RuleType == plans.MarketFourMemeLargeTrade ||
		rule.RuleType == plans.MarketLiquidityAdded ||
		rule.RuleType == plans.MarketLiquidityRemoved) &&
		!eventThresholdMatch(rule, project, snapshots, event) {
		return false, nil, "", nil
	}
	value := eventValue(rule, project, snapshots, event)
	return true, value, marketEventNote(rule, project, event, value), event.Metadata
}

func eventThresholdMatch(
	rule store.MarketRule,
	project store.MarketProject,
	snapshots []store.MarketSnapshot,
	event store.MarketEvent,
) bool {
	value := eventValue(rule, project, snapshots, event)
	return comparePointer(value, rule.ThresholdValue) >= 0
}

func eventValue(
	rule store.MarketRule,
	project store.MarketProject,
	snapshots []store.MarketSnapshot,
	event store.MarketEvent,
) *string {
	switch rule.ThresholdUnit {
	case "token":
		return event.TokenAmount
	case "percent":
		if strings.Contains(rule.RuleType, "holder") && event.TokenAmountRaw != nil &&
			project.TotalSupplyRaw != nil {
			return ratioPercent(*event.TokenAmountRaw, *project.TotalSupplyRaw)
		}
		if event.USDValue != nil && len(snapshots) > 0 && snapshots[0].LiquidityUSD != nil {
			return ratioPercent(*event.USDValue, *snapshots[0].LiquidityUSD)
		}
	case "count":
		value := "1"
		return &value
	case "progress":
		return metadataDecimal(event.Metadata, "progress_percent")
	default:
		return event.USDValue
	}
	return nil
}

func holderEventCondition(
	rule store.MarketRule,
	project store.MarketProject,
	event store.MarketEvent,
	holders map[string]store.MarketHolder,
) (bool, *string, string, json.RawMessage) {
	address := ""
	if event.WalletAddress != nil {
		address = strings.ToLower(*event.WalletAddress)
	}
	if address == "" {
		address = metadataString(event.Metadata, "holder_address")
	}
	if _, tracked := holders[address]; !tracked {
		return false, nil, "", nil
	}
	switch rule.RuleType {
	case plans.MarketHolderRankEntered:
		if event.EventType != "holder_rank_entered" {
			return false, nil, "", nil
		}
		rank := metadataDecimal(event.Metadata, "new_rank")
		if rank == nil || rankGreaterThanThreshold(rank, rule.ThresholdValue) {
			return false, nil, "", nil
		}
		return true, rank, marketEventNote(rule, project, event, rank), event.Metadata
	case plans.MarketHolderRankExited:
		if event.EventType != "holder_rank_exited" {
			return false, nil, "", nil
		}
		rank := metadataDecimal(event.Metadata, "old_rank")
		if rank == nil || rankGreaterThanThreshold(rank, rule.ThresholdValue) {
			return false, nil, "", nil
		}
		return true, rank, marketEventNote(rule, project, event, rank), event.Metadata
	case plans.MarketHolderIncrease, plans.MarketHolderDecrease:
		if event.EventType == "holder_increase" || event.EventType == "holder_decrease" {
			expected := "holder_increase"
			if rule.RuleType == plans.MarketHolderDecrease {
				expected = "holder_decrease"
			}
			if event.EventType != expected {
				return false, nil, "", nil
			}
		} else if event.EventType == marketparse.EventTokenTransfer {
			from := metadataString(event.Metadata, "from_address")
			to := metadataString(event.Metadata, "to_address")
			address := to
			if rule.RuleType == plans.MarketHolderDecrease {
				address = from
			}
			if _, tracked := holders[address]; !tracked {
				return false, nil, "", nil
			}
		} else {
			return false, nil, "", nil
		}
	}
	value := eventValue(rule, project, nil, event)
	if comparePointer(value, rule.ThresholdValue) < 0 {
		return false, nil, "", nil
	}
	return true, value, marketEventNote(rule, project, event, value), event.Metadata
}

func rankGreaterThanThreshold(rank *string, threshold string) bool {
	rankValue, rankOK := pointerRat(rank)
	thresholdValue, thresholdOK := rat(threshold)
	return !rankOK || !thresholdOK || rankValue.Cmp(thresholdValue) > 0
}

func consecutiveCount(
	rule store.MarketRule,
	project store.MarketProject,
	snapshots []store.MarketSnapshot,
	events []store.MarketEvent,
	current store.MarketEvent,
	direction string,
) int64 {
	window := time.Duration(defaultWindow(rule.WindowMinutes, 15)) * time.Minute
	from := current.OccurredAt.Add(-window)
	var count int64
	for _, candidate := range events {
		if candidate.ID > current.ID || candidate.EventType != direction ||
			candidate.OccurredAt.Before(from) || candidate.OccurredAt.After(current.OccurredAt) {
			continue
		}
		if eventThresholdMatch(rule, project, snapshots, candidate) {
			count++
		}
	}
	return count
}

func holderIndex(
	holders []store.MarketHolder,
	labels []store.MarketAddressLabel,
) map[string]store.MarketHolder {
	excluded := make(map[string]bool)
	for _, label := range labels {
		if label.Excluded == 1 {
			excluded[strings.ToLower(label.Address)] = true
		}
	}
	result := make(map[string]store.MarketHolder)
	for _, holder := range holders {
		address := strings.ToLower(holder.HolderAddress)
		if holder.Excluded == 1 || excluded[address] {
			continue
		}
		result[address] = holder
	}
	return result
}

func cooldownActive(rule store.MarketRule, now time.Time) bool {
	return rule.LastTriggeredAt != nil && rule.CooldownSeconds > 0 &&
		now.Before(rule.LastTriggeredAt.Add(time.Duration(rule.CooldownSeconds)*time.Second))
}

func snapshotBaseline(
	snapshots []store.MarketSnapshot,
	latestAt time.Time,
	windowMinutes *int32,
) *store.MarketSnapshot {
	window := time.Duration(defaultWindow(windowMinutes, 60)) * time.Minute
	target := latestAt.Add(-window)
	for index := 1; index < len(snapshots); index++ {
		if !snapshots[index].CapturedAt.After(target) {
			value := snapshots[index]
			return &value
		}
	}
	return nil
}

func snapshotVolume(snapshot store.MarketSnapshot, window *int32) *string {
	minutes := defaultWindow(window, 60)
	switch {
	case minutes <= 5:
		return snapshot.Volume5mUSD
	case minutes <= 15:
		return snapshot.Volume15mUSD
	case minutes <= 60:
		return snapshot.Volume1hUSD
	case minutes <= 360:
		return snapshot.Volume6hUSD
	default:
		return snapshot.Volume24hUSD
	}
}

func snapshotTrades(snapshot store.MarketSnapshot, window *int32) (int64, int64, bool) {
	minutes := defaultWindow(window, 60)
	switch {
	case minutes <= 5 && snapshot.Buys5m != nil && snapshot.Sells5m != nil:
		return *snapshot.Buys5m, *snapshot.Sells5m, true
	case minutes <= 60 && snapshot.Buys1h != nil && snapshot.Sells1h != nil:
		return *snapshot.Buys1h, *snapshot.Sells1h, true
	case snapshot.Buys24h != nil && snapshot.Sells24h != nil:
		return *snapshot.Buys24h, *snapshot.Sells24h, true
	default:
		return 0, 0, false
	}
}

func historicalVolumeAverage(
	snapshots []store.MarketSnapshot,
	window *int32,
) (*big.Rat, bool) {
	total := new(big.Rat)
	var count int64
	for _, snapshot := range snapshots {
		value, exists := pointerRat(snapshotVolume(snapshot, window))
		if !exists {
			continue
		}
		total.Add(total, value)
		count++
		if count == 12 {
			break
		}
	}
	if count == 0 {
		return nil, false
	}
	return total.Quo(total, big.NewRat(count, 1)), true
}

func percentChange(current, previous *string) (*big.Rat, bool) {
	currentValue, ok := pointerRat(current)
	if !ok {
		return nil, false
	}
	previousValue, ok := pointerRat(previous)
	if !ok || previousValue.Sign() == 0 {
		return nil, false
	}
	change := new(big.Rat).Sub(currentValue, previousValue)
	change.Quo(change, previousValue)
	change.Mul(change, big.NewRat(100, 1))
	return change, true
}

func ratioPercent(numerator, denominator string) *string {
	left, leftOK := rat(numerator)
	right, rightOK := rat(denominator)
	if !leftOK || !rightOK || right.Sign() == 0 {
		return nil
	}
	value := new(big.Rat).Quo(left, right)
	value.Mul(value, big.NewRat(100, 1))
	result := decimalString(value)
	return &result
}

func rat(value string) (*big.Rat, bool) {
	result, ok := new(big.Rat).SetString(strings.TrimSpace(value))
	return result, ok
}

func pointerRat(value *string) (*big.Rat, bool) {
	if value == nil {
		return nil, false
	}
	return rat(*value)
}

func comparePointer(value *string, threshold string) int {
	left, leftOK := pointerRat(value)
	right, rightOK := rat(threshold)
	if !leftOK || !rightOK {
		return -1
	}
	return left.Cmp(right)
}

func decimalString(value *big.Rat) string {
	result := value.FloatString(8)
	result = strings.TrimRight(strings.TrimRight(result, "0"), ".")
	if result == "" || result == "-0" {
		return "0"
	}
	return result
}

func metadataString(payload json.RawMessage, key string) string {
	var values map[string]any
	if json.Unmarshal(payload, &values) != nil {
		return ""
	}
	value, _ := values[key].(string)
	return strings.ToLower(strings.TrimSpace(value))
}

func metadataDecimal(payload json.RawMessage, key string) *string {
	var values map[string]any
	if json.Unmarshal(payload, &values) != nil {
		return nil
	}
	switch value := values[key].(type) {
	case string:
		if _, ok := rat(value); ok {
			return &value
		}
	case float64:
		result := strconv.FormatFloat(value, 'f', -1, 64)
		return &result
	}
	return nil
}

func metadataContains(payload json.RawMessage, key, expected string) bool {
	return strings.Contains(metadataString(payload, key), strings.ToLower(expected))
}

func defaultWindow(value *int32, fallback int32) int32 {
	if value == nil || *value <= 0 {
		return fallback
	}
	return *value
}

func stringPointer(value string) *string {
	return &value
}

func snapshotNote(
	rule store.MarketRule,
	project store.MarketProject,
	current *string,
	previous *string,
) string {
	return fmt.Sprintf(
		"%s %s：当前 %s，阈值 %s %s",
		project.TokenSymbol,
		rule.RuleType,
		stringValue(current),
		rule.ThresholdValue,
		rule.ThresholdUnit,
	)
}

func marketEventNote(
	rule store.MarketRule,
	project store.MarketProject,
	event store.MarketEvent,
	value *string,
) string {
	return fmt.Sprintf(
		"%s %s：%s %s（%s）",
		project.TokenSymbol,
		rule.RuleType,
		stringValue(value),
		rule.ThresholdUnit,
		event.EventKey,
	)
}

func stringValue(value *string) string {
	if value == nil {
		return "-"
	}
	return *value
}
