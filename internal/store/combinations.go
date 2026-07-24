package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	minimumCombinationMembers       = 2
	combinationRecentEventLimit int = 3
)

func combinationRuleSlotCost(memberCount int) int64 {
	return int64(memberCount) + 1
}

type CreateCombinationMemberParams struct {
	Rule                 CreateWatchRuleParams
	RequiredTriggerCount int64
}

type CreateCombinationRuleParams struct {
	DeBoxUserID          string
	Note                 string
	CycleType            string
	CycleMinutes         int32
	NotificationChatID   string
	NotificationChatType string
	NotificationLabel    string
	NotificationLanguage string
	Members              []CreateCombinationMemberParams
}

type walletValue struct {
	WalletAddress string `db:"wallet_address"`
}

type watchRuleID struct {
	ID int64 `db:"id"`
}

type combinationRecentEvent struct {
	WatchRuleID int64  `db:"watch_rule_id"`
	Note        string `db:"note"`
}

func (s *Store) CreateCombinationRuleWithinQuota(
	ctx context.Context,
	params CreateCombinationRuleParams,
	policy QuotaPolicy,
) (CombinationRule, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (CombinationRule, error) {
		if err := lockUser(ctx, tx, params.DeBoxUserID); err != nil {
			return CombinationRule{}, err
		}
		if err := requirePolicyPlan(ctx, tx, params.DeBoxUserID, policy); err != nil {
			return CombinationRule{}, err
		}
		if !policy.CombinationRules {
			return CombinationRule{}, ErrCombinationRulesDenied
		}
		if len(params.Members) < minimumCombinationMembers {
			return CombinationRule{}, ErrInvalidCombinationRule
		}
		if params.CycleMinutes <= 0 {
			return CombinationRule{}, ErrInvalidCombinationRule
		}
		if params.NotificationChatType == "group" && !policy.GroupNotification {
			return CombinationRule{}, ErrGroupNotificationDenied
		}
		for _, member := range params.Members {
			if member.RequiredTriggerCount <= 0 {
				return CombinationRule{}, ErrInvalidCombinationRule
			}
			if !policy.allowsRuleType(member.Rule.RuleType) {
				return CombinationRule{}, ErrRuleTypeDenied
			}
		}

		activeRuleCount, err := countActiveRuleSlots(ctx, tx, params.DeBoxUserID)
		if err != nil {
			return CombinationRule{}, err
		}
		if activeRuleCount+combinationRuleSlotCost(len(params.Members)) > int64(policy.RuleLimit) {
			return CombinationRule{}, ErrRuleLimitReached
		}
		if err := requireCombinationWalletQuota(ctx, tx, params, policy.WalletLimit); err != nil {
			return CombinationRule{}, err
		}

		cycleType := normalizeCycleType(params.CycleType)
		combination, err := collectOne[CombinationRule](ctx, tx, `
			INSERT INTO combination_rules (
				debox_user_id, note, cycle_type, cycle_minutes,
				notification_chat_id, notification_chat_type,
				notification_label, notification_language,
				aggregation_anchor_at
			)
			VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8,
				CASE WHEN $3 = 'fixed' THEN NOW() ELSE NULL END
			)
			RETURNING `+combinationRuleColumns,
			params.DeBoxUserID,
			truncate(strings.TrimSpace(params.Note), 500),
			cycleType,
			params.CycleMinutes,
			params.NotificationChatID,
			params.NotificationChatType,
			params.NotificationLabel,
			normalizeLanguage(params.NotificationLanguage),
		)
		if err != nil {
			return CombinationRule{}, fmt.Errorf("create combination rule: %w", err)
		}

		combination.Members = make([]CombinationRuleMember, 0, len(params.Members))
		for _, memberParams := range params.Members {
			ruleParams := memberParams.Rule
			ruleParams.DeBoxUserID = params.DeBoxUserID
			ruleParams.NotificationChatID = params.NotificationChatID
			ruleParams.NotificationChatType = params.NotificationChatType
			ruleParams.NotificationLabel = params.NotificationLabel
			ruleParams.NotificationLanguage = normalizeLanguage(params.NotificationLanguage)
			ruleParams.RuleScope = "combination"
			ruleParams.DeliveryMode = "realtime"
			ruleParams.CycleType = "fixed"
			ruleParams.CycleMinutes = 60
			ruleParams.TriggerCountThreshold = 1

			rule, err := createWatchRule(ctx, tx, ruleParams)
			if err != nil {
				return CombinationRule{}, err
			}
			member, err := collectOne[CombinationRuleMember](ctx, tx, `
				INSERT INTO combination_rule_members (
					combination_rule_id, watch_rule_id, required_trigger_count
				)
				VALUES ($1, $2, $3)
				RETURNING `+combinationRuleMemberColumns,
				combination.ID,
				rule.ID,
				memberParams.RequiredTriggerCount,
			)
			if err != nil {
				return CombinationRule{}, fmt.Errorf("create combination member: %w", err)
			}
			member.Rule = rule
			combination.Members = append(combination.Members, member)
		}
		return combination, nil
	})
}

