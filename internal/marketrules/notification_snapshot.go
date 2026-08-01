package marketrules

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type marketNotificationSnapshotPayload struct {
	SchemaVersion int                              `json:"schema_version"`
	Delivery      store.MarketNotificationDelivery `json:"delivery"`
}

func (service *Service) createMarketNotificationSnapshot(
	ctx context.Context,
	delivery store.MarketNotificationDelivery,
	text string,
) (store.NotificationDetailSnapshot, error) {
	params, err := marketNotificationSnapshotParams(delivery, text)
	if err != nil {
		return store.NotificationDetailSnapshot{}, err
	}
	return service.repository.CreateNotificationDetailSnapshot(ctx, params)
}

func marketNotificationSnapshotParams(
	delivery store.MarketNotificationDelivery,
	text string,
) (store.CreateNotificationDetailSnapshotParams, error) {
	compact := compactMarketNotificationDelivery(delivery)
	details, err := json.Marshal(marketNotificationSnapshotPayload{
		SchemaVersion: 1,
		Delivery:      compact,
	})
	if err != nil {
		return store.CreateNotificationDetailSnapshotParams{}, fmt.Errorf(
			"encode market notification snapshot: %w",
			err,
		)
	}
	kind, sourceType, err := marketSnapshotKind(delivery.Kind)
	if err != nil {
		return store.CreateNotificationDetailSnapshotParams{}, err
	}
	sourceID := delivery.ID
	ruleID, ruleType, ruleName, threshold := marketSnapshotRule(delivery)
	return store.CreateNotificationDetailSnapshotParams{
		SourceKey:            fmt.Sprintf("%s:%d", kind, delivery.ID),
		NotificationKind:     kind,
		SourceType:           sourceType,
		SourceID:             &sourceID,
		DeBoxUserID:          delivery.DeBoxUserID,
		RuleID:               ruleID,
		RuleType:             ruleType,
		RuleName:             ruleName,
		RuleThreshold:        threshold,
		ActualValue:          marketSnapshotActual(delivery),
		NotificationChatID:   delivery.NotificationChatID,
		NotificationChatType: delivery.NotificationChatType,
		NotificationLanguage: delivery.NotificationLanguage,
		NotificationLabel:    delivery.NotificationLabel,
		NotificationText:     text,
		Details:              details,
	}, nil
}

func marketSnapshotKind(kind string) (string, string, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "realtime":
		return store.NotificationKindMarketRealtime, "market_notification_delivery", nil
	case "stage":
		return store.NotificationKindMarketStage, "market_notification_delivery", nil
	case "combination":
		return store.NotificationKindMarketCombination, "market_notification_delivery", nil
	default:
		return "", "", fmt.Errorf("unsupported market notification snapshot kind %q", kind)
	}
}

func marketSnapshotRule(
	delivery store.MarketNotificationDelivery,
) (*int64, string, string, string) {
	english := notificationLanguage(delivery.NotificationLanguage) == "en"
	if delivery.Rule != nil {
		ruleID := delivery.Rule.ID
		threshold := strings.TrimSpace(delivery.Rule.ThresholdValue)
		if unit := strings.TrimSpace(delivery.Rule.ThresholdUnit); unit != "" {
			threshold = strings.TrimSpace(threshold + " " + unit)
		}
		if delivery.Kind == "stage" && delivery.Rule.TriggerCountThreshold > 0 {
			threshold = appendMarketSnapshotCondition(
				threshold,
				"send_after="+strconv.FormatInt(delivery.Rule.TriggerCountThreshold, 10),
			)
		}
		return &ruleID,
			delivery.Rule.RuleType,
			ruleDisplay(delivery.Rule.RuleType, english),
			threshold
	}
	if delivery.MarketCombinationRuleID != nil {
		name := "市场组合提醒"
		if english {
			name = "Market combination alert"
		}
		return delivery.MarketCombinationRuleID,
			"market_combination",
			name,
			marketCombinationSnapshotThreshold(delivery.CombinationMembers)
	}
	return nil, "", "", ""
}

