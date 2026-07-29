package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const marketStageWindowColumns = `
	id, debox_user_id, market_rule_id, starts_at, ends_at, trigger_count,
	notification_status, notification_message_id, notification_error,
	notification_attempts, notification_attempted_at, notification_sent_at,
	next_attempt_at, closed_at, created_at, updated_at
`

const marketCombinationRuleColumns = `
	id, debox_user_id, note, cycle_type, cycle_minutes,
	notification_chat_id, notification_chat_type, notification_label,
	notification_language, enabled, run_status, pause_reason,
	aggregation_anchor_at, created_at, updated_at
`

const marketCombinationMemberColumns = `
	id, market_combination_rule_id, source_type, watch_rule_id,
	market_rule_id, required_trigger_count, created_at
`

const marketCombinationMemberJoinedColumns = `
	mcm.id, mcm.market_combination_rule_id, mcm.source_type, mcm.watch_rule_id,
	mcm.market_rule_id, mcm.required_trigger_count, mcm.created_at
`

const marketCombinationWindowColumns = `
	id, debox_user_id, market_combination_rule_id, starts_at, ends_at,
	total_trigger_count, notification_status, notification_message_id,
	notification_error, notification_attempts, notification_attempted_at,
	notification_sent_at, next_attempt_at, closed_at, created_at, updated_at
`

type MarketDeliveryClaim struct {
	Kind string `db:"kind" json:"kind"`
	ID   int64  `db:"id" json:"id"`
}

type CreateMarketCombinationMemberParams struct {
	SourceType           string
	WatchRuleID          *int64
	MarketRuleID         *int64
	RequiredTriggerCount int64
}

type CreateMarketCombinationParams struct {
	DeBoxUserID          string
	Note                 string
	CycleType            string
	CycleMinutes         int32
	NotificationChatID   string
	NotificationChatType string
	NotificationLabel    string
	NotificationLanguage string
	Members              []CreateMarketCombinationMemberParams
}

type RecordMarketCombinationTriggerParams struct {
	SourceType          string
	WatchTriggerEventID *int64
	MarketRuleEventID   *int64
	OccurredAt          time.Time
	Note                string
}

func (s *Store) RecordMarketStageTrigger(
	ctx context.Context,
	ruleEventID int64,
) (MarketStageWindow, bool, error) {
	var notificationDue bool
	window, err := withTxValue(ctx, s.db, func(tx DBTX) (MarketStageWindow, error) {
		window, due, err := recordMarketStageTrigger(ctx, tx, ruleEventID)
		notificationDue = due
		return window, err
	})
	return window, notificationDue, err
}

type marketStageConfiguration struct {
	DeBoxUserID           string     `db:"debox_user_id"`
	MarketRuleID          int64      `db:"market_rule_id"`
	CycleType             string     `db:"cycle_type"`
	CycleMinutes          int32      `db:"cycle_minutes"`
	TriggerCountThreshold int64      `db:"trigger_count_threshold"`
	AggregationAnchorAt   *time.Time `db:"aggregation_anchor_at"`
}

