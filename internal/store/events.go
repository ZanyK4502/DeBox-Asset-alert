package store

import (
	"context"
	"fmt"
	"time"
)

func (s *Store) CountDailyAlertEvents(
	ctx context.Context,
	deboxUserID string,
	timezoneName string,
) (int64, error) {
	count, err := queryCount(ctx, s.db, `
		SELECT COUNT(*)
		FROM alert_events ae
		JOIN watch_rules wr ON wr.id = ae.watch_rule_id
		WHERE wr.debox_user_id = $1
		  AND (ae.created_at AT TIME ZONE $2)::date = (NOW() AT TIME ZONE $2)::date
	`, deboxUserID, timezoneName)
	if err != nil {
		return 0, fmt.Errorf("count daily alert events: %w", err)
	}
	return count, nil
}

type CreateAlertEventParams struct {
	WatchRuleID           int64
	EventType             string
	PreviousValue         *string
	CurrentValue          *string
	NotificationMessageID *string
	NotificationStatus    string
}

func (s *Store) CreateAlertEvent(
	ctx context.Context,
	params CreateAlertEventParams,
) (AlertEvent, error) {
	status := params.NotificationStatus
	if status == "" {
		status = "pending"
	}
	event, err := collectOne[AlertEvent](ctx, s.db, `
		INSERT INTO alert_events (
			watch_rule_id, event_type, previous_value, current_value,
			notification_message_id, notification_status
		)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+alertEventColumns,
		params.WatchRuleID,
		params.EventType,
		params.PreviousValue,
		params.CurrentValue,
		params.NotificationMessageID,
		status,
	)
	if err != nil {
		return AlertEvent{}, fmt.Errorf("create alert event: %w", err)
	}
	return event, nil
}

func (s *Store) UpdateAlertEventNotification(
	ctx context.Context,
	eventID int64,
	status string,
	messageID *string,
	notificationError string,
) (AlertEvent, error) {
	if status != "sent" && status != "failed" {
		return AlertEvent{}, ErrInvalidNotificationStatus
	}
	event, err := collectOne[AlertEvent](ctx, s.db, `
		UPDATE alert_events
		SET notification_message_id = $1,
		    notification_status = $2,
		    notification_error = $3,
		    notification_attempts = notification_attempts + 1,
		    notification_attempted_at = NOW(),
		    notification_sent_at = CASE WHEN $2 = 'sent' THEN NOW() ELSE NULL END
		WHERE id = $4
		RETURNING `+alertEventColumns,
		messageID,
		status,
		truncate(notificationError, 500),
		eventID,
	)
	if isNoRows(err) {
		return AlertEvent{}, ErrNotFound
	}
	if err != nil {
		return AlertEvent{}, fmt.Errorf("update alert event notification: %w", err)
	}
	return event, nil
}

func (s *Store) DailySummaryStatistics(
	ctx context.Context,
	deboxUserID string,
	periodStart time.Time,
	periodEnd time.Time,
) (SummaryStatistics, error) {
	statistics, err := collectOne[SummaryStatistics](ctx, s.db, `
		WITH active_rules AS MATERIALIZED (
			SELECT id, wallet_address, rule_type
			FROM watch_rules
			WHERE debox_user_id = $1
			  AND enabled = 1
			  AND run_status = 'active'
		),
		rule_stats AS (
			SELECT
				COUNT(*) AS rule_count,
				COUNT(DISTINCT LOWER(wallet_address)) AS wallet_count,
				COUNT(*) FILTER (
					WHERE rule_type IN (
						'balance_change', 'incoming', 'outgoing', 'balance_threshold'
					)
				) AS asset_rule_count,
				COUNT(*) FILTER (WHERE rule_type = 'approval_change') AS approval_rule_count,
				COUNT(*) FILTER (WHERE rule_type = 'address_interaction') AS interaction_rule_count
			FROM active_rules
		),
		event_stats AS (
			SELECT
				COUNT(*) AS event_count,
				COUNT(*) FILTER (
					WHERE ae.event_type IN (
						'balance_change', 'incoming', 'outgoing', 'balance_threshold'
					)
				) AS asset_event_count,
				COUNT(*) FILTER (WHERE ae.event_type = 'approval_change') AS approval_event_count,
				COUNT(*) FILTER (WHERE ae.event_type = 'address_interaction') AS interaction_event_count,
				COUNT(*) FILTER (WHERE ae.notification_status = 'failed') AS failed_notification_count
			FROM alert_events ae
			JOIN active_rules ar ON ar.id = ae.watch_rule_id
			WHERE ae.created_at >= $2 AND ae.created_at < $3
		)
		SELECT * FROM rule_stats CROSS JOIN event_stats
	`, deboxUserID, periodStart, periodEnd)
	if err != nil {
		return SummaryStatistics{}, fmt.Errorf("get daily summary statistics: %w", err)
	}
	return statistics, nil
}

func (s *Store) ListSummaryRecentEvents(
	ctx context.Context,
	deboxUserID string,
	periodStart time.Time,
	periodEnd time.Time,
	limit int,
) ([]SummaryEvent, error) {
	events, err := collectMany[SummaryEvent](ctx, s.db, `
		SELECT `+alertEventColumnsQualified+`,
		       wr.chain_key, wr.wallet_address, wr.token_address,
		       wr.rule_type, wr.target_address
		FROM alert_events ae
		JOIN watch_rules wr ON wr.id = ae.watch_rule_id
		WHERE wr.debox_user_id = $1
		  AND wr.enabled = 1
		  AND wr.run_status = 'active'
		  AND ae.created_at >= $2
		  AND ae.created_at < $3
		ORDER BY ae.created_at DESC, ae.id DESC
		LIMIT $4
	`, deboxUserID, periodStart, periodEnd, clamp(limit, 1, 20))
	if err != nil {
		return nil, fmt.Errorf("list summary recent events: %w", err)
	}
	return events, nil
}
