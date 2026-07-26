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
						'balance_change', 'incoming', 'outgoing',
						'balance_threshold', 'balance_threshold_high'
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
						'balance_change', 'incoming', 'outgoing',
						'balance_threshold', 'balance_threshold_high'
					)
				) AS asset_event_count,
				COUNT(*) FILTER (WHERE ae.event_type = 'approval_change') AS approval_event_count,
				COUNT(*) FILTER (WHERE ae.event_type = 'address_interaction') AS interaction_event_count,
				COUNT(*) FILTER (WHERE ae.notification_status = 'failed') AS failed_notification_count
			FROM alert_events ae
			JOIN active_rules ar ON ar.id = ae.watch_rule_id
			WHERE ae.created_at >= $2 AND ae.created_at < $3
		),
		active_market_projects AS MATERIALIZED (
			SELECT id, chain_id, token_address
			FROM market_projects
			WHERE debox_user_id = $1 AND status = 'active'
		),
		market_rule_stats AS (
			SELECT
				(SELECT COUNT(*) FROM active_market_projects) AS market_project_count,
				COUNT(*) AS market_rule_count
			FROM market_rules mr
			JOIN active_market_projects amp ON amp.id = mr.market_project_id
			WHERE mr.enabled = 1 AND mr.run_status = 'active'
		),
		market_event_stats AS (
			SELECT
				COUNT(me.id) AS market_event_count,
				COUNT(me.id) FILTER (WHERE me.event_type = 'buy') AS market_buy_count,
				COUNT(me.id) FILTER (WHERE me.event_type = 'sell') AS market_sell_count,
				COALESCE(SUM(me.usd_value) FILTER (
					WHERE me.event_type = 'buy'
				), 0)::text AS market_buy_usd,
				COALESCE(SUM(me.usd_value) FILTER (
					WHERE me.event_type = 'sell'
				), 0)::text AS market_sell_usd,
				(
					COALESCE(SUM(me.usd_value) FILTER (
						WHERE me.event_type = 'buy'
					), 0) -
					COALESCE(SUM(me.usd_value) FILTER (
						WHERE me.event_type = 'sell'
					), 0)
				)::text AS market_net_buy_usd,
				COUNT(me.id) FILTER (
					WHERE me.event_type IN ('liquidity_added', 'liquidity_removed')
				) AS liquidity_event_count,
				COUNT(me.id) FILTER (
					WHERE me.event_type IN (
						'holder_increase', 'holder_decrease',
						'holder_rank_entered', 'holder_rank_exited'
					)
				) AS holder_event_count
			FROM active_market_projects amp
			LEFT JOIN market_events me
			  ON me.chain_id = amp.chain_id
			 AND me.token_address = amp.token_address
			 AND me.reorged = 0
			 AND me.occurred_at >= $2
			 AND me.occurred_at < $3
		),
		market_notification_stats AS (
			SELECT COUNT(*) FILTER (
				WHERE mre.notification_status = 'failed'
			) AS market_failed_notification_count
			FROM market_rule_events mre
			JOIN market_rules mr ON mr.id = mre.market_rule_id
			JOIN active_market_projects amp ON amp.id = mr.market_project_id
			WHERE mre.created_at >= $2 AND mre.created_at < $3
		)
		SELECT *
		FROM rule_stats
		CROSS JOIN event_stats
		CROSS JOIN market_rule_stats
		CROSS JOIN market_event_stats
		CROSS JOIN market_notification_stats
	`, deboxUserID, periodStart, periodEnd)
	if err != nil {
		return SummaryStatistics{}, fmt.Errorf("get daily summary statistics: %w", err)
	}
	return statistics, nil
}

func (s *Store) ListSummaryRecentMarketEvents(
	ctx context.Context,
	deboxUserID string,
	periodStart time.Time,
	periodEnd time.Time,
	limit int,
) ([]MarketSummaryEvent, error) {
	events, err := collectMany[MarketSummaryEvent](ctx, s.db, `
		SELECT
			me.id,
			mp.id AS market_project_id,
			mp.token_symbol,
			me.event_type,
			me.wallet_address,
			me.token_amount::text AS token_amount,
			me.usd_value::text AS usd_value,
			me.transaction_hash,
			me.occurred_at
		FROM market_projects mp
		JOIN market_events me
		  ON me.chain_id = mp.chain_id
		 AND me.token_address = mp.token_address
		WHERE mp.debox_user_id = $1
		  AND mp.status = 'active'
		  AND me.reorged = 0
		  AND me.occurred_at >= $2
		  AND me.occurred_at < $3
		ORDER BY me.occurred_at DESC, me.id DESC
		LIMIT $4
	`, deboxUserID, periodStart, periodEnd, clamp(limit, 1, 20))
	if err != nil {
		return nil, fmt.Errorf("list summary recent market events: %w", err)
	}
	return events, nil
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