func recordMarketStageTrigger(
	ctx context.Context,
	tx DBTX,
	ruleEventID int64,
) (MarketStageWindow, bool, error) {
	config, err := collectOne[marketStageConfiguration](ctx, tx, `
		SELECT
			mr.debox_user_id, mr.id AS market_rule_id, mr.cycle_type,
			mr.cycle_minutes, mr.trigger_count_threshold, mr.aggregation_anchor_at
		FROM market_rule_events mre
		JOIN market_rules mr ON mr.id = mre.market_rule_id
		JOIN market_events me ON me.id = mre.market_event_id
		JOIN market_projects mp ON mp.id = mr.market_project_id
		WHERE mre.id = $1
		  AND mre.notification_status = 'staged'
		  AND mr.enabled = 1 AND mr.run_status = 'active'
		  AND mr.delivery_mode = 'stage'
		  AND mp.status = 'active'
		  AND me.reorged = 0
		FOR UPDATE OF mr, mre
	`, ruleEventID)
	if isNoRows(err) {
		return MarketStageWindow{}, false, ErrNotFound
	}
	if err != nil {
		return MarketStageWindow{}, false, fmt.Errorf("lock market stage trigger: %w", err)
	}
	var now time.Time
	if err := tx.QueryRow(ctx, "SELECT NOW()").Scan(&now); err != nil {
		return MarketStageWindow{}, false, fmt.Errorf("get market stage time: %w", err)
	}
	window, err := currentMarketStageWindow(ctx, tx, config, now)
	if err != nil {
		return MarketStageWindow{}, false, err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO market_stage_window_events (
			market_stage_window_id, market_rule_event_id
		)
		VALUES ($1, $2)
		ON CONFLICT DO NOTHING
	`, window.ID, ruleEventID)
	if err != nil {
		return MarketStageWindow{}, false, fmt.Errorf("record market stage event: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return window, false, nil
	}
	window, err = collectOne[MarketStageWindow](ctx, tx, `
		UPDATE market_stage_windows
		SET trigger_count = trigger_count + 1,
		    notification_status = CASE
		      WHEN notification_status IN ('sent', 'sending', 'skipped')
		      THEN notification_status
		      WHEN trigger_count + 1 >= $2 AND notification_status = 'collecting'
		      THEN 'pending'
		      ELSE notification_status
		    END,
		    next_attempt_at = CASE
		      WHEN trigger_count + 1 >= $2 AND notification_status = 'collecting'
		      THEN $3
		      ELSE next_attempt_at
		    END,
		    updated_at = $3
		WHERE id = $1
		RETURNING `+marketStageWindowColumns,
		window.ID,
		config.TriggerCountThreshold,
		now,
	)
	if err != nil {
		return MarketStageWindow{}, false, fmt.Errorf("advance market stage window: %w", err)
	}
	if window.NotificationStatus == "sent" ||
		window.NotificationStatus == "sending" ||
		window.NotificationStatus == "skipped" {
		if _, err := tx.Exec(ctx, `
			UPDATE market_rule_events
			SET notification_status = 'skipped',
			    notification_error = 'stage notification already emitted for this window'
			WHERE id = $1 AND notification_status = 'staged'
		`, ruleEventID); err != nil {
			return MarketStageWindow{}, false, fmt.Errorf(
				"skip late market stage event: %w",
				err,
			)
		}
	}
	return window, window.NotificationStatus == "pending", nil
}

func currentMarketStageWindow(
	ctx context.Context,
	tx DBTX,
	config marketStageConfiguration,
	now time.Time,
) (MarketStageWindow, error) {
	window, err := collectOptional[MarketStageWindow](ctx, tx, `
		SELECT `+marketStageWindowColumns+`
		FROM market_stage_windows
		WHERE market_rule_id = $1 AND closed_at IS NULL
		FOR UPDATE
	`, config.MarketRuleID)
	if err != nil {
		return MarketStageWindow{}, fmt.Errorf("get market stage window: %w", err)
	}
	if window != nil && !now.Before(window.EndsAt) {
		if _, err := tx.Exec(ctx, `
			UPDATE market_stage_windows
			SET closed_at = $2,
			    notification_status = CASE
			      WHEN notification_status = 'collecting' THEN 'skipped'
			      ELSE notification_status
			    END,
			    updated_at = $2
			WHERE id = $1
		`, window.ID, now); err != nil {
			return MarketStageWindow{}, fmt.Errorf("close market stage window: %w", err)
		}
		window = nil
	}
	if window != nil {
		return *window, nil
	}
	startsAt, endsAt := marketWindowBounds(
		config.CycleType,
		config.CycleMinutes,
		config.AggregationAnchorAt,
		now,
	)
	created, err := collectOne[MarketStageWindow](ctx, tx, `
		INSERT INTO market_stage_windows (
			debox_user_id, market_rule_id, starts_at, ends_at
		)
		VALUES ($1, $2, $3, $4)
		RETURNING `+marketStageWindowColumns,
		config.DeBoxUserID,
		config.MarketRuleID,
		startsAt,
		endsAt,
	)
	if err != nil {
		return MarketStageWindow{}, fmt.Errorf("create market stage window: %w", err)
	}
	return created, nil
}

func marketWindowBounds(
	cycleType string,
	cycleMinutes int32,
	anchor *time.Time,
	now time.Time,
) (time.Time, time.Time) {
	duration := time.Duration(cycleMinutes) * time.Minute
	if cycleType != "fixed" || anchor == nil || anchor.IsZero() {
		return now, now.Add(duration)
	}
	start := anchor.UTC()
	if now.Before(start) {
		return start, start.Add(duration)
	}
	steps := int64(now.Sub(start) / duration)
	start = start.Add(time.Duration(steps) * duration)
	return start, start.Add(duration)
}

func (s *Store) CreateMarketCombinationWithinQuota(
	ctx context.Context,
	params CreateMarketCombinationParams,
	policy QuotaPolicy,
) (MarketCombinationRule, error) {
	normalizeMarketCombinationParams(&params)
	return withTxValue(ctx, s.db, func(tx DBTX) (MarketCombinationRule, error) {
		if err := lockUser(ctx, tx, params.DeBoxUserID); err != nil {
			return MarketCombinationRule{}, err
		}
		if err := requirePolicyPlan(ctx, tx, params.DeBoxUserID, policy); err != nil {
			return MarketCombinationRule{}, err
		}
		if !policy.CombinationRules {
			return MarketCombinationRule{}, ErrCombinationRulesDenied
		}
		if params.NotificationChatType == "group" && !policy.GroupNotification {
			return MarketCombinationRule{}, ErrGroupNotificationDenied
		}
		if params.NotificationChatType == "group" {
			var exists bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1
					FROM notification_groups
					WHERE debox_user_id = $1
					  AND gid = $2
					  AND enabled = 1
				)
			`, params.DeBoxUserID, params.NotificationChatID).Scan(&exists); err != nil {
				return MarketCombinationRule{}, fmt.Errorf(
					"validate market combination notification group: %w",
					err,
				)
			}
			if !exists {
				return MarketCombinationRule{}, ErrNotFound
			}
		}
		if len(params.Members) < 2 {
			return MarketCombinationRule{}, ErrInvalidCombinationRule
		}
		seenMembers := make(map[string]struct{}, len(params.Members))
		for _, member := range params.Members {
			key := member.SourceType + ":"
			if member.SourceType == "watch" && member.WatchRuleID != nil {
				key += fmt.Sprint(*member.WatchRuleID)
			} else if member.SourceType == "market" && member.MarketRuleID != nil {
				key += fmt.Sprint(*member.MarketRuleID)
			} else {
				return MarketCombinationRule{}, ErrInvalidCombinationRule
			}
			if _, exists := seenMembers[key]; exists {
				return MarketCombinationRule{}, ErrInvalidCombinationRule
			}
			seenMembers[key] = struct{}{}
		}
		count, err := countActiveRuleSlots(ctx, tx, params.DeBoxUserID)
		if err != nil {
			return MarketCombinationRule{}, err
		}
		if count >= int64(policy.RuleLimit) {
			return MarketCombinationRule{}, ErrRuleLimitReached
		}
		combination, err := collectOne[MarketCombinationRule](ctx, tx, `
			INSERT INTO market_combination_rules (
				debox_user_id, note, cycle_type, cycle_minutes,
				notification_chat_id, notification_chat_type,
				notification_label, notification_language,
				aggregation_anchor_at
			)
			VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8,
				CASE WHEN $3 = 'fixed' THEN NOW() ELSE NULL END
			)
			RETURNING `+marketCombinationRuleColumns,
			params.DeBoxUserID,
			params.Note,
			params.CycleType,
			params.CycleMinutes,
			params.NotificationChatID,
			params.NotificationChatType,
			params.NotificationLabel,
			params.NotificationLanguage,
		)
		if err != nil {
			return MarketCombinationRule{}, fmt.Errorf("create market combination: %w", err)
		}
		for _, member := range params.Members {
			if err := validateMarketCombinationMember(
				ctx,
				tx,
				params.DeBoxUserID,
				member,
				policy,
			); err != nil {
				return MarketCombinationRule{}, err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO market_combination_members (
					market_combination_rule_id, source_type, watch_rule_id,
					market_rule_id, required_trigger_count
				)
				VALUES ($1, $2, $3, $4, $5)
			`,
				combination.ID,
				member.SourceType,
				member.WatchRuleID,
				member.MarketRuleID,
				member.RequiredTriggerCount,
			); err != nil {
				return MarketCombinationRule{}, fmt.Errorf("create market combination member: %w", err)
			}
		}
		combination.Members, err = listMarketCombinationMembers(ctx, tx, combination.ID)
		if err != nil {
			return MarketCombinationRule{}, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO market_combination_rule_projects (
				market_combination_rule_id, market_project_id
			)
			SELECT DISTINCT $1::bigint, mr.market_project_id
			FROM market_combination_members mcm
			JOIN market_rules mr ON mr.id = mcm.market_rule_id
			WHERE mcm.market_combination_rule_id = $1
			  AND mcm.source_type = 'market'
			ON CONFLICT DO NOTHING
		`, combination.ID); err != nil {
			return MarketCombinationRule{}, fmt.Errorf(
				"bind market combination projects: %w",
				err,
			)
		}
		return combination, nil
	})
}

func (s *Store) ListMarketCombinationRules(
	ctx context.Context,
	deboxUserID string,
) ([]MarketCombinationRule, error) {
	values, err := collectMany[MarketCombinationRule](ctx, s.db, `
		SELECT `+marketCombinationRuleColumns+`
		FROM market_combination_rules
		WHERE debox_user_id = $1 AND enabled = 1
		ORDER BY created_at DESC, id DESC
	`, strings.TrimSpace(deboxUserID))
	if err != nil {
		return nil, fmt.Errorf("list market combinations: %w", err)
	}
	for index := range values {
		values[index].Members, err = listMarketCombinationMembers(
			ctx, s.db, values[index].ID,
		)
		if err != nil {
			return nil, err
		}
	}
	return values, nil
}

