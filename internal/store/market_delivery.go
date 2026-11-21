package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const marketStageWindowColumns = `
	id, debox_user_id, market_rule_id, starts_at, ends_at, trigger_count,
	notification_status, delivery_outcome, notification_message_id, notification_error,
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
	total_trigger_count, notification_status, delivery_outcome, notification_message_id,
	notification_error, notification_attempts, notification_attempted_at,
	notification_sent_at, next_attempt_at, closed_at, created_at, updated_at
`

type MarketDeliveryClaim struct {
	Kind       string `db:"kind" json:"kind"`
	ID         int64  `db:"id" json:"id"`
	LeaseToken int64  `db:"notification_lease_token" json:"lease_token"`
}

var ErrMarketDeliveryCapacity = notificationdelivery.ErrCapacity
var ErrMarketDeliveryLeaseLost = errors.New("market delivery lease is no longer owned")
var ErrMarketDeliveryBudget = errors.New("market notification database budget exhausted; work remains deferred")
var ErrMarketRuleEvaluationInactive = errors.New("market rule stopped before evaluation could be persisted")
var ErrMarketRuleEvaluationStale = errors.New("market notification source fence changed during evaluation")

type marketDeliveryBudgetDB struct {
	DBTX
	ctx    context.Context
	owner  string
	ruleID int64
}

func (b *marketDeliveryBudgetDB) Begin(context.Context) (pgx.Tx, error) { return b.DBTX.Begin(b.ctx) }
func (b *marketDeliveryBudgetDB) Exec(_ context.Context, q string, args ...any) (pgconn.CommandTag, error) {
	return b.DBTX.Exec(b.ctx, q, args...)
}
func (b *marketDeliveryBudgetDB) Query(_ context.Context, q string, args ...any) (pgx.Rows, error) {
	return b.DBTX.Query(b.ctx, q, args...)
}
func (b *marketDeliveryBudgetDB) QueryRow(_ context.Context, q string, args ...any) pgx.Row {
	return b.DBTX.QueryRow(b.ctx, q, args...)
}
func marketDeliveryBudgetContext(ctx context.Context, db DBTX) (context.Context, context.CancelFunc) {
	if bound, ok := db.(*marketDeliveryBudgetDB); ok {
		ctx = bound.ctx
	}
	return context.WithTimeout(ctx, 2*time.Second)
}
func marketDeliveryBudgetError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var pgError *pgconn.PgError
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || (errors.As(err, &pgError) && (pgError.Code == "57014" || pgError.Code == "55P03")) {
		return fmt.Errorf("%w: %v", ErrMarketDeliveryBudget, err)
	}
	return err
}
func withMarketDeliveryTx[T any](ctx context.Context, db DBTX, fn func(DBTX) (T, error)) (T, error) {
	ctx, cancel := marketDeliveryBudgetContext(ctx, db)
	defer cancel()
	value, err := withTxValue(ctx, db, func(tx DBTX) (T, error) {
		var zero T
		bounded := &marketDeliveryBudgetDB{DBTX: tx, ctx: ctx}
		if parent, ok := db.(*marketDeliveryBudgetDB); ok {
			bounded.owner = parent.owner
			bounded.ruleID = parent.ruleID
		}
		if _, err := bounded.Exec(ctx, `SET LOCAL statement_timeout='2s'; SET LOCAL lock_timeout='2s'`); err != nil {
			return zero, err
		}
		return fn(bounded)
	})
	return value, marketDeliveryBudgetError(ctx, err)
}

func lockMarketRuleNotificationOwner(ctx context.Context, tx DBTX, ruleID int64) error {
	if bound, ok := tx.(*marketDeliveryBudgetDB); ok && bound.owner != "" && bound.ruleID == ruleID {
		return nil
	}
	var owner string
	if err := tx.QueryRow(ctx, `SELECT debox_user_id FROM market_rules WHERE id=$1`, ruleID).Scan(&owner); err != nil {
		return err
	}
	if err := lockUser(ctx, tx, owner); err != nil {
		return err
	}
	if bound, ok := tx.(*marketDeliveryBudgetDB); ok {
		bound.owner = owner
		bound.ruleID = ruleID
	}
	return nil
}

func lockMarketDeliverySources(ctx context.Context, tx DBTX, claim MarketDeliveryClaim) error {
	if claim.Kind == "realtime" {
		return nil
	}
	link, column := "market_stage_window_events", "market_stage_window_id"
	if claim.Kind == "combination" {
		link, column = "market_combination_trigger_events", "market_combination_window_id"
	}
	rows, err := tx.Query(ctx, `SELECT mre.id FROM market_rule_events mre JOIN `+link+` l ON l.market_rule_event_id=mre.id
	 WHERE l.`+column+`=$1 ORDER BY mre.id LIMIT 2001 FOR NO KEY UPDATE OF mre`, claim.ID)
	if err != nil {
		return err
	}
	ids, err := collectInt64Rows(rows)
	if err != nil {
		return err
	}
	if len(ids) > notificationdelivery.MaxDetailEvents {
		return ErrMarketDeliveryCapacity
	}
	return nil
}

type MarketDeliveryPayload struct {
	SnapshotID       int64                        `json:"snapshot_id"`
	SnapshotPublicID string                       `json:"snapshot_public_id"`
	Payload          notificationdelivery.Payload `json:"payload"`
}

func marketDeliveryTable(kind string) (string, error) {
	switch kind {
	case "realtime":
		return "market_rule_events", nil
	case "stage":
		return "market_stage_windows", nil
	case "combination":
		return "market_combination_windows", nil
	default:
		return "", ErrInvalidNotificationStatus
	}
}

// V2 deliveries keep the exact ready revision that produced their frozen
// projection. A revived parent alone cannot validate a retired projection.
func marketDeliveryEventValidSQL(details, event string) string {
	return fmt.Sprintf(`%[2]s.confirmed=1 AND %[2]s.reorged=0 AND
	 (NOT (%[1]s ? 'trade_projection') OR EXISTS(
	  SELECT 1 FROM market_trade_ready_events ready JOIN market_transaction_receipts receipt
	   ON receipt.id=ready.receipt_id AND receipt.revision=ready.receipt_revision
	  WHERE ready.id=CASE WHEN pg_input_is_valid(%[1]s #>> '{trade_projection,ready_id}','bigint')
	    THEN (%[1]s #>> '{trade_projection,ready_id}')::bigint END
	   AND ready.parent_event_id=%[2]s.id AND ready.valid AND receipt.complete AND receipt.confirmed AND NOT receipt.reorged
	   AND receipt.block_hash=%[2]s.block_hash
	   AND EXISTS(SELECT 1 FROM market_scanned_blocks b WHERE b.chain_id=receipt.chain_id
	     AND b.block_number=receipt.block_number AND b.block_hash=receipt.block_hash AND b.canonical=1)))`, details, event)
}

