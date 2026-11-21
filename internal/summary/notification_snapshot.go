package summary

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const dailySummarySnapshotSchemaVersion = 2

type dailySummaryContent = store.DailySummaryReportContent

type dailySummarySnapshotDetails struct {
	SchemaVersion int                      `json:"schema_version"`
	Target        store.DailySummaryTarget `json:"target"`
	PeriodStart   time.Time                `json:"period_start"`
	PeriodEnd     time.Time                `json:"period_end"`
	dailySummaryContent
}

func (e *Executor) loadDailySummaryContent(
	ctx context.Context,
	subscription store.Subscription,
	periodStart time.Time,
	periodEnd time.Time,
) (dailySummaryContent, error) {
	if repository, ok := e.deps.Repository.(interface {
		LoadDailySummaryReportContent(context.Context, string, time.Time, time.Time, int) (store.DailySummaryReportContent, error)
	}); ok {
		return repository.LoadDailySummaryReportContent(ctx, subscription.DeBoxUserID, periodStart, periodEnd, recentEventLimit)
	}
	// Compatibility for pure in-memory readers. Store implements the consistent,
	// server-budgeted path above; no production database query uses this branch.
	ctx, cancel := context.WithTimeout(ctx, store.DailySummaryReadTimeout)
	defer cancel()
	statistics, err := e.deps.Repository.DailySummaryStatistics(
		ctx,
		subscription.DeBoxUserID,
		periodStart,
		periodEnd,
	)
	if err != nil {
		return dailySummaryContent{}, err
	}
	addressEvents, err := e.deps.Repository.ListSummaryRecentEvents(
		ctx,
		subscription.DeBoxUserID,
		periodStart,
		periodEnd,
		recentEventLimit,
	)
	if err != nil {
		return dailySummaryContent{}, err
	}
	marketEvents, err := e.deps.Repository.ListSummaryRecentMarketEvents(
		ctx,
		subscription.DeBoxUserID,
		periodStart,
		periodEnd,
		recentEventLimit,
	)
	if err != nil {
		return dailySummaryContent{}, err
	}
	marketSummaries, err := e.deps.Repository.ListDailyMarketProjectChainSummaries(
		ctx,
		subscription.DeBoxUserID,
		periodStart,
		periodEnd,
	)
	if err != nil {
		return dailySummaryContent{}, err
	}
	if addressEvents == nil {
		addressEvents = make([]store.SummaryEvent, 0)
	}
	if marketEvents == nil {
		marketEvents = make([]store.MarketSummaryEvent, 0)
	}
	if marketSummaries == nil {
		marketSummaries = make([]store.MarketProjectChainSummary, 0)
	}
	return dailySummaryContent{
		Statistics:      statistics,
		AddressEvents:   addressEvents,
		MarketEvents:    marketEvents,
		MarketSummaries: marketSummaries,
	}, nil
}

func (e *Executor) createDailySummarySnapshot(
	ctx context.Context,
	subscription store.Subscription,
	target store.DailySummaryTarget,
	periodStart time.Time,
	periodEnd time.Time,
	text string,
	content dailySummaryContent,
) (store.NotificationDetailSnapshot, error) {
	params, err := dailySummarySnapshotParams(subscription, target, periodStart, periodEnd, text, content)
	if err != nil {
		return store.NotificationDetailSnapshot{}, err
	}
	return e.deps.Repository.CreateNotificationDetailSnapshot(ctx, params)
}

func dailySummarySnapshotParams(subscription store.Subscription, target store.DailySummaryTarget, periodStart, periodEnd time.Time, text string, content dailySummaryContent) (store.CreateNotificationDetailSnapshotParams, error) {
	details, err := json.Marshal(dailySummarySnapshotDetails{
		SchemaVersion:       dailySummarySnapshotSchemaVersion,
		Target:              target,
		PeriodStart:         periodStart,
		PeriodEnd:           periodEnd,
		dailySummaryContent: content,
	})
	if err != nil {
		return store.CreateNotificationDetailSnapshotParams{}, fmt.Errorf(
			"marshal daily summary notification snapshot: %w",
			err,
		)
	}
	ruleName := "每日摘要"
	if normalizeLanguage(target.Language) == "en" {
		ruleName = "Daily summary"
	}
	sourceID := optionalPositiveInt64(target.ID)
	return store.CreateNotificationDetailSnapshotParams{
		SourceKey: fmt.Sprintf(
			"daily_summary:%d:%d:%s:%s:%d",
			subscription.ID,
			target.ID,
			strings.TrimSpace(target.ChatType),
			strings.TrimSpace(target.ChatID),
			periodEnd.UTC().UnixNano(),
		),
		NotificationKind: store.NotificationKindDailySummary,
		SourceType:       "daily_summary_target",
		SourceID:         sourceID,
		DeBoxUserID:      subscription.DeBoxUserID,
		RuleType:         "daily_summary",
		RuleName:         ruleName,
		RuleThreshold:    fmt.Sprintf("push_time=%s;timezone=%s", target.PushTime, target.Timezone),
		ActualValue: fmt.Sprintf(
			"address_events=%d;market_events=%d;market_anomalies=%d",
			content.Statistics.EventCount,
			content.Statistics.MarketEventCount,
			content.Statistics.MarketAnomalyCount,
		),
		NotificationChatID:   target.ChatID,
		NotificationChatType: target.ChatType,
		NotificationLanguage: target.Language,
		NotificationLabel:    target.Label,
		NotificationText:     text,
		Details:              details,
	}, nil
}

func optionalPositiveInt64(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}