func (s *Store) ArchiveMarketCombinationRule(
	ctx context.Context,
	combinationID int64,
	deboxUserID string,
) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE market_combination_rules
		SET run_status = 'paused',
		    pause_reason = 'user_archived',
		    updated_at = NOW()
		WHERE id = $1 AND debox_user_id = $2 AND enabled = 1
	`, combinationID, strings.TrimSpace(deboxUserID))
	if err != nil {
		return false, fmt.Errorf("archive market combination: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *Store) RestoreMarketCombinationWithinQuota(
	ctx context.Context,
	combinationID int64,
	deboxUserID string,
	policy QuotaPolicy,
) (MarketCombinationRule, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (MarketCombinationRule, error) {
		if err := lockUser(ctx, tx, deboxUserID); err != nil {
			return MarketCombinationRule{}, err
		}
		if err := requirePolicyPlan(ctx, tx, deboxUserID, policy); err != nil {
			return MarketCombinationRule{}, err
		}
		if !policy.CombinationRules {
			return MarketCombinationRule{}, ErrCombinationRulesDenied
		}
		value, err := collectOne[MarketCombinationRule](ctx, tx, `
			SELECT `+marketCombinationRuleColumns+`
			FROM market_combination_rules
			WHERE id = $1 AND debox_user_id = $2 AND enabled = 1
			FOR UPDATE
		`, combinationID, deboxUserID)
		if isNoRows(err) {
			return MarketCombinationRule{}, ErrNotFound
		}
		if err != nil {
			return MarketCombinationRule{}, fmt.Errorf(
				"lock market combination: %w",
				err,
			)
		}
		members, err := listMarketCombinationMembers(ctx, tx, combinationID)
		if err != nil {
			return MarketCombinationRule{}, err
		}
		if len(members) < 2 {
			return MarketCombinationRule{}, ErrInvalidCombinationRule
		}
		for _, member := range members {
			if err := validateMarketCombinationMember(
				ctx,
				tx,
				deboxUserID,
				CreateMarketCombinationMemberParams{
					SourceType:           member.SourceType,
					WatchRuleID:          member.WatchRuleID,
					MarketRuleID:         member.MarketRuleID,
					RequiredTriggerCount: member.RequiredTriggerCount,
				},
				policy,
			); err != nil {
				return MarketCombinationRule{}, err
			}
		}
		if value.NotificationChatType == "group" && !policy.GroupNotification {
			return MarketCombinationRule{}, ErrGroupNotificationDenied
		}
		if value.RunStatus == "active" {
			value.Members = members
			return value, nil
		}
		count, err := countActiveRuleSlots(ctx, tx, deboxUserID)
		if err != nil {
			return MarketCombinationRule{}, err
		}
		if count >= int64(policy.RuleLimit) {
			return MarketCombinationRule{}, ErrRuleLimitReached
		}
		value, err = collectOne[MarketCombinationRule](ctx, tx, `
			UPDATE market_combination_rules
			SET run_status = 'active',
			    pause_reason = '',
			    aggregation_anchor_at = CASE
			      WHEN cycle_type = 'fixed' THEN NOW()
			      ELSE NULL
			    END,
			    updated_at = NOW()
			WHERE id = $1
			RETURNING `+marketCombinationRuleColumns,
			combinationID,
		)
		if err != nil {
			return MarketCombinationRule{}, fmt.Errorf(
				"restore market combination: %w",
				err,
			)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE market_combination_windows
			SET closed_at = NOW(),
			    notification_status = CASE
			      WHEN notification_status = 'collecting' THEN 'skipped'
			      ELSE notification_status
			    END,
			    updated_at = NOW()
			WHERE market_combination_rule_id = $1 AND closed_at IS NULL
		`, combinationID); err != nil {
			return MarketCombinationRule{}, fmt.Errorf(
				"reset market combination window: %w",
				err,
			)
		}
		value.Members = members
		return value, nil
	})
}

func normalizeMarketCombinationParams(params *CreateMarketCombinationParams) {
	params.DeBoxUserID = strings.TrimSpace(params.DeBoxUserID)
	params.Note = strings.TrimSpace(params.Note)
	params.CycleType = strings.ToLower(strings.TrimSpace(params.CycleType))
	if params.CycleType != "follow" {
		params.CycleType = "fixed"
	}
	if params.CycleMinutes <= 0 {
		params.CycleMinutes = 60
	}
	params.NotificationChatID = strings.TrimSpace(params.NotificationChatID)
	if params.NotificationChatID == "" {
		params.NotificationChatID = params.DeBoxUserID
	}
	params.NotificationChatType = strings.ToLower(strings.TrimSpace(params.NotificationChatType))
	if params.NotificationChatType != "group" {
		params.NotificationChatType = "private"
	}
	params.NotificationLanguage = normalizeLanguage(params.NotificationLanguage)
	for index := range params.Members {
		params.Members[index].SourceType = strings.ToLower(
			strings.TrimSpace(params.Members[index].SourceType),
		)
		if params.Members[index].RequiredTriggerCount <= 0 {
			params.Members[index].RequiredTriggerCount = 1
		}
	}
}

func validateMarketCombinationMember(
	ctx context.Context,
	db DBTX,
	deboxUserID string,
	member CreateMarketCombinationMemberParams,
	policy QuotaPolicy,
) error {
	switch member.SourceType {
	case "watch":
		if member.WatchRuleID == nil || member.MarketRuleID != nil {
			return ErrInvalidCombinationRule
		}
		var ruleType, chatType, runStatus string
		var enabled int32
		err := db.QueryRow(ctx, `
			SELECT rule_type, notification_chat_type, enabled, run_status
			FROM watch_rules
			WHERE id = $1 AND debox_user_id = $2
		`, *member.WatchRuleID, deboxUserID).Scan(
			&ruleType, &chatType, &enabled, &runStatus,
		)
		if isNoRows(err) || enabled != 1 || runStatus != "active" {
			return ErrInvalidCombinationRule
		}
		if err != nil {
			return fmt.Errorf("validate watch combination member: %w", err)
		}
		if !policy.allowsRuleType(ruleType) {
			return ErrRuleTypeDenied
		}
	case "market":
		if member.MarketRuleID == nil || member.WatchRuleID != nil {
			return ErrInvalidCombinationRule
		}
		var ruleType, runStatus string
		var enabled int32
		err := db.QueryRow(ctx, `
			SELECT rule_type, enabled, run_status
			FROM market_rules
			WHERE id = $1 AND debox_user_id = $2
		`, *member.MarketRuleID, deboxUserID).Scan(
			&ruleType, &enabled, &runStatus,
		)
		if isNoRows(err) || enabled != 1 || runStatus != "active" {
			return ErrInvalidCombinationRule
		}
		if err != nil {
			return fmt.Errorf("validate market combination member: %w", err)
		}
		if !policy.allowsMarketRuleType(ruleType) {
			return ErrMarketRuleTypeDenied
		}
	default:
		return ErrInvalidCombinationRule
	}
	return nil
}

func listMarketCombinationMembers(
	ctx context.Context,
	db DBTX,
	combinationID int64,
) ([]MarketCombinationMember, error) {
	values, err := collectMany[MarketCombinationMember](ctx, db, `
		SELECT `+marketCombinationMemberColumns+`
		FROM market_combination_members
		WHERE market_combination_rule_id = $1
		ORDER BY id
	`, combinationID)
	if err != nil {
		return nil, fmt.Errorf("list market combination members: %w", err)
	}
	return values, nil
}

