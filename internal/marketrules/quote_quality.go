package marketrules

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type eventValuationProof struct {
	SchemaVersion int                 `json:"valuation_schema_version"`
	Complete      bool                `json:"valuation_complete"`
	Quote         marketquality.Quote `json:"valuation_quote_quality"`
	At            *time.Time          `json:"valuation_at"`
}

func (proof eventValuationProof) availableAt(eventAt time.Time) bool {
	at := eventAt.UTC().Truncate(time.Microsecond)
	return proof.SchemaVersion == 1 && proof.Complete && proof.At != nil && !eventAt.IsZero() &&
		proof.At.UTC().Truncate(time.Microsecond).Equal(at) && proof.Quote.AvailableAt(at)
}

func snapshotQuoteAvailable(snapshot store.MarketSnapshot, at time.Time) bool {
	return snapshot.QuoteQuality.AvailableAt(at)
}

func eventValuationAvailable(event store.MarketEvent) bool {
	var metadata struct {
		eventValuationProof
		Proofs     []eventValuationProof `json:"valuation_metadata"`
		Projection *struct {
			SourceLegIDs []int64               `json:"source_leg_ids"`
			Proofs       []eventValuationProof `json:"valuation_metadata"`
		} `json:"trade_projection"`
	}
	if json.Unmarshal(event.Metadata, &metadata) != nil {
		return false
	}
	proofs := metadata.Proofs
	if metadata.Projection != nil {
		if len(metadata.Projection.SourceLegIDs) == 0 || len(metadata.Projection.Proofs) != len(metadata.Projection.SourceLegIDs) {
			return false
		}
		proofs = metadata.Projection.Proofs
	}
	if len(proofs) == 0 {
		return metadata.Projection == nil && metadata.eventValuationProof.availableAt(event.OccurredAt)
	}
	for _, proof := range proofs {
		if !proof.availableAt(event.OccurredAt) {
			return false
		}
	}
	return true
}

func ruleNeedsUSDValuation(rule store.MarketRule) bool {
	switch rule.ThresholdUnit {
	case "token", "count", "progress":
		return false
	case "percent":
		return !isHolderRule(rule.RuleType)
	default:
		return true
	}
}

func quoteDependentEvent(rule store.MarketRule, event store.MarketEvent, holders map[string]store.MarketHolder) bool {
	if !ruleNeedsUSDValuation(rule) {
		return false
	}
	switch rule.RuleType {
	case plans.MarketLargeBuy, plans.MarketConsecutiveLargeBuy:
		return event.EventType == marketparse.EventBuy
	case plans.MarketLargeSell, plans.MarketConsecutiveLargeSell:
		return event.EventType == marketparse.EventSell
	case plans.MarketFourMemeLargeTrade:
		return (event.EventType == marketparse.EventBuy || event.EventType == marketparse.EventSell) &&
			metadataContains(event.Metadata, "protocol", "four_meme")
	case plans.MarketLiquidityAdded:
		return event.EventType == marketparse.EventLiquidityAdded
	case plans.MarketLiquidityRemoved:
		return event.EventType == marketparse.EventLiquidityRemoved
	case plans.MarketHolderIncrease, plans.MarketHolderDecrease:
		if _, tracked := holders[holderEventAddress(rule.RuleType, event)]; !tracked {
			return false
		}
		expected := "holder_increase"
		if rule.RuleType == plans.MarketHolderDecrease {
			expected = "holder_decrease"
		}
		return event.EventType == expected || event.EventType == marketparse.EventTokenTransfer
	default:
		return false
	}
}

func eventQuoteUnavailable(rule store.MarketRule, snapshots []store.MarketSnapshot, event store.MarketEvent, now time.Time) string {
	if !nonnegativeDecimal(event.USDValue) {
		return "market_usd_value_unavailable"
	}
	if !eventValuationAvailable(event) {
		return "market_valuation_quote_unavailable"
	}
	if rule.ThresholdUnit == "percent" {
		if _, present := tradeProjectionFromEvent(event); present {
			return tradeProjectionLiquidityUnavailable(event, now)
		}
		if len(snapshots) == 0 || !snapshotQuoteAvailable(snapshots[0], now) || !positiveDecimal(snapshots[0].LiquidityUSD) {
			return "market_liquidity_quote_unavailable"
		}
	}
	return ""
}

