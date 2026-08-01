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
		address_events AS MATERIALIZED (
			SELECT ae.event_type, ae.created_at AS occurred_at
			FROM alert_events ae
			JOIN active_rules ar ON ar.id = ae.watch_rule_id
			WHERE ae.created_at >= $2 AND ae.created_at < $3
			UNION ALL
			SELECT
				rte.event_type,
				COALESCE(rte.occurred_at, rte.detected_at, rte.created_at) AS occurred_at
			FROM rule_trigger_events rte
			JOIN active_rules ar ON ar.id = rte.watch_rule_id
			WHERE COALESCE(rte.occurred_at, rte.detected_at, rte.created_at) >= $2
			  AND COALESCE(rte.occurred_at, rte.detected_at, rte.created_at) < $3
		),
		event_stats AS (
			SELECT
				COUNT(*) AS event_count,
				COUNT(*) FILTER (
					WHERE event_type IN (
						'balance_change', 'incoming', 'outgoing',
						'balance_threshold', 'balance_threshold_high'
					)
				) AS asset_event_count,
				COUNT(*) FILTER (WHERE event_type = 'approval_change') AS approval_event_count,
				COUNT(*) FILTER (WHERE event_type = 'address_interaction') AS interaction_event_count,
				COUNT(*) FILTER (
					WHERE event_type IN (
						'outgoing', 'balance_threshold',
						'approval_change', 'address_interaction'
					)
				) AS address_risk_event_count,
				COUNT(*) FILTER (WHERE event_type = 'incoming') AS address_incoming_count,
				COUNT(*) FILTER (WHERE event_type = 'outgoing') AS address_outgoing_count
			FROM address_events
		),
		address_notification_stats AS (
			SELECT (
				(
					SELECT COUNT(*)
					FROM alert_events ae
					JOIN active_rules ar ON ar.id = ae.watch_rule_id
					WHERE ae.notification_status = 'failed'
					  AND ae.created_at >= $2 AND ae.created_at < $3
				) + (
					SELECT COUNT(*)
					FROM aggregate_notifications an
					WHERE an.debox_user_id = $1
					  AND an.notification_status = 'failed'
					  AND an.created_at >= $2 AND an.created_at < $3
				)
			) AS failed_notification_count
		),
		active_market_projects AS MATERIALIZED (
			SELECT id
			FROM market_projects
			WHERE debox_user_id = $1 AND status = 'active'
		),
		active_market_deployments AS MATERIALIZED (
			SELECT
				mp.id AS market_project_id,
				mad.chain_id,
				mad.token_address
			FROM market_projects mp
			JOIN market_project_deployments mpd
			  ON mpd.market_project_id = mp.id
			 AND mpd.status = 'active'
			JOIN market_asset_deployments mad
			  ON mad.id = mpd.market_asset_deployment_id
			WHERE mp.debox_user_id = $1 AND mp.status = 'active'
			UNION
			SELECT
				mp.id AS market_project_id,
				mp.chain_id,
				mp.token_address
			FROM market_projects mp
			WHERE mp.debox_user_id = $1
			  AND mp.status = 'active'
			  AND NOT EXISTS (
				SELECT 1
				FROM market_project_deployments mpd
				WHERE mpd.market_project_id = mp.id
				  AND mpd.status = 'active'
			  )
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
			FROM market_events me
			WHERE me.reorged = 0
			  AND me.occurred_at >= $2
			  AND me.occurred_at < $3
			  AND EXISTS (
				SELECT 1
				FROM active_market_deployments amd
				WHERE amd.chain_id = me.chain_id
				  AND amd.token_address = me.token_address
			  )
		),
		market_notification_stats AS (
			SELECT
				COUNT(*) AS market_anomaly_count,
				(
					COUNT(*) FILTER (
						WHERE mre.notification_status = 'failed'
					) + (
						SELECT COUNT(*)
						FROM market_stage_windows msw
						JOIN market_rules stage_rule ON stage_rule.id = msw.market_rule_id
						JOIN active_market_projects stage_project
						  ON stage_project.id = stage_rule.market_project_id
						WHERE msw.notification_status = 'failed'
						  AND msw.created_at >= $2 AND msw.created_at < $3
					) + (
						SELECT COUNT(*)
						FROM market_combination_windows mcw
						WHERE mcw.debox_user_id = $1
						  AND mcw.notification_status = 'failed'
						  AND mcw.created_at >= $2 AND mcw.created_at < $3
					)
				) AS market_failed_notification_count
			FROM market_rule_events mre
			JOIN market_rules mr ON mr.id = mre.market_rule_id
			JOIN active_market_projects amp ON amp.id = mr.market_project_id
			WHERE mre.created_at >= $2 AND mre.created_at < $3
		)
		SELECT *
		FROM rule_stats
		CROSS JOIN event_stats
		CROSS JOIN address_notification_stats
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
		WITH active_market_deployments AS MATERIALIZED (
			SELECT
				mp.id AS market_project_id,
				mp.token_name,
				mp.token_symbol,
				mad.chain_id,
				mad.token_address
			FROM market_projects mp
			JOIN market_project_deployments mpd
			  ON mpd.market_project_id = mp.id
			 AND mpd.status = 'active'
			JOIN market_asset_deployments mad
			  ON mad.id = mpd.market_asset_deployment_id
			WHERE mp.debox_user_id = $1 AND mp.status = 'active'
			UNION
			SELECT
				mp.id AS market_project_id,
				mp.token_name,
				mp.token_symbol,
				mp.chain_id,
				mp.token_address
			FROM market_projects mp
			WHERE mp.debox_user_id = $1
			  AND mp.status = 'active'
			  AND NOT EXISTS (
				SELECT 1
				FROM market_project_deployments mpd
				WHERE mpd.market_project_id = mp.id
				  AND mpd.status = 'active'
			  )
		)
		SELECT
			me.id,
			matched.market_project_id,
			matched.token_name,
			matched.token_symbol,
			me.chain_key,
			me.token_address,
			COALESCE(pool.protocol, '') AS protocol,
			COALESCE(pool.protocol_version, '') AS protocol_version,
			pool.pool_address,
			me.event_type,
			me.wallet_address,
			me.token_amount::text AS token_amount,
			me.usd_value::text AS usd_value,
			me.transaction_hash,
			me.occurred_at
		FROM market_events me
		JOIN LATERAL (
			SELECT amd.*
			FROM active_market_deployments amd
			WHERE amd.chain_id = me.chain_id
			  AND amd.token_address = me.token_address
			ORDER BY amd.market_project_id
			LIMIT 1
		) matched ON TRUE
		LEFT JOIN market_pools pool ON pool.id = me.market_pool_id
		WHERE me.reorged = 0
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