func (s *Store) RecordMarketCombinationTrigger(
	ctx context.Context,
	params RecordMarketCombinationTriggerParams,
) (int64, error) {
	params.SourceType = strings.ToLower(strings.TrimSpace(params.SourceType))
	if params.SourceType == "watch" {
		if params.WatchTriggerEventID == nil || params.MarketRuleEventID != nil {
			return 0, ErrInvalidCombinationRule
		}
	} else if params.SourceType == "market" {
		if params.MarketRuleEventID == nil || params.WatchTriggerEventID != nil {
			return 0, ErrInvalidCombinationRule
		}
	} else {
		return 0, ErrInvalidCombinationRule
	}
	if params.OccurredAt.IsZero() {
		params.OccurredAt = time.Now().UTC()
	}
	var members []MarketCombinationMember
	var err error
	if params.SourceType == "watch" {
		members, err = collectMany[MarketCombinationMember](ctx, s.db, `
			SELECT `+marketCombinationMemberJoinedColumns+`
			FROM market_combination_members mcm
			JOIN market_combination_rules mcr
			  ON mcr.id = mcm.market_combination_rule_id
			JOIN rule_trigger_events rte ON rte.id = $1
			WHERE mcm.source_type = 'watch'
			  AND mcm.watch_rule_id = rte.watch_rule_id
			  AND mcr.debox_user_id = rte.debox_user_id
			  AND mcr.enabled = 1 AND mcr.run_status = 'active'
		`, *params.WatchTriggerEventID)
	} else {
		members, err = collectMany[MarketCombinationMember](ctx, s.db, `
			SELECT `+marketCombinationMemberJoinedColumns+`
			FROM market_combination_members mcm
			JOIN market_combination_rules mcr
			  ON mcr.id = mcm.market_combination_rule_id
			JOIN market_rule_events mre ON mre.id = $1
			JOIN market_rules mr ON mr.id = mre.market_rule_id
			WHERE mcm.source_type = 'market'
			  AND mcm.market_rule_id = mre.market_rule_id
			  AND mcr.debox_user_id = mr.debox_user_id
			  AND mcr.enabled = 1 AND mcr.run_status = 'active'
		`, *params.MarketRuleEventID)
	}
	if err != nil {
		return 0, fmt.Errorf("list matching market combinations: %w", err)
	}
	var recorded int64
	for _, member := range members {
		inserted, err := s.recordMarketCombinationMemberTrigger(ctx, member, params)
		if err != nil {
			return recorded, err
		}
		if inserted {
			recorded++
		}
	}
	return recorded, nil
}

func (s *Store) recordMarketCombinationMemberTrigger(
	ctx context.Context,
	member MarketCombinationMember,
	params RecordMarketCombinationTriggerParams,
) (bool, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (bool, error) {
		config, err := collectOne[MarketCombinationRule](ctx, tx, `
			SELECT `+marketCombinationRuleColumns+`
			FROM market_combination_rules
			WHERE id = $1 AND enabled = 1 AND run_status = 'active'
			FOR UPDATE
		`, member.MarketCombinationRuleID)
		if isNoRows(err) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("lock market combination: %w", err)
		}
		var now time.Time
		if err := tx.QueryRow(ctx, "SELECT NOW()").Scan(&now); err != nil {
			return false, fmt.Errorf("get market combination time: %w", err)
		}
		occurredAt := params.OccurredAt.UTC()
		if occurredAt.After(now) {
			occurredAt = now
		}
		if config.CycleType == "follow" &&
			!occurredAt.Add(time.Duration(config.CycleMinutes)*time.Minute).After(now) {
			return false, nil
		}
		window, err := currentMarketCombinationWindow(
			ctx, tx, config, now, occurredAt,
		)
		if err != nil {
			return false, err
		}
		if params.OccurredAt.Before(window.StartsAt) ||
			!params.OccurredAt.Before(window.EndsAt) {
			return false, nil
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO market_combination_trigger_events (
				market_combination_window_id, market_combination_member_id,
				source_type, watch_trigger_event_id, market_rule_event_id,
				note, occurred_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT DO NOTHING
		`,
			window.ID,
			member.ID,
			params.SourceType,
			params.WatchTriggerEventID,
			params.MarketRuleEventID,
			truncate(params.Note, 2000),
			params.OccurredAt.UTC(),
		)
		if err != nil {
			return false, fmt.Errorf("record market combination trigger: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return false, nil
		}
		if _, err := tx.Exec(ctx, `
			UPDATE market_combination_window_members
			SET trigger_count = trigger_count + 1,
			    reached_at = CASE
			      WHEN reached_at IS NULL
			       AND trigger_count + 1 >= required_trigger_count
			      THEN $3 ELSE reached_at
			    END,
			    updated_at = $3
			WHERE market_combination_window_id = $1
			  AND market_combination_member_id = $2
		`, window.ID, member.ID, now); err != nil {
			return false, fmt.Errorf("advance market combination member: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE market_combination_windows
			SET total_trigger_count = total_trigger_count + 1,
			    updated_at = $2
			WHERE id = $1
		`, window.ID, now); err != nil {
			return false, fmt.Errorf("advance market combination window: %w", err)
		}
		var complete bool
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) >= 2 AND BOOL_AND(trigger_count >= required_trigger_count)
			FROM market_combination_window_members
			WHERE market_combination_window_id = $1
		`, window.ID).Scan(&complete); err != nil {
			return false, fmt.Errorf("check market combination completion: %w", err)
		}
		if complete {
			if _, err := tx.Exec(ctx, `
				UPDATE market_combination_windows
				SET notification_status = CASE
				      WHEN notification_status = 'collecting' THEN 'pending'
				      ELSE notification_status
				    END,
				    next_attempt_at = $2,
				    updated_at = $2
				WHERE id = $1
			`, window.ID, now); err != nil {
				return false, fmt.Errorf("claim completed market combination: %w", err)
			}
		}
		return true, nil
	})
}

func currentMarketCombinationWindow(
	ctx context.Context,
	tx DBTX,
	config MarketCombinationRule,
	now time.Time,
	firstOccurredAt time.Time,
) (MarketCombinationWindow, error) {
	window, err := collectOptional[MarketCombinationWindow](ctx, tx, `
		SELECT `+marketCombinationWindowColumns+`
		FROM market_combination_windows
		WHERE market_combination_rule_id = $1 AND closed_at IS NULL
		FOR UPDATE
	`, config.ID)
	if err != nil {
		return MarketCombinationWindow{}, fmt.Errorf("get market combination window: %w", err)
	}
	if window != nil && !now.Before(window.EndsAt) {
		if _, err := tx.Exec(ctx, `
			UPDATE market_combination_windows
			SET closed_at = $2,
			    notification_status = CASE
			      WHEN notification_status = 'collecting' THEN 'skipped'
			      ELSE notification_status
			    END,
			    updated_at = $2
			WHERE id = $1
		`, window.ID, now); err != nil {
			return MarketCombinationWindow{}, fmt.Errorf("close market combination window: %w", err)
		}
		window = nil
	}
	if window != nil {
		return *window, nil
	}
	windowReference := now
	if config.CycleType == "follow" && !firstOccurredAt.IsZero() {
		windowReference = firstOccurredAt
	}
	startsAt, endsAt := marketWindowBounds(
		config.CycleType,
		config.CycleMinutes,
		config.AggregationAnchorAt,
		windowReference,
	)
	created, err := collectOne[MarketCombinationWindow](ctx, tx, `
		INSERT INTO market_combination_windows (
			debox_user_id, market_combination_rule_id, starts_at, ends_at
		)
		VALUES ($1, $2, $3, $4)
		RETURNING `+marketCombinationWindowColumns,
		config.DeBoxUserID,
		config.ID,
		startsAt,
		endsAt,
	)
	if err != nil {
		return MarketCombinationWindow{}, fmt.Errorf("create market combination window: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO market_combination_window_members (
			market_combination_window_id, market_combination_member_id,
			required_trigger_count
		)
		SELECT $1, id, required_trigger_count
		FROM market_combination_members
		WHERE market_combination_rule_id = $2
	`, created.ID, config.ID); err != nil {
		return MarketCombinationWindow{}, fmt.Errorf("snapshot market combination members: %w", err)
	}
	return created, nil
}

