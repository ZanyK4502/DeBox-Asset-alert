package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type addressNotificationSnapshotPayload struct {
	SchemaVersion int                             `json:"schema_version"`
	Rule          *store.WatchRule                `json:"rule,omitempty"`
	AlertEvent    *store.AlertEvent               `json:"alert_event,omitempty"`
	Stage         *store.StageTriggerResult       `json:"stage,omitempty"`
	Combination   *store.CombinationTriggerResult `json:"combination,omitempty"`
	TokenSymbol   string                          `json:"token_symbol,omitempty"`
	Note          string                          `json:"note,omitempty"`
	OccurredAt    *time.Time                      `json:"occurred_at,omitempty"`
}

func (e *Executor) createAddressRealtimeSnapshot(
	ctx context.Context,
	rule store.WatchRule,
	event store.AlertEvent,
	tokenSymbol string,
	note string,
	occurredAt time.Time,
	text string,
) (store.NotificationDetailSnapshot, error) {
	details, err := json.Marshal(addressNotificationSnapshotPayload{
		SchemaVersion: 1,
		Rule:          &rule,
		AlertEvent:    &event,
		TokenSymbol:   strings.TrimSpace(tokenSymbol),
		Note:          strings.TrimSpace(note),
		OccurredAt:    &occurredAt,
	})
	if err != nil {
		return store.NotificationDetailSnapshot{}, fmt.Errorf(
			"encode address realtime notification snapshot: %w",
			err,
		)
	}
	eventID := event.ID
	ruleID := rule.ID
	return e.deps.Repository.CreateNotificationDetailSnapshot(
		ctx,
		store.CreateNotificationDetailSnapshotParams{
			SourceKey:            fmt.Sprintf("address_realtime:%d", event.ID),
			NotificationKind:     store.NotificationKindAddressRealtime,
			SourceType:           "alert_event",
			SourceID:             &eventID,
			DeBoxUserID:          rule.DeBoxUserID,
			RuleID:               &ruleID,
			RuleType:             rule.RuleType,
			RuleName:             addressSnapshotRuleName(rule.RuleType, rule.NotificationLanguage),
			RuleThreshold:        rule.Threshold,
			ActualValue:          stringValue(event.CurrentValue),
			NotificationChatID:   rule.NotificationChatID,
			NotificationChatType: rule.NotificationChatType,
			NotificationLanguage: rule.NotificationLanguage,
			NotificationLabel:    rule.NotificationLabel,
			NotificationText:     text,
			Details:              details,
		},
	)
}

func (e *Executor) createAddressStageSnapshot(
	ctx context.Context,
	rule store.WatchRule,
	result store.StageTriggerResult,
	text string,
) (store.NotificationDetailSnapshot, error) {
	details, err := json.Marshal(addressNotificationSnapshotPayload{
		SchemaVersion: 1,
		Rule:          &rule,
		Stage:         &result,
	})
	if err != nil {
		return store.NotificationDetailSnapshot{}, fmt.Errorf(
			"encode address stage notification snapshot: %w",
			err,
		)
	}
	if result.Notification == nil {
		return store.NotificationDetailSnapshot{}, errorsNewMissingNotification("stage")
	}
	notificationID := result.Notification.ID
	ruleID := rule.ID
	threshold := strings.TrimSpace(rule.Threshold)
	if result.TriggerCountThreshold > 0 {
		threshold = appendSnapshotCondition(
			threshold,
			"send_after="+strconv.FormatInt(result.TriggerCountThreshold, 10),
		)
	}
	return e.deps.Repository.CreateNotificationDetailSnapshot(
		ctx,
		store.CreateNotificationDetailSnapshotParams{
			SourceKey:            fmt.Sprintf("address_stage:%d", notificationID),
			NotificationKind:     store.NotificationKindAddressStage,
			SourceType:           "aggregate_notification",
			SourceID:             &notificationID,
			DeBoxUserID:          rule.DeBoxUserID,
			RuleID:               &ruleID,
			RuleType:             rule.RuleType,
			RuleName:             addressSnapshotRuleName(rule.RuleType, rule.NotificationLanguage),
			RuleThreshold:        threshold,
			ActualValue:          addressStageSnapshotActual(result),
			NotificationChatID:   result.Notification.NotificationChatID,
			NotificationChatType: result.Notification.NotificationChatType,
			NotificationLanguage: result.Notification.NotificationLanguage,
			NotificationLabel:    result.Notification.NotificationLabel,
			NotificationText:     text,
			Details:              details,
		},
	)
}