func requireCombinationWalletQuota(
	ctx context.Context,
	tx DBTX,
	params CreateCombinationRuleParams,
	walletLimit int,
) error {
	wallets, err := collectMany[walletValue](ctx, tx, `
		SELECT DISTINCT LOWER(wallet_address) AS wallet_address
		FROM watch_rules
		WHERE debox_user_id = $1 AND enabled = 1 AND run_status = 'active'
	`, params.DeBoxUserID)
	if err != nil {
		return fmt.Errorf("list active wallets: %w", err)
	}
	unique := make(map[string]struct{}, len(wallets)+len(params.Members))
	for _, wallet := range wallets {
		unique[strings.ToLower(wallet.WalletAddress)] = struct{}{}
	}
	for _, member := range params.Members {
		unique[strings.ToLower(member.Rule.WalletAddress)] = struct{}{}
	}
	if len(unique) > walletLimit {
		return ErrWalletLimitReached
	}
	return nil
}

func (s *Store) ListUserCombinationRules(
	ctx context.Context,
	deboxUserID string,
) ([]CombinationRule, error) {
	rules, err := collectMany[CombinationRule](ctx, s.db, `
		SELECT `+combinationRuleColumns+`
		FROM combination_rules
		WHERE debox_user_id = $1
		ORDER BY created_at DESC, id DESC
	`, deboxUserID)
	if err != nil {
		return nil, fmt.Errorf("list combination rules: %w", err)
	}
	for index := range rules {
		members, err := listCombinationMembers(ctx, s.db, rules[index].ID)
		if err != nil {
			return nil, err
		}
		rules[index].Members = members
	}
	return rules, nil
}

func listCombinationMembers(
	ctx context.Context,
	db DBTX,
	combinationRuleID int64,
) ([]CombinationRuleMember, error) {
	members, err := collectMany[CombinationRuleMember](ctx, db, `
		SELECT `+combinationRuleMemberColumns+`
		FROM combination_rule_members
		WHERE combination_rule_id = $1
		ORDER BY id ASC
	`, combinationRuleID)
	if err != nil {
		return nil, fmt.Errorf("list combination members: %w", err)
	}
	for index := range members {
		rule, err := collectOne[WatchRule](ctx, db, `
			SELECT `+watchRuleColumns+`
			FROM watch_rules
			WHERE id = $1
		`, members[index].WatchRuleID)
		if err != nil {
			return nil, fmt.Errorf("get combination member rule: %w", err)
		}
		members[index].Rule = rule
	}
	return members, nil
}

func (s *Store) DeleteCombinationRule(
	ctx context.Context,
	combinationRuleID int64,
	deboxUserID string,
) (bool, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (bool, error) {
		combination, err := collectOptional[CombinationRule](ctx, tx, `
			SELECT `+combinationRuleColumns+`
			FROM combination_rules
			WHERE id = $1 AND debox_user_id = $2
			FOR UPDATE
		`, combinationRuleID, deboxUserID)
		if err != nil {
			return false, fmt.Errorf("lock combination rule: %w", err)
		}
		if combination == nil {
			return false, ErrNotFound
		}
		memberIDs, err := collectMany[watchRuleID](ctx, tx, `
			SELECT watch_rule_id AS id
			FROM combination_rule_members
			WHERE combination_rule_id = $1
		`, combinationRuleID)
		if err != nil {
			return false, fmt.Errorf("list combination member ids: %w", err)
		}
		if _, err := tx.Exec(ctx, "DELETE FROM combination_rules WHERE id = $1", combinationRuleID); err != nil {
			return false, fmt.Errorf("delete combination rule: %w", err)
		}
		for _, member := range memberIDs {
			if _, err := tx.Exec(ctx, `
				DELETE FROM watch_rules
				WHERE id = $1 AND debox_user_id = $2 AND rule_scope = 'combination'
			`, member.ID, deboxUserID); err != nil {
				return false, fmt.Errorf("delete combination member rule: %w", err)
			}
		}
		return true, nil
	})
}