func (s *Store) ProcessPendingWatchCombinationTriggers(
	ctx context.Context,
	limit int,
) (int64, error) {
	type pending struct {
		ID         int64     `db:"id"`
		OccurredAt time.Time `db:"occurred_at"`
		Note       string    `db:"note"`
	}
	values, err := collectMany[pending](ctx, s.db, `
		SELECT DISTINCT
			rte.id,
			COALESCE(rte.occurred_at, rte.detected_at) AS occurred_at,
			COALESCE(rte.details->>'note', '') AS note
		FROM rule_trigger_events rte
		JOIN market_combination_members mcm
		  ON mcm.source_type = 'watch'
		 AND mcm.watch_rule_id = rte.watch_rule_id
		JOIN market_combination_rules mcr
		  ON mcr.id = mcm.market_combination_rule_id
		 AND mcr.debox_user_id = rte.debox_user_id
		WHERE mcr.enabled = 1 AND mcr.run_status = 'active'
		  AND COALESCE(rte.occurred_at, rte.detected_at) >= mcr.created_at
		  AND NOT EXISTS (
			SELECT 1
			FROM market_combination_trigger_events mcte
			WHERE mcte.market_combination_member_id = mcm.id
			  AND mcte.watch_trigger_event_id = rte.id
		  )
		ORDER BY rte.id
		LIMIT $1
	`, clamp(limit, 1, 1000))
	if err != nil {
		return 0, fmt.Errorf("list pending watch combination triggers: %w", err)
	}
	var processed int64
	for _, value := range values {
		id := value.ID
		count, err := s.RecordMarketCombinationTrigger(
			ctx,
			RecordMarketCombinationTriggerParams{
				SourceType:          "watch",
				WatchTriggerEventID: &id,
				OccurredAt:          value.OccurredAt,
				Note:                value.Note,
			},
		)
		if err != nil {
			return processed, err
		}
		processed += count
	}
	return processed, nil
}