func tradeProjectionLiquidityUnavailable(event store.MarketEvent, now time.Time) string {
	var metadata struct {
		Projection struct {
			SelectedPoolIDs []int64 `json:"selected_pool_ids"`
			Snapshots       []struct {
				PoolID  int64               `json:"pool_id"`
				Quality marketquality.Quote `json:"quote_quality"`
			} `json:"liquidity_snapshots"`
		} `json:"trade_projection"`
	}
	if json.Unmarshal(event.Metadata, &metadata) != nil || len(metadata.Projection.SelectedPoolIDs) == 0 ||
		len(metadata.Projection.Snapshots) != len(metadata.Projection.SelectedPoolIDs) {
		return "trade_liquidity_quote_unavailable"
	}
	byPool := make(map[int64]bool, len(metadata.Projection.Snapshots))
	for _, snapshot := range metadata.Projection.Snapshots {
		if !snapshot.Quality.AvailableAt(now) || byPool[snapshot.PoolID] {
			return "trade_liquidity_quote_unavailable"
		}
		byPool[snapshot.PoolID] = true
	}
	for _, id := range metadata.Projection.SelectedPoolIDs {
		if !byPool[id] {
			return "trade_liquidity_quote_unavailable"
		}
	}
	return ""
}

func validateTradeWindowValuations(rule store.MarketRule, snapshots []store.MarketSnapshot, events []store.MarketEvent, cursor int64, useReadyID bool, now time.Time, sourceAfter ...*time.Time) error {
	var after *time.Time
	if len(sourceAfter) > 0 {
		after = sourceAfter[0]
	}
	return validateTradeWindowValuationsAfterDecisions(rule, snapshots, events, cursor, useReadyID, now, after, nil)
}

func validateTradeWindowValuationsAfterDecisions(rule store.MarketRule, snapshots []store.MarketSnapshot, events []store.MarketEvent, cursor int64, useReadyID bool, now time.Time, sourceAfter *time.Time, decisions map[int64]bool) error {
	if !isConsecutiveTradeFactsRule(rule.RuleType) || !ruleNeedsUSDValuation(rule) {
		return nil
	}
	direction := marketparse.EventBuy
	if rule.RuleType == plans.MarketConsecutiveLargeSell {
		direction = marketparse.EventSell
	}
	sequence := func(event store.MarketEvent) int64 {
		if useReadyID {
			return event.TradeReadyID
		}
		return event.ID
	}
	window := time.Duration(defaultWindow(rule.WindowMinutes, 15)) * time.Minute
	var unavailable []store.MarketEvent
	for _, candidate := range events {
		if candidate.EventType == direction && candidate.Confirmed == 1 && candidate.Reorged == 0 && eventQuoteUnavailable(rule, snapshots, candidate, now) != "" {
			unavailable = append(unavailable, candidate)
		}
	}
	if len(unavailable) == 0 {
		return nil
	}
	for _, current := range events {
		if decisions[current.ID] {
			continue
		}
		if notificationStageSourceExpired(rule, current, now) {
			continue
		}
		if notificationSourceOld(current, sourceAfter) {
			continue
		}
		if sequence(current) <= cursor || current.EventType != direction || current.Confirmed != 1 || current.Reorged != 0 {
			continue
		}
		from := current.OccurredAt.Add(-window)
		for _, candidate := range unavailable {
			if sequence(candidate) > sequence(current) || candidate.EventType != direction || candidate.Confirmed != 1 || candidate.Reorged != 0 ||
				candidate.OccurredAt.Before(from) || candidate.OccurredAt.After(current.OccurredAt) {
				continue
			}
			return fmt.Errorf("%w: valuation_unavailable", ErrTradeWindowContextIncomplete)
		}
	}
	return nil
}

func positiveDecimal(value *string) bool {
	number, ok := pointerRat(value)
	return ok && number.Sign() > 0
}

func nonnegativeDecimal(value *string) bool {
	number, ok := pointerRat(value)
	return ok && number.Sign() >= 0
}