func (s *Store) UpdateCombinationRuleNotificationLanguage(
	ctx context.Context,
	combinationRuleID int64,
	deboxUserID string,
	language string,
) (CombinationRule, error) {
	combination, err := collectOne[CombinationRule](ctx, s.db, `
		UPDATE combination_rules
		SET notification_language = $1
		WHERE id = $2 AND debox_user_id = $3
		RETURNING `+combinationRuleColumns,
		normalizeLanguage(language),
		combinationRuleID,
		deboxUserID,
	)
	if isNoRows(err) {
		return CombinationRule{}, ErrNotFound
	}
	if err != nil {
		return CombinationRule{}, fmt.Errorf("update combination notification language: %w", err)
	}
	combination.Members, err = listCombinationMembers(ctx, s.db, combinationRuleID)
	if err != nil {
		return CombinationRule{}, err
	}
	return combination, nil
}

func (s *Store) RestoreCombinationRuleWithinQuota(
	ctx context.Context,
	combinationRuleID int64,
	deboxUserID string,
	policy QuotaPolicy,
) (CombinationRule, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (CombinationRule, error) {
		if err := lockUser(ctx, tx, deboxUserID); err != nil {
			return CombinationRule{}, err
		}
		if err := requirePolicyPlan(ctx, tx, deboxUserID, policy); err != nil {
			return CombinationRule{}, err
		}
		if !policy.CombinationRules {
			return CombinationRule{}, ErrCombinationRulesDenied
		}
		combination, err := collectOne[CombinationRule](ctx, tx, `
			SELECT `+combinationRuleColumns+`
			FROM combination_rules
			WHERE id = $1 AND debox_user_id = $2 AND enabled = 1
			FOR UPDATE
		`, combinationRuleID, deboxUserID)
		if isNoRows(err) {
			return CombinationRule{}, ErrNotFound
		}
		if err != nil {
			return CombinationRule{}, fmt.Errorf("lock combination rule: %w", err)
		}
		members, err := listCombinationMembers(ctx, tx, combinationRuleID)
		if err != nil {
			return CombinationRule{}, err
		}
		if len(members) < minimumCombinationMembers {
			return CombinationRule{}, ErrInvalidCombinationRule
		}
		if combination.NotificationChatType == "group" && !policy.GroupNotification {
			return CombinationRule{}, ErrGroupNotificationDenied
		}
		for _, member := range members {
			if !policy.allowsRuleType(member.Rule.RuleType) {
				return CombinationRule{}, ErrRuleTypeDenied
			}
		}
		if combination.RunStatus == "active" {
			combination.Members = members
			return combination, nil
		}
		if err := requireRestoredCombinationQuota(ctx, tx, deboxUserID, members, policy); err != nil {
			return CombinationRule{}, err
		}

		restored, err := collectOne[CombinationRule](ctx, tx, `
			UPDATE combination_rules
			SET run_status = 'active',
			    aggregation_anchor_at = CASE WHEN cycle_type = 'fixed' THEN NOW() ELSE NULL END
			WHERE id = $1
			RETURNING `+combinationRuleColumns,
			combinationRuleID,
		)
		if err != nil {
			return CombinationRule{}, fmt.Errorf("restore combination rule: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE watch_rules
			SET run_status = 'active'
			WHERE debox_user_id = $1
			  AND rule_scope = 'combination'
			  AND id IN (
				SELECT watch_rule_id
				FROM combination_rule_members
				WHERE combination_rule_id = $2
			  )
		`, deboxUserID, combinationRuleID); err != nil {
			return CombinationRule{}, fmt.Errorf("restore combination members: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE aggregation_windows
			SET closed_at = NOW(), updated_at = NOW()
			WHERE combination_rule_id = $1 AND closed_at IS NULL
		`, combinationRuleID); err != nil {
			return CombinationRule{}, fmt.Errorf("reset restored combination aggregation: %w", err)
		}
		restored.Members, err = listCombinationMembers(ctx, tx, combinationRuleID)
		if err != nil {
			return CombinationRule{}, err
		}
		return restored, nil
	})
}