func marketSnapshotActual(delivery store.MarketNotificationDelivery) string {
	if delivery.Kind == "combination" {
		values := make([]string, 0, len(delivery.CombinationMembers))
		for _, member := range delivery.CombinationMembers {
			values = append(values, member.RuleType+"="+
				strconv.FormatInt(member.TriggerCount, 10))
		}
		return strings.Join(values, ";")
	}
	if delivery.CurrentValue != nil && strings.TrimSpace(*delivery.CurrentValue) != "" {
		return strings.TrimSpace(*delivery.CurrentValue)
	}
	if delivery.Event != nil {
		for _, value := range []*string{
			delivery.Event.USDValue,
			delivery.Event.TokenAmount,
			delivery.Event.PriceUSD,
		} {
			if value != nil && strings.TrimSpace(*value) != "" {
				return strings.TrimSpace(*value)
			}
		}
	}
	if delivery.TriggerCount > 0 {
		return strconv.FormatInt(delivery.TriggerCount, 10)
	}
	return ""
}

func marketCombinationSnapshotThreshold(
	members []store.MarketCombinationProgress,
) string {
	values := make([]string, 0, len(members))
	for _, member := range members {
		value := member.RuleType + ">=" + strconv.FormatInt(member.RequiredTriggerCount, 10)
		switch {
		case member.MarketRule != nil && strings.TrimSpace(member.MarketRule.ThresholdValue) != "":
			value += "@" + strings.TrimSpace(member.MarketRule.ThresholdValue)
		case member.WatchRule != nil && strings.TrimSpace(member.WatchRule.Threshold) != "":
			value += "@" + strings.TrimSpace(member.WatchRule.Threshold)
		}
		values = append(values, value)
	}
	return strings.Join(values, ";")
}

func appendMarketSnapshotCondition(current, addition string) string {
	if strings.TrimSpace(current) == "" {
		return addition
	}
	return current + ";" + addition
}

func compactMarketNotificationDelivery(
	delivery store.MarketNotificationDelivery,
) store.MarketNotificationDelivery {
	result := delivery
	result.Project.Metadata = nil
	result.Rule = compactMarketRule(delivery.Rule)
	result.Event = compactMarketEvent(delivery.Event)
	result.Pool = compactMarketPool(delivery.Pool)
	result.Snapshot = compactMarketSnapshot(delivery.Snapshot)
	result.RecentEvents = compactMarketNotificationEvents(delivery.RecentEvents)
	result.StageEvents = compactMarketNotificationEvents(delivery.StageEvents)
	result.CombinationMembers = make(
		[]store.MarketCombinationProgress,
		len(delivery.CombinationMembers),
	)
	for index, member := range delivery.CombinationMembers {
		result.CombinationMembers[index] = member
		result.CombinationMembers[index].MarketRule = compactMarketRule(member.MarketRule)
		if member.WatchRule != nil {
			watchRule := *member.WatchRule
			result.CombinationMembers[index].WatchRule = &watchRule
		}
		result.CombinationMembers[index].MarketEvents = compactMarketNotificationEvents(
			member.MarketEvents,
		)
		result.CombinationMembers[index].RecentEvents = compactMarketNotificationEvents(
			member.RecentEvents,
		)
	}
	return result
}

func compactMarketNotificationEvents(
	events []store.MarketNotificationEvent,
) []store.MarketNotificationEvent {
	result := make([]store.MarketNotificationEvent, len(events))
	for index, event := range events {
		result[index] = event
		result[index].Project.Metadata = nil
		result[index].Event.RawPayload = nil
		result[index].Pool = compactMarketPool(event.Pool)
		result[index].Snapshot = compactMarketSnapshot(event.Snapshot)
	}
	return result
}

func compactMarketRule(rule *store.MarketRule) *store.MarketRule {
	if rule == nil {
		return nil
	}
	result := *rule
	result.State = nil
	return &result
}

func compactMarketEvent(event *store.MarketEvent) *store.MarketEvent {
	if event == nil {
		return nil
	}
	result := *event
	result.RawPayload = nil
	return &result
}

func compactMarketPool(pool *store.MarketPool) *store.MarketPool {
	if pool == nil {
		return nil
	}
	result := *pool
	result.Metadata = nil
	return &result
}

func compactMarketSnapshot(snapshot *store.MarketSnapshot) *store.MarketSnapshot {
	if snapshot == nil {
		return nil
	}
	result := *snapshot
	result.RawPayload = nil
	return &result
}