func (s *Store) ReconcileMarketNotificationSources(ctx context.Context) error {
	_, err := withTxValue(ctx, s.db, func(tx DBTX) (struct{}, error) {
		if _, err := tx.Exec(ctx, `
			UPDATE market_rule_events mre
			SET notification_status = 'skipped',
			    notification_error = 'source event was removed by a chain reorganization'
			FROM market_events me
			WHERE me.id = mre.market_event_id
			  AND me.reorged = 1
			  AND mre.notification_status IN (
				'pending', 'sending', 'failed', 'staged', 'combined'
			  )
		`); err != nil {
			return struct{}{}, fmt.Errorf("skip reorged market rule events: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM market_combination_trigger_events mcte
			USING market_rule_events mre, market_events me
			WHERE mcte.market_rule_event_id = mre.id
			  AND mre.market_event_id = me.id
			  AND me.reorged = 1
		`); err != nil {
			return struct{}{}, fmt.Errorf("remove reorged market combination triggers: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE market_stage_windows msw
			SET trigger_count = (
			      SELECT COUNT(*)
			      FROM market_stage_window_events mswe
			      JOIN market_rule_events mre ON mre.id = mswe.market_rule_event_id
			      JOIN market_events me ON me.id = mre.market_event_id
			      WHERE mswe.market_stage_window_id = msw.id
			        AND me.reorged = 0
			    ),
			    notification_status = CASE
			      WHEN msw.notification_status IN ('sent', 'sending') THEN msw.notification_status
			      WHEN (
			        SELECT COUNT(*)
			        FROM market_stage_window_events mswe
			        JOIN market_rule_events mre ON mre.id = mswe.market_rule_event_id
			        JOIN market_events me ON me.id = mre.market_event_id
			        WHERE mswe.market_stage_window_id = msw.id
			          AND me.reorged = 0
			      ) >= mr.trigger_count_threshold THEN 'pending'
			      ELSE 'collecting'
			    END,
			    updated_at = NOW()
			FROM market_rules mr
			WHERE mr.id = msw.market_rule_id
			  AND msw.closed_at IS NULL
			  AND msw.notification_status NOT IN ('sent', 'skipped')
		`); err != nil {
			return struct{}{}, fmt.Errorf("reconcile market stage windows: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE market_combination_window_members mcwm
			SET trigger_count = (
			      SELECT COUNT(*)
			      FROM market_combination_trigger_events mcte
			      WHERE mcte.market_combination_window_id = mcwm.market_combination_window_id
			        AND mcte.market_combination_member_id = mcwm.market_combination_member_id
			    ),
			    reached_at = CASE
			      WHEN (
			        SELECT COUNT(*)
			        FROM market_combination_trigger_events mcte
			        WHERE mcte.market_combination_window_id = mcwm.market_combination_window_id
			          AND mcte.market_combination_member_id = mcwm.market_combination_member_id
			      ) >= mcwm.required_trigger_count
			      THEN COALESCE(mcwm.reached_at, NOW())
			      ELSE NULL
			    END,
			    updated_at = NOW()
		`); err != nil {
			return struct{}{}, fmt.Errorf("reconcile market combination members: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE market_combination_windows mcw
			SET total_trigger_count = (
			      SELECT COALESCE(SUM(mcwm.trigger_count), 0)
			      FROM market_combination_window_members mcwm
			      WHERE mcwm.market_combination_window_id = mcw.id
			    ),
			    notification_status = CASE
			      WHEN mcw.notification_status IN ('sent', 'sending') THEN mcw.notification_status
			      WHEN (
			        SELECT COUNT(*)
			        FROM market_combination_window_members mcwm
			        WHERE mcwm.market_combination_window_id = mcw.id
			      ) >= 2
			      AND (
			        SELECT COALESCE(
			          BOOL_AND(mcwm.trigger_count >= mcwm.required_trigger_count),
			          FALSE
			        )
			        FROM market_combination_window_members mcwm
			        WHERE mcwm.market_combination_window_id = mcw.id
			      ) THEN 'pending'
			      ELSE 'collecting'
			    END,
			    updated_at = NOW()
			WHERE mcw.closed_at IS NULL
			  AND mcw.notification_status NOT IN ('sent', 'skipped')
		`); err != nil {
			return struct{}{}, fmt.Errorf("reconcile market combination windows: %w", err)
		}
		return struct{}{}, nil
	})
	return err
}

func (s *Store) RecoverStaleMarketDeliveries(
	ctx context.Context,
	lease time.Duration,
) (int64, error) {
	if lease <= 0 {
		lease = 5 * time.Minute
	}
	var total int64
	for _, table := range []string{
		"market_rule_events",
		"market_stage_windows",
		"market_combination_windows",
	} {
		query := fmt.Sprintf(`
			UPDATE %s
			SET notification_status = 'failed',
			    notification_error = 'notification worker lease expired',
			    next_attempt_at = NOW()
			WHERE notification_status = 'sending'
			  AND notification_attempted_at < NOW() - $1::interval
		`, table)
		tag, err := s.db.Exec(ctx, query, durationInterval(lease))
		if err != nil {
			return total, fmt.Errorf("recover stale %s deliveries: %w", table, err)
		}
		total += tag.RowsAffected()
	}
	return total, nil
}

func (s *Store) ClaimMarketDeliveries(
	ctx context.Context,
	limit int,
) ([]MarketDeliveryClaim, error) {
	limit = clamp(limit, 1, 200)
	return withTxValue(ctx, s.db, func(tx DBTX) ([]MarketDeliveryClaim, error) {
		claims := make([]MarketDeliveryClaim, 0, limit)
		tables := []struct {
			kind  string
			table string
			extra string
		}{
			{"realtime", "market_rule_events", `
				AND EXISTS (
					SELECT 1
					FROM market_events me
					JOIN market_rules mr ON mr.id = market_rule_events.market_rule_id
					JOIN market_projects mp ON mp.id = mr.market_project_id
					WHERE me.id = market_rule_events.market_event_id
					  AND me.reorged = 0
					  AND mr.enabled = 1 AND mr.run_status = 'active'
					  AND mp.status = 'active'
				)
			`},
			{"stage", "market_stage_windows", `
				AND EXISTS (
					SELECT 1
					FROM market_rules mr
					JOIN market_projects mp ON mp.id = mr.market_project_id
					WHERE mr.id = market_stage_windows.market_rule_id
					  AND mr.enabled = 1 AND mr.run_status = 'active'
					  AND mp.status = 'active'
				)
			`},
			{"combination", "market_combination_windows", `
				AND EXISTS (
					SELECT 1
					FROM market_combination_rules mcr
					WHERE mcr.id = market_combination_windows.market_combination_rule_id
					  AND mcr.enabled = 1 AND mcr.run_status = 'active'
				)
			`},
		}
		for _, target := range tables {
			remaining := limit - len(claims)
			if remaining <= 0 {
				break
			}
			query := fmt.Sprintf(`
				WITH candidates AS (
					SELECT id
					FROM %s
					WHERE notification_status IN ('pending', 'failed')
					  AND next_attempt_at <= NOW()
					  AND notification_attempts < 5
					  %s
					ORDER BY id
					FOR UPDATE SKIP LOCKED
					LIMIT $1
				)
				UPDATE %s
				SET notification_status = 'sending',
				    notification_attempted_at = NOW()
				WHERE id IN (SELECT id FROM candidates)
				RETURNING id
			`, target.table, target.extra, target.table)
			rows, err := tx.Query(ctx, query, remaining)
			if err != nil {
				return nil, fmt.Errorf("claim %s market delivery: %w", target.kind, err)
			}
			ids, err := collectInt64Rows(rows)
			if err != nil {
				return nil, fmt.Errorf("collect %s market deliveries: %w", target.kind, err)
			}
			for _, id := range ids {
				claims = append(claims, MarketDeliveryClaim{Kind: target.kind, ID: id})
			}
		}
		return claims, nil
	})
}

func (s *Store) LoadMarketDelivery(
	ctx context.Context,
	claim MarketDeliveryClaim,
) (MarketNotificationDelivery, error) {
	switch claim.Kind {
	case "realtime":
		return s.loadRealtimeMarketDelivery(ctx, claim.ID)
	case "stage":
		return s.loadStageMarketDelivery(ctx, claim.ID)
	case "combination":
		return s.loadCombinationMarketDelivery(ctx, claim.ID)
	default:
		return MarketNotificationDelivery{}, ErrInvalidNotificationStatus
	}
}

func (s *Store) loadRealtimeMarketDelivery(
	ctx context.Context,
	id int64,
) (MarketNotificationDelivery, error) {
	ruleEvent, err := collectOne[MarketRuleEvent](ctx, s.db, `
		SELECT `+marketRuleEventColumns+`
		FROM market_rule_events
		WHERE id = $1 AND notification_status = 'sending'
	`, id)
	if isNoRows(err) {
		return MarketNotificationDelivery{}, ErrNotFound
	}
	if err != nil {
		return MarketNotificationDelivery{}, fmt.Errorf("load realtime market delivery: %w", err)
	}
	rule, err := collectOne[MarketRule](ctx, s.db, `
		SELECT `+marketRuleColumns+` FROM market_rules WHERE id = $1
	`, ruleEvent.MarketRuleID)
	if err != nil {
		return MarketNotificationDelivery{}, fmt.Errorf("load realtime market rule: %w", err)
	}
	event, err := collectOne[MarketEvent](ctx, s.db, `
		SELECT `+marketEventColumns+`
		FROM market_events
		WHERE id = $1 AND reorged = 0
	`, ruleEvent.MarketEventID)
	if isNoRows(err) {
		return MarketNotificationDelivery{}, ErrNotFound
	}
	if err != nil {
		return MarketNotificationDelivery{}, fmt.Errorf("load realtime market event: %w", err)
	}
	project, err := collectOne[MarketProject](ctx, s.db, `
		SELECT `+marketProjectColumns+` FROM market_projects WHERE id = $1
	`, rule.MarketProjectID)
	if err != nil {
		return MarketNotificationDelivery{}, fmt.Errorf("load realtime market project: %w", err)
	}
	delivery := MarketNotificationDelivery{
		Kind:                 "realtime",
		ID:                   id,
		DeBoxUserID:          rule.DeBoxUserID,
		MarketRuleID:         &rule.ID,
		MarketRuleEventID:    &ruleEvent.ID,
		NotificationChatID:   rule.NotificationChatID,
		NotificationChatType: rule.NotificationChatType,
		NotificationLanguage: rule.NotificationLanguage,
		NotificationLabel:    rule.NotificationLabel,
		Project:              project,
		Rule:                 &rule,
		Event:                &event,
		TriggerCount:         1,
		StartsAt:             event.OccurredAt,
		EndsAt:               event.OccurredAt,
		Note:                 ruleEvent.Note,
		AddressLabel:         marketRuleEventAddressLabel(ruleEvent.Details),
	}
	delivery.Timezone = s.marketNotificationTimezone(ctx, rule.DeBoxUserID)
	if event.MarketPoolID != nil {
		delivery.Pool, _ = s.GetMarketPool(ctx, *event.MarketPoolID)
	}
	delivery.Snapshot, _ = s.LatestMarketSnapshot(
		ctx,
		event.ChainID,
		event.TokenAddress,
		event.MarketPoolID,
	)
	return delivery, nil
}

func (s *Store) loadStageMarketDelivery(
	ctx context.Context,
	id int64,
) (MarketNotificationDelivery, error) {
	window, err := collectOne[MarketStageWindow](ctx, s.db, `
		SELECT `+marketStageWindowColumns+`
		FROM market_stage_windows
		WHERE id = $1 AND notification_status = 'sending'
	`, id)
	if isNoRows(err) {
		return MarketNotificationDelivery{}, ErrNotFound
	}
	if err != nil {
		return MarketNotificationDelivery{}, fmt.Errorf("load stage market delivery: %w", err)
	}
	rule, err := collectOne[MarketRule](ctx, s.db, `
		SELECT `+marketRuleColumns+` FROM market_rules WHERE id = $1
	`, window.MarketRuleID)
	if err != nil {
		return MarketNotificationDelivery{}, fmt.Errorf("load stage market rule: %w", err)
	}
	project, err := collectOne[MarketProject](ctx, s.db, `
		SELECT `+marketProjectColumns+` FROM market_projects WHERE id = $1
	`, rule.MarketProjectID)
	if err != nil {
		return MarketNotificationDelivery{}, fmt.Errorf("load stage market project: %w", err)
	}
	recentEvents, err := s.marketStageRecentEvents(ctx, id, project, 5)
	if err != nil {
		return MarketNotificationDelivery{}, err
	}
	return MarketNotificationDelivery{
		Kind:                 "stage",
		ID:                   id,
		DeBoxUserID:          rule.DeBoxUserID,
		MarketRuleID:         &rule.ID,
		NotificationChatID:   rule.NotificationChatID,
		NotificationChatType: rule.NotificationChatType,
		NotificationLanguage: rule.NotificationLanguage,
		NotificationLabel:    rule.NotificationLabel,
		Project:              project,
		Rule:                 &rule,
		TriggerCount:         window.TriggerCount,
		StartsAt:             window.StartsAt,
		EndsAt:               window.EndsAt,
		RecentEvents:         recentEvents,
		Timezone:             s.marketNotificationTimezone(ctx, rule.DeBoxUserID),
	}, nil
}

func (s *Store) marketStageRecentEvents(
	ctx context.Context,
	windowID int64,
	project MarketProject,
	limit int,
) ([]MarketNotificationEvent, error) {
	type eventReference struct {
		MarketRuleEventID int64 `db:"market_rule_event_id"`
	}
	values, err := collectMany[eventReference](ctx, s.db, `
		SELECT mre.id AS market_rule_event_id
		FROM market_stage_window_events mswe
		JOIN market_rule_events mre ON mre.id = mswe.market_rule_event_id
		JOIN market_events me ON me.id = mre.market_event_id
		WHERE mswe.market_stage_window_id = $1 AND me.reorged = 0
		ORDER BY mre.created_at DESC, mre.id DESC
		LIMIT $2
	`, windowID, limit)
	if err != nil {
		return nil, fmt.Errorf("list market stage events: %w", err)
	}
	result := make([]MarketNotificationEvent, 0, len(values))
	for _, value := range values {
		item, err := s.loadMarketNotificationEvent(ctx, value.MarketRuleEventID, &project)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func (s *Store) loadCombinationMarketDelivery(
	ctx context.Context,
	id int64,
) (MarketNotificationDelivery, error) {
	window, err := collectOne[MarketCombinationWindow](ctx, s.db, `
		SELECT `+marketCombinationWindowColumns+`
		FROM market_combination_windows
		WHERE id = $1 AND notification_status = 'sending'
	`, id)
	if isNoRows(err) {
		return MarketNotificationDelivery{}, ErrNotFound
	}
	if err != nil {
		return MarketNotificationDelivery{}, fmt.Errorf("load market combination delivery: %w", err)
	}
	combination, err := collectOne[MarketCombinationRule](ctx, s.db, `
		SELECT `+marketCombinationRuleColumns+`
		FROM market_combination_rules
		WHERE id = $1
	`, window.MarketCombinationRuleID)
	if err != nil {
		return MarketNotificationDelivery{}, fmt.Errorf("load market combination rule: %w", err)
	}
	progress, err := s.marketCombinationProgress(ctx, id)
	if err != nil {
		return MarketNotificationDelivery{}, err
	}
	combinationID := combination.ID
	return MarketNotificationDelivery{
		Kind:                    "combination",
		ID:                      id,
		DeBoxUserID:             combination.DeBoxUserID,
		MarketCombinationRuleID: &combinationID,
		NotificationChatID:      combination.NotificationChatID,
		NotificationChatType:    combination.NotificationChatType,
		NotificationLanguage:    combination.NotificationLanguage,
		NotificationLabel:       combination.NotificationLabel,
		TriggerCount:            window.TotalTriggerCount,
		StartsAt:                window.StartsAt,
		EndsAt:                  window.EndsAt,
		Note:                    combination.Note,
		CombinationMembers:      progress,
		Timezone:                s.marketNotificationTimezone(ctx, combination.DeBoxUserID),
	}, nil
}

func (s *Store) marketCombinationProgress(
	ctx context.Context,
	windowID int64,
) ([]MarketCombinationProgress, error) {
	values, err := collectMany[MarketCombinationProgress](ctx, s.db, `
		SELECT
			mcm.id AS member_id,
			mcm.source_type,
			CASE
			  WHEN mcm.source_type = 'watch' THEN wr.rule_type
			  ELSE mr.rule_type
			END AS rule_type,
			mcwm.required_trigger_count,
			mcwm.trigger_count
		FROM market_combination_window_members mcwm
		JOIN market_combination_members mcm
		  ON mcm.id = mcwm.market_combination_member_id
		LEFT JOIN watch_rules wr ON wr.id = mcm.watch_rule_id
		LEFT JOIN market_rules mr ON mr.id = mcm.market_rule_id
		WHERE mcwm.market_combination_window_id = $1
		ORDER BY mcm.id
	`, windowID)
	if err != nil {
		return nil, fmt.Errorf("load market combination progress: %w", err)
	}
	for index := range values {
		type triggerValue struct {
			Note              string `db:"note"`
			MarketRuleEventID *int64 `db:"market_rule_event_id"`
		}
		triggers, err := collectMany[triggerValue](ctx, s.db, `
			SELECT mcte.note, mcte.market_rule_event_id
			FROM market_combination_trigger_events mcte
			WHERE mcte.market_combination_window_id = $1
			  AND mcte.market_combination_member_id = $2
			ORDER BY mcte.created_at DESC, mcte.id DESC
			LIMIT 3
		`, windowID, values[index].MemberID)
		if err != nil {
			return nil, fmt.Errorf("load market combination triggers: %w", err)
		}
		for _, trigger := range triggers {
			if trigger.MarketRuleEventID == nil {
				values[index].RecentNotes = append(values[index].RecentNotes, trigger.Note)
				continue
			}
			event, err := s.loadMarketNotificationEvent(ctx, *trigger.MarketRuleEventID, nil)
			if err != nil {
				return nil, err
			}
			values[index].RecentEvents = append(values[index].RecentEvents, event)
		}
	}
	return values, nil
}

func (s *Store) loadMarketNotificationEvent(
	ctx context.Context,
	marketRuleEventID int64,
	knownProject *MarketProject,
) (MarketNotificationEvent, error) {
	ruleEvent, err := collectOne[MarketRuleEvent](ctx, s.db, `
		SELECT `+marketRuleEventColumns+`
		FROM market_rule_events
		WHERE id = $1
	`, marketRuleEventID)
	if err != nil {
		return MarketNotificationEvent{}, fmt.Errorf("load market notification rule event: %w", err)
	}
	event, err := collectOne[MarketEvent](ctx, s.db, `
		SELECT `+marketEventColumns+`
		FROM market_events
		WHERE id = $1 AND reorged = 0
	`, ruleEvent.MarketEventID)
	if err != nil {
		return MarketNotificationEvent{}, fmt.Errorf("load market notification event: %w", err)
	}
	project := MarketProject{}
	if knownProject != nil {
		project = *knownProject
	} else {
		project, err = collectOne[MarketProject](ctx, s.db, `
			SELECT `+marketProjectColumns+`
			FROM market_projects
			WHERE id = (
				SELECT mr.market_project_id
				FROM market_rules mr
				WHERE mr.id = $1
			)
		`, ruleEvent.MarketRuleID)
		if err != nil {
			return MarketNotificationEvent{}, fmt.Errorf("load market notification project: %w", err)
		}
	}
	result := MarketNotificationEvent{
		Project:      project,
		Event:        event,
		Note:         ruleEvent.Note,
		AddressLabel: marketRuleEventAddressLabel(ruleEvent.Details),
	}
	if event.MarketPoolID != nil {
		result.Pool, _ = s.GetMarketPool(ctx, *event.MarketPoolID)
	}
	return result, nil
}

func marketRuleEventAddressLabel(details json.RawMessage) string {
	values := struct {
		AddressLabel string `json:"address_label"`
	}{}
	if err := json.Unmarshal(details, &values); err != nil {
		return ""
	}
	return strings.TrimSpace(values.AddressLabel)
}

func (s *Store) marketNotificationTimezone(ctx context.Context, deboxUserID string) string {
	type timezoneValue struct {
		Timezone string `db:"timezone"`
	}
	value, err := collectOptional[timezoneValue](ctx, s.db, `
		SELECT daily_summary_timezone AS timezone
		FROM subscriptions
		WHERE debox_user_id = $1
		  AND status = 'active'
		  AND (is_permanent = 1 OR expires_at > NOW())
		ORDER BY CASE plan_code
		           WHEN 'professional' THEN 2
		           WHEN 'standard' THEN 1
		           ELSE 0
		         END DESC,
		         is_permanent DESC,
		         expires_at DESC
		LIMIT 1
	`, deboxUserID)
	if err != nil || value == nil || strings.TrimSpace(value.Timezone) == "" {
		return "Asia/Shanghai"
	}
	return value.Timezone
}

func (s *Store) CompleteMarketDelivery(
	ctx context.Context,
	claim MarketDeliveryClaim,
	messageID *string,
	deliveryError string,
) error {
	status := "sent"
	if deliveryError != "" {
		status = "failed"
	}
	table := ""
	switch claim.Kind {
	case "realtime":
		table = "market_rule_events"
	case "stage":
		table = "market_stage_windows"
	case "combination":
		table = "market_combination_windows"
	default:
		return ErrInvalidNotificationStatus
	}
	query := fmt.Sprintf(`
		UPDATE %s
		SET notification_status = CASE
		      WHEN $2 = 'failed' AND notification_attempts + 1 >= 5 THEN 'skipped'
		      ELSE $2
		    END,
		    notification_message_id = COALESCE($3, notification_message_id),
		    notification_error = $4,
		    notification_attempts = notification_attempts + 1,
		    notification_attempted_at = NOW(),
		    notification_sent_at = CASE WHEN $2 = 'sent' THEN NOW() ELSE notification_sent_at END,
		    next_attempt_at = CASE
		      WHEN $2 = 'failed'
		      THEN NOW() + make_interval(
		        secs => LEAST(1800, 30 * CAST(POWER(2, notification_attempts) AS INTEGER))
		      )
		      ELSE next_attempt_at
		    END
		WHERE id = $1 AND notification_status = 'sending'
	`, table)
	tag, err := s.db.Exec(
		ctx,
		query,
		claim.ID,
		status,
		messageID,
		truncate(deliveryError, 2000),
	)
	if err != nil {
		return fmt.Errorf("complete %s market delivery: %w", claim.Kind, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if status == "sent" {
		switch claim.Kind {
		case "stage":
			_, _ = s.db.Exec(ctx, `
				UPDATE market_rule_events mre
				SET notification_status = 'sent',
				    notification_message_id = $2,
				    notification_sent_at = NOW()
				FROM market_stage_window_events mswe
				WHERE mswe.market_stage_window_id = $1
				  AND mre.id = mswe.market_rule_event_id
				  AND mre.notification_status = 'staged'
			`, claim.ID, messageID)
		case "combination":
			_, _ = s.db.Exec(ctx, `
				UPDATE market_rule_events mre
				SET notification_status = 'sent',
				    notification_message_id = $2,
				    notification_sent_at = NOW()
				FROM market_combination_trigger_events mcte
				WHERE mcte.market_combination_window_id = $1
				  AND mre.id = mcte.market_rule_event_id
				  AND mre.notification_status = 'combined'
			`, claim.ID, messageID)
		}
	}
	return nil
}

func durationInterval(value time.Duration) string {
	return fmt.Sprintf("%f seconds", value.Seconds())
}

// Scan target helpers keep large join scans type-safe without duplicating the
// column order used throughout the market store.
func marketRuleScanTargets(value *MarketRule) []any {
	return []any{
		&value.ID, &value.DeBoxUserID, &value.MarketProjectID, &value.MarketPoolID,
		&value.RuleType, &value.ThresholdValue, &value.ThresholdUnit,
		&value.WindowMinutes, &value.Sensitivity, &value.CooldownSeconds,
		&value.RuleScope, &value.DeliveryMode, &value.CycleType,
		&value.CycleMinutes, &value.TriggerCountThreshold,
		&value.NotificationChatID, &value.NotificationChatType,
		&value.NotificationLabel, &value.NotificationLanguage,
		&value.Enabled, &value.RunStatus, &value.PauseReason,
		&value.AggregationAnchorAt, &value.State, &value.LastEvaluatedAt,
		&value.LastTriggeredAt, &value.CreatedAt, &value.UpdatedAt,
	}
}

func marketEventScanTargets(value *MarketEvent) []any {
	return []any{
		&value.ID, &value.MarketPoolID, &value.ChainKey, &value.ChainID,
		&value.TokenAddress, &value.EventType, &value.EventKey,
		&value.TransactionHash, &value.TransactionIndex, &value.LogIndex,
		&value.BlockNumber, &value.BlockHash, &value.WalletAddress,
		&value.TokenAmountRaw, &value.QuoteAmountRaw, &value.TokenAmount,
		&value.QuoteAmount, &value.USDValue, &value.PriceUSD, &value.Source,
		&value.Confidence, &value.Confirmed, &value.Reorged, &value.OccurredAt,
		&value.ObservedAt, &value.RawPayload, &value.Metadata,
	}
}

func marketRuleEventScanTargets(value *MarketRuleEvent) []any {
	return []any{
		&value.ID, &value.MarketRuleID, &value.MarketEventID, &value.TriggerKey,
		&value.PreviousValue, &value.CurrentValue, &value.Note, &value.Details,
		&value.NotificationMessageID, &value.NotificationStatus,
		&value.NotificationError, &value.NotificationAttempts,
		&value.NotificationAttemptedAt, &value.NotificationSentAt,
		&value.NextAttemptAt, &value.CreatedAt,
	}
}

func marketProjectScanTargets(value *MarketProject) []any {
	return []any{
		&value.ID, &value.DeBoxUserID, &value.ChainKey, &value.ChainID,
		&value.TokenAddress, &value.TokenName, &value.TokenSymbol,
		&value.TokenDecimals, &value.TotalSupplyRaw, &value.Status,
		&value.PauseReason, &value.FourMemeStatus, &value.MainPoolID,
		&value.Metadata, &value.LastDiscoveredAt, &value.CreatedAt,
		&value.UpdatedAt,
	}
}

func marketStageWindowScanTargets(value *MarketStageWindow) []any {
	return []any{
		&value.ID, &value.DeBoxUserID, &value.MarketRuleID, &value.StartsAt,
		&value.EndsAt, &value.TriggerCount, &value.NotificationStatus,
		&value.NotificationMessageID, &value.NotificationError,
		&value.NotificationAttempts, &value.NotificationAttemptedAt,
		&value.NotificationSentAt, &value.NextAttemptAt, &value.ClosedAt,
		&value.CreatedAt, &value.UpdatedAt,
	}
}

func marketCombinationRuleScanTargets(value *MarketCombinationRule) []any {
	return []any{
		&value.ID, &value.DeBoxUserID, &value.Note, &value.CycleType,
		&value.CycleMinutes, &value.NotificationChatID,
		&value.NotificationChatType, &value.NotificationLabel,
		&value.NotificationLanguage, &value.Enabled, &value.RunStatus,
		&value.PauseReason, &value.AggregationAnchorAt, &value.CreatedAt,
		&value.UpdatedAt,
	}
}

func marketCombinationWindowScanTargets(value *MarketCombinationWindow) []any {
	return []any{
		&value.ID, &value.DeBoxUserID, &value.MarketCombinationRuleID,
		&value.StartsAt, &value.EndsAt, &value.TotalTriggerCount,
		&value.NotificationStatus, &value.NotificationMessageID,
		&value.NotificationError, &value.NotificationAttempts,
		&value.NotificationAttemptedAt, &value.NotificationSentAt,
		&value.NextAttemptAt, &value.ClosedAt, &value.CreatedAt,
		&value.UpdatedAt,
	}
}

func encodeDetails(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}
