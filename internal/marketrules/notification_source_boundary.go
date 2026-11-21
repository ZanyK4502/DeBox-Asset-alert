package marketrules

import (
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func notificationSourceOld(event store.MarketEvent, after *time.Time) bool {
	return after != nil && event.OccurredAt.Before(*after)
}

// This filters current evaluation obligations, never the historical facts used
// to count a fresh candidate's complete cumulative window.
func notificationCurrentEvents(events []store.MarketEvent, after *time.Time) []store.MarketEvent {
	if after == nil {
		return events
	}
	result := make([]store.MarketEvent, 0, len(events))
	for _, event := range events {
		if !notificationSourceOld(event, after) {
			result = append(result, event)
		}
	}
	return result
}

func notificationStageSourceExpired(rule store.MarketRule, event store.MarketEvent, now time.Time) bool {
	return rule.RuleScope != "combination" && rule.DeliveryMode == "stage" && rule.CycleMinutes > 0 && !event.OccurredAt.Add(time.Duration(rule.CycleMinutes)*time.Minute).After(now)
}

func notificationCurrentRuleEvents(rule store.MarketRule, events []store.MarketEvent, after *time.Time, now time.Time, existing ...map[int64]bool) []store.MarketEvent {
	var decisions map[int64]bool
	if len(existing) > 0 {
		decisions = existing[0]
	}
	result := make([]store.MarketEvent, 0, len(events))
	for _, event := range events {
		if !decisions[event.ID] && !notificationSourceOld(event, after) && !notificationStageSourceExpired(rule, event, now) {
			result = append(result, event)
		}
	}
	return result
}

func notificationStageExpiredSuppression(event store.MarketEvent) SuppressedTrigger {
	result := notificationBoundarySuppression(event)
	result.Reason = "source window expired or precedes recorded recovery cutoff"
	result.Note = "来源所属的原阶段时效已过，未评估新的提醒。"
	return result
}

func notificationBoundarySuppression(event store.MarketEvent) SuppressedTrigger {
	copy := event
	return SuppressedTrigger{Trigger: Trigger{Event: &copy, EventType: event.EventType, EventKey: event.EventKey, Note: "来源早于已记录的通知恢复边界，未评估新的提醒。", Details: event.Metadata, OccurredAt: event.OccurredAt.UTC()}, Reason: "source precedes recorded recovery cutoff"}
}

func notificationEventFamily(rule store.MarketRule, event store.MarketEvent) bool {
	switch rule.RuleType {
	case plans.MarketLargeBuy, plans.MarketConsecutiveLargeBuy:
		return event.EventType == marketparse.EventBuy
	case plans.MarketLargeSell, plans.MarketConsecutiveLargeSell:
		return event.EventType == marketparse.EventSell
	case plans.MarketFourMemeLargeTrade:
		return (event.EventType == marketparse.EventBuy || event.EventType == marketparse.EventSell) && metadataContains(event.Metadata, "protocol", "four_meme")
	case plans.MarketLiquidityAdded:
		return event.EventType == marketparse.EventLiquidityAdded
	case plans.MarketLiquidityRemoved:
		return event.EventType == marketparse.EventLiquidityRemoved
	case plans.MarketNewPool:
		return event.EventType == marketparse.EventPoolInitialized
	case plans.MarketFourMemeMigration:
		return event.EventType == marketparse.EventMigrated
	case plans.MarketHolderIncrease:
		return event.EventType == "holder_increase"
	case plans.MarketHolderDecrease:
		return event.EventType == "holder_decrease"
	case plans.MarketHolderRankEntered:
		return event.EventType == "holder_rank_entered" || event.EventType == "holder_rank_changed"
	case plans.MarketHolderRankExited:
		return event.EventType == "holder_rank_exited" || event.EventType == "holder_rank_changed"
	}
	return false
}