func (s *Store) ListDailyMarketProjectChainSummaries(
	ctx context.Context,
	deboxUserID string,
	periodStart time.Time,
	periodEnd time.Time,
) ([]MarketProjectChainSummary, error) {
	summaries, err := collectMany[MarketProjectChainSummary](ctx, s.db, `
		WITH active_deployments AS MATERIALIZED (
			SELECT
				mp.id AS market_project_id,
				mp.token_name,
				mp.token_symbol,
				mad.chain_key,
				mad.chain_id,
				mad.token_address,
				mpd.default_market_pool_id
			FROM market_projects mp
			JOIN market_project_deployments mpd
			  ON mpd.market_project_id = mp.id
			 AND mpd.status = 'active'
			JOIN market_asset_deployments mad
			  ON mad.id = mpd.market_asset_deployment_id
			WHERE mp.debox_user_id = $1 AND mp.status = 'active'
			UNION
			SELECT
				mp.id AS market_project_id,
				mp.token_name,
				mp.token_symbol,
				mp.chain_key,
				mp.chain_id,
				mp.token_address,
				mp.main_pool_id AS default_market_pool_id
			FROM market_projects mp
			WHERE mp.debox_user_id = $1
			  AND mp.status = 'active'
			  AND NOT EXISTS (
				SELECT 1
				FROM market_project_deployments mpd
				WHERE mpd.market_project_id = mp.id
				  AND mpd.status = 'active'
			  )
		)
		SELECT
			ad.market_project_id,
			ad.token_name,
			ad.token_symbol,
			ad.chain_key,
			ad.chain_id,
			ad.token_address,
			start_snapshot.price_usd::text AS start_price_usd,
			end_snapshot.price_usd::text AS end_price_usd,
			COALESCE(coverage.snapshot_count, 0) AS snapshot_count,
			COALESCE(coverage.price_sample_count, 0) AS price_sample_count,
			COALESCE(coverage.liquidity_sample_count, 0) AS liquidity_sample_count,
			COALESCE(coverage.volume_sample_count, 0) AS volume_sample_count,
			COALESCE(events.trade_volume_usd, 0)::text AS trade_volume_usd,
			COALESCE(events.buy_count, 0) AS buy_count,
			COALESCE(events.buy_usd, 0)::text AS buy_usd,
			COALESCE(events.sell_count, 0) AS sell_count,
			COALESCE(events.sell_usd, 0)::text AS sell_usd,
			COALESCE(large_trades.large_trade_count, 0) AS large_trade_count,
			COALESCE(events.holder_increase_count, 0) AS holder_increase_count,
			COALESCE(events.holder_decrease_count, 0) AS holder_decrease_count,
			COALESCE(events.holder_rank_enter_count, 0) AS holder_rank_enter_count,
			COALESCE(events.holder_rank_exit_count, 0) AS holder_rank_exit_count
		FROM active_deployments ad
		LEFT JOIN LATERAL (
			SELECT
				COALESCE(SUM(me.usd_value) FILTER (
					WHERE me.event_type IN ('buy', 'sell')
				), 0) AS trade_volume_usd,
				COUNT(*) FILTER (WHERE me.event_type = 'buy') AS buy_count,
				COALESCE(SUM(me.usd_value) FILTER (
					WHERE me.event_type = 'buy'
				), 0) AS buy_usd,
				COUNT(*) FILTER (WHERE me.event_type = 'sell') AS sell_count,
				COALESCE(SUM(me.usd_value) FILTER (
					WHERE me.event_type = 'sell'
				), 0) AS sell_usd,
				COUNT(*) FILTER (
					WHERE me.event_type = 'holder_increase'
				) AS holder_increase_count,
				COUNT(*) FILTER (
					WHERE me.event_type = 'holder_decrease'
				) AS holder_decrease_count,
				COUNT(*) FILTER (
					WHERE me.event_type = 'holder_rank_entered'
				) AS holder_rank_enter_count,
				COUNT(*) FILTER (
					WHERE me.event_type = 'holder_rank_exited'
				) AS holder_rank_exit_count
			FROM market_events me
			WHERE me.chain_id = ad.chain_id
			  AND me.token_address = ad.token_address
			  AND me.reorged = 0
			  AND me.occurred_at >= $2
			  AND me.occurred_at < $3
		) events ON TRUE
		LEFT JOIN LATERAL (
			SELECT COUNT(DISTINCT mre.market_event_id) AS large_trade_count
			FROM market_rule_events mre
			JOIN market_rules mr ON mr.id = mre.market_rule_id
			JOIN market_events me ON me.id = mre.market_event_id
			WHERE mr.market_project_id = ad.market_project_id
			  AND mr.rule_type IN (
				'market_large_buy',
				'market_large_sell',
				'market_consecutive_large_buy',
				'market_consecutive_large_sell',
				'market_four_meme_large_trade'
			  )
			  AND me.chain_id = ad.chain_id
			  AND me.token_address = ad.token_address
			  AND me.reorged = 0
			  AND me.occurred_at >= $2
			  AND me.occurred_at < $3
		) large_trades ON TRUE
		LEFT JOIN LATERAL (
			SELECT ms.price_usd
			FROM market_snapshots ms
			WHERE ms.chain_id = ad.chain_id
			  AND ms.token_address = ad.token_address
			  AND ms.price_usd IS NOT NULL
			  AND (
				ad.default_market_pool_id IS NULL
				OR ms.market_pool_id = ad.default_market_pool_id
			  )
			  AND ms.captured_at >= $2
			  AND ms.captured_at < $3
			ORDER BY ms.captured_at, ms.id
			LIMIT 1
		) start_snapshot ON TRUE
		LEFT JOIN LATERAL (
			SELECT ms.price_usd
			FROM market_snapshots ms
			WHERE ms.chain_id = ad.chain_id
			  AND ms.token_address = ad.token_address
			  AND ms.price_usd IS NOT NULL
			  AND (
				ad.default_market_pool_id IS NULL
				OR ms.market_pool_id = ad.default_market_pool_id
			  )
			  AND ms.captured_at >= $2
			  AND ms.captured_at < $3
			ORDER BY ms.captured_at DESC, ms.id DESC
			LIMIT 1
		) end_snapshot ON TRUE
		LEFT JOIN LATERAL (
			SELECT
				COUNT(*) AS snapshot_count,
				COUNT(ms.price_usd) AS price_sample_count,
				COUNT(ms.liquidity_usd) AS liquidity_sample_count,
				COUNT(*) FILTER (
					WHERE ms.volume_5m_usd IS NOT NULL
					   OR ms.volume_15m_usd IS NOT NULL
					   OR ms.volume_1h_usd IS NOT NULL
					   OR ms.volume_6h_usd IS NOT NULL
					   OR ms.volume_24h_usd IS NOT NULL
				) AS volume_sample_count
			FROM market_snapshots ms
			WHERE ms.chain_id = ad.chain_id
			  AND ms.token_address = ad.token_address
			  AND (
				ad.default_market_pool_id IS NULL
				OR ms.market_pool_id = ad.default_market_pool_id
			  )
			  AND ms.captured_at >= $2
			  AND ms.captured_at < $3
		) coverage ON TRUE
		ORDER BY ad.market_project_id, ad.chain_id
	`, deboxUserID, periodStart, periodEnd)
	if err != nil {
		return nil, fmt.Errorf("list daily market project chain summaries: %w", err)
	}
	return summaries, nil
}