func (e *Executor) createAddressCombinationSnapshot(
	ctx context.Context,
	triggerRule store.WatchRule,
	result store.CombinationTriggerResult,
	text string,
) (store.NotificationDetailSnapshot, error) {
	details, err := json.Marshal(addressNotificationSnapshotPayload{
		SchemaVersion: 1,
		Rule:          &triggerRule,
		Combination:   &result,
	})
	if err != nil {
		return store.NotificationDetailSnapshot{}, fmt.Errorf(
			"encode address combination notification snapshot: %w",
			err,
		)
	}
	if result.Notification == nil {
		return store.NotificationDetailSnapshot{}, errorsNewMissingNotification("combination")
	}
	notificationID := result.Notification.ID
	combinationRuleID := result.CombinationRuleID
	english := normalizeLanguage(result.Notification.NotificationLanguage) == "en"
	ruleName := "地址组合提醒"
	if english {
		ruleName = "Address combination alert"
	}
	return e.deps.Repository.CreateNotificationDetailSnapshot(
		ctx,
		store.CreateNotificationDetailSnapshotParams{
			SourceKey:            fmt.Sprintf("address_combination:%d", notificationID),
			NotificationKind:     store.NotificationKindAddressCombination,
			SourceType:           "aggregate_notification",
			SourceID:             &notificationID,
			DeBoxUserID:          result.Notification.DeBoxUserID,
			RuleID:               &combinationRuleID,
			RuleType:             "combination",
			RuleName:             ruleName,
			RuleThreshold:        addressCombinationSnapshotThreshold(result.MemberProgress),
			ActualValue:          addressCombinationSnapshotActual(result.MemberProgress),
			NotificationChatID:   result.Notification.NotificationChatID,
			NotificationChatType: result.Notification.NotificationChatType,
			NotificationLanguage: result.Notification.NotificationLanguage,
			NotificationLabel:    result.Notification.NotificationLabel,
			NotificationText:     text,
			Details:              details,
		},
	)
}

func addressSnapshotRuleName(ruleType, language string) string {
	if label := ruleTypeLabels[normalizeLanguage(language)][ruleType]; label != "" {
		return label
	}
	return strings.ReplaceAll(ruleType, "_", " ")
}

func addressStageSnapshotActual(result store.StageTriggerResult) string {
	for index := len(result.Events) - 1; index >= 0; index-- {
		if value := stringValue(result.Events[index].CurrentValue); value != "" {
			return value
		}
	}
	return strconv.FormatInt(result.TotalTriggerCount, 10)
}

func addressCombinationSnapshotThreshold(
	members []store.CombinationMemberProgress,
) string {
	values := make([]string, 0, len(members))
	for _, member := range members {
		value := member.RuleType + ">=" + strconv.FormatInt(member.RequiredTriggerCount, 10)
		if member.Rule != nil && strings.TrimSpace(member.Rule.Threshold) != "" {
			value += "@" + strings.TrimSpace(member.Rule.Threshold)
		}
		values = append(values, value)
	}
	return strings.Join(values, ";")
}

func addressCombinationSnapshotActual(
	members []store.CombinationMemberProgress,
) string {
	values := make([]string, 0, len(members))
	for _, member := range members {
		values = append(values, member.RuleType+"="+
			strconv.FormatInt(member.TriggerCount, 10))
	}
	return strings.Join(values, ";")
}

func appendSnapshotCondition(current, addition string) string {
	if strings.TrimSpace(current) == "" {
		return addition
	}
	return current + ";" + addition
}

func errorsNewMissingNotification(kind string) error {
	return fmt.Errorf("%s notification snapshot is missing its notification record", kind)
}