func enqueueMarketRuleEvent(ctx context.Context, tx DBTX, event MarketRuleEvent) error {
	if event.NotificationStatus == "skipped" {
		return nil
	}
	if event.NotificationStatus == "staged" {
		if _, _, err := recordMarketStageTrigger(ctx, tx, event.ID); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	var occurredAt time.Time
	if err := tx.QueryRow(ctx, `SELECT occurred_at FROM market_events WHERE id=$1`, event.MarketEventID).Scan(&occurredAt); err != nil {
		return err
	}
	_, err := recordMarketCombinationTrigger(ctx, tx, RecordMarketCombinationTriggerParams{
		SourceType: "market", MarketRuleEventID: &event.ID, OccurredAt: occurredAt, Note: event.Note,
	})
	return err
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
	window, err := withMarketDeliveryTx(ctx, s.db, func(tx DBTX) (MarketStageWindow, error) {
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
	OccurredAt            time.Time  `db:"occurred_at"`
}

func recordMarketStageTrigger(
	ctx context.Context,
	tx DBTX,
	ruleEventID int64,
) (MarketStageWindow, bool, error) {
	config, err := collectOne[marketStageConfiguration](ctx, tx, `
		SELECT
			mr.debox_user_id, mr.id AS market_rule_id, mr.cycle_type,
			mr.cycle_minutes, mr.trigger_count_threshold, mr.aggregation_anchor_at, me.occurred_at
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
		  AND me.confirmed = 1
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
	cutoff, err := marketResourceNotificationCutoff(ctx, tx, config.MarketRuleID, false)
	if err != nil {
		return MarketStageWindow{}, false, err
	}
	if (cutoff != nil && config.OccurredAt.Before(*cutoff)) || !config.OccurredAt.Add(time.Duration(config.CycleMinutes)*time.Minute).After(now) {
		_, err := tx.Exec(ctx, `UPDATE market_rule_events SET notification_status='skipped',delivery_outcome='cancelled',
		 notification_error='source window expired or precedes recorded recovery cutoff' WHERE id=$1 AND notification_status='staged'`, ruleEventID)
		return MarketStageWindow{}, false, err
	}
	existing, err := collectOptional[MarketStageWindow](ctx, tx, `SELECT `+marketStageWindowColumns+` FROM market_stage_windows
  WHERE id=(SELECT market_stage_window_id FROM market_stage_window_events WHERE market_rule_event_id=$1)`, ruleEventID)
	if err != nil {
		return MarketStageWindow{}, false, err
	}
	if existing != nil {
		changed, err := recountMarketStageWindow(ctx, tx, existing.ID, config.TriggerCountThreshold)
		if err != nil {
			return MarketStageWindow{}, false, err
		}
		if !changed && (existing.ClosedAt != nil || !now.Before(existing.EndsAt) || existing.NotificationStatus == "sent" || existing.NotificationStatus == "skipped") {
			if _, err := tx.Exec(ctx, `UPDATE market_rule_events SET notification_status='skipped',delivery_outcome='cancelled',notification_error='original stage window is no longer deliverable' WHERE id=$1 AND notification_status='staged'`, ruleEventID); err != nil {
				return MarketStageWindow{}, false, err
			}
		}
		window, err := collectOne[MarketStageWindow](ctx, tx, `SELECT `+marketStageWindowColumns+` FROM market_stage_windows WHERE id=$1`, existing.ID)
		return window, false, err
	}
	window, err := currentMarketStageWindow(ctx, tx, config, now)
	if err != nil {
		return MarketStageWindow{}, false, err
	}
	var emitted bool
	if err := tx.QueryRow(ctx, `SELECT notification_status IN('sent','sending','skipped') OR notification_payload<>'{}'::jsonb
	 FROM market_stage_windows WHERE id=$1`, window.ID).Scan(&emitted); err != nil {
		return MarketStageWindow{}, false, err
	}
	if emitted {
		_, err := tx.Exec(ctx, `UPDATE market_rule_events SET notification_status='skipped',notification_error='stage notification already emitted for this window'
		 WHERE id=$1 AND notification_status='staged'`, ruleEventID)
		return window, false, err
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
	cutoff, err := marketResourceNotificationCutoff(ctx, tx, config.MarketRuleID, false)
	if err != nil {
		return MarketStageWindow{}, err
	}
	window, err := collectOptional[MarketStageWindow](ctx, tx, `
		SELECT `+marketStageWindowColumns+`
		FROM market_stage_windows
		WHERE market_rule_id = $1 AND closed_at IS NULL
		FOR UPDATE
	`, config.MarketRuleID)
	if err != nil {
		return MarketStageWindow{}, fmt.Errorf("get market stage window: %w", err)
	}
	if window != nil && cutoff != nil && window.CreatedAt.Before(*cutoff) {
		if err := closePreCutoffMarketWindow(ctx, tx, "market_stage_windows", window.ID, *cutoff); err != nil {
			return MarketStageWindow{}, err
		}
		window = nil
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
	if cutoff != nil && startsAt.Before(*cutoff) {
		startsAt = *cutoff
	}
	created, err := collectOne[MarketStageWindow](ctx, tx, `
		INSERT INTO market_stage_windows (
			debox_user_id, market_rule_id, starts_at, ends_at,delivery_outcome,notification_route
		)
		SELECT $1,$2,$3,$4,'not_attempted',jsonb_build_object('chat_id',notification_chat_id,'chat_type',notification_chat_type) FROM market_rules WHERE id=$2
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
	return withMarketDeliveryTx(ctx, s.db, func(tx DBTX) (MarketCombinationRule, error) {
		if err := lockUser(ctx, tx, params.DeBoxUserID); err != nil {
			return MarketCombinationRule{}, err
		}
		if err := requirePolicyPlan(ctx, tx, params.DeBoxUserID, policy); err != nil {
			return MarketCombinationRule{}, err
		}
		var scopeErr error
		policy, scopeErr = s.marketScopePolicy(ctx, tx, params.DeBoxUserID, policy)
		if scopeErr != nil {
			return MarketCombinationRule{}, scopeErr
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
		hasHolderMembers := false
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
			isHolder, err := validateMarketCombinationMember(
				ctx,
				tx,
				params.DeBoxUserID,
				member,
				policy,
			)
			if err != nil {
				return MarketCombinationRule{}, err
			}
			hasHolderMembers = hasHolderMembers || isHolder
		}
		duplicate, err := hasDuplicateMarketCombination(ctx, tx, params)
		if err != nil {
			return MarketCombinationRule{}, err
		}
		if duplicate {
			return MarketCombinationRule{}, ErrMarketCombinationExists
		}
		count, err := countActiveRuleSlots(ctx, tx, params.DeBoxUserID)
		if err != nil {
			return MarketCombinationRule{}, err
		}
		if count >= int64(policy.RuleLimit) {
			return MarketCombinationRule{}, ErrRuleLimitReached
		}
		holderTargets, err := s.beginMarketHolderTargetCheck(ctx, tx, hasHolderMembers)
		if err != nil {
			return MarketCombinationRule{}, err
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
		if err := s.checkMarketHolderTargetLimit(ctx, tx, holderTargets); err != nil {
			return MarketCombinationRule{}, err
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
		  AND NOT EXISTS (
			SELECT 1
			FROM market_combination_rule_projects mcrp
			JOIN market_projects mp ON mp.id = mcrp.market_project_id
			WHERE mcrp.market_combination_rule_id = market_combination_rules.id
			  AND mp.status = 'paused'
			  AND mp.pause_reason = 'subscription_expired'
		  )
	`, combinationID, strings.TrimSpace(deboxUserID))
	if err != nil {
		return false, fmt.Errorf("archive market combination: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *Store) DeletePausedMarketCombinationRule(
	ctx context.Context,
	combinationID int64,
	deboxUserID string,
) error {
	_, err := withMarketDeliveryTx(ctx, s.db, func(tx DBTX) (bool, error) {
		var runStatus string
		var frozen bool
		err := tx.QueryRow(ctx, `
			SELECT
				mcr.run_status,
				EXISTS (
					SELECT 1
					FROM market_combination_rule_projects mcrp
					JOIN market_projects mp ON mp.id = mcrp.market_project_id
					WHERE mcrp.market_combination_rule_id = mcr.id
					  AND mp.status = 'paused'
					  AND mp.pause_reason = 'subscription_expired'
				)
			FROM market_combination_rules mcr
			WHERE mcr.id = $1 AND mcr.debox_user_id = $2 AND mcr.enabled = 1
			FOR UPDATE OF mcr
		`, combinationID, strings.TrimSpace(deboxUserID)).Scan(&runStatus, &frozen)
		if isNoRows(err) {
			return false, ErrNotFound
		}
		if err != nil {
			return false, fmt.Errorf(
				"lock paused market combination for deletion: %w",
				err,
			)
		}
		if runStatus != "paused" {
			return false, ErrMarketCombinationNotPaused
		}
		if frozen {
			return false, ErrInvalidMarketStatus
		}
		command, err := tx.Exec(ctx, `
			DELETE FROM market_combination_rules
			WHERE id = $1 AND debox_user_id = $2
			  AND enabled = 1 AND run_status = 'paused'
		`, combinationID, strings.TrimSpace(deboxUserID))
		if err != nil {
			return false, fmt.Errorf("delete paused market combination: %w", err)
		}
		if command.RowsAffected() != 1 {
			return false, ErrNotFound
		}
		return true, nil
	})
	return err
}

func (s *Store) RestoreMarketCombinationWithinQuota(
	ctx context.Context,
	combinationID int64,
	deboxUserID string,
	policy QuotaPolicy,
) (MarketCombinationRule, error) {
	return withMarketDeliveryTx(ctx, s.db, func(tx DBTX) (MarketCombinationRule, error) {
		if err := lockUser(ctx, tx, deboxUserID); err != nil {
			return MarketCombinationRule{}, err
		}
		if err := requirePolicyPlan(ctx, tx, deboxUserID, policy); err != nil {
			return MarketCombinationRule{}, err
		}
		var scopeErr error
		policy, scopeErr = s.marketScopePolicy(ctx, tx, deboxUserID, policy)
		if scopeErr != nil {
			return MarketCombinationRule{}, scopeErr
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
		var frozen bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM market_combination_rule_projects mcrp
				JOIN market_projects mp ON mp.id = mcrp.market_project_id
				WHERE mcrp.market_combination_rule_id = $1
				  AND mp.status = 'paused'
				  AND mp.pause_reason = 'subscription_expired'
			)
		`, combinationID).Scan(&frozen); err != nil {
			return MarketCombinationRule{}, fmt.Errorf(
				"check frozen market combination: %w",
				err,
			)
		}
		if frozen {
			return MarketCombinationRule{}, ErrInvalidMarketStatus
		}
		members, err := listMarketCombinationMembers(ctx, tx, combinationID)
		if err != nil {
			return MarketCombinationRule{}, err
		}
		if len(members) < 2 {
			return MarketCombinationRule{}, ErrInvalidCombinationRule
		}
		hasHolderMembers := false
		for _, member := range members {
			isHolder, err := validateMarketCombinationMember(
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
			)
			if err != nil {
				return MarketCombinationRule{}, err
			}
			hasHolderMembers = hasHolderMembers || isHolder
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
		holderTargets, err := s.beginMarketHolderTargetCheck(ctx, tx, hasHolderMembers)
		if err != nil {
			return MarketCombinationRule{}, err
		}
		if err := advanceMarketCombinationNotificationFence(ctx, tx, value.ID); err != nil {
			return MarketCombinationRule{}, err
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
		if err := s.checkMarketHolderTargetLimit(ctx, tx, holderTargets); err != nil {
			return MarketCombinationRule{}, err
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
) (bool, error) {
	switch member.SourceType {
	case "watch":
		if member.WatchRuleID == nil || member.MarketRuleID != nil {
			return false, ErrInvalidCombinationRule
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
			return false, ErrInvalidCombinationRule
		}
		if err != nil {
			return false, fmt.Errorf("validate watch combination member: %w", err)
		}
		if !policy.allowsRuleType(ruleType) {
			return false, ErrRuleTypeDenied
		}
	case "market":
		if member.MarketRuleID == nil || member.WatchRuleID != nil {
			return false, ErrInvalidCombinationRule
		}
		var ruleType, runStatus, threshold string
		var window *int32
		var enabled int32
		err := db.QueryRow(ctx, `
			SELECT rule_type, enabled, run_status, window_minutes, threshold_value::text
			FROM market_rules
			WHERE id = $1 AND debox_user_id = $2
		`, *member.MarketRuleID, deboxUserID).Scan(
			&ruleType, &enabled, &runStatus, &window, &threshold,
		)
		if isNoRows(err) || enabled != 1 || runStatus != "active" {
			return false, ErrInvalidCombinationRule
		}
		if err != nil {
			return false, fmt.Errorf("validate market combination member: %w", err)
		}
		if !policy.allowsMarketRuleType(ruleType) {
			return false, ErrMarketRuleTypeDenied
		}
		if err := validateMarketRuleRuntimeParameters(ruleType, window, threshold); err != nil {
			return false, err
		}
		if marketScopeEnabled(policy) {
			rule, err := collectOne[MarketRule](ctx, db, `SELECT `+marketRuleColumns+` FROM market_rules WHERE id=$1 AND debox_user_id=$2`, *member.MarketRuleID, deboxUserID)
			if err != nil {
				return false, fmt.Errorf("read market combination member scope: %w", err)
			}
			if err := validateStoredMarketRuleScope(ctx, db, rule, policy); err != nil {
				return false, err
			}
		}
		return isMarketHolderRule(ruleType), nil
	default:
		return false, ErrInvalidCombinationRule
	}
	return false, nil
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
	return withMarketDeliveryTx(ctx, s.db, func(tx DBTX) (int64, error) {
		return recordMarketCombinationTrigger(ctx, tx, params)
	})
}

func recordMarketCombinationTrigger(ctx context.Context, tx DBTX, params RecordMarketCombinationTriggerParams) (int64, error) {
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
	// Routing uses the persisted fact's time, never a replay caller's current
	// clock. Invalid sources receive a terminal decision for each member.
	var sourceValid bool
	if params.SourceType == "watch" {
		err := tx.QueryRow(ctx, `SELECT COALESCE(rte.occurred_at,rte.detected_at,rte.created_at),
			wr.enabled=1 AND wr.run_status='active'
			FROM rule_trigger_events rte JOIN watch_rules wr ON wr.id=rte.watch_rule_id WHERE rte.id=$1`, *params.WatchTriggerEventID).Scan(&params.OccurredAt, &sourceValid)
		if err != nil {
			return 0, err
		}
	} else {
		err := tx.QueryRow(ctx, `SELECT me.occurred_at, (`+marketDeliveryEventValidSQL("mre.details", "me")+`)
			AND mr.enabled=1 AND mr.run_status='active' AND mp.status='active'
   AND(mr.notification_source_after IS NULL OR(me.occurred_at>=mr.notification_source_after AND mre.created_at>=mr.notification_source_after))
			AND (mre.notification_status<>'skipped' OR mre.delivery_outcome='unknown'
			 OR mre.notification_error='stage notification already emitted for this window' OR mre.notification_error='notification destination is no longer authorized or differs from frozen route')
			FROM market_rule_events mre JOIN market_events me ON me.id=mre.market_event_id
			JOIN market_rules mr ON mr.id=mre.market_rule_id JOIN market_projects mp ON mp.id=mr.market_project_id
			WHERE mre.id=$1`, *params.MarketRuleEventID).Scan(&params.OccurredAt, &sourceValid)
		if err != nil {
			return 0, err
		}
	}
	sourceReason := "source_unavailable"
	cutoff, err := notificationSourceCutoff(ctx, tx)
	if err != nil {
		return 0, err
	}
	if cutoff != nil && params.OccurredAt.Before(*cutoff) {
		sourceValid = false
		sourceReason = "source_precedes_recovery_cutoff"
	}
	var members []MarketCombinationMember
	if params.SourceType == "watch" {
		members, err = collectMany[MarketCombinationMember](ctx, tx, `
			SELECT `+marketCombinationMemberJoinedColumns+`
			FROM market_combination_members mcm
			JOIN market_combination_rules mcr
			  ON mcr.id = mcm.market_combination_rule_id
			JOIN rule_trigger_events rte ON rte.id = $1
			WHERE mcm.source_type = 'watch'
			  AND mcm.watch_rule_id = rte.watch_rule_id
			  AND mcr.debox_user_id = rte.debox_user_id
			  AND mcr.enabled = 1 AND mcr.run_status = 'active'
			ORDER BY mcr.id, mcm.id
			LIMIT 201
		`, *params.WatchTriggerEventID)
	} else {
		members, err = collectMany[MarketCombinationMember](ctx, tx, `
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
			ORDER BY mcr.id, mcm.id
			LIMIT 201
		`, *params.MarketRuleEventID)
	}
	if err != nil {
		return 0, fmt.Errorf("list matching market combinations: %w", err)
	}
	if len(members) > 200 {
		return 0, ErrMarketDeliveryCapacity
	}
	var recorded int64
	for _, member := range members {
		inserted, err := recordMarketCombinationMemberTrigger(ctx, tx, member, params, sourceValid, sourceReason)
		if err != nil {
			return recorded, err
		}
		if inserted {
			recorded++
		}
	}
	return recorded, nil
}

func recordMarketCombinationMemberTrigger(
	ctx context.Context,
	tx DBTX,
	member MarketCombinationMember,
	params RecordMarketCombinationTriggerParams,
	sourceValid bool,
	sourceReason string,
) (bool, error) {
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
	cutoff, err := marketResourceNotificationCutoff(ctx, tx, config.ID, true)
	if err != nil {
		return false, err
	}
	if cutoff != nil && params.OccurredAt.Before(*cutoff) {
		return false, recordMarketCombinationDecision(ctx, tx, member.ID, params, "source_precedes_resource_fence")
	}
	var decided bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM market_combination_source_decisions WHERE market_combination_member_id=$1
   AND((source_type='watch' AND watch_trigger_event_id=$2)OR(source_type='market' AND market_rule_event_id=$3)))`, member.ID, params.WatchTriggerEventID, params.MarketRuleEventID).Scan(&decided); err != nil {
		return false, err
	}
	if decided {
		return false, nil
	}
	if !sourceValid {
		return false, recordMarketCombinationDecision(ctx, tx, member.ID, params, sourceReason)
	}
	if occurredAt.After(now) {
		return false, recordMarketCombinationDecision(ctx, tx, member.ID, params, "source_time_in_future")
	}
	if occurredAt.Before(config.CreatedAt) {
		return false, recordMarketCombinationDecision(ctx, tx, member.ID, params, "source_precedes_combination")
	}
	if config.CycleType == "follow" &&
		!occurredAt.Add(time.Duration(config.CycleMinutes)*time.Minute).After(now) {
		return false, recordMarketCombinationDecision(ctx, tx, member.ID, params, "source_window_expired")
	}
	var existingWindow *int64
	if err := tx.QueryRow(ctx, `SELECT (SELECT market_combination_window_id FROM market_combination_trigger_events WHERE market_combination_member_id=$1
   AND((source_type='watch' AND watch_trigger_event_id=$2)OR(source_type='market' AND market_rule_event_id=$3)) LIMIT 1)`, member.ID, params.WatchTriggerEventID, params.MarketRuleEventID).Scan(&existingWindow); err != nil {
		return false, err
	}
	if existingWindow != nil {
		return false, recountMarketCombinationWindow(ctx, tx, *existingWindow)
	}
	window, err := currentMarketCombinationWindow(
		ctx, tx, config, now, occurredAt,
	)
	if err != nil {
		return false, err
	}
	var emitted bool
	if err := tx.QueryRow(ctx, `SELECT notification_status IN('sent','sending','skipped') OR notification_payload<>'{}'::jsonb
		 FROM market_combination_windows WHERE id=$1`, window.ID).Scan(&emitted); err != nil {
		return false, err
	}
	if emitted {
		return false, recordMarketCombinationDecision(ctx, tx, member.ID, params, "window_already_emitted")
	}
	if params.OccurredAt.Before(window.StartsAt) ||
		!params.OccurredAt.Before(window.EndsAt) {
		return false, recordMarketCombinationDecision(ctx, tx, member.ID, params, "source_outside_window")
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
				    next_attempt_at = CASE WHEN notification_status='collecting' THEN $2 ELSE next_attempt_at END,
				    updated_at = $2
				WHERE id = $1
			`, window.ID, now); err != nil {
			return false, fmt.Errorf("claim completed market combination: %w", err)
		}
	}
	return true, nil
}

func recordMarketCombinationDecision(ctx context.Context, tx DBTX, memberID int64, params RecordMarketCombinationTriggerParams, reason string) error {
	_, err := tx.Exec(ctx, `INSERT INTO market_combination_source_decisions
		(market_combination_member_id,source_type,watch_trigger_event_id,market_rule_event_id,disposition,reason)
		VALUES($1,$2,$3,$4,'skipped',$5) ON CONFLICT DO NOTHING`, memberID, params.SourceType, params.WatchTriggerEventID, params.MarketRuleEventID, reason)
	return err
}

// Reconfirmation only recomputes unprepared, open windows. Links and source
// IDs are retained, so a reinclusion is not counted as an additional trigger.
func recountMarketStageWindow(ctx context.Context, tx DBTX, id, threshold int64) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE market_stage_windows w SET trigger_count=c.n,
   notification_status=CASE WHEN c.n>=$2 THEN 'pending' ELSE 'collecting' END,updated_at=NOW()
  FROM(SELECT COUNT(*) n FROM market_stage_window_events l JOIN market_rule_events mre ON mre.id=l.market_rule_event_id
   JOIN market_events me ON me.id=mre.market_event_id WHERE l.market_stage_window_id=$1 AND mre.notification_status='staged'
    AND (`+marketDeliveryEventValidSQL("mre.details", "me")+`))c
  WHERE w.id=$1 AND w.closed_at IS NULL AND w.ends_at>NOW() AND w.notification_status IN('collecting','pending','failed')
   AND w.notification_payload='{}'::jsonb AND w.notification_send_started_at IS NULL AND w.delivery_outcome<>'unknown'
   AND w.trigger_count IS DISTINCT FROM c.n`, id, threshold)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}
func recountMarketCombinationWindow(ctx context.Context, tx DBTX, id int64) error {
	var editable bool
	if err := tx.QueryRow(ctx, `SELECT closed_at IS NULL AND ends_at>NOW() AND notification_status IN('collecting','pending','failed')
   AND notification_payload='{}'::jsonb AND notification_send_started_at IS NULL AND delivery_outcome<>'unknown'
   FROM market_combination_windows WHERE id=$1 FOR UPDATE`, id).Scan(&editable); err != nil {
		return err
	}
	if !editable {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE market_combination_window_members wm SET trigger_count=c.n,
   reached_at=CASE WHEN c.n>=wm.required_trigger_count THEN COALESCE(wm.reached_at,NOW()) ELSE NULL END,updated_at=NOW()
  FROM(SELECT m.market_combination_member_id mid,COUNT(l.id) FILTER(WHERE l.source_type='watch' OR
    ((`+marketDeliveryEventValidSQL("mre.details", "me")+`) AND (mre.notification_status<>'skipped' OR mre.delivery_outcome='unknown')))n
   FROM market_combination_window_members m LEFT JOIN market_combination_trigger_events l ON l.market_combination_window_id=m.market_combination_window_id
     AND l.market_combination_member_id=m.market_combination_member_id
   LEFT JOIN market_rule_events mre ON mre.id=l.market_rule_event_id LEFT JOIN market_events me ON me.id=mre.market_event_id
   WHERE m.market_combination_window_id=$1 GROUP BY m.market_combination_member_id)c
  WHERE wm.market_combination_window_id=$1 AND wm.market_combination_member_id=c.mid AND wm.trigger_count IS DISTINCT FROM c.n`, id); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE market_combination_windows w SET total_trigger_count=c.n,
   notification_status=CASE WHEN c.complete THEN 'pending' ELSE 'collecting' END,updated_at=NOW()
  FROM(SELECT SUM(trigger_count)n,COUNT(*)>=2 AND BOOL_AND(trigger_count>=required_trigger_count)complete FROM market_combination_window_members WHERE market_combination_window_id=$1)c
  WHERE w.id=$1 AND w.total_trigger_count IS DISTINCT FROM c.n`, id)
	return err
}

func currentMarketCombinationWindow(
	ctx context.Context,
	tx DBTX,
	config MarketCombinationRule,
	now time.Time,
	firstOccurredAt time.Time,
) (MarketCombinationWindow, error) {
	cutoff, err := marketResourceNotificationCutoff(ctx, tx, config.ID, true)
	if err != nil {
		return MarketCombinationWindow{}, err
	}

	window, err := collectOptional[MarketCombinationWindow](ctx, tx, `
		SELECT `+marketCombinationWindowColumns+`
		FROM market_combination_windows
		WHERE market_combination_rule_id = $1 AND closed_at IS NULL
		FOR UPDATE
	`, config.ID)
	if err != nil {
		return MarketCombinationWindow{}, fmt.Errorf("get market combination window: %w", err)
	}
	if window != nil && cutoff != nil && window.CreatedAt.Before(*cutoff) {
		if err := closePreCutoffMarketWindow(ctx, tx, "market_combination_windows", window.ID, *cutoff); err != nil {
			return MarketCombinationWindow{}, err
		}
		window = nil
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
	if cutoff != nil && startsAt.Before(*cutoff) {
		startsAt = *cutoff
	}
	created, err := collectOne[MarketCombinationWindow](ctx, tx, `
		INSERT INTO market_combination_windows (
			debox_user_id, market_combination_rule_id, starts_at, ends_at,delivery_outcome,notification_route
		)
		SELECT $1,$2,$3,$4,'not_attempted',jsonb_build_object('chat_id',notification_chat_id,'chat_type',notification_chat_type) FROM market_combination_rules WHERE id=$2
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
	ctx, cancel := marketDeliveryBudgetContext(ctx, s.db)
	defer cancel()

	type pending struct {
		ID         int64     `db:"id"`
		OccurredAt time.Time `db:"occurred_at"`
		Note       string    `db:"note"`
	}
	values, err := withMarketDeliveryTx(ctx, s.db, func(tx DBTX) ([]pending, error) {
		return collectMany[pending](ctx, tx, `
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
		  AND NOT EXISTS (
			SELECT 1 FROM market_combination_source_decisions decision
			WHERE decision.market_combination_member_id=mcm.id
			  AND decision.source_type='watch' AND decision.watch_trigger_event_id=rte.id
		  )
		ORDER BY rte.id
		LIMIT $1
	`, clamp(limit, 1, 1000))
	})
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
	ctx, cancel := marketDeliveryBudgetContext(ctx, s.db)
	defer cancel()

	if err := s.reconcileInvalidMarketDeliveryRoutes(ctx); err != nil {
		return err
	}
	if err := s.reconcileMarketDeliveryCutoff(ctx); err != nil {
		return err
	}
	_, err := withMarketDeliveryTx(ctx, s.db, func(tx DBTX) (struct{}, error) {
		rows, err := tx.Query(ctx, `SELECT mre.id FROM market_rule_events mre JOIN market_events me ON me.id=mre.market_event_id
    WHERE me.reorged=1 AND NOT mre.notification_source_reconciled ORDER BY mre.id
		  LIMIT 1000 FOR NO KEY UPDATE OF mre SKIP LOCKED`)
		if err != nil {
			return struct{}{}, err
		}
		ids, err := collectInt64Rows(rows)
		if err != nil {
			return struct{}{}, err
		}
		if len(ids) == 0 {
			return struct{}{}, nil
		}
		// Retain source links for audit. Only windows affected by this bounded set
		// need their counters reconciled; successful and unknown deliveries freeze.
		if _, err = tx.Exec(ctx, `UPDATE market_stage_windows w SET
    trigger_count=CASE WHEN w.notification_send_started_at IS NOT NULL OR w.notification_payload<>'{}'::jsonb THEN w.trigger_count ELSE c.n END,
    notification_status=CASE WHEN w.notification_send_started_at IS NOT NULL OR w.notification_payload<>'{}'::jsonb THEN 'skipped'
      WHEN c.n>=mr.trigger_count_threshold THEN 'pending' ELSE 'collecting' END,
    delivery_outcome=CASE WHEN w.notification_send_started_at IS NOT NULL THEN 'unknown'
      WHEN w.notification_payload<>'{}'::jsonb THEN 'cancelled' ELSE w.delivery_outcome END,
    notification_error=CASE WHEN w.notification_payload<>'{}'::jsonb OR w.notification_send_started_at IS NOT NULL
      THEN 'source event was removed by a chain reorganization' ELSE w.notification_error END,
    updated_at=NOW()
    FROM market_rules mr,
      (SELECT sw.id,COUNT(*) FILTER(WHERE me.reorged=0) AS n FROM market_stage_windows sw
       JOIN market_stage_window_events link ON link.market_stage_window_id=sw.id
       JOIN market_rule_events mre ON mre.id=link.market_rule_event_id JOIN market_events me ON me.id=mre.market_event_id
       WHERE sw.id IN(SELECT market_stage_window_id FROM market_stage_window_events WHERE market_rule_event_id=ANY($1)) GROUP BY sw.id)c
    WHERE w.id=c.id AND mr.id=w.market_rule_id AND w.notification_status NOT IN('sent','skipped')
      AND w.delivery_outcome<>'unknown'
      AND (w.trigger_count IS DISTINCT FROM c.n OR w.notification_payload<>'{}'::jsonb OR w.notification_status='sending')`, ids); err != nil {
			return struct{}{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE market_combination_window_members wm SET trigger_count=c.n,
      reached_at=CASE WHEN c.n>=wm.required_trigger_count THEN COALESCE(wm.reached_at,NOW()) ELSE NULL END,updated_at=NOW()
    FROM (SELECT wm.market_combination_window_id wid,wm.market_combination_member_id mid,
       COUNT(link.id) FILTER(WHERE link.source_type='watch' OR me.reorged=0) AS n
      FROM market_combination_window_members wm JOIN market_combination_windows w ON w.id=wm.market_combination_window_id
      LEFT JOIN market_combination_trigger_events link ON link.market_combination_window_id=wm.market_combination_window_id AND link.market_combination_member_id=wm.market_combination_member_id
      LEFT JOIN market_rule_events mre ON mre.id=link.market_rule_event_id LEFT JOIN market_events me ON me.id=mre.market_event_id
      WHERE wm.market_combination_window_id IN(SELECT market_combination_window_id FROM market_combination_trigger_events WHERE market_rule_event_id=ANY($1))
        AND w.notification_status NOT IN('sent','skipped') AND w.delivery_outcome<>'unknown'
        AND w.notification_payload='{}'::jsonb AND w.notification_send_started_at IS NULL
      GROUP BY wm.market_combination_window_id,wm.market_combination_member_id)c
    WHERE wm.market_combination_window_id=c.wid AND wm.market_combination_member_id=c.mid AND wm.trigger_count IS DISTINCT FROM c.n`, ids); err != nil {
			return struct{}{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE market_combination_windows w SET total_trigger_count=c.n,
     notification_status=CASE WHEN w.notification_send_started_at IS NOT NULL OR w.notification_payload<>'{}'::jsonb THEN 'skipped'
       WHEN c.complete THEN 'pending' ELSE 'collecting' END,
     delivery_outcome=CASE WHEN w.notification_send_started_at IS NOT NULL THEN 'unknown'
       WHEN w.notification_payload<>'{}'::jsonb THEN 'cancelled' ELSE w.delivery_outcome END,
     notification_error=CASE WHEN w.notification_payload<>'{}'::jsonb OR w.notification_send_started_at IS NOT NULL
       THEN 'source event was removed by a chain reorganization' ELSE w.notification_error END,updated_at=NOW()
    FROM(SELECT wm.market_combination_window_id id,SUM(wm.trigger_count) n,
      COUNT(*)>=2 AND BOOL_AND(wm.trigger_count>=wm.required_trigger_count) complete
      FROM market_combination_window_members wm WHERE wm.market_combination_window_id IN
        (SELECT market_combination_window_id FROM market_combination_trigger_events WHERE market_rule_event_id=ANY($1)) GROUP BY wm.market_combination_window_id)c
    WHERE w.id=c.id AND w.notification_status NOT IN('sent','skipped') AND w.delivery_outcome<>'unknown'
      AND (w.total_trigger_count IS DISTINCT FROM c.n OR w.notification_payload<>'{}'::jsonb OR w.notification_status='sending')`, ids); err != nil {
			return struct{}{}, err
		}
		_, err = tx.Exec(ctx, `UPDATE market_rule_events SET notification_source_reconciled=TRUE,
     notification_status=CASE WHEN notification_status IN('pending','sending','failed','staged','combined') THEN 'skipped' ELSE notification_status END,
     delivery_outcome=CASE WHEN notification_status='sent' OR delivery_outcome='unknown' THEN delivery_outcome
       WHEN notification_send_started_at IS NOT NULL OR (notification_lease_token=0 AND (notification_attempted_at IS NOT NULL OR(notification_status IN('pending','failed') AND delivery_outcome=''))) THEN 'unknown'
       WHEN notification_status IN('pending','sending','failed','staged','combined') OR notification_error='source event was removed by a chain reorganization' THEN 'cancelled' ELSE delivery_outcome END,
     notification_error=CASE WHEN notification_status IN('pending','sending','failed','staged','combined')
       THEN LEFT(CASE WHEN notification_error='' THEN 'source event was removed by a chain reorganization' ELSE notification_error||'; source event was removed by a chain reorganization' END,2000) ELSE notification_error END
    WHERE id=ANY($1)`, ids)
		return struct{}{}, err
	})
	if err != nil {
		return err
	}
	return marketDeliveryBudgetError(ctx, s.repairMarketNotificationSources(ctx, 1000))
}

func (s *Store) repairMarketNotificationSources(ctx context.Context, limit int) error {
	// Already-routed sources are excluded in SQL. Invalid/expired sources get a
	// terminal decision, so they cannot monopolize the oldest bounded page.
	type source struct {
		ID int64 `db:"id"`
	}
	values, err := withMarketDeliveryTx(ctx, s.db, func(tx DBTX) ([]source, error) {
		return collectMany[source](ctx, tx, `SELECT mre.id FROM market_rule_events mre
   JOIN market_rules mr ON mr.id=mre.market_rule_id JOIN market_projects mp ON mp.id=mr.market_project_id
   JOIN market_events me ON me.id=mre.market_event_id
   WHERE me.confirmed=1 AND me.reorged=0 AND mr.enabled=1 AND mr.run_status='active' AND mp.status='active'
    AND mre.notification_status IN('staged','combined','pending','failed','sent')
    AND ((mre.notification_status='staged' AND NOT EXISTS(SELECT 1 FROM market_stage_window_events l WHERE l.market_rule_event_id=mre.id))
     OR EXISTS(SELECT 1 FROM market_combination_members m JOIN market_combination_rules cr ON cr.id=m.market_combination_rule_id
       WHERE m.source_type='market' AND m.market_rule_id=mr.id AND cr.enabled=1 AND cr.run_status='active'
        AND NOT EXISTS(SELECT 1 FROM market_combination_trigger_events l WHERE l.market_combination_member_id=m.id AND l.source_type='market' AND l.market_rule_event_id=mre.id)
        AND NOT EXISTS(SELECT 1 FROM market_combination_source_decisions d WHERE d.market_combination_member_id=m.id AND d.source_type='market' AND d.market_rule_event_id=mre.id)))
   ORDER BY mre.id LIMIT $1`, clamp(limit, 1, 1000))
	})
	if err != nil {
		return err
	}
	for _, v := range values {
		_, err = withMarketDeliveryTx(ctx, s.db, func(tx DBTX) (struct{}, error) {
			event, err := collectOne[MarketRuleEvent](ctx, tx, `SELECT `+marketRuleEventColumns+` FROM market_rule_events WHERE id=$1`, v.ID)
			if err != nil {
				return struct{}{}, err
			}
			return struct{}{}, enqueueMarketRuleEvent(ctx, tx, event)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) RecoverStaleMarketDeliveries(ctx context.Context, lease time.Duration) (int64, error) {
	return withMarketDeliveryTx(ctx, s.db, func(tx DBTX) (int64, error) { return newWithDB(tx).recoverStaleMarketDeliveries(ctx, lease) })
}

func (s *Store) recoverStaleMarketDeliveries(ctx context.Context, lease time.Duration) (int64, error) {
	ctx, cancel := marketDeliveryBudgetContext(ctx, s.db)
	defer cancel()

	if lease <= 0 {
		lease = 5 * time.Minute
	}
	var total int64
	for _, table := range []string{"market_rule_events", "market_stage_windows", "market_combination_windows"} {
		query := fmt.Sprintf(`WITH candidates AS(SELECT id FROM %s WHERE (notification_status='sending'
    AND COALESCE(notification_lease_expires_at,notification_attempted_at+$1::interval)<NOW())
    OR (notification_status IN('pending','failed') AND notification_lease_token=0 AND delivery_outcome='')
    ORDER BY notification_attempted_at,id LIMIT 1000 FOR UPDATE SKIP LOCKED)
   UPDATE %s d SET notification_status=CASE WHEN d.notification_send_started_at IS NOT NULL OR d.notification_lease_token=0
       THEN 'skipped' WHEN d.notification_attempts>=5 THEN 'skipped' ELSE 'failed' END,
    delivery_outcome=CASE WHEN d.notification_send_started_at IS NOT NULL OR d.notification_lease_token=0 THEN 'unknown'
       WHEN d.notification_attempts>=5 THEN 'rejected' ELSE 'retryable' END,
    notification_error=LEFT(CASE WHEN d.notification_error='' THEN '' ELSE d.notification_error||'; ' END||
     CASE WHEN d.notification_send_started_at IS NOT NULL OR d.notification_lease_token=0
       THEN 'delivery result unknown after worker interruption; automatic resend paused'
       ELSE 'worker interrupted before send began' END,2000),
    next_attempt_at=NOW(),notification_lease_expires_at=NULL
   WHERE d.id IN(SELECT id FROM candidates)`, table, table)
		tag, err := s.db.Exec(ctx, query, durationInterval(lease))
		if err != nil {
			return total, fmt.Errorf("recover stale market delivery: %w", err)
		}
		total += tag.RowsAffected()
	}
	return total, nil
}

func (s *Store) ClaimMarketDeliveries(ctx context.Context, limit int) ([]MarketDeliveryClaim, error) {
	limit = clamp(limit, 1, notificationdelivery.MaxBatch)
	return withMarketDeliveryTx(ctx, s.db, func(tx DBTX) ([]MarketDeliveryClaim, error) {
		cutoff, err := notificationSourceCutoff(ctx, tx)
		if err != nil {
			return nil, err
		}
		claims := make([]MarketDeliveryClaim, 0, limit)
		// Rotate the first kind with the database clock so a busy realtime queue
		// cannot indefinitely starve stage and combination deliveries.
		var first int
		if err := tx.QueryRow(ctx, `SELECT (FLOOR(EXTRACT(EPOCH FROM NOW())/5)::bigint%3)::integer`).Scan(&first); err != nil {
			return nil, err
		}
		kinds := []string{"realtime", "stage", "combination"}
		for i := 0; i < 6; i++ {
			kind := kinds[(first+i)%3]
			table, _ := marketDeliveryTable(kind)
			extra := "(" + marketDeliveryEligibleSQL(kind, "d", "$2") + ") AND (" + marketDeliveryRouteValidSQL(kind, "d") + ")"
			query := fmt.Sprintf(`WITH candidates AS(SELECT d.id FROM %s d
      WHERE d.notification_status IN('pending','failed') AND d.delivery_outcome IN('not_attempted','retryable')
       AND d.next_attempt_at<=NOW() AND d.notification_attempts<5 AND %s
      ORDER BY d.id LIMIT $1 FOR UPDATE OF d SKIP LOCKED)
    UPDATE %s d SET notification_status='sending',notification_attempted_at=NOW(),
      notification_lease_token=d.notification_lease_token+1,
      notification_lease_expires_at=NOW()+INTERVAL '5 minutes',notification_send_started_at=NULL
    WHERE d.id IN(SELECT id FROM candidates) RETURNING d.id,d.notification_lease_token`, table, extra, table)
			kindLimit := limit - len(claims)
			if i < 3 && kindLimit > (limit+2)/3 {
				kindLimit = (limit + 2) / 3
			}
			rows, err := tx.Query(ctx, query, kindLimit, cutoff)
			if err != nil {
				return nil, err
			}
			values, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[MarketDeliveryClaim])
			if err != nil {
				return nil, err
			}
			for _, v := range values {
				v.Kind = kind
				claims = append(claims, v)
			}
			if len(claims) == limit {
				break
			}
		}
		return claims, nil
	})
}

// The predicate is checked both at claim and immediately before the network
// boundary. Cursor height alone is never evidence that an event is confirmed.
const marketDeliveryRouteCancelledReason = "notification destination is no longer authorized or differs from frozen route"

func marketDeliveryRouteValidSQL(kind, alias string) string {
	table, column := "market_rules", "market_rule_id"
	professional := "r.rule_type NOT IN('market_price_above','market_price_below','market_price_increase','market_price_decrease','market_liquidity_below','market_liquidity_decrease','market_volume_above','market_volume_spike','market_trade_imbalance')"
	if kind == "combination" {
		table, column, professional = "market_combination_rules", "market_combination_rule_id", "TRUE"
	}
	return `EXISTS(SELECT 1 FROM ` + table + ` r WHERE r.id=` + alias + `.` + column + `
  AND ` + alias + `.notification_route->>'chat_id'=r.notification_chat_id
  AND ` + alias + `.notification_route->>'chat_type'=r.notification_chat_type
  AND(` + alias + `.notification_payload='{}'::jsonb OR(
    ` + alias + `.notification_payload#>>'{payload,chat_id}'=r.notification_chat_id AND ` + alias + `.notification_payload#>>'{payload,chat_type}'=r.notification_chat_type))
  AND EXISTS(SELECT 1 FROM subscriptions sub WHERE sub.debox_user_id=r.debox_user_id AND sub.status='active'
   AND(sub.is_permanent=1 OR sub.expires_at>NOW()) AND(sub.plan_code='professional' OR(sub.plan_code='standard' AND NOT(` + professional + `))))
  AND((r.notification_chat_type='private' AND r.notification_chat_id=r.debox_user_id)
   OR(r.notification_chat_type='group' AND EXISTS(SELECT 1 FROM notification_groups g WHERE g.debox_user_id=r.debox_user_id AND g.gid=r.notification_chat_id AND g.enabled=1)
    AND EXISTS(SELECT 1 FROM subscriptions sub WHERE sub.debox_user_id=r.debox_user_id AND sub.status='active'
     AND sub.plan_code='professional' AND(sub.is_permanent=1 OR sub.expires_at>NOW())))))`
}
func (s *Store) reconcileInvalidMarketDeliveryRoutes(ctx context.Context) error {
	_, err := withMarketDeliveryTx(ctx, s.db, func(tx DBTX) (struct{}, error) {
		for _, kind := range []string{"realtime", "stage", "combination"} {
			table, _ := marketDeliveryTable(kind)
			closeSQL := ""
			lock := "FOR NO KEY UPDATE"
			if kind != "realtime" {
				closeSQL = ",closed_at=COALESCE(d.closed_at,NOW()),updated_at=NOW()"
				lock = "FOR UPDATE"
			}
			_, err := tx.Exec(ctx, fmt.Sprintf(`WITH candidates AS(SELECT d.id FROM %s d
     WHERE d.notification_status IN('collecting','pending','failed','sending') AND d.delivery_outcome<>'unknown'
       AND NOT COALESCE((%s),FALSE) ORDER BY d.id LIMIT 1000 %s OF d SKIP LOCKED)
    UPDATE %s d SET notification_status='skipped',delivery_outcome=CASE
      WHEN(d.notification_status='sending' AND(d.notification_send_started_at IS NOT NULL OR d.notification_lease_token=0))
       OR(d.notification_status IN('pending','failed') AND d.notification_lease_token=0 AND d.delivery_outcome='')
      THEN 'unknown' ELSE 'cancelled' END,notification_error=LEFT(CASE WHEN d.notification_error='' THEN $1 ELSE d.notification_error||'; '||$1 END,2000)%s WHERE d.id IN(SELECT id FROM candidates)`, table, marketDeliveryRouteValidSQL(kind, "d"), lock, table, closeSQL), marketDeliveryRouteCancelledReason)
			if err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, nil
	})
	return err
}

func marketDeliveryEligibleSQL(kind, alias, cutoff string) string {
	switch kind {
	case "realtime":
		return `EXISTS(SELECT 1 FROM market_events me JOIN market_rules mr ON mr.id=` + alias + `.market_rule_id
   JOIN market_projects mp ON mp.id=mr.market_project_id WHERE me.id=` + alias + `.market_event_id
    AND ` + marketDeliveryEventValidSQL(alias+`.details`, "me") + `
    AND mr.enabled=1 AND mr.run_status='active' AND mp.status='active'
    AND ` + marketRuleSupportedParametersSQL("mr") + `
    AND (mr.notification_source_after IS NULL OR(me.occurred_at>=mr.notification_source_after AND ` + alias + `.created_at>=mr.notification_source_after))
    AND (` + cutoff + `::timestamptz IS NULL OR(me.occurred_at>=` + cutoff + ` AND ` + alias + `.created_at>=` + cutoff + `)))`
	case "stage":
		return `EXISTS(SELECT 1 FROM market_rules mr JOIN market_projects mp ON mp.id=mr.market_project_id
   WHERE mr.id=` + alias + `.market_rule_id AND mr.enabled=1 AND mr.run_status='active' AND mp.status='active'
    AND ` + marketRuleSupportedParametersSQL("mr") + `
    AND (mr.notification_source_after IS NULL OR(` + alias + `.created_at>=mr.notification_source_after AND NOT EXISTS(
      SELECT 1 FROM market_stage_window_events link JOIN market_rule_events e ON e.id=link.market_rule_event_id JOIN market_events me ON me.id=e.market_event_id
      WHERE link.market_stage_window_id=` + alias + `.id AND me.occurred_at<mr.notification_source_after))))
   AND (` + cutoff + `::timestamptz IS NULL OR ` + alias + `.created_at>=` + cutoff + `)
   AND NOT EXISTS(SELECT 1 FROM market_stage_window_events l JOIN market_rule_events mre ON mre.id=l.market_rule_event_id
    JOIN market_events me ON me.id=mre.market_event_id WHERE l.market_stage_window_id=` + alias + `.id
      AND(NOT COALESCE((` + marketDeliveryEventValidSQL("mre.details", "me") + `),FALSE)
       OR(` + cutoff + `::timestamptz IS NOT NULL AND me.occurred_at<` + cutoff + `)))`
	case "combination":
		return `EXISTS(SELECT 1 FROM market_combination_rules cr WHERE cr.id=` + alias + `.market_combination_rule_id
    AND cr.enabled=1 AND cr.run_status='active'
    AND(cr.notification_source_after IS NULL OR(` + alias + `.created_at>=cr.notification_source_after AND NOT EXISTS(
      SELECT 1 FROM market_combination_trigger_events link WHERE link.market_combination_window_id=` + alias + `.id AND link.occurred_at<cr.notification_source_after))))
   AND (` + cutoff + `::timestamptz IS NULL OR ` + alias + `.created_at>=` + cutoff + `)
   AND NOT EXISTS(SELECT 1 FROM market_combination_trigger_events l LEFT JOIN market_rule_events mre ON mre.id=l.market_rule_event_id
    LEFT JOIN market_events me ON me.id=mre.market_event_id LEFT JOIN market_rules mr ON mr.id=mre.market_rule_id
    LEFT JOIN market_projects mp ON mp.id=mr.market_project_id
    LEFT JOIN rule_trigger_events rte ON rte.id=l.watch_trigger_event_id LEFT JOIN watch_rules wr ON wr.id=rte.watch_rule_id
    WHERE l.market_combination_window_id=` + alias + `.id
     AND((l.source_type='market' AND(NOT COALESCE((` + marketDeliveryEventValidSQL("mre.details", "me") + `),FALSE)
       OR mr.enabled<>1 OR mr.run_status<>'active' OR mp.status<>'active'
       OR(mr.notification_source_after IS NOT NULL AND(me.occurred_at<mr.notification_source_after OR mre.created_at<mr.notification_source_after))))
      OR(l.source_type='watch' AND(wr.enabled<>1 OR wr.run_status<>'active'))
      OR(` + cutoff + `::timestamptz IS NOT NULL AND l.occurred_at<` + cutoff + `)))`
	}
	return "FALSE"
}

func (s *Store) GetMarketDeliveryPayload(ctx context.Context, claim MarketDeliveryClaim) (*MarketDeliveryPayload, error) {
	return withMarketDeliveryTx(ctx, s.db, func(tx DBTX) (*MarketDeliveryPayload, error) {
		return newWithDB(tx).getMarketDeliveryPayload(ctx, claim)
	})
}

func (s *Store) getMarketDeliveryPayload(ctx context.Context, claim MarketDeliveryClaim) (*MarketDeliveryPayload, error) {
	ctx, cancel := marketDeliveryBudgetContext(ctx, s.db)
	defer cancel()

	table, err := marketDeliveryTable(claim.Kind)
	if err != nil {
		return nil, err
	}
	if claim.LeaseToken <= 0 {
		return nil, ErrMarketDeliveryLeaseLost
	}
	var raw json.RawMessage
	err = s.db.QueryRow(ctx, fmt.Sprintf(`SELECT notification_payload FROM %s WHERE id=$1 AND notification_status='sending'
   AND notification_lease_token=$2 AND notification_lease_expires_at>NOW()`, table), claim.ID, claim.LeaseToken).Scan(&raw)
	if isNoRows(err) {
		return nil, ErrMarketDeliveryLeaseLost
	}
	if err != nil {
		return nil, err
	}
	if string(raw) == "{}" {
		return nil, nil
	}
	var payload MarketDeliveryPayload
	if err = json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	if err = payload.Payload.Validate(); err != nil {
		return nil, err
	}
	return &payload, nil
}

func (s *Store) PrepareMarketDelivery(ctx context.Context, claim MarketDeliveryClaim, payload MarketDeliveryPayload) (MarketDeliveryPayload, error) {
	table, err := marketDeliveryTable(claim.Kind)
	if err != nil {
		return MarketDeliveryPayload{}, err
	}
	if claim.LeaseToken <= 0 || payload.SnapshotID <= 0 {
		return MarketDeliveryPayload{}, ErrMarketDeliveryLeaseLost
	}
	return withMarketDeliveryTx(ctx, s.db, func(tx DBTX) (MarketDeliveryPayload, error) {
		var existing json.RawMessage
		err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT notification_payload FROM %s WHERE id=$1 AND notification_status='sending'
   AND notification_lease_token=$2 AND notification_lease_expires_at>NOW() AND notification_send_started_at IS NULL FOR UPDATE`, table), claim.ID, claim.LeaseToken).Scan(&existing)
		if isNoRows(err) {
			return MarketDeliveryPayload{}, ErrMarketDeliveryLeaseLost
		}
		if err != nil {
			return MarketDeliveryPayload{}, err
		}
		if string(existing) != "{}" {
			var frozen MarketDeliveryPayload
			err = json.Unmarshal(existing, &frozen)
			return frozen, err
		}
		ownerSQL := `SELECT debox_user_id FROM ` + table + ` WHERE id=$4`
		if claim.Kind == "realtime" {
			ownerSQL = `SELECT mr.debox_user_id FROM market_rule_events mre JOIN market_rules mr ON mr.id=mre.market_rule_id WHERE mre.id=$4`
		}
		snapshot, err := collectOne[NotificationDetailSnapshot](ctx, tx, `SELECT id,public_id,notification_chat_id,notification_chat_type,notification_text
   FROM notification_detail_snapshots WHERE id=$1 AND source_key=$2 AND notification_kind=$3 AND source_id=$4
     AND source_type='market_notification_delivery' AND debox_user_id=(`+ownerSQL+`)`, payload.SnapshotID, fmt.Sprintf("market_%s:%d", claim.Kind, claim.ID), "market_"+claim.Kind, claim.ID)
		if err != nil {
			return MarketDeliveryPayload{}, err
		}
		payload.SnapshotPublicID = snapshot.PublicID
		payload.Payload.ChatID = snapshot.NotificationChatID
		payload.Payload.ChatType = snapshot.NotificationChatType
		payload.Payload.Text = snapshot.NotificationText
		if err = payload.Payload.Validate(); err != nil {
			return MarketDeliveryPayload{}, err
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return MarketDeliveryPayload{}, err
		}
		if len(raw) > notificationdelivery.MaxPayloadBytes {
			return MarketDeliveryPayload{}, ErrMarketDeliveryCapacity
		}
		var bounded bool
		if err = tx.QueryRow(ctx, `SELECT octet_length($1::jsonb::text)<=65536`, string(raw)).Scan(&bounded); err != nil {
			return MarketDeliveryPayload{}, err
		}
		if !bounded {
			return MarketDeliveryPayload{}, ErrMarketDeliveryCapacity
		}
		_, err = tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET notification_payload=$3::jsonb,notification_snapshot_id=$4
    WHERE id=$1 AND notification_status='sending' AND notification_lease_token=$2`, table), claim.ID, claim.LeaseToken, string(raw), payload.SnapshotID)
		return payload, err
	})
}

func (s *Store) BeginMarketDeliverySend(ctx context.Context, claim MarketDeliveryClaim) error {
	table, err := marketDeliveryTable(claim.Kind)
	if err != nil {
		return err
	}
	started, err := withMarketDeliveryTx(ctx, s.db, func(tx DBTX) (bool, error) {
		cutoff, err := notificationSourceCutoff(ctx, tx)
		if err != nil {
			return false, err
		}
		query := fmt.Sprintf(`UPDATE %s d SET notification_send_started_at=NOW(),notification_attempts=notification_attempts+1
    WHERE d.id=$1 AND d.notification_status='sending' AND d.notification_lease_token=$2 AND $2>0
     AND d.notification_lease_expires_at>NOW() AND d.notification_send_started_at IS NULL AND d.notification_attempts<5
     AND d.notification_payload<>'{}'::jsonb AND d.delivery_outcome IN('not_attempted','retryable') AND (%s) AND(%s)`, table, marketDeliveryEligibleSQL(claim.Kind, "d", "$3"), marketDeliveryRouteValidSQL(claim.Kind, "d"))
		tag, err := tx.Exec(ctx, query, claim.ID, claim.LeaseToken, cutoff)
		if err != nil {
			return false, err
		}
		if tag.RowsAffected() == 1 {
			return true, nil
		}
		var owned bool
		err = tx.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE id=$1 AND notification_status='sending' AND notification_lease_token=$2
    AND $2>0 AND notification_lease_expires_at>NOW() AND notification_send_started_at IS NULL)`, table), claim.ID, claim.LeaseToken).Scan(&owned)
		if err != nil {
			return false, err
		}
		if !owned {
			return false, ErrMarketDeliveryLeaseLost
		}
		// Persist an observed revocation before returning. A rapid group re-enable
		// cannot resurrect work that this worker already proved unsafe to send.
		_, err = tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET notification_status='skipped',delivery_outcome='cancelled',notification_lease_expires_at=NULL,
    notification_error=LEFT(CASE WHEN notification_error='' THEN $3 ELSE notification_error||'; '||$3 END,2000)
    WHERE id=$1 AND notification_status='sending' AND notification_lease_token=$2 AND notification_send_started_at IS NULL`, table), claim.ID, claim.LeaseToken, marketDeliveryRouteCancelledReason)
		return false, err
	})
	if err != nil {
		return err
	}
	if !started {
		return ErrMarketDeliveryLeaseLost
	}
	return nil
}

// DeferMarketDeliveryAdmission is exclusively for AdmissionDeferred, evidence
// from the admission gate that no HTTP Do occurred. A normal rejection, timeout
// or unknown delivery must go through CompleteMarketDeliveryResult instead.
// sendStarted identifies exactly the BeginSend attempt that may be refunded.
func (s *Store) DeferMarketDeliveryAdmission(ctx context.Context, claim MarketDeliveryClaim, retryAt time.Time, sendStarted bool) error {
	table, err := marketDeliveryTable(claim.Kind)
	if err != nil {
		return err
	}
	if claim.ID < 1 || claim.LeaseToken < 1 || retryAt.IsZero() {
		return ErrMarketDeliveryLeaseLost
	}
	_, err = withMarketDeliveryTx(ctx, s.db, func(tx DBTX) (struct{}, error) {
		startedPredicate, refund := "notification_send_started_at IS NULL", "0"
		if sendStarted {
			startedPredicate, refund = "notification_send_started_at IS NOT NULL AND notification_attempts>0", "1"
		}
		tag, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET notification_status='pending',
 notification_attempts=notification_attempts-%s,
 delivery_outcome=CASE WHEN notification_attempts-%s=0 THEN 'not_attempted' ELSE 'retryable' END,
 next_attempt_at=GREATEST(next_attempt_at,$3),notification_send_started_at=NULL,notification_lease_expires_at=NULL
 WHERE id=$1 AND notification_status='sending' AND notification_lease_token=$2 AND $2>0
 AND notification_lease_expires_at>clock_timestamp() AND delivery_outcome IN('not_attempted','retryable') AND %s`, table, refund, refund, startedPredicate), claim.ID, claim.LeaseToken, retryAt.UTC())
		if err == nil && tag.RowsAffected() != 1 {
			err = ErrMarketDeliveryLeaseLost
		}
		return struct{}{}, err
	})
	return err
}

func (s *Store) LoadMarketDelivery(ctx context.Context, claim MarketDeliveryClaim) (MarketNotificationDelivery, error) {
	return withMarketDeliveryTx(ctx, s.db, func(tx DBTX) (MarketNotificationDelivery, error) { return newWithDB(tx).loadMarketDelivery(ctx, claim) })
}

func (s *Store) loadMarketDelivery(
	ctx context.Context,
	claim MarketDeliveryClaim,
) (MarketNotificationDelivery, error) {
	ctx, cancel := marketDeliveryBudgetContext(ctx, s.db)
	defer cancel()

	if _, err := s.GetMarketDeliveryPayload(ctx, claim); err != nil {
		return MarketNotificationDelivery{}, err
	}
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
	event, frozenSnapshot, hasProjection, err := frozenMarketTradeEvent(event, ruleEvent.Details)
	if err != nil {
		return MarketNotificationDelivery{}, err
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
		PreviousValue:        ruleEvent.PreviousValue,
		CurrentValue:         ruleEvent.CurrentValue,
		TriggerCount:         1,
		StartsAt:             event.OccurredAt,
		EndsAt:               event.OccurredAt,
		Note:                 ruleEvent.Note,
		AddressLabel:         marketRuleEventAddressLabel(ruleEvent.Details),
	}
	delivery.Timezone = s.notificationTimezone(ctx, rule.DeBoxUserID)
	if event.MarketPoolID != nil {
		delivery.Pool, _ = s.GetMarketPool(ctx, *event.MarketPoolID)
	}
	if hasProjection {
		delivery.Snapshot = frozenSnapshot
	} else {
		delivery.Snapshot = s.marketNotificationSnapshot(ctx, ruleEvent, event)
	}
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
	stageEvents, err := s.marketStageEvents(
		ctx,
		id,
		project,
		int(window.TriggerCount),
	)
	if err != nil {
		return MarketNotificationDelivery{}, err
	}
	recentStart := len(stageEvents) - 5
	if recentStart < 0 {
		recentStart = 0
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
		RecentEvents:         stageEvents[recentStart:],
		StageEvents:          stageEvents,
		Timezone:             s.notificationTimezone(ctx, rule.DeBoxUserID),
	}, nil
}

func (s *Store) marketStageEvents(
	ctx context.Context,
	windowID int64,
	project MarketProject,
	limit int,
) ([]MarketNotificationEvent, error) {
	if limit > notificationdelivery.MaxDetailEvents {
		return nil, ErrMarketDeliveryCapacity
	}
	type eventReference struct {
		MarketRuleEventID int64 `db:"market_rule_event_id"`
	}
	values, err := collectMany[eventReference](ctx, s.db, `
		SELECT mre.id AS market_rule_event_id
		FROM market_stage_window_events mswe
		JOIN market_rule_events mre ON mre.id = mswe.market_rule_event_id
		JOIN market_events me ON me.id = mre.market_event_id
		WHERE mswe.market_stage_window_id = $1 AND me.reorged = 0
		ORDER BY me.occurred_at, mre.created_at, mre.id
		LIMIT $2
	`, windowID, notificationdelivery.MaxDetailEvents+1)
	if err != nil {
		return nil, fmt.Errorf("list market stage events: %w", err)
	}
	if len(values) > notificationdelivery.MaxDetailEvents {
		return nil, ErrMarketDeliveryCapacity
	}
	if len(values) != limit {
		return nil, fmt.Errorf("market stage source count changed before preparation")
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
	progress, err := s.marketCombinationProgress(ctx, id, combination.DeBoxUserID)
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
		Timezone:                s.notificationTimezone(ctx, combination.DeBoxUserID),
	}, nil
}

func (s *Store) marketCombinationProgress(
	ctx context.Context,
	windowID int64,
	deboxUserID string,
) ([]MarketCombinationProgress, error) {
	values, err := collectMany[MarketCombinationProgress](ctx, s.db, `
		SELECT
			mcm.id AS member_id,
			mcm.source_type,
			mcm.watch_rule_id,
			mcm.market_rule_id,
			CASE
			  WHEN mcm.source_type = 'watch' THEN wr.rule_type
			  ELSE mr.rule_type
			END AS rule_type,
			mcwm.required_trigger_count,
			mcwm.trigger_count,
			mcwm.reached_at
		FROM market_combination_window_members mcwm
		JOIN market_combination_members mcm
		  ON mcm.id = mcwm.market_combination_member_id
		LEFT JOIN watch_rules wr ON wr.id = mcm.watch_rule_id
		LEFT JOIN market_rules mr ON mr.id = mcm.market_rule_id
		WHERE mcwm.market_combination_window_id = $1
		ORDER BY mcm.id
		LIMIT 201
	`, windowID)
	if err != nil {
		return nil, fmt.Errorf("load market combination progress: %w", err)
	}
	if len(values) > notificationdelivery.MaxDetailMembers {
		return nil, ErrMarketDeliveryCapacity
	}
	totalEvents := 0
	for index := range values {
		type triggerValue struct {
			WatchTriggerEventID *int64 `db:"watch_trigger_event_id"`
			Note                string `db:"note"`
			MarketRuleEventID   *int64 `db:"market_rule_event_id"`
		}
		if values[index].MarketRuleID != nil {
			values[index].MarketRule, err = s.GetMarketRule(
				ctx,
				*values[index].MarketRuleID,
				deboxUserID,
			)
			if err != nil {
				return nil, err
			}
		}
		if values[index].WatchRuleID != nil {
			values[index].WatchRule, err = s.GetWatchRule(
				ctx,
				*values[index].WatchRuleID,
				deboxUserID,
			)
			if err != nil {
				return nil, err
			}
			if values[index].WatchRule == nil {
				return nil, ErrNotFound
			}
		}
		triggers, err := collectMany[triggerValue](ctx, s.db, `
			SELECT
				mcte.watch_trigger_event_id,
				mcte.note,
				mcte.market_rule_event_id
			FROM market_combination_trigger_events mcte
			WHERE mcte.market_combination_window_id = $1
			  AND mcte.market_combination_member_id = $2
			ORDER BY mcte.occurred_at, mcte.id
			LIMIT 2001
		`, windowID, values[index].MemberID)
		if err != nil {
			return nil, fmt.Errorf("load market combination triggers: %w", err)
		}
		totalEvents += len(triggers)
		if totalEvents > notificationdelivery.MaxDetailEvents {
			return nil, ErrMarketDeliveryCapacity
		}
		for _, trigger := range triggers {
			if trigger.WatchTriggerEventID != nil {
				event, err := collectOne[stageEventValue](ctx, s.db, `
					SELECT
						previous_value,
						current_value,
						COALESCE(details->>'token_symbol', '') AS token_symbol,
						COALESCE(details->>'note', $2) AS note,
						COALESCE(occurred_at, detected_at, created_at) AS occurred_at
					FROM rule_trigger_events
					WHERE id = $1
				`, *trigger.WatchTriggerEventID, trigger.Note)
				if err != nil {
					return nil, fmt.Errorf(
						"load watch combination event: %w",
						err,
					)
				}
				values[index].WatchEvents = append(
					values[index].WatchEvents,
					StageTriggerEvent{
						PreviousValue: event.PreviousValue,
						CurrentValue:  event.CurrentValue,
						TokenSymbol:   event.TokenSymbol,
						Note:          event.Note,
						OccurredAt:    event.OccurredAt,
					},
				)
				continue
			}
			if trigger.MarketRuleEventID == nil {
				continue
			}
			event, err := s.loadMarketNotificationEvent(ctx, *trigger.MarketRuleEventID, nil)
			if err != nil {
				return nil, err
			}
			values[index].MarketEvents = append(values[index].MarketEvents, event)
		}
		for eventIndex := len(values[index].WatchEvents) - 1; eventIndex >= 0 &&
			len(values[index].RecentNotes) < combinationRecentEventLimit; eventIndex-- {
			values[index].RecentNotes = append(
				values[index].RecentNotes,
				values[index].WatchEvents[eventIndex].Note,
			)
		}
		for eventIndex := len(values[index].MarketEvents) - 1; eventIndex >= 0 &&
			len(values[index].RecentEvents) < combinationRecentEventLimit; eventIndex-- {
			values[index].RecentEvents = append(
				values[index].RecentEvents,
				values[index].MarketEvents[eventIndex],
			)
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
	event, frozenSnapshot, hasProjection, err := frozenMarketTradeEvent(event, ruleEvent.Details)
	if err != nil {
		return MarketNotificationEvent{}, err
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
		Project:       project,
		Event:         event,
		PreviousValue: ruleEvent.PreviousValue,
		CurrentValue:  ruleEvent.CurrentValue,
		Note:          ruleEvent.Note,
		AddressLabel:  marketRuleEventAddressLabel(ruleEvent.Details),
	}
	if hasProjection {
		result.Snapshot = frozenSnapshot
	} else {
		result.Snapshot = s.marketNotificationSnapshot(ctx, ruleEvent, event)
	}
	if event.MarketPoolID != nil {
		result.Pool, _ = s.GetMarketPool(ctx, *event.MarketPoolID)
	}
	return result, nil
}

func (s *Store) marketNotificationSnapshot(
	ctx context.Context,
	ruleEvent MarketRuleEvent,
	event MarketEvent,
) *MarketSnapshot {
	var snapshot *MarketSnapshot
	if snapshotID := marketRuleEventSnapshotID(ruleEvent.Details); snapshotID > 0 {
		snapshot, _ = collectOptional[MarketSnapshot](ctx, s.db, `
			SELECT `+marketSnapshotColumns+`
			FROM market_snapshots
			WHERE id = $1 AND chain_id = $2 AND token_address = $3
		`, snapshotID, event.ChainID, event.TokenAddress)
	}
	if snapshot == nil {
		snapshot, _ = s.LatestMarketSnapshot(
			ctx,
			event.ChainID,
			event.TokenAddress,
			event.MarketPoolID,
		)
	}
	return snapshot
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

func marketRuleEventSnapshotID(details json.RawMessage) int64 {
	values := struct {
		SnapshotID int64 `json:"snapshot_id"`
	}{}
	if err := json.Unmarshal(details, &values); err != nil {
		return 0
	}
	return values.SnapshotID
}

func (s *Store) notificationTimezone(ctx context.Context, deboxUserID string) string {
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

// CompleteMarketDelivery is retained for callers that already carry a lease.
// An unclassified transport error is uncertain, never an automatic retry.
func (s *Store) CompleteMarketDelivery(ctx context.Context, claim MarketDeliveryClaim, messageID *string, deliveryError string) error {
	var failure *notificationdelivery.Failure
	if deliveryError != "" {
		value := notificationdelivery.Classify(errors.New(deliveryError))
		failure = &value
	}
	return s.CompleteMarketDeliveryResult(ctx, claim, messageID, failure)
}

func (s *Store) CompleteMarketDeliveryResult(ctx context.Context, claim MarketDeliveryClaim, messageID *string, failure *notificationdelivery.Failure) error {
	table, err := marketDeliveryTable(claim.Kind)
	if err != nil {
		return err
	}
	if claim.LeaseToken <= 0 {
		return ErrMarketDeliveryLeaseLost
	}
	if failure == nil && (messageID == nil || strings.TrimSpace(*messageID) == "") {
		failure = &notificationdelivery.Failure{Kind: notificationdelivery.Unknown, Code: "success_without_message_id"}
	}
	_, err = withMarketDeliveryTx(ctx, s.db, func(tx DBTX) (struct{}, error) {
		if failure == nil {
			if err := lockMarketDeliverySources(ctx, tx, claim); err != nil {
				return struct{}{}, err
			}
		}
		var attempts int
		var began bool
		err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT notification_attempts,notification_send_started_at IS NOT NULL FROM %s WHERE id=$1 AND notification_status='sending'
    AND notification_lease_token=$2 AND notification_lease_expires_at>NOW() FOR UPDATE`, table), claim.ID, claim.LeaseToken).Scan(&attempts, &began)
		if isNoRows(err) {
			return struct{}{}, ErrMarketDeliveryLeaseLost
		}
		if err != nil {
			return struct{}{}, err
		}
		if began && failure != nil && failure.Kind == notificationdelivery.Retryable && (failure.Code == "market_delivery_preparation_failed" || failure.Code == "cancelled_before_send") {
			attempts--
		}
		status, outcome, note := "sent", "sent", ""
		delay := time.Duration(0)
		if failure != nil {
			outcome = failure.Kind
			note = failure.Error()
			status = "skipped"
			switch failure.Kind {
			case notificationdelivery.Retryable:
				if attempts < notificationdelivery.MaxAttempts {
					status = "failed"
					delay = notificationdelivery.RetryDelay(attempts, failure.RetryAfter)
				} else {
					outcome = notificationdelivery.Rejected
					note = "retry limit reached: " + note
				}
			case notificationdelivery.Rejected, notificationdelivery.Unknown:
			default:
				outcome = notificationdelivery.Unknown
			}
		}
		_, err = tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET notification_status=$3,delivery_outcome=$4,
     notification_message_id=COALESCE($5,notification_message_id),notification_error=$6,
     notification_sent_at=CASE WHEN $3='sent' THEN NOW() ELSE notification_sent_at END,
     next_attempt_at=CASE WHEN $3='failed' THEN NOW()+$7::interval ELSE next_attempt_at END,
     notification_lease_expires_at=NULL,notification_attempts=$8
    WHERE id=$1 AND notification_status='sending' AND notification_lease_token=$2`, table), claim.ID, claim.LeaseToken, status, outcome, messageID, truncate(note, 2000), durationInterval(delay), attempts)
		if err != nil {
			return struct{}{}, err
		}
		if status == "sent" {
			if err := markMarketDeliveryChildrenSent(ctx, tx, claim.Kind, claim.ID, messageID); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, nil
	})
	return err
}

func closePreCutoffMarketWindow(ctx context.Context, tx DBTX, table string, id int64, cutoff time.Time) error {
	if table != "market_stage_windows" && table != "market_combination_windows" {
		return ErrInvalidNotificationStatus
	}
	_, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET closed_at=COALESCE(closed_at,$2),
   notification_status=CASE WHEN notification_status='sent' OR delivery_outcome='unknown' THEN notification_status ELSE 'skipped' END,
   delivery_outcome=CASE WHEN notification_status='sent' OR delivery_outcome='unknown' THEN delivery_outcome
     WHEN (notification_status='sending' AND (notification_send_started_at IS NOT NULL OR notification_lease_token=0)) OR (notification_status IN('pending','failed') AND notification_lease_token=0 AND delivery_outcome='') THEN 'unknown' ELSE 'cancelled' END,
   notification_error=CASE WHEN notification_status='sent' OR delivery_outcome='unknown' THEN notification_error
     ELSE LEFT(CASE WHEN notification_error='' THEN 'window precedes recorded recovery cutoff' ELSE notification_error||'; window precedes recorded recovery cutoff' END,2000) END,
   updated_at=NOW() WHERE id=$1 AND created_at<$2 AND (closed_at IS NULL OR notification_status IN('collecting','pending','sending','failed'))`, table), id, cutoff)
	return err
}

// ResolveMarketDeliveryUnknown is an explicit control-plane operation. There
// is deliberately no worker or HTTP route invoking it. An operator must have
// independent evidence of delivery or non-delivery and record that evidence.
func (s *Store) ResolveMarketDeliveryUnknown(ctx context.Context, kind string, id int64, delivered bool, messageID, reason string) error {
	table, err := marketDeliveryTable(kind)
	if err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	messageID = strings.TrimSpace(messageID)
	if id <= 0 || reason == "" || len(reason) > 1000 || delivered != (messageID != "") {
		return ErrInvalidNotificationStatus
	}
	_, err = withMarketDeliveryTx(ctx, s.db, func(tx DBTX) (struct{}, error) {
		if delivered {
			if err := lockMarketDeliverySources(ctx, tx, MarketDeliveryClaim{Kind: kind, ID: id}); err != nil {
				return struct{}{}, err
			}
		}
		var attempts int
		var token int64
		var previousError string
		err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT notification_attempts,notification_lease_token,notification_error FROM %s WHERE id=$1
		 AND notification_status='skipped' AND delivery_outcome='unknown' FOR UPDATE`, table), id).Scan(&attempts, &token, &previousError)
		if isNoRows(err) {
			return struct{}{}, ErrNotFound
		}
		if err != nil {
			return struct{}{}, err
		}
		var accepted *string
		if delivered {
			accepted = &messageID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO market_delivery_unknown_resolutions(kind,source_id,lease_token,attempts,delivered,message_id,explanation,previous_error)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, kind, id, token, attempts, delivered, accepted, reason, previousError); err != nil {
			return struct{}{}, err
		}
		status, outcome := "failed", notificationdelivery.Retryable
		if delivered {
			status, outcome = "sent", "sent"
		} else if attempts >= notificationdelivery.MaxAttempts {
			status, outcome = "skipped", notificationdelivery.Rejected
		}
		_, err = tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET notification_status=$2,delivery_outcome=$3,
		 notification_message_id=CASE WHEN $2='sent' THEN $4 ELSE notification_message_id END,
		 notification_sent_at=CASE WHEN $2='sent' THEN NOW() ELSE notification_sent_at END,
		 notification_error=LEFT(notification_error||'; explicit resolution: '||$5,2000),
		 notification_lease_token=notification_lease_token+1,notification_lease_expires_at=NULL,
		 notification_send_started_at=NULL,next_attempt_at=NOW() WHERE id=$1`, table), id, status, outcome, messageID, reason)
		if err != nil {
			return struct{}{}, err
		}
		if delivered {
			if err := markMarketDeliveryChildrenSent(ctx, tx, kind, id, &messageID); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, nil
	})
	return err
}

func markMarketDeliveryChildrenSent(ctx context.Context, tx DBTX, kind string, id int64, messageID *string) error {
	var query string
	switch kind {
	case "stage":
		query = `UPDATE market_rule_events mre SET notification_status='sent',delivery_outcome='sent',notification_message_id=$2,notification_sent_at=NOW()
	 FROM market_stage_window_events link WHERE link.market_stage_window_id=$1 AND mre.id=link.market_rule_event_id AND mre.notification_status='staged'`
	case "combination":
		query = `UPDATE market_rule_events mre SET notification_status='sent',delivery_outcome='sent',notification_message_id=$2,notification_sent_at=NOW()
	 FROM market_combination_trigger_events link WHERE link.market_combination_window_id=$1 AND mre.id=link.market_rule_event_id AND mre.notification_status='combined'`
	}
	if query == "" {
		return nil
	}
	_, err := tx.Exec(ctx, query, id, messageID)
	return err
}

// No cutoff exists until the explicitly authorized cutover is recorded.
func (s *Store) reconcileMarketDeliveryCutoff(ctx context.Context) error {
	cutoff, err := notificationSourceCutoff(ctx, s.db)
	if err != nil {
		return err
	}
	_, err = withMarketDeliveryTx(ctx, s.db, func(tx DBTX) (struct{}, error) {
		_, err := tx.Exec(ctx, `WITH candidates AS(SELECT mre.id FROM market_rule_events mre JOIN market_events me ON me.id=mre.market_event_id
    JOIN market_rules mr ON mr.id=mre.market_rule_id
    WHERE GREATEST($1::timestamptz,mr.notification_source_after) IS NOT NULL
     AND (me.occurred_at<GREATEST($1::timestamptz,mr.notification_source_after) OR mre.created_at<GREATEST($1::timestamptz,mr.notification_source_after))
     AND mre.notification_status IN('pending','sending','failed','staged','combined') ORDER BY mre.id LIMIT 1000 FOR NO KEY UPDATE OF mre SKIP LOCKED)
   UPDATE market_rule_events d SET notification_status='skipped',
    delivery_outcome=CASE WHEN (d.notification_status='sending' AND(d.notification_send_started_at IS NOT NULL OR d.notification_lease_token=0)) OR (d.notification_status IN('pending','failed') AND d.notification_lease_token=0 AND d.delivery_outcome='')
      THEN 'unknown' ELSE 'cancelled' END,notification_error=LEFT(CASE WHEN d.notification_error='' THEN 'source precedes recorded recovery or lifecycle fence' ELSE d.notification_error||'; source precedes recorded recovery or lifecycle fence' END,2000)
    WHERE d.id IN(SELECT id FROM candidates)`, cutoff)
		if err != nil {
			return struct{}{}, err
		}
		for _, kind := range []string{"stage", "combination"} {
			table, parent, column := "market_stage_windows", "market_rules", "market_rule_id"
			if kind == "combination" {
				table, parent, column = "market_combination_windows", "market_combination_rules", "market_combination_rule_id"
			}
			_, err = tx.Exec(ctx, fmt.Sprintf(`WITH candidates AS(SELECT d.id,GREATEST($1::timestamptz,r.notification_source_after) AS fence
      FROM %s d JOIN %s r ON r.id=d.%s
      WHERE GREATEST($1::timestamptz,r.notification_source_after) IS NOT NULL
       AND d.created_at<GREATEST($1::timestamptz,r.notification_source_after)
       AND(d.closed_at IS NULL OR d.notification_status IN('collecting','pending','sending','failed'))
      ORDER BY d.id LIMIT 1000 FOR UPDATE OF d SKIP LOCKED)
    UPDATE %s d SET closed_at=COALESCE(d.closed_at,c.fence),
     notification_status=CASE WHEN d.notification_status='sent' OR d.delivery_outcome='unknown' THEN d.notification_status ELSE 'skipped' END,
     delivery_outcome=CASE WHEN d.notification_status='sent' OR d.delivery_outcome='unknown' THEN d.delivery_outcome
      WHEN (d.notification_status='sending' AND(d.notification_send_started_at IS NOT NULL OR d.notification_lease_token=0)) OR (d.notification_status IN('pending','failed') AND d.notification_lease_token=0 AND d.delivery_outcome='')THEN 'unknown' ELSE 'cancelled' END,
     notification_error=CASE WHEN d.notification_status='sent' OR d.delivery_outcome='unknown' THEN d.notification_error ELSE LEFT(CASE WHEN d.notification_error='' THEN 'window precedes recorded recovery or lifecycle fence' ELSE d.notification_error||'; window precedes recorded recovery or lifecycle fence' END,2000) END,
     updated_at=NOW() FROM candidates c WHERE d.id=c.id`, table, parent, column, table), cutoff)
			if err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, nil
	})
	return err
}

func marketResourceNotificationCutoff(ctx context.Context, tx DBTX, ruleID int64, combination bool) (*time.Time, error) {
	cutoff, err := notificationSourceCutoff(ctx, tx)
	if err != nil {
		return nil, err
	}
	table := "market_rules"
	if combination {
		table = "market_combination_rules"
	}
	var resource *time.Time
	if err := tx.QueryRow(ctx, `SELECT notification_source_after FROM `+table+` WHERE id=$1`, ruleID).Scan(&resource); err != nil {
		return nil, err
	}
	if resource != nil && (cutoff == nil || resource.After(*cutoff)) {
		return resource, nil
	}
	return cutoff, nil
}

// Read before pure evaluation so an old event cannot consume an in-memory
// crossing or cooldown. Persistence and BeginSend independently recheck it.
func (s *Store) GetMarketRuleNotificationSourceAfter(ctx context.Context, ruleID int64) (*time.Time, error) {
	return withMarketDeliveryTx(ctx, s.db, func(tx DBTX) (*time.Time, error) { return marketResourceNotificationCutoff(ctx, tx, ruleID, false) })
}

// Existing logical triggers cannot reserve a second private cooldown merely
// because the same parent was published under a new ready revision. Presence
// with false means the narrowly proven, never-prepared reorg recovery case.
func (s *Store) ListMarketRuleNotificationEventDecisions(ctx context.Context, ruleID int64, eventIDs []int64) (map[int64]bool, error) {
	if ruleID <= 0 {
		return nil, ErrInvalidMarketRuleEvent
	}
	if len(eventIDs) > 2000 {
		return nil, ErrMarketDeliveryCapacity
	}
	unique := make([]int64, 0, len(eventIDs))
	seen := make(map[int64]bool, len(eventIDs))
	for _, id := range eventIDs {
		if id <= 0 {
			return nil, ErrInvalidMarketRuleEvent
		}
		if !seen[id] {
			unique = append(unique, id)
			seen[id] = true
		}
	}
	if len(unique) == 0 {
		return map[int64]bool{}, nil
	}
	return withMarketDeliveryTx(ctx, s.db, func(tx DBTX) (map[int64]bool, error) {
		cutoff, err := marketResourceNotificationCutoff(ctx, tx, ruleID, false)
		if err != nil {
			return nil, err
		}
		type decision struct {
			EventID  int64 `db:"event_id"`
			Terminal bool  `db:"terminal"`
		}
		values, err := collectMany[decision](ctx, tx, `SELECT mre.market_event_id event_id,NOT COALESCE((
		 mre.notification_status='skipped' AND mre.delivery_outcome='cancelled'
		 AND RIGHT(mre.notification_error,LENGTH('source event was removed by a chain reorganization'))='source event was removed by a chain reorganization'
		 AND mre.notification_payload='{}'::jsonb AND mre.notification_snapshot_id IS NULL
		 AND mre.notification_send_started_at IS NULL AND mre.notification_message_id IS NULL AND mre.notification_attempts=0
		 AND(mre.notification_attempted_at IS NULL OR mre.notification_lease_token>0)
		 AND mr.enabled=1 AND mr.run_status='active' AND mp.status='active'
		 AND($3::timestamptz IS NULL OR(me.occurred_at>=$3 AND mre.created_at>=$3))
		 AND me.confirmed=1 AND me.reorged=0 AND mre.details ? 'trade_projection'
		 AND EXISTS(SELECT 1 FROM market_trade_ready_events ready JOIN market_transaction_receipts receipt
		  ON receipt.id=ready.receipt_id AND receipt.revision=ready.receipt_revision
		  WHERE ready.parent_event_id=me.id AND ready.valid AND receipt.complete AND receipt.confirmed AND NOT receipt.reorged
		   AND receipt.block_hash=me.block_hash AND EXISTS(SELECT 1 FROM market_scanned_blocks b WHERE b.chain_id=receipt.chain_id
		    AND b.block_number=receipt.block_number AND b.block_hash=receipt.block_hash AND b.canonical=1))
		 AND (`+marketDeliveryRouteValidSQL("realtime", "mre")+`)
		 AND NOT EXISTS(SELECT 1 FROM market_stage_window_events l JOIN market_stage_windows w ON w.id=l.market_stage_window_id
		   WHERE l.market_rule_event_id=mre.id AND(w.closed_at IS NOT NULL OR w.ends_at<=NOW() OR w.notification_payload<>'{}'::jsonb
		    OR w.notification_send_started_at IS NOT NULL OR w.notification_status IN('sent','sending','skipped') OR w.delivery_outcome='unknown'
		    OR NOT COALESCE((`+marketDeliveryRouteValidSQL("stage", "w")+`),FALSE)))
		 AND NOT EXISTS(SELECT 1 FROM market_combination_trigger_events l JOIN market_combination_windows w ON w.id=l.market_combination_window_id
		   WHERE l.source_type='market' AND l.market_rule_event_id=mre.id AND(w.closed_at IS NOT NULL OR w.ends_at<=NOW() OR w.notification_payload<>'{}'::jsonb
		    OR w.notification_send_started_at IS NOT NULL OR w.notification_status IN('sent','sending','skipped') OR w.delivery_outcome='unknown'
		    OR NOT COALESCE((`+marketDeliveryRouteValidSQL("combination", "w")+`),FALSE)))
		 ),FALSE) terminal FROM market_rule_events mre JOIN market_events me ON me.id=mre.market_event_id
		 JOIN market_rules mr ON mr.id=mre.market_rule_id JOIN market_projects mp ON mp.id=mr.market_project_id
		 WHERE mre.market_rule_id=$1 AND mre.market_event_id=ANY($2::bigint[])`, ruleID, unique, cutoff)
		if err != nil {
			return nil, err
		}
		result := make(map[int64]bool, len(values))
		for _, value := range values {
			result[value.EventID] = value.Terminal
		}
		return result, nil
	})
}

func advanceMarketRuleNotificationFence(ctx context.Context, tx DBTX, ruleID int64) error {
	if _, err := tx.Exec(ctx, `UPDATE market_rules SET notification_source_after=NOW() WHERE id=$1`, ruleID); err != nil {
		return err
	}
	return cancelMarketResourceDeliveries(ctx, tx, "market_rule_id", ruleID, "market rule was stopped or restored")
}
func advanceMarketCombinationNotificationFence(ctx context.Context, tx DBTX, ruleID int64) error {
	if _, err := tx.Exec(ctx, `UPDATE market_combination_rules SET notification_source_after=NOW() WHERE id=$1`, ruleID); err != nil {
		return err
	}
	return cancelMarketResourceDeliveries(ctx, tx, "market_combination_rule_id", ruleID, "market combination was stopped or restored")
}

// Called by entitlement transitions in the same transaction. The fence protects
// every queued row immediately; terminal bookkeeping is bounded per table.
func cancelMarketDeliveriesForUser(ctx context.Context, tx DBTX, owner, reason string) error {
	if _, err := tx.Exec(ctx, `UPDATE market_rules SET notification_source_after=NOW() WHERE debox_user_id=$1 AND enabled=1 AND run_status='active'`, owner); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE market_combination_rules SET notification_source_after=NOW() WHERE debox_user_id=$1 AND enabled=1 AND run_status='active'`, owner); err != nil {
		return err
	}
	for _, kind := range []string{"realtime", "stage", "combination"} {
		table, _ := marketDeliveryTable(kind)
		ownerPredicate := `d.debox_user_id=$1`
		if kind == "realtime" {
			ownerPredicate = `EXISTS(SELECT 1 FROM market_rules mr WHERE mr.id=d.market_rule_id AND mr.debox_user_id=$1)`
		}
		if err := cancelMarketDeliveryPage(ctx, tx, table, ownerPredicate, owner, reason); err != nil {
			return err
		}
	}
	return nil
}
func cancelMarketResourceDeliveries(ctx context.Context, tx DBTX, column string, id int64, reason string) error {
	kinds := []string{"realtime", "stage"}
	if column == "market_combination_rule_id" {
		kinds = []string{"combination"}
	}
	for _, kind := range kinds {
		table, _ := marketDeliveryTable(kind)
		if err := cancelMarketDeliveryPage(ctx, tx, table, `d.`+column+`=$1`, id, reason); err != nil {
			return err
		}
	}
	return nil
}
func cancelMarketDeliveryPage(ctx context.Context, tx DBTX, table, predicate string, value any, reason string) error {
	_, err := tx.Exec(ctx, fmt.Sprintf(`WITH candidates AS(SELECT d.id FROM %s d WHERE %s
  AND d.notification_status NOT IN('sent','skipped') AND d.delivery_outcome<>'unknown'
  ORDER BY d.id LIMIT 1000 FOR UPDATE OF d SKIP LOCKED)
 UPDATE %s d SET notification_status='skipped',
  delivery_outcome=CASE WHEN (d.notification_status='sending' AND(d.notification_send_started_at IS NOT NULL OR d.notification_lease_token=0)) OR (d.notification_status IN('pending','failed') AND d.notification_lease_token=0 AND d.delivery_outcome='')
    THEN 'unknown' ELSE 'cancelled' END,notification_error=LEFT(CASE WHEN d.notification_error='' THEN $2 ELSE d.notification_error||'; '||$2 END,2000)
 WHERE d.id IN(SELECT id FROM candidates)`, table, predicate, table), value, truncate(reason, 2000))
	return err
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
		&value.NotificationMessageID, &value.NotificationStatus, &value.DeliveryOutcome,
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
		&value.EndsAt, &value.TriggerCount, &value.NotificationStatus, &value.DeliveryOutcome,
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
		&value.NotificationStatus, &value.DeliveryOutcome, &value.NotificationMessageID,
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