func (s *Store) ListSummaryRecentEvents(
	ctx context.Context,
	deboxUserID string,
	periodStart time.Time,
	periodEnd time.Time,
	limit int,
) ([]SummaryEvent, error) {
	events, err := collectMany[SummaryEvent](ctx, s.db, `
		WITH summary_events AS (
			SELECT
				ae.id,
				ae.watch_rule_id,
				ae.event_type,
				ae.previous_value,
				ae.current_value,
				ae.notification_message_id,
				ae.notification_status,
				ae.notification_error,
				ae.notification_attempts,
				ae.notification_attempted_at,
				ae.notification_sent_at,
				ae.created_at,
				wr.chain_key,
				wr.wallet_address,
				wr.token_address,
				wr.rule_type,
				wr.target_address,
				ae.created_at AS sort_at
			FROM alert_events ae
			JOIN watch_rules wr ON wr.id = ae.watch_rule_id
			WHERE wr.debox_user_id = $1
			  AND wr.enabled = 1
			  AND wr.run_status = 'active'
			  AND ae.created_at >= $2
			  AND ae.created_at < $3

			UNION ALL

			SELECT
				rte.id,
				rte.watch_rule_id,
				rte.event_type,
				rte.previous_value,
				rte.current_value,
				an.notification_message_id,
				COALESCE(an.notification_status, 'staged') AS notification_status,
				COALESCE(an.notification_error, '') AS notification_error,
				COALESCE(an.notification_attempts, 0) AS notification_attempts,
				an.notification_attempted_at,
				an.notification_sent_at,
				rte.created_at,
				wr.chain_key,
				wr.wallet_address,
				wr.token_address,
				wr.rule_type,
				wr.target_address,
				COALESCE(rte.occurred_at, rte.detected_at, rte.created_at) AS sort_at
			FROM rule_trigger_events rte
			JOIN watch_rules wr ON wr.id = rte.watch_rule_id
			LEFT JOIN aggregate_notifications an
			  ON an.aggregation_window_id = rte.aggregation_window_id
			WHERE wr.debox_user_id = $1
			  AND wr.enabled = 1
			  AND wr.run_status = 'active'
			  AND COALESCE(rte.occurred_at, rte.detected_at, rte.created_at) >= $2
			  AND COALESCE(rte.occurred_at, rte.detected_at, rte.created_at) < $3
		)
		SELECT
			id,
			watch_rule_id,
			event_type,
			previous_value,
			current_value,
			notification_message_id,
			notification_status,
			notification_error,
			notification_attempts,
			notification_attempted_at,
			notification_sent_at,
			created_at,
			chain_key,
			wallet_address,
			token_address,
			rule_type,
			target_address
		FROM summary_events
		ORDER BY sort_at DESC, id DESC
		LIMIT $4
	`, deboxUserID, periodStart, periodEnd, clamp(limit, 1, 20))
	if err != nil {
		return nil, fmt.Errorf("list summary recent events: %w", err)
	}
	return events, nil
}