func requireRestoredCombinationQuota(
	ctx context.Context,
	tx DBTX,
	deboxUserID string,
	members []CombinationRuleMember,
	policy QuotaPolicy,
) error {
	activeRuleCount, err := countActiveRuleSlots(ctx, tx, deboxUserID)
	if err != nil {
		return err
	}
	if activeRuleCount+combinationRuleSlotCost(len(members)) > int64(policy.RuleLimit) {
		return ErrRuleLimitReached
	}
	wallets, err := collectMany[walletValue](ctx, tx, `
		SELECT DISTINCT LOWER(wallet_address) AS wallet_address
		FROM watch_rules
		WHERE debox_user_id = $1 AND enabled = 1 AND run_status = 'active'
	`, deboxUserID)
	if err != nil {
		return fmt.Errorf("list active wallets: %w", err)
	}
	unique := make(map[string]struct{}, len(wallets)+len(members))
	for _, wallet := range wallets {
		unique[strings.ToLower(wallet.WalletAddress)] = struct{}{}
	}
	for _, member := range members {
		unique[strings.ToLower(member.Rule.WalletAddress)] = struct{}{}
	}
	if len(unique) > policy.WalletLimit {
		return ErrWalletLimitReached
	}
	return nil
}

type RecordCombinationTriggerParams struct {
	WatchRuleID   int64
	DeBoxUserID   string
	PreviousValue *string
	CurrentValue  *string
	Note          string
	EventKey      string
	OccurredAt    *time.Time
}

type combinationTriggerConfiguration struct {
	CombinationRuleID    int64     `db:"combination_rule_id"`
	DeBoxUserID          string    `db:"debox_user_id"`
	Note                 string    `db:"note"`
	CycleType            string    `db:"cycle_type"`
	CycleMinutes         int32     `db:"cycle_minutes"`
	AggregationAnchorAt  time.Time `db:"aggregation_anchor_at"`
	NotificationChatID   string    `db:"notification_chat_id"`
	NotificationChatType string    `db:"notification_chat_type"`
	NotificationLabel    string    `db:"notification_label"`
	NotificationLanguage string    `db:"notification_language"`
	WatchRuleID          int64     `db:"watch_rule_id"`
	RuleType             string    `db:"rule_type"`
}

