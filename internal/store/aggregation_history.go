package store

import (
	"context"
	"fmt"
)

const AggregationHistoryRetentionDays = 30

type AggregationCleanupResult struct {
	NotificationsDeleted int64
	EventsDeleted        int64
	WindowsDeleted       int64
}

func (s *Store) CleanupAggregationHistory(
	ctx context.Context,
) (AggregationCleanupResult, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (AggregationCleanupResult, error) {
		result := AggregationCleanupResult{}

		tag, err := tx.Exec(ctx, `
			DELETE FROM aggregate_notifications an
			WHERE an.created_at < NOW() - ($1 * INTERVAL '1 day')
			  AND NOT EXISTS (
				SELECT 1
				FROM rule_trigger_events rte
				WHERE rte.aggregation_window_id = an.aggregation_window_id
				  AND rte.created_at >= NOW() - ($1 * INTERVAL '1 day')
			  )
		`, AggregationHistoryRetentionDays)
		if err != nil {
			return result, fmt.Errorf("delete expired aggregate notifications: %w", err)
		}
		result.NotificationsDeleted = tag.RowsAffected()

		tag, err = tx.Exec(ctx, `
			DELETE FROM rule_trigger_events
			WHERE created_at < NOW() - ($1 * INTERVAL '1 day')
		`, AggregationHistoryRetentionDays)
		if err != nil {
			return result, fmt.Errorf("delete expired aggregation events: %w", err)
		}
		result.EventsDeleted = tag.RowsAffected()

		tag, err = tx.Exec(ctx, `
			DELETE FROM aggregation_windows aw
			WHERE aw.ends_at < NOW() - ($1 * INTERVAL '1 day')
			  AND NOT EXISTS (
				SELECT 1
				FROM rule_trigger_events rte
				WHERE rte.aggregation_window_id = aw.id
			  )
			  AND NOT EXISTS (
				SELECT 1
				FROM aggregate_notifications an
				WHERE an.aggregation_window_id = aw.id
			  )
		`, AggregationHistoryRetentionDays)
		if err != nil {
			return result, fmt.Errorf("delete expired aggregation windows: %w", err)
		}
		result.WindowsDeleted = tag.RowsAffected()

		return result, nil
	})
}

func (s *Store) ListAggregationEventHistory(
	ctx context.Context,
	deboxUserID string,
	beforeID int64,
	limit int,
) (AggregationHistoryPage, error) {
	pageLimit := clamp(limit, 1, 100)
	if beforeID < 0 {
		beforeID = 0
	}
	events, err := collectMany[AggregationHistoryEvent](ctx, s.db, `
		SELECT
			rte.id,
			rte.aggregation_window_id,
			aw.source_type,
			rte.watch_rule_id,
			aw.combination_rule_id,
			COALESCE(cr.note, '') AS combination_note,
			wr.chain_key,
			wr.chain_id,
			wr.wallet_address,
			wr.token_address,
			wr.target_address,
			wr.target_label,
			wr.rule_type,
			rte.event_type,
			rte.event_key,
			rte.previous_value,
			rte.current_value,
			COALESCE(rte.details->>'note', '') AS note,
			CASE
				WHEN aw.source_type = 'combination' THEN cr.cycle_type
				ELSE wr.cycle_type
			END AS cycle_type,
			CASE
				WHEN aw.source_type = 'combination' THEN cr.cycle_minutes
				ELSE wr.cycle_minutes
			END AS cycle_minutes,
			COALESCE(awm.required_trigger_count, wr.trigger_count_threshold)
				AS required_trigger_count,
			aw.total_trigger_count AS window_total_trigger_count,
			aw.starts_at AS window_starts_at,
			aw.ends_at AS window_ends_at,
			COALESCE(
				an.notification_kind,
				CASE
					WHEN aw.source_type = 'combination' THEN 'combination'
					ELSE 'stage'
				END
			) AS notification_kind,
			COALESCE(an.notification_status, 'not_sent') AS notification_status,
			COALESCE(an.notification_error, '') AS notification_error,
			an.notification_sent_at,
			COALESCE(rte.occurred_at, rte.detected_at) AS occurred_at,
			rte.detected_at,
			rte.created_at
		FROM rule_trigger_events rte
		JOIN aggregation_windows aw
		  ON aw.id = rte.aggregation_window_id
		JOIN watch_rules wr
		  ON wr.id = rte.watch_rule_id
		LEFT JOIN combination_rules cr
		  ON cr.id = aw.combination_rule_id
		LEFT JOIN aggregation_window_members awm
		  ON awm.aggregation_window_id = aw.id
		 AND awm.watch_rule_id = rte.watch_rule_id
		LEFT JOIN aggregate_notifications an
		  ON an.aggregation_window_id = aw.id
		WHERE rte.debox_user_id = $1
		  AND rte.created_at >= NOW() - ($4 * INTERVAL '1 day')
		  AND ($2 = 0 OR rte.id < $2)
		ORDER BY rte.id DESC
		LIMIT $3
	`,
		deboxUserID,
		beforeID,
		pageLimit+1,
		AggregationHistoryRetentionDays,
	)
	if err != nil {
		return AggregationHistoryPage{}, fmt.Errorf("list aggregation event history: %w", err)
	}

	hasMore := len(events) > pageLimit
	if hasMore {
		events = events[:pageLimit]
	}
	var nextBeforeID *int64
	if hasMore && len(events) > 0 {
		cursor := events[len(events)-1].ID
		nextBeforeID = &cursor
	}

	stats, err := collectOne[AggregationHistoryStats](ctx, s.db, `
		WITH event_stats AS (
			SELECT
				COUNT(*) AS event_count,
				COUNT(*) FILTER (WHERE aw.source_type = 'rule') AS stage_event_count,
				COUNT(*) FILTER (WHERE aw.source_type = 'combination')
					AS combination_event_count
			FROM rule_trigger_events rte
			JOIN aggregation_windows aw ON aw.id = rte.aggregation_window_id
			WHERE rte.debox_user_id = $1
			  AND rte.created_at >= NOW() - ($2 * INTERVAL '1 day')
		),
		notification_stats AS (
			SELECT
				COUNT(*) AS notification_count,
				COUNT(*) FILTER (WHERE notification_status = 'sent')
					AS sent_notification_count,
				COUNT(*) FILTER (WHERE notification_status = 'failed')
					AS failed_notification_count,
				COUNT(*) FILTER (WHERE notification_status = 'pending')
					AS pending_notification_count
			FROM aggregate_notifications
			WHERE debox_user_id = $1
			  AND created_at >= NOW() - ($2 * INTERVAL '1 day')
		)
		SELECT
			event_stats.event_count,
			event_stats.stage_event_count,
			event_stats.combination_event_count,
			notification_stats.notification_count,
			notification_stats.sent_notification_count,
			notification_stats.failed_notification_count,
			notification_stats.pending_notification_count
		FROM event_stats
		CROSS JOIN notification_stats
	`, deboxUserID, AggregationHistoryRetentionDays)
	if err != nil {
		return AggregationHistoryPage{}, fmt.Errorf("count aggregation event history: %w", err)
	}

	return AggregationHistoryPage{
		Events:        events,
		Stats:         stats,
		RetentionDays: AggregationHistoryRetentionDays,
		HasMore:       hasMore,
		NextBeforeID:  nextBeforeID,
	}, nil
}
