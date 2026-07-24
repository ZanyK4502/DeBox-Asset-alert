package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

const stageRecentEventLimit = 5

type RecordStageTriggerParams struct {
	WatchRuleID   int64
	DeBoxUserID   string
	PreviousValue *string
	CurrentValue  *string
	Note          string
	EventKey      string
	OccurredAt    *time.Time
}

type stageRuleConfiguration struct {
	ID                    int64     `db:"id"`
	DeBoxUserID           string    `db:"debox_user_id"`
	RuleType              string    `db:"rule_type"`
	DeliveryMode          string    `db:"delivery_mode"`
	CycleType             string    `db:"cycle_type"`
	CycleMinutes          int32     `db:"cycle_minutes"`
	TriggerCountThreshold int64     `db:"trigger_count_threshold"`
	AggregationAnchorAt   time.Time `db:"aggregation_anchor_at"`
	NotificationChatID    string    `db:"notification_chat_id"`
	NotificationChatType  string    `db:"notification_chat_type"`
	NotificationLabel     string    `db:"notification_label"`
	NotificationLanguage  string    `db:"notification_language"`
	Enabled               int32     `db:"enabled"`
	RunStatus             string    `db:"run_status"`
}

type stageEventNote struct {
	Note string `db:"note"`
}

func (s *Store) RecordStageTrigger(
	ctx context.Context,
	params RecordStageTriggerParams,
) (StageTriggerResult, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (StageTriggerResult, error) {
		config, err := collectOne[stageRuleConfiguration](ctx, tx, `
			SELECT id, debox_user_id, rule_type, delivery_mode, cycle_type,
			       cycle_minutes, trigger_count_threshold,
			       COALESCE(aggregation_anchor_at, created_at) AS aggregation_anchor_at,
			       notification_chat_id, notification_chat_type,
			       notification_label, notification_language, enabled, run_status
			FROM watch_rules
			WHERE id = $1 AND debox_user_id = $2
			FOR UPDATE
		`, params.WatchRuleID, params.DeBoxUserID)
		if isNoRows(err) {
			return StageTriggerResult{}, ErrNotFound
		}
		if err != nil {
			return StageTriggerResult{}, fmt.Errorf("lock stage watch rule: %w", err)
		}
		if config.Enabled != 1 || config.RunStatus != "active" {
			return StageTriggerResult{}, ErrNotFound
		}
		if config.DeliveryMode != "stage" {
			return StageTriggerResult{}, ErrStageDeliveryRequired
		}

		var now time.Time
		if err := tx.QueryRow(ctx, "SELECT NOW()").Scan(&now); err != nil {
			return StageTriggerResult{}, fmt.Errorf("get aggregation time: %w", err)
		}
		window, err := currentStageWindow(ctx, tx, config, now)
		if err != nil {
			return StageTriggerResult{}, err
		}

		details, err := json.Marshal(map[string]string{"note": truncate(params.Note, 1000)})
		if err != nil {
			return StageTriggerResult{}, fmt.Errorf("encode stage trigger details: %w", err)
		}
		var triggerEventID int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO rule_trigger_events (
				debox_user_id, watch_rule_id, aggregation_window_id,
				event_type, event_key, previous_value, current_value,
				details, occurred_at, detected_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10)
			RETURNING id
		`,
			config.DeBoxUserID,
			config.ID,
			window.ID,
			config.RuleType,
			params.EventKey,
			params.PreviousValue,
			params.CurrentValue,
			string(details),
			params.OccurredAt,
			now,
		).Scan(&triggerEventID); err != nil {
			return StageTriggerResult{}, fmt.Errorf("record stage trigger event: %w", err)
		}

		if err := tx.QueryRow(ctx, `
			UPDATE aggregation_windows
			SET total_trigger_count = total_trigger_count + 1,
			    updated_at = $2
			WHERE id = $1
			RETURNING total_trigger_count
		`, window.ID, now).Scan(&window.TotalTriggerCount); err != nil {
			return StageTriggerResult{}, fmt.Errorf("increment stage trigger count: %w", err)
		}

		result := StageTriggerResult{
			WindowID:              window.ID,
			TriggerEventID:        triggerEventID,
			TotalTriggerCount:     window.TotalTriggerCount,
			TriggerCountThreshold: config.TriggerCountThreshold,
			WindowStartsAt:        window.StartsAt,
			WindowEndsAt:          window.EndsAt,
			RecentNotes:           []string{},
		}
		if window.NotificationSent == 1 ||
			window.TotalTriggerCount < config.TriggerCountThreshold {
			return result, nil
		}

		tag, err := tx.Exec(ctx, `
			UPDATE aggregation_windows
			SET notification_sent = 1,
			    notification_sent_at = $2,
			    updated_at = $2
			WHERE id = $1 AND notification_sent = 0
		`, window.ID, now)
		if err != nil {
			return StageTriggerResult{}, fmt.Errorf("claim stage notification: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return result, nil
		}

		notification, err := collectOne[AggregateNotification](ctx, tx, `
			INSERT INTO aggregate_notifications (
				debox_user_id, aggregation_window_id, notification_kind,
				notification_chat_id, notification_chat_type, notification_label,
				notification_language, trigger_count_snapshot, notification_status
			)
			VALUES ($1, $2, 'stage', $3, $4, $5, $6, $7, 'pending')
			RETURNING `+aggregateNotificationColumns,
			config.DeBoxUserID,
			window.ID,
			config.NotificationChatID,
			config.NotificationChatType,
			config.NotificationLabel,
			normalizeLanguage(config.NotificationLanguage),
			window.TotalTriggerCount,
		)
		if err != nil {
			return StageTriggerResult{}, fmt.Errorf("create stage notification: %w", err)
		}
		recentEvents, err := collectMany[stageEventNote](ctx, tx, `
			SELECT COALESCE(details->>'note', '') AS note
			FROM rule_trigger_events
			WHERE aggregation_window_id = $1
			ORDER BY created_at DESC, id DESC
			LIMIT $2
		`, window.ID, stageRecentEventLimit)
		if err != nil {
			return StageTriggerResult{}, fmt.Errorf("list recent stage events: %w", err)
		}
		notes := make([]string, 0, len(recentEvents))
		for _, event := range recentEvents {
			notes = append(notes, event.Note)
		}
		result.NotificationDue = true
		result.RecentNotes = notes
		result.Notification = &notification
		return result, nil
	})
}

func currentStageWindow(
	ctx context.Context,
	tx DBTX,
	config stageRuleConfiguration,
	now time.Time,
) (AggregationWindow, error) {
	window, err := collectOptional[AggregationWindow](ctx, tx, `
		SELECT `+aggregationWindowColumns+`
		FROM aggregation_windows
		WHERE watch_rule_id = $1 AND closed_at IS NULL
		FOR UPDATE
	`, config.ID)
	if err != nil {
		return AggregationWindow{}, fmt.Errorf("get open stage window: %w", err)
	}
	if window != nil && !now.Before(window.EndsAt) {
		if _, err := tx.Exec(ctx, `
			UPDATE aggregation_windows
			SET closed_at = $2, updated_at = $2
			WHERE id = $1
		`, window.ID, now); err != nil {
			return AggregationWindow{}, fmt.Errorf("close expired stage window: %w", err)
		}
		window = nil
	}
	if window != nil {
		return *window, nil
	}

	startsAt, endsAt := stageWindowBounds(config, now)
	created, err := collectOne[AggregationWindow](ctx, tx, `
		INSERT INTO aggregation_windows (
			debox_user_id, source_type, watch_rule_id, starts_at, ends_at
		)
		VALUES ($1, 'rule', $2, $3, $4)
		RETURNING `+aggregationWindowColumns,
		config.DeBoxUserID,
		config.ID,
		startsAt,
		endsAt,
	)
	if err != nil {
		return AggregationWindow{}, fmt.Errorf("create stage window: %w", err)
	}
	return created, nil
}

func stageWindowBounds(config stageRuleConfiguration, now time.Time) (time.Time, time.Time) {
	cycleSeconds := int64(config.CycleMinutes) * 60
	if config.CycleType == "follow" {
		return now, time.Unix(now.Unix()+cycleSeconds, int64(now.Nanosecond())).In(now.Location())
	}
	anchor := config.AggregationAnchorAt
	if anchor.IsZero() || now.Before(anchor) {
		anchor = now
	}
	elapsedSeconds := now.Unix() - anchor.Unix()
	cycleIndex := elapsedSeconds / cycleSeconds
	startUnix := anchor.Unix() + cycleIndex*cycleSeconds
	startsAt := time.Unix(startUnix, int64(anchor.Nanosecond())).In(anchor.Location())
	endsAt := time.Unix(startUnix+cycleSeconds, int64(anchor.Nanosecond())).In(anchor.Location())
	return startsAt, endsAt
}

func (s *Store) UpdateAggregateNotification(
	ctx context.Context,
	notificationID int64,
	status string,
	messageID *string,
	notificationError string,
) (AggregateNotification, error) {
	if status != "sent" && status != "failed" {
		return AggregateNotification{}, ErrInvalidNotificationStatus
	}
	notification, err := collectOne[AggregateNotification](ctx, s.db, `
		UPDATE aggregate_notifications
		SET notification_message_id = $1,
		    notification_status = $2,
		    notification_error = $3,
		    notification_attempts = notification_attempts + 1,
		    notification_attempted_at = NOW(),
		    notification_sent_at = CASE WHEN $2 = 'sent' THEN NOW() ELSE NULL END
		WHERE id = $4
		RETURNING `+aggregateNotificationColumns,
		messageID,
		status,
		truncate(notificationError, 500),
		notificationID,
	)
	if isNoRows(err) {
		return AggregateNotification{}, ErrNotFound
	}
	if err != nil {
		return AggregateNotification{}, fmt.Errorf("update aggregate notification: %w", err)
	}
	return notification, nil
}