func (s *Store) RecordCombinationTrigger(
	ctx context.Context,
	params RecordCombinationTriggerParams,
) (CombinationTriggerResult, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (CombinationTriggerResult, error) {
		config, err := collectOne[combinationTriggerConfiguration](ctx, tx, `
			SELECT
				cr.id AS combination_rule_id,
				cr.debox_user_id,
				cr.note,
				cr.cycle_type,
				cr.cycle_minutes,
				COALESCE(cr.aggregation_anchor_at, cr.created_at) AS aggregation_anchor_at,
				cr.notification_chat_id,
				cr.notification_chat_type,
				cr.notification_label,
				cr.notification_language,
				wr.id AS watch_rule_id,
				wr.rule_type
			FROM combination_rules cr
			JOIN combination_rule_members crm ON crm.combination_rule_id = cr.id
			JOIN watch_rules wr ON wr.id = crm.watch_rule_id
			WHERE wr.id = $1
			  AND wr.debox_user_id = $2
			  AND wr.rule_scope = 'combination'
			  AND wr.enabled = 1
			  AND wr.run_status = 'active'
			  AND cr.enabled = 1
			  AND cr.run_status = 'active'
			FOR UPDATE OF cr, wr
		`, params.WatchRuleID, params.DeBoxUserID)
		if isNoRows(err) {
			return CombinationTriggerResult{}, ErrNotFound
		}
		if err != nil {
			return CombinationTriggerResult{}, fmt.Errorf("lock combination trigger: %w", err)
		}

		var now time.Time
		if err := tx.QueryRow(ctx, "SELECT NOW()").Scan(&now); err != nil {
			return CombinationTriggerResult{}, fmt.Errorf("get combination time: %w", err)
		}
		window, err := currentCombinationWindow(ctx, tx, config, now)
		if err != nil {
			return CombinationTriggerResult{}, err
		}
		details, err := json.Marshal(map[string]string{"note": truncate(params.Note, 1000)})
		if err != nil {
			return CombinationTriggerResult{}, fmt.Errorf("encode combination trigger details: %w", err)
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
			config.WatchRuleID,
			window.ID,
			config.RuleType,
			params.EventKey,
			params.PreviousValue,
			params.CurrentValue,
			string(details),
			params.OccurredAt,
			now,
		).Scan(&triggerEventID); err != nil {
			return CombinationTriggerResult{}, fmt.Errorf("record combination trigger event: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE aggregation_windows
			SET total_trigger_count = total_trigger_count + 1, updated_at = $2
			WHERE id = $1
		`, window.ID, now); err != nil {
			return CombinationTriggerResult{}, fmt.Errorf("increment combination total: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE aggregation_window_members
			SET trigger_count = trigger_count + 1,
			    reached_at = CASE
			      WHEN reached_at IS NULL AND trigger_count + 1 >= required_trigger_count THEN $3
			      ELSE reached_at
			    END,
			    updated_at = $3
			WHERE aggregation_window_id = $1 AND watch_rule_id = $2
		`, window.ID, config.WatchRuleID, now); err != nil {
			return CombinationTriggerResult{}, fmt.Errorf("increment combination member: %w", err)
		}

		progress, err := combinationProgress(ctx, tx, window.ID)
		if err != nil {
			return CombinationTriggerResult{}, err
		}
		result := CombinationTriggerResult{
			CombinationRuleID: config.CombinationRuleID,
			WindowID:          window.ID,
			TriggerEventID:    triggerEventID,
			WindowStartsAt:    window.StartsAt,
			WindowEndsAt:      window.EndsAt,
			MemberProgress:    progress,
		}
		if window.NotificationSent == 1 || !allCombinationMembersReached(progress) {
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
			return CombinationTriggerResult{}, fmt.Errorf("claim combination notification: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return result, nil
		}

		var totalTriggerCount int64
		if err := tx.QueryRow(ctx, `
			SELECT total_trigger_count FROM aggregation_windows WHERE id = $1
		`, window.ID).Scan(&totalTriggerCount); err != nil {
			return CombinationTriggerResult{}, fmt.Errorf("get combination total: %w", err)
		}
		notification, err := collectOne[AggregateNotification](ctx, tx, `
			INSERT INTO aggregate_notifications (
				debox_user_id, aggregation_window_id, notification_kind,
				notification_chat_id, notification_chat_type, notification_label,
				notification_language, note, trigger_count_snapshot,
				notification_status
			)
			VALUES ($1, $2, 'combination', $3, $4, $5, $6, $7, $8, 'pending')
			RETURNING `+aggregateNotificationColumns,
			config.DeBoxUserID,
			window.ID,
			config.NotificationChatID,
			config.NotificationChatType,
			config.NotificationLabel,
			normalizeLanguage(config.NotificationLanguage),
			config.Note,
			totalTriggerCount,
		)
		if err != nil {
			return CombinationTriggerResult{}, fmt.Errorf("create combination notification: %w", err)
		}
		recentEvents, err := collectMany[combinationRecentEvent](ctx, tx, `
			SELECT watch_rule_id, note
			FROM (
				SELECT
					watch_rule_id,
					COALESCE(details->>'note', '') AS note,
					ROW_NUMBER() OVER (
						PARTITION BY watch_rule_id
						ORDER BY created_at DESC, id DESC
					) AS event_position
				FROM rule_trigger_events
				WHERE aggregation_window_id = $1
			) ranked_events
			WHERE event_position <= $2
			ORDER BY watch_rule_id ASC, event_position ASC
		`, window.ID, combinationRecentEventLimit)
		if err != nil {
			return CombinationTriggerResult{}, fmt.Errorf("list recent combination events: %w", err)
		}
		result.MemberProgress = attachRecentCombinationEvents(result.MemberProgress, recentEvents)
		result.NotificationDue = true
		result.Notification = &notification
		return result, nil
	})
}

func attachRecentCombinationEvents(
	progress []CombinationMemberProgress,
	events []combinationRecentEvent,
) []CombinationMemberProgress {
	memberIndex := make(map[int64]int, len(progress))
	for index := range progress {
		progress[index].RecentNotes = []string{}
		memberIndex[progress[index].WatchRuleID] = index
	}
	for _, event := range events {
		index, ok := memberIndex[event.WatchRuleID]
		if !ok || len(progress[index].RecentNotes) >= combinationRecentEventLimit {
			continue
		}
		progress[index].RecentNotes = append(progress[index].RecentNotes, event.Note)
	}
	return progress
}

func currentCombinationWindow(
	ctx context.Context,
	tx DBTX,
	config combinationTriggerConfiguration,
	now time.Time,
) (AggregationWindow, error) {
	window, err := collectOptional[AggregationWindow](ctx, tx, `
		SELECT `+aggregationWindowColumns+`
		FROM aggregation_windows
		WHERE combination_rule_id = $1 AND closed_at IS NULL
		FOR UPDATE
	`, config.CombinationRuleID)
	if err != nil {
		return AggregationWindow{}, fmt.Errorf("get open combination window: %w", err)
	}
	if window != nil && !now.Before(window.EndsAt) {
		if _, err := tx.Exec(ctx, `
			UPDATE aggregation_windows
			SET closed_at = $2, updated_at = $2
			WHERE id = $1
		`, window.ID, now); err != nil {
			return AggregationWindow{}, fmt.Errorf("close expired combination window: %w", err)
		}
		window = nil
	}
	if window != nil {
		return *window, nil
	}

	startsAt, endsAt := combinationWindowBounds(config, now)
	created, err := collectOne[AggregationWindow](ctx, tx, `
		INSERT INTO aggregation_windows (
			debox_user_id, source_type, combination_rule_id, starts_at, ends_at
		)
		VALUES ($1, 'combination', $2, $3, $4)
		RETURNING `+aggregationWindowColumns,
		config.DeBoxUserID,
		config.CombinationRuleID,
		startsAt,
		endsAt,
	)
	if err != nil {
		return AggregationWindow{}, fmt.Errorf("create combination window: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO aggregation_window_members (
			aggregation_window_id, watch_rule_id, required_trigger_count
		)
		SELECT $1, watch_rule_id, required_trigger_count
		FROM combination_rule_members
		WHERE combination_rule_id = $2
	`, created.ID, config.CombinationRuleID); err != nil {
		return AggregationWindow{}, fmt.Errorf("snapshot combination members: %w", err)
	}
	return created, nil
}

func combinationWindowBounds(
	config combinationTriggerConfiguration,
	now time.Time,
) (time.Time, time.Time) {
	return stageWindowBounds(stageRuleConfiguration{
		CycleType:           config.CycleType,
		CycleMinutes:        config.CycleMinutes,
		AggregationAnchorAt: config.AggregationAnchorAt,
	}, now)
}

func combinationProgress(
	ctx context.Context,
	tx DBTX,
	windowID int64,
) ([]CombinationMemberProgress, error) {
	progress, err := collectMany[CombinationMemberProgress](ctx, tx, `
		SELECT
			awm.watch_rule_id,
			wr.rule_type,
			awm.required_trigger_count,
			awm.trigger_count
		FROM aggregation_window_members awm
		JOIN watch_rules wr ON wr.id = awm.watch_rule_id
		WHERE awm.aggregation_window_id = $1
		ORDER BY awm.watch_rule_id ASC
	`, windowID)
	if err != nil {
		return nil, fmt.Errorf("list combination progress: %w", err)
	}
	return progress, nil
}

func allCombinationMembersReached(progress []CombinationMemberProgress) bool {
	if len(progress) < minimumCombinationMembers {
		return false
	}
	for _, member := range progress {
		if member.TriggerCount < member.RequiredTriggerCount {
			return false
		}
	}
	return true
}
