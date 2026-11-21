package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const marketRuleColumns = `
	id, debox_user_id, market_project_id, market_pool_id, rule_type,
	threshold_value::text AS threshold_value, threshold_unit, window_minutes,
	sensitivity, cooldown_seconds, repeat_while_active, rule_scope, delivery_mode, cycle_type,
	cycle_minutes, trigger_count_threshold,
	deployment_scope, pool_scope, cooldown_scope, notification_chat_id,
	notification_chat_type, notification_label, notification_language,
	enabled, run_status, pause_reason, aggregation_anchor_at,
	state, last_evaluated_at, last_triggered_at,
	created_at, updated_at
`

const marketEventColumns = `
	id, market_pool_id, market_asset_deployment_id,
	chain_key, chain_id, token_address,
	event_type, event_key, transaction_hash, transaction_index, log_index,
	block_number, block_hash, wallet_address,
	token_amount_raw::text AS token_amount_raw,
	quote_amount_raw::text AS quote_amount_raw,
	token_amount::text AS token_amount,
	quote_amount::text AS quote_amount,
	usd_value::text AS usd_value,
	price_usd::text AS price_usd,
	source, confidence::text AS confidence, confirmed, reorged,
	occurred_at, observed_at, raw_payload, metadata
`

const marketRuleEventColumns = `
	id, market_rule_id, market_event_id, trigger_key,
	previous_value, current_value, note, details,
	notification_message_id, notification_status, delivery_outcome, notification_error,
	notification_attempts, notification_attempted_at, notification_sent_at,
	next_attempt_at, created_at
`

type CreateMarketRuleParams struct {
	DeBoxUserID                string
	MarketProjectID            int64
	MarketPoolID               *int64
	DeploymentScope            string
	MarketProjectDeploymentIDs []int64
	PoolScope                  string
	MarketProjectPoolIDs       []int64
	CooldownScope              string
	RuleType                   string
	ThresholdValue             string
	ThresholdUnit              string
	WindowMinutes              *int32
	Sensitivity                string
	CooldownSeconds            int32
	RepeatWhileActive          bool
	RuleScope                  string
	DeliveryMode               string
	CycleType                  string
	CycleMinutes               int32
	TriggerCountThreshold      int64
	NotificationChatID         string
	NotificationChatType       string
	NotificationLabel          string
	NotificationLanguage       string
	State                      json.RawMessage
}

type CreateMarketEventParams struct {
	MarketPoolID     *int64
	ChainKey         string
	ChainID          int64
	TokenAddress     string
	EventType        string
	EventKey         string
	TransactionHash  *string
	TransactionIndex *int32
	LogIndex         *int32
	BlockNumber      *int64
	BlockHash        *string
	WalletAddress    *string
	TokenAmountRaw   *string
	QuoteAmountRaw   *string
	TokenAmount      *string
	QuoteAmount      *string
	USDValue         *string
	PriceUSD         *string
	Source           string
	Confidence       string
	Confirmed        bool
	OccurredAt       time.Time
	RawPayload       json.RawMessage
	Metadata         json.RawMessage
}

type CreateMarketRuleEventParams struct {
	MarketRuleID       int64
	MarketEventID      int64
	TriggerKey         string
	PreviousValue      *string
	CurrentValue       *string
	Note               string
	Details            json.RawMessage
	NotificationStatus string
	NotificationError  string
}

func (s *Store) CreateMarketRuleWithinQuota(
	ctx context.Context,
	params CreateMarketRuleParams,
	policy QuotaPolicy,
) (MarketRule, error) {
	normalizeMarketRuleParams(&params)
	if err := validateMarketRuleRuntimeParameters(params.RuleType, params.WindowMinutes, params.ThresholdValue); err != nil {
		return MarketRule{}, err
	}
	return withTxValue(ctx, s.db, func(tx DBTX) (MarketRule, error) {
		if err := lockUser(ctx, tx, params.DeBoxUserID); err != nil {
			return MarketRule{}, err
		}
		if err := requirePolicyPlan(ctx, tx, params.DeBoxUserID, policy); err != nil {
			return MarketRule{}, err
		}
		var scopeErr error
		policy, scopeErr = s.marketScopePolicy(ctx, tx, params.DeBoxUserID, policy)
		if scopeErr != nil {
			return MarketRule{}, scopeErr
		}
		if !policy.allowsMarketRuleType(params.RuleType) {
			return MarketRule{}, ErrMarketRuleTypeDenied
		}
		if params.NotificationChatType == "group" && !policy.GroupNotification {
			return MarketRule{}, ErrGroupNotificationDenied
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
				return MarketRule{}, fmt.Errorf("validate market notification group: %w", err)
			}
			if !exists {
				return MarketRule{}, ErrNotFound
			}
		}
		if params.DeliveryMode == "stage" && !policy.StageNotifications {
			return MarketRule{}, ErrStageNotificationsDenied
		}
		var projectStatus string
		if err := tx.QueryRow(ctx, `
			SELECT status
			FROM market_projects
			WHERE id = $1 AND debox_user_id = $2
			FOR UPDATE
		`, params.MarketProjectID, params.DeBoxUserID).Scan(&projectStatus); err != nil {
			if isNoRows(err) {
				return MarketRule{}, ErrNotFound
			}
			return MarketRule{}, fmt.Errorf("lock market project: %w", err)
		}
		if projectStatus != "active" {
			return MarketRule{}, ErrInvalidMarketStatus
		}
		if err := validateMarketRuleScopes(ctx, tx, params, policy); err != nil {
			return MarketRule{}, err
		}
		if params.MarketPoolID != nil {
			var linked, primary bool
			if err := tx.QueryRow(ctx, `
				SELECT
					EXISTS (
						SELECT 1
						FROM market_project_pools
						WHERE market_project_id = $1
						  AND market_pool_id = $2
						  AND selected = 1
					),
					EXISTS (
						SELECT 1
						FROM market_project_pools
						WHERE market_project_id = $1
						  AND market_pool_id = $2
						  AND is_primary = 1
					)
			`, params.MarketProjectID, *params.MarketPoolID).Scan(&linked, &primary); err != nil {
				return MarketRule{}, fmt.Errorf("check selected market pool: %w", err)
			}
			if !linked || (!policy.MultiPoolMonitoring && !primary) {
				return MarketRule{}, ErrMarketPoolMismatch
			}
		}
		duplicate, err := hasDuplicateMarketRule(ctx, tx, params)
		if err != nil {
			return MarketRule{}, err
		}
		if duplicate {
			return MarketRule{}, ErrMarketRuleExists
		}
		count, err := countActiveRuleSlots(ctx, tx, params.DeBoxUserID)
		if err != nil {
			return MarketRule{}, err
		}
		if count >= int64(policy.RuleLimit) {
			return MarketRule{}, ErrRuleLimitReached
		}
		if len(params.State) == 0 || string(params.State) == "{}" {
			var lastEventID int64
			if err := tx.QueryRow(ctx, `
				SELECT COALESCE(MAX(me.id), 0)
				FROM market_events me
				JOIN market_projects mp
				  ON mp.chain_id = me.chain_id
				 AND mp.token_address = me.token_address
				WHERE mp.id = $1
			`, params.MarketProjectID).Scan(&lastEventID); err != nil {
				return MarketRule{}, fmt.Errorf("initialize market rule cursor: %w", err)
			}
			params.State, _ = json.Marshal(map[string]int64{"last_event_id": lastEventID})
		}
		holderTargets, err := s.beginMarketHolderTargetCheck(ctx, tx, params.RuleScope == "standalone" && isMarketHolderRule(params.RuleType))
		if err != nil {
			return MarketRule{}, err
		}
		rule, err := createMarketRule(ctx, tx, params)
		if err != nil {
			return MarketRule{}, err
		}
		if err := createMarketRuleScopes(ctx, tx, rule.ID, params); err != nil {
			return MarketRule{}, err
		}
		if err := s.checkMarketHolderTargetLimit(ctx, tx, holderTargets); err != nil {
			return MarketRule{}, err
		}
		return rule, nil
	})
}

func createMarketRule(
	ctx context.Context,
	db DBTX,
	params CreateMarketRuleParams,
) (MarketRule, error) {
	rule, err := collectOne[MarketRule](ctx, db, `
		INSERT INTO market_rules (
			debox_user_id, market_project_id, market_pool_id, rule_type,
			threshold_value, threshold_unit, window_minutes, sensitivity,
			cooldown_seconds, repeat_while_active, rule_scope, delivery_mode, cycle_type,
			cycle_minutes, trigger_count_threshold,
			deployment_scope, pool_scope, cooldown_scope, notification_chat_id,
			notification_chat_type, notification_label, notification_language,
			aggregation_anchor_at, state, trade_activation_after
		)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
			$13, $14, $15, $16, $17, $18, $19, $20, $21, $22,
			CASE WHEN $12 = 'stage' AND $13 = 'fixed' THEN NOW() ELSE NULL END,
			$23, CASE WHEN $4 IN ('market_large_buy','market_large_sell','market_consecutive_large_buy','market_consecutive_large_sell') THEN NOW() ELSE NULL END
		)
		RETURNING `+marketRuleColumns,
		params.DeBoxUserID,
		params.MarketProjectID,
		params.MarketPoolID,
		params.RuleType,
		params.ThresholdValue,
		params.ThresholdUnit,
		params.WindowMinutes,
		params.Sensitivity,
		params.CooldownSeconds,
		params.RepeatWhileActive,
		params.RuleScope,
		params.DeliveryMode,
		params.CycleType,
		params.CycleMinutes,
		params.TriggerCountThreshold,
		params.DeploymentScope,
		params.PoolScope,
		params.CooldownScope,
		params.NotificationChatID,
		params.NotificationChatType,
		params.NotificationLabel,
		params.NotificationLanguage,
		normalizedJSON(params.State),
	)
	if err != nil {
		return MarketRule{}, fmt.Errorf("create market rule: %w", err)
	}
	return rule, nil
}

func validateMarketRuleScopes(
	ctx context.Context,
	db DBTX,
	params CreateMarketRuleParams,
	policy QuotaPolicy,
) error {
	if params.DeploymentScope == "selected" && len(params.MarketProjectDeploymentIDs) == 0 {
		return ErrInvalidMarketRule
	}
	if params.PoolScope == "selected" && len(params.MarketProjectPoolIDs) == 0 &&
		params.MarketPoolID == nil {
		return ErrInvalidMarketRule
	}
	for _, deploymentID := range params.MarketProjectDeploymentIDs {
		if deploymentID <= 0 {
			return ErrInvalidMarketRule
		}
	}
	for _, projectPoolID := range params.MarketProjectPoolIDs {
		if projectPoolID <= 0 {
			return ErrInvalidMarketRule
		}
	}
	if !policy.MultiPoolMonitoring && params.PoolScope == "all" {
		return ErrMarketPoolMismatch
	}
	for _, deploymentID := range uniquePositiveIDs(params.MarketProjectDeploymentIDs) {
		var exists bool
		if err := db.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM market_project_deployments
				WHERE id = $1 AND market_project_id = $2 AND status = 'active'
			)
		`, deploymentID, params.MarketProjectID).Scan(&exists); err != nil {
			return fmt.Errorf("validate market rule deployment scope: %w", err)
		}
		if !exists {
			return ErrInvalidMarketRule
		}
	}
	for _, projectPoolID := range uniquePositiveIDs(params.MarketProjectPoolIDs) {
		var selected, primary bool
		if err := db.QueryRow(ctx, `
			SELECT selected = 1, is_primary = 1
			FROM market_project_pools
			WHERE id = $1 AND market_project_id = $2
		`, projectPoolID, params.MarketProjectID).Scan(&selected, &primary); err != nil {
			if isNoRows(err) {
				return ErrMarketPoolMismatch
			}
			return fmt.Errorf("validate market rule pool scope: %w", err)
		}
		if !selected || (!policy.MultiPoolMonitoring && !primary) {
			return ErrMarketPoolMismatch
		}
	}
	return validateMarketProjectScope(ctx, db, params.MarketProjectID, params.DeBoxUserID, policy)
}

func createMarketRuleScopes(
	ctx context.Context,
	db DBTX,
	ruleID int64,
	params CreateMarketRuleParams,
) error {
	for _, deploymentID := range uniquePositiveIDs(params.MarketProjectDeploymentIDs) {
		if _, err := db.Exec(ctx, `
			INSERT INTO market_rule_deployments (
				market_rule_id, market_project_deployment_id
			)
			VALUES ($1, $2)
			ON CONFLICT DO NOTHING
		`, ruleID, deploymentID); err != nil {
			return fmt.Errorf("create market rule deployment scope: %w", err)
		}
	}
	projectPoolIDs := append([]int64(nil), params.MarketProjectPoolIDs...)
	if params.MarketPoolID != nil && len(projectPoolIDs) == 0 {
		var projectPoolID int64
		if err := db.QueryRow(ctx, `
			SELECT id
			FROM market_project_pools
			WHERE market_project_id = $1 AND market_pool_id = $2
		`, params.MarketProjectID, *params.MarketPoolID).Scan(&projectPoolID); err != nil {
			if !isNoRows(err) {
				return fmt.Errorf("resolve legacy market rule pool scope: %w", err)
			}
		} else {
			projectPoolIDs = append(projectPoolIDs, projectPoolID)
		}
	}
	for _, projectPoolID := range uniquePositiveIDs(projectPoolIDs) {
		if _, err := db.Exec(ctx, `
			INSERT INTO market_rule_pools (market_rule_id, market_project_pool_id)
			VALUES ($1, $2)
			ON CONFLICT DO NOTHING
		`, ruleID, projectPoolID); err != nil {
			return fmt.Errorf("create market rule pool scope: %w", err)
		}
	}
	return nil
}

func uniquePositiveIDs(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (s *Store) GetMarketRule(
	ctx context.Context,
	ruleID int64,
	deboxUserID string,
) (*MarketRule, error) {
	rule, err := collectOptional[MarketRule](ctx, s.db, `
		SELECT `+marketRuleColumns+`
		FROM market_rules
		WHERE id = $1 AND debox_user_id = $2
	`, ruleID, deboxUserID)
	if err != nil {
		return nil, fmt.Errorf("get market rule: %w", err)
	}
	return rule, nil
}

func (s *Store) ListMarketRules(
	ctx context.Context,
	deboxUserID string,
	projectID *int64,
) ([]MarketRule, error) {
	query := `
		SELECT ` + marketRuleColumns + `
		FROM market_rules
		WHERE debox_user_id = $1
	`
	args := []any{deboxUserID}
	if projectID != nil {
		query += " AND market_project_id = $2"
		args = append(args, *projectID)
	}
	query += " ORDER BY created_at DESC, id DESC"
	rules, err := collectMany[MarketRule](ctx, s.db, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list market rules: %w", err)
	}
	return rules, nil
}

func (s *Store) CountMarketRules(ctx context.Context, deboxUserID string) (int64, error) {
	return queryCount(ctx, s.db, `
		SELECT COUNT(*)
		FROM market_rules
		WHERE debox_user_id = $1 AND enabled = 1 AND run_status = 'active'
	`, deboxUserID)
}

func (s *Store) DeleteMarketRule(
	ctx context.Context,
	ruleID int64,
	deboxUserID string,
) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		DELETE FROM market_rules mr
		USING market_projects mp
		WHERE mr.id = $1 AND mr.debox_user_id = $2
		  AND mr.rule_scope = 'standalone'
		  AND mp.id = mr.market_project_id
		  AND mp.status <> 'paused'
		  AND mp.pause_reason <> 'subscription_expired'
	`, ruleID, deboxUserID)
	if err != nil {
		return false, fmt.Errorf("delete market rule: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *Store) RestoreMarketRuleWithinQuota(
	ctx context.Context,
	ruleID int64,
	deboxUserID string,
	policy QuotaPolicy,
) (MarketRule, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (MarketRule, error) {
		if err := lockUser(ctx, tx, deboxUserID); err != nil {
			return MarketRule{}, err
		}
		if err := requirePolicyPlan(ctx, tx, deboxUserID, policy); err != nil {
			return MarketRule{}, err
		}
		var scopeErr error
		policy, scopeErr = s.marketScopePolicy(ctx, tx, deboxUserID, policy)
		if scopeErr != nil {
			return MarketRule{}, scopeErr
		}
		rule, err := collectOne[MarketRule](ctx, tx, `
			SELECT `+marketRuleColumns+`
			FROM market_rules
			WHERE id = $1 AND debox_user_id = $2
			FOR UPDATE
		`, ruleID, deboxUserID)
		if isNoRows(err) {
			return MarketRule{}, ErrNotFound
		}
		if err != nil {
			return MarketRule{}, fmt.Errorf("lock market rule: %w", err)
		}
		if rule.Enabled != 1 || rule.RuleScope != "standalone" {
			return MarketRule{}, ErrNotFound
		}
		if err := validateMarketRuleRuntimeParameters(rule.RuleType, rule.WindowMinutes, rule.ThresholdValue); err != nil {
			return MarketRule{}, err
		}
		var projectFrozen bool
		if err := tx.QueryRow(ctx, `
			SELECT status = 'paused' AND pause_reason = 'subscription_expired'
			FROM market_projects
			WHERE id = $1 AND debox_user_id = $2
		`, rule.MarketProjectID, deboxUserID).Scan(&projectFrozen); err != nil {
			if isNoRows(err) {
				return MarketRule{}, ErrNotFound
			}
			return MarketRule{}, fmt.Errorf("check frozen market project: %w", err)
		}
		if projectFrozen {
			return MarketRule{}, ErrInvalidMarketStatus
		}
		if !policy.allowsMarketRuleType(rule.RuleType) {
			return MarketRule{}, ErrMarketRuleTypeDenied
		}
		if rule.NotificationChatType == "group" && !policy.GroupNotification {
			return MarketRule{}, ErrGroupNotificationDenied
		}
		if rule.DeliveryMode == "stage" && !policy.StageNotifications {
			return MarketRule{}, ErrStageNotificationsDenied
		}
		if !policy.MultiPoolMonitoring && rule.PoolScope == "all" {
			return MarketRule{}, ErrMarketPoolMismatch
		}
		if !policy.MultiPoolMonitoring && rule.PoolScope == "selected" {
			var nonPrimary bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1
					FROM market_rule_pools mrp
					JOIN market_project_pools mpp
					  ON mpp.id = mrp.market_project_pool_id
					WHERE mrp.market_rule_id = $1
					  AND mpp.is_primary <> 1
				)
			`, rule.ID).Scan(&nonPrimary); err != nil {
				return MarketRule{}, fmt.Errorf("validate restored market pool scope: %w", err)
			}
			if nonPrimary {
				return MarketRule{}, ErrMarketPoolMismatch
			}
		}
		if err := validateStoredMarketRuleScope(ctx, tx, rule, policy); err != nil {
			return MarketRule{}, err
		}
		if rule.RunStatus == "active" {
			return rule, nil
		}
		count, err := countActiveRuleSlots(ctx, tx, deboxUserID)
		if err != nil {
			return MarketRule{}, err
		}
		if count >= int64(policy.RuleLimit) {
			return MarketRule{}, ErrRuleLimitReached
		}
		holderTargets, err := s.beginMarketHolderTargetCheck(ctx, tx, isMarketHolderRule(rule.RuleType))
		if err != nil {
			return MarketRule{}, err
		}
		if err := advanceMarketRuleNotificationFence(ctx, tx, rule.ID); err != nil {
			return MarketRule{}, err
		}
		restored, err := collectOne[MarketRule](ctx, tx, `
			UPDATE market_rules
			SET run_status = 'active',
			    pause_reason = '',
			    aggregation_anchor_at = CASE
			      WHEN delivery_mode = 'stage' AND cycle_type = 'fixed' THEN NOW()
			      ELSE NULL
			    END,
			    updated_at = NOW()
			WHERE id = $1 AND debox_user_id = $2 AND enabled = 1
			RETURNING `+marketRuleColumns,
			ruleID,
			deboxUserID,
		)
		if err != nil {
			return MarketRule{}, fmt.Errorf("restore market rule: %w", err)
		}
		if err := s.checkMarketHolderTargetLimit(ctx, tx, holderTargets); err != nil {
			return MarketRule{}, err
		}
		return restored, nil
	})
}

func (s *Store) UpdateMarketRuleState(
	ctx context.Context,
	ruleID int64,
	state json.RawMessage,
	triggered bool,
) (MarketRule, error) {
	rule, err := collectOne[MarketRule](ctx, s.db, `
		UPDATE market_rules
		SET state = $1,
		    last_evaluated_at = NOW(),
		    next_evaluation_at = NULL,
		    last_triggered_at = CASE WHEN $2 THEN NOW() ELSE last_triggered_at END,
		    updated_at = NOW()
		WHERE id = $3
		RETURNING `+marketRuleColumns,
		normalizedJSON(state),
		triggered,
		ruleID,
	)
	if isNoRows(err) {
		return MarketRule{}, ErrNotFound
	}
	if err != nil {
		return MarketRule{}, fmt.Errorf("update market rule state: %w", err)
	}
	return rule, nil
}

func (s *Store) ListEnabledMarketRules(
	ctx context.Context,
	limit int,
) ([]MarketRule, error) {
	limit = clamp(limit, 1, 1000)
	rules, err := collectMany[MarketRule](ctx, s.db, `
		SELECT
			mr.id, mr.debox_user_id, mr.market_project_id, mr.market_pool_id,
			mr.rule_type, mr.threshold_value::text AS threshold_value,
			mr.threshold_unit, mr.window_minutes, mr.sensitivity,
			mr.cooldown_seconds, mr.repeat_while_active, mr.rule_scope, mr.delivery_mode,
			mr.cycle_type, mr.cycle_minutes, mr.trigger_count_threshold,
			mr.deployment_scope, mr.pool_scope, mr.cooldown_scope,
			mr.notification_chat_id, mr.notification_chat_type,
			mr.notification_label, mr.notification_language,
			mr.enabled, mr.run_status, mr.pause_reason, mr.aggregation_anchor_at,
			mr.state, mr.last_evaluated_at,
			mr.last_triggered_at, mr.created_at, mr.updated_at
		FROM market_rules mr
		JOIN market_projects mp ON mp.id = mr.market_project_id
		WHERE mr.enabled = 1
		  AND mr.run_status = 'active'
		  AND (mr.next_evaluation_at IS NULL OR mr.next_evaluation_at<=NOW())
		  AND mp.status = 'active'
		  AND `+marketRuleSupportedParametersSQL("mr")+`
		ORDER BY COALESCE(mr.last_evaluated_at, mr.created_at), mr.id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list enabled market rules: %w", err)
	}
	return rules, nil
}

func (s *Store) CreateMarketEvent(
	ctx context.Context,
	params CreateMarketEventParams,
) (MarketEvent, bool, error) {
	return createMarketEvent(ctx, s.db, params)
}

func createMarketEvent(
	ctx context.Context,
	db DBTX,
	params CreateMarketEventParams,
) (MarketEvent, bool, error) {
	if err := normalizeMarketEventParams(&params); err != nil {
		return MarketEvent{}, false, err
	}
	event, err := collectOne[MarketEvent](ctx, db, `
		INSERT INTO market_events (
			market_pool_id, chain_key, chain_id, token_address,
			event_type, event_key, transaction_hash, transaction_index,
			log_index, block_number, block_hash, wallet_address,
			token_amount_raw, quote_amount_raw, token_amount, quote_amount,
			usd_value, price_usd, source, confidence, confirmed,
			occurred_at, raw_payload, metadata
		)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
			$13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24
		)
		ON CONFLICT DO NOTHING
		RETURNING `+marketEventColumns,
		params.MarketPoolID,
		params.ChainKey,
		params.ChainID,
		params.TokenAddress,
		params.EventType,
		params.EventKey,
		params.TransactionHash,
		params.TransactionIndex,
		params.LogIndex,
		params.BlockNumber,
		params.BlockHash,
		params.WalletAddress,
		params.TokenAmountRaw,
		params.QuoteAmountRaw,
		params.TokenAmount,
		params.QuoteAmount,
		params.USDValue,
		params.PriceUSD,
		params.Source,
		params.Confidence,
		boolInt(params.Confirmed),
		params.OccurredAt,
		normalizedJSON(params.RawPayload),
		normalizedJSON(params.Metadata),
	)
	if err == nil {
		return event, true, nil
	}
	if !isNoRows(err) {
		return MarketEvent{}, false, fmt.Errorf("create market event: %w", err)
	}
	existing, err := collectOne[MarketEvent](ctx, db, `
		SELECT `+marketEventColumns+`
		FROM market_events
		WHERE chain_id = $1
		  AND token_address = $2
		  AND (
		      event_key = $3
		      OR (
		          $4::text IS NOT NULL
		          AND $5::integer IS NOT NULL
		          AND transaction_hash = $4
		          AND log_index = $5
		      )
		  )
		ORDER BY id
		LIMIT 1
	`, params.ChainID, params.TokenAddress, params.EventKey, params.TransactionHash, params.LogIndex)
	if err != nil {
		return MarketEvent{}, false, fmt.Errorf("get duplicate market event: %w", err)
	}
	// A transaction can be included again after a shallow reorganization. The
	// uniqueness keys intentionally keep one logical event, so revive the
	// retired row with its new canonical block placement instead of silently
	// leaving it marked as reorged.
	if existing.Reorged == 1 {
		revived, reviveErr := collectOne[MarketEvent](ctx, db, `
			UPDATE market_events
			SET market_pool_id = $1,
			    event_type = $2,
			    transaction_index = $3,
			    log_index = $4,
			    block_number = $5,
			    block_hash = $6,
			    wallet_address = $7,
			    token_amount_raw = $8,
			    quote_amount_raw = $9,
			    token_amount = $10,
			    quote_amount = $11,
			    usd_value = $12,
			    price_usd = $13,
			    source = $14,
			    confidence = $15,
			    confirmed = $16,
			    reorged = 0,
			    occurred_at = $17,
			    observed_at = NOW(),
			    raw_payload = $18,
			    metadata = $19
			WHERE id = $20 AND reorged = 1
			RETURNING `+marketEventColumns,
			params.MarketPoolID,
			params.EventType,
			params.TransactionIndex,
			params.LogIndex,
			params.BlockNumber,
			params.BlockHash,
			params.WalletAddress,
			params.TokenAmountRaw,
			params.QuoteAmountRaw,
			params.TokenAmount,
			params.QuoteAmount,
			params.USDValue,
			params.PriceUSD,
			params.Source,
			params.Confidence,
			boolInt(params.Confirmed),
			params.OccurredAt,
			normalizedJSON(params.RawPayload),
			normalizedJSON(params.Metadata),
			existing.ID,
		)
		if reviveErr != nil {
			return MarketEvent{}, false, fmt.Errorf("revive re-included market event: %w", reviveErr)
		}
		return revived, true, nil
	}
	return existing, false, nil
}

func (s *Store) ListMarketEvents(
	ctx context.Context,
	projectID int64,
	deboxUserID string,
	beforeID int64,
	limit int,
) ([]MarketEvent, error) {
	return s.ListMarketEventsFiltered(ctx, projectID, deboxUserID, MarketEventFilter{
		BeforeID: beforeID,
		Limit:    limit,
	})
}

func (s *Store) ListMarketEventsFiltered(
	ctx context.Context,
	projectID int64,
	deboxUserID string,
	filter MarketEventFilter,
) ([]MarketEvent, error) {
	filter.Limit = clamp(filter.Limit, 1, 500)
	filter.ChainKey = strings.ToLower(strings.TrimSpace(filter.ChainKey))
	filter.EventType = strings.ToLower(strings.TrimSpace(filter.EventType))
	filter.WalletAddress = strings.ToLower(strings.TrimSpace(filter.WalletAddress))
	events, err := collectMany[MarketEvent](ctx, s.db, `
		SELECT
			me.id, me.market_pool_id, me.market_asset_deployment_id,
			me.chain_key, me.chain_id, me.token_address,
			me.event_type, me.event_key, me.transaction_hash, me.transaction_index,
			me.log_index, me.block_number, me.block_hash, me.wallet_address,
			me.token_amount_raw::text AS token_amount_raw,
			me.quote_amount_raw::text AS quote_amount_raw,
			me.token_amount::text AS token_amount,
			me.quote_amount::text AS quote_amount,
			me.usd_value::text AS usd_value,
			me.price_usd::text AS price_usd,
			me.source, me.confidence::text AS confidence, me.confirmed, me.reorged,
			me.occurred_at, me.observed_at, me.raw_payload, me.metadata
		FROM market_events me
		JOIN market_projects mp ON mp.id = $1
		WHERE mp.debox_user_id = $2
		  AND (
			(mp.chain_id = me.chain_id AND mp.token_address = me.token_address)
			OR EXISTS (
				SELECT 1
				FROM market_project_deployments mpd
				JOIN market_asset_deployments mad
				  ON mad.id = mpd.market_asset_deployment_id
				WHERE mpd.market_project_id = mp.id
				  AND mpd.status <> 'removed'
				  AND (
					me.market_asset_deployment_id = mad.id
					OR (
						me.chain_id = mad.chain_id
						AND me.token_address = mad.token_address
					)
				  )
			)
		  )
		  AND ($3::bigint = 0 OR me.id < $3)
		  AND ($4::text = '' OR me.chain_key = $4)
		  AND ($5::text = '' OR me.event_type = $5)
		  AND ($6::bigint = 0 OR me.market_pool_id = $6)
		  AND ($7::text = '' OR me.wallet_address = $7)
		ORDER BY me.id DESC
		LIMIT $8
	`,
		projectID,
		strings.TrimSpace(deboxUserID),
		filter.BeforeID,
		filter.ChainKey,
		filter.EventType,
		filter.MarketPoolID,
		filter.WalletAddress,
		filter.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list filtered market events: %w", err)
	}
	return events, nil
}

func (s *Store) ListMarketRuleEventHistory(
	ctx context.Context,
	projectID int64,
	deboxUserID string,
	filter MarketEventFilter,
) ([]MarketRuleEventHistory, error) {
	filter.Limit = clamp(filter.Limit, 1, 100)
	filter.ChainKey = strings.ToLower(strings.TrimSpace(filter.ChainKey))
	filter.RuleType = strings.ToLower(strings.TrimSpace(filter.RuleType))
	filter.WalletAddress = strings.ToLower(strings.TrimSpace(filter.WalletAddress))
	events, err := collectMany[MarketRuleEventHistory](ctx, s.db, `
		SELECT
			mre.id,
			mre.market_rule_id,
			mre.market_event_id,
			mr.rule_type,
			mr.threshold_value::text AS threshold_value,
			mr.threshold_unit,
			mre.previous_value,
			mre.current_value,
			mre.note,
			mre.notification_status, mre.delivery_outcome,
			mre.notification_error,
			mre.notification_sent_at,
			mre.created_at,
			CASE WHEN trade_scope.frozen THEN
				CASE WHEN cardinality(trade_scope.pool_ids) = 1 THEN trade_scope.pool_ids[1] END
				ELSE me.market_pool_id END AS market_pool_id,
			CASE WHEN trade_scope.frozen THEN trade_scope.pool_ids ELSE ARRAY[]::bigint[] END AS trade_pool_ids,
			me.chain_key,
			me.event_type,
			me.transaction_hash,
			me.wallet_address,
			CASE WHEN trade_scope.frozen THEN NULLIF(mre.details #>> '{trade_projection,token_amount}', '')
				ELSE me.token_amount::text END AS token_amount,
			CASE WHEN trade_scope.frozen THEN NULLIF(mre.details #>> '{trade_projection,usd_value}', '')
				ELSE me.usd_value::text END AS usd_value,
			me.source,
			me.occurred_at,
			(mre.notification_status = 'sent') AS notification_successful,
			COALESCE(mre.details->>'address_label', '') AS address_label,
			LOWER(COALESCE(mre.details->>'address_excluded', 'false')) = 'true'
				AS address_excluded,
			COALESCE(
				ARRAY(
					SELECT DISTINCT BTRIM(mcr.note)
					FROM market_combination_trigger_events mcte
					JOIN market_combination_windows mcw
					  ON mcw.id = mcte.market_combination_window_id
					JOIN market_combination_rules mcr
					  ON mcr.id = mcw.market_combination_rule_id
					WHERE mcte.source_type = 'market'
					  AND mcte.market_rule_event_id = mre.id
					  AND BTRIM(mcr.note) <> ''
					ORDER BY BTRIM(mcr.note)
				),
				ARRAY[]::text[]
			) AS combination_notes
		FROM market_rule_events mre
		JOIN market_rules mr ON mr.id = mre.market_rule_id
		JOIN market_events me ON me.id = mre.market_event_id
		JOIN market_projects mp ON mp.id = mr.market_project_id
		CROSS JOIN LATERAL (
			SELECT mre.details ? 'trade_projection' AS frozen,
				ARRAY(
					SELECT DISTINCT value::bigint
					FROM jsonb_array_elements_text(
						CASE WHEN jsonb_typeof(mre.details #> '{trade_projection,pool_ids}') = 'array'
						THEN mre.details #> '{trade_projection,pool_ids}' ELSE '[]'::jsonb END
					)
					ORDER BY 1
				) AS pool_ids
		) trade_scope
		WHERE mr.market_project_id = $1
		  AND mr.debox_user_id = $2
		  AND mre.created_at >= COALESCE(mp.frozen_at, NOW()) - INTERVAL '30 days'
		  AND (mp.frozen_at IS NULL OR mre.created_at <= mp.frozen_at)
		  AND ($3::bigint = 0 OR mre.id < $3)
		  AND ($4::text = '' OR me.chain_key = $4)
		  AND ($5::text = '' OR mr.rule_type = $5)
		  AND ($6::bigint = 0 OR CASE WHEN trade_scope.frozen THEN $6 = ANY(trade_scope.pool_ids)
			ELSE me.market_pool_id = $6 END)
		  AND ($7::text = '' OR me.wallet_address = $7)
		ORDER BY mre.id DESC
		LIMIT $8
	`,
		projectID,
		strings.TrimSpace(deboxUserID),
		filter.BeforeID,
		filter.ChainKey,
		filter.RuleType,
		filter.MarketPoolID,
		filter.WalletAddress,
		filter.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list market rule event history: %w", err)
	}
	return events, nil
}

func (s *Store) ListMarketEventsAfter(
	ctx context.Context,
	projectID int64,
	deboxUserID string,
	afterID int64,
	limit int,
) ([]MarketEvent, error) {
	limit = clamp(limit, 1, 2000)
	events, err := collectMany[MarketEvent](ctx, s.db, `
		SELECT
			me.id, me.market_pool_id, me.market_asset_deployment_id,
			me.chain_key, me.chain_id, me.token_address,
			me.event_type, me.event_key, me.transaction_hash, me.transaction_index,
			me.log_index, me.block_number, me.block_hash, me.wallet_address,
			me.token_amount_raw::text AS token_amount_raw,
			me.quote_amount_raw::text AS quote_amount_raw,
			me.token_amount::text AS token_amount,
			me.quote_amount::text AS quote_amount,
			me.usd_value::text AS usd_value,
			me.price_usd::text AS price_usd,
			me.source, me.confidence::text AS confidence, me.confirmed, me.reorged,
			me.occurred_at, me.observed_at, me.raw_payload, me.metadata
		FROM market_events me
		JOIN market_projects mp ON mp.id = $1
		WHERE mp.debox_user_id = $2
		  AND (
			(mp.chain_id = me.chain_id AND mp.token_address = me.token_address)
			OR EXISTS (
				SELECT 1
				FROM market_project_deployments mpd
				JOIN market_asset_deployments mad
				  ON mad.id = mpd.market_asset_deployment_id
				WHERE mpd.market_project_id = mp.id
				  AND mpd.status <> 'removed'
				  AND (
					me.market_asset_deployment_id = mad.id
					OR (
						me.chain_id = mad.chain_id
						AND me.token_address = mad.token_address
					)
				  )
			)
		  )
		  AND me.id > $3
		ORDER BY me.id
		LIMIT $4
	`, projectID, deboxUserID, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list new market events: %w", err)
	}
	return events, nil
}

func (s *Store) MarkMarketBlockConfirmed(
	ctx context.Context,
	chainID int64,
	throughBlock int64,
) (int64, error) {
	if chainID <= 0 || throughBlock < 0 {
		return 0, ErrInvalidMarketEvent
	}
	type confirmationResult struct {
		Count   int64
		Pending bool
	}
	result, err := withTxValue(ctx, s.db, func(tx DBTX) (confirmationResult, error) {
		if err := lockMarketTradeChain(ctx, tx, chainID); err != nil {
			return confirmationResult{}, err
		}
		rows, err := tx.Query(ctx, `
			WITH RECURSIVE candidates AS MATERIALIZED (
				SELECT r.id,r.block_number FROM market_transaction_receipts r
				WHERE r.chain_id=$1 AND r.block_number<=$2 AND r.complete AND NOT r.reorged
				  AND NOT r.confirmed
				  AND EXISTS(SELECT 1 FROM market_scanned_blocks b WHERE b.chain_id=r.chain_id
				    AND b.block_number=r.block_number AND b.block_hash=r.block_hash AND b.canonical=1)
				ORDER BY r.block_number,r.id LIMIT 2000 FOR UPDATE SKIP LOCKED
			), ranked AS MATERIALIZED (
				SELECT candidates.*,ROW_NUMBER() OVER(ORDER BY block_number,id) AS ordinal FROM candidates
			), picked AS (
				SELECT c.*,cost.parents AS total_parents FROM ranked c
				CROSS JOIN LATERAL (SELECT GREATEST(1,COUNT(*)) AS parents FROM market_transaction_events x WHERE x.receipt_id=c.id) cost
				WHERE c.ordinal=1
				UNION ALL
				SELECT c.*,previous.total_parents+cost.parents FROM picked previous JOIN ranked c ON c.ordinal=previous.ordinal+1
				CROSS JOIN LATERAL (SELECT GREATEST(1,COUNT(*)) AS parents FROM market_transaction_events x WHERE x.receipt_id=c.id) cost
				WHERE previous.total_parents<10000 AND previous.total_parents+cost.parents<=10000
			) SELECT id FROM picked ORDER BY ordinal
		`, chainID, throughBlock)
		if err != nil {
			return confirmationResult{}, fmt.Errorf("find verified complete market receipts: %w", err)
		}
		ids, err := collectInt64Rows(rows)
		if err != nil {
			return confirmationResult{}, err
		}
		count, err := confirmMarketReceipts(ctx, tx, ids)
		if err != nil {
			return confirmationResult{}, err
		}
		var pending bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM market_transaction_receipts r WHERE r.chain_id=$1 AND r.block_number<=$2
			AND r.complete AND NOT r.reorged AND NOT r.confirmed
			AND EXISTS(SELECT 1 FROM market_scanned_blocks b WHERE b.chain_id=r.chain_id
			AND b.block_number=r.block_number AND b.block_hash=r.block_hash AND b.canonical=1)
		)`, chainID, throughBlock).Scan(&pending); err != nil {
			return confirmationResult{}, err
		}
		return confirmationResult{Count: count, Pending: pending}, nil
	})
	if err != nil {
		return 0, err
	}
	// Report the bounded remainder only after COMMIT. Returning it from inside
	// withTxValue would roll back progress and retry the same first batch forever.
	if result.Pending {
		return result.Count, ErrMarketTradeConfirmationPending
	}
	return result.Count, nil
}

func (s *Store) ListUnconfirmedMarketEventBlocks(
	ctx context.Context,
	chainID int64,
	fromBlock int64,
	toBlock int64,
) ([]int64, error) {
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT block_number
		FROM market_events
		WHERE chain_id = $1
		  AND block_number BETWEEN $2 AND $3
		  AND confirmed = 0
		  AND reorged = 0
		ORDER BY block_number
	`, chainID, fromBlock, toBlock)
	if err != nil {
		return nil, fmt.Errorf("list unconfirmed market event blocks: %w", err)
	}
	values, err := collectInt64Rows(rows)
	if err != nil {
		return nil, fmt.Errorf("collect unconfirmed market event blocks: %w", err)
	}
	return values, nil
}

func (s *Store) MarkMarketBlockReorged(
	ctx context.Context,
	chainID int64,
	blockNumber int64,
	canonicalBlockHash string,
) (int64, error) {
	canonicalBlockHash = strings.ToLower(strings.TrimSpace(canonicalBlockHash))
	if chainID <= 0 || blockNumber < 0 ||
		!marketHashPattern.MatchString(canonicalBlockHash) {
		return 0, ErrInvalidMarketEvent
	}
	return withTxValue(ctx, s.db, func(tx DBTX) (int64, error) {
		if err := lockMarketTradeChain(ctx, tx, chainID); err != nil {
			return 0, err
		}
		var replacedBlock bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM market_scanned_blocks
		 WHERE chain_id=$1 AND block_number=$2 AND canonical=1 AND block_hash<>$3)`, chainID, blockNumber, canonicalBlockHash).Scan(&replacedBlock); err != nil {
			return 0, err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE market_events
			SET reorged = 1, confirmed = 0
			WHERE chain_id = $1
			  AND block_number = $2
			  AND block_hash IS NOT NULL
			  AND block_hash <> $3
			  AND reorged = 0
		`, chainID, blockNumber, canonicalBlockHash)
		if err != nil {
			return 0, fmt.Errorf("mark market block reorged: %w", err)
		}
		if tag.RowsAffected() > 0 || replacedBlock {
			if err := rewindMarketScanCoverage(ctx, tx, chainID, "", blockNumber-1); err != nil {
				return 0, err
			}
		}
		if tag.RowsAffected() > 0 {
			if _, err := tx.Exec(ctx, `
				UPDATE market_rule_events mre
				SET notification_status='skipped',delivery_outcome=CASE WHEN notification_send_started_at IS NOT NULL OR(notification_lease_token=0 AND(notification_status='sending' OR(notification_status IN('pending','failed') AND delivery_outcome=''))) THEN 'unknown' ELSE 'cancelled' END,
 notification_error=LEFT(CASE WHEN notification_error='' THEN 'source event was removed by a chain reorganization' ELSE notification_error||'; source event was removed by a chain reorganization' END,2000)
				FROM market_events me
				WHERE me.id = mre.market_event_id
				  AND me.chain_id = $1
				  AND me.block_number = $2
				  AND me.reorged = 1
				  AND mre.notification_status IN ('pending', 'sending', 'failed', 'staged', 'combined') AND mre.delivery_outcome<>'unknown'
			`, chainID, blockNumber); err != nil {
				return 0, fmt.Errorf("skip reorged market notifications: %w", err)
			}
		}
		if err := retireMarketTradeReceipts(ctx, tx, chainID, blockNumber, &blockNumber, canonicalBlockHash); err != nil {
			return 0, err
		}
		if err := reconcileReorgedFourMemeTokens(ctx, tx, chainID, blockNumber, &blockNumber); err != nil {
			return 0, err
		}
		return tag.RowsAffected(), nil
	})
}

func (s *Store) CreateMarketRuleEvent(
	ctx context.Context,
	params CreateMarketRuleEventParams,
) (MarketRuleEvent, bool, error) {
	return createMarketRuleEvent(ctx, s.db, params)
}

// WithMarketRuleEvaluationWrite only wraps persistence. Provider calls,
// read-only compact statistics and pure evaluation must finish before it.
func (s *Store) WithMarketRuleEvaluationWrite(ctx context.Context, ruleID int64, write func(*Store) error) error {
	if ruleID <= 0 || write == nil {
		return ErrInvalidMarketRuleEvent
	}
	_, err := withMarketDeliveryTx(ctx, s.db, func(tx DBTX) (struct{}, error) {
		var owner string
		if err := tx.QueryRow(ctx, `SELECT debox_user_id FROM market_rules WHERE id=$1`, ruleID).Scan(&owner); err != nil {
			if isNoRows(err) {
				return struct{}{}, ErrMarketRuleEvaluationInactive
			}
			return struct{}{}, err
		}
		// Match lifecycle/configuration lock order before taking rule/project
		// rows. A stopped rule must not consume a cursor even with no trigger.
		if err := lockUser(ctx, tx, owner); err != nil {
			return struct{}{}, err
		}
		if bounded, ok := tx.(*marketDeliveryBudgetDB); ok {
			bounded.owner = owner
			bounded.ruleID = ruleID
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtext('notification-recovery-boundary'))`); err != nil {
			return struct{}{}, err
		}
		var id int64
		if err := tx.QueryRow(ctx, `SELECT mr.id FROM market_rules mr JOIN market_projects mp ON mp.id=mr.market_project_id
		 WHERE mr.id=$1 AND mr.enabled=1 AND mr.run_status='active' AND mp.status='active' FOR UPDATE OF mr,mp`, ruleID).Scan(&id); err != nil {
			if isNoRows(err) {
				return struct{}{}, ErrMarketRuleEvaluationInactive
			}
			return struct{}{}, err
		}
		return struct{}{}, write(newWithDB(tx))
	})
	return err
}

// CreateMarketRuleEventAndEnqueue commits a logical trigger and all of its
// market delivery associations together. Replaying an existing trigger repairs
// missing associations without incrementing an existing link a second time.
func (s *Store) CreateMarketRuleEventAndEnqueue(ctx context.Context, params CreateMarketRuleEventParams) (MarketRuleEvent, bool, error) {
	type result struct {
		Event   MarketRuleEvent
		Created bool
	}
	value, err := withMarketDeliveryTx(ctx, s.db, func(tx DBTX) (result, error) {
		if err := lockMarketRuleNotificationOwner(ctx, tx, params.MarketRuleID); err != nil {
			return result{}, err
		}
		event, created, err := createMarketRuleEvent(ctx, tx, params)
		if err != nil {
			return result{}, err
		}
		if err := enqueueMarketRuleEvent(ctx, tx, event); err != nil {
			return result{}, err
		}
		event, err = collectOne[MarketRuleEvent](ctx, tx, `SELECT `+marketRuleEventColumns+` FROM market_rule_events WHERE id=$1`, event.ID)
		if err != nil {
			return result{}, err
		}
		return result{Event: event, Created: created}, nil
	})
	return value.Event, value.Created, err
}

func createMarketRuleEvent(ctx context.Context, db DBTX, params CreateMarketRuleEventParams) (MarketRuleEvent, bool, error) {
	params.TriggerKey = strings.TrimSpace(params.TriggerKey)
	params.NotificationStatus = normalizeInitialMarketNotificationStatus(
		params.NotificationStatus,
	)
	if params.MarketRuleID <= 0 || params.MarketEventID <= 0 || params.TriggerKey == "" {
		return MarketRuleEvent{}, false, ErrInvalidMarketRuleEvent
	}
	cutoff, err := marketResourceNotificationCutoff(ctx, db, params.MarketRuleID, false)
	if err != nil {
		return MarketRuleEvent{}, false, err
	}
	if cutoff != nil && params.NotificationStatus != "skipped" {
		var occurredAt time.Time
		if err := db.QueryRow(ctx, `SELECT occurred_at FROM market_events WHERE id=$1`, params.MarketEventID).Scan(&occurredAt); err != nil {
			return MarketRuleEvent{}, false, err
		}
		if occurredAt.Before(*cutoff) {
			params.NotificationStatus = "skipped"
			params.NotificationError = "source precedes recorded recovery cutoff"
		}
	}
	value, err := collectOne[MarketRuleEvent](ctx, db, `
		INSERT INTO market_rule_events (
			market_rule_id, market_event_id, trigger_key,
			previous_value, current_value, note, details,
			notification_status, delivery_outcome, notification_error,notification_route
		)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8,
		 CASE WHEN $8='skipped' THEN 'cancelled' ELSE 'not_attempted' END,$9,
		 jsonb_build_object('chat_id',mr.notification_chat_id,'chat_type',mr.notification_chat_type)
		FROM market_rules mr
		JOIN market_projects mp ON mp.id = mr.market_project_id
		JOIN market_events me ON me.id = $2
		WHERE mr.id = $1
		  AND mr.enabled = 1 AND mr.run_status = 'active' AND mp.status = 'active'
		  AND (
			(mp.chain_id = me.chain_id AND mp.token_address = me.token_address)
			OR EXISTS (
				SELECT 1
				FROM market_project_deployments mpd
				JOIN market_asset_deployments mad
				  ON mad.id = mpd.market_asset_deployment_id
				WHERE mpd.market_project_id = mp.id
				  AND mpd.status = 'active'
				  AND mad.chain_id = me.chain_id
				  AND mad.token_address = me.token_address
				  AND (
					mr.deployment_scope = 'all'
					OR EXISTS (
						SELECT 1
						FROM market_rule_deployments mrd
						WHERE mrd.market_rule_id = mr.id
						  AND mrd.market_project_deployment_id = mpd.id
					)
				  )
			)
		  )
		  AND me.reorged = 0
		  AND me.confirmed = 1
		  AND ($8='skipped' OR (`+marketDeliveryEventValidSQL("($7::jsonb)", "me")+`))
		ON CONFLICT DO NOTHING
		RETURNING `+marketRuleEventColumns,
		params.MarketRuleID,
		params.MarketEventID,
		params.TriggerKey,
		params.PreviousValue,
		params.CurrentValue,
		truncate(params.Note, 2000),
		normalizedJSON(params.Details),
		params.NotificationStatus,
		truncate(params.NotificationError, 2000),
	)
	if err == nil {
		return value, true, nil
	}
	if !isNoRows(err) {
		return MarketRuleEvent{}, false, fmt.Errorf("create market rule event: %w", err)
	}
	existing, err := collectOptional[MarketRuleEvent](ctx, db, `
		SELECT `+marketRuleEventColumns+`
		FROM market_rule_events
		WHERE market_rule_id = $1
		  AND (market_event_id = $2 OR trigger_key = $3)
		LIMIT 1
	`, params.MarketRuleID, params.MarketEventID, params.TriggerKey)
	if err != nil {
		return MarketRuleEvent{}, false, fmt.Errorf("get duplicate market rule event: %w", err)
	}
	if existing == nil {
		return MarketRuleEvent{}, false, ErrInvalidMarketRuleEvent
	}
	if params.NotificationStatus != "skipped" && existing.MarketEventID == params.MarketEventID && existing.NotificationStatus == "skipped" {
		revived, err := reviveUnsentReorgedMarketRuleEvent(ctx, db, *existing, params, cutoff)
		if err != nil {
			return MarketRuleEvent{}, false, err
		}
		if revived != nil {
			return *revived, false, nil
		}
	}
	return *existing, false, nil
}

func reviveUnsentReorgedMarketRuleEvent(ctx context.Context, db DBTX, existing MarketRuleEvent, params CreateMarketRuleEventParams, cutoff *time.Time) (*MarketRuleEvent, error) {
	// Only a newly verified trade-ready revision may recover a never-attempted,
	// never-prepared source cancelled by reorg. Other skipped decisions and all
	// possibly delivered/frozen records remain immutable.
	return collectOptional[MarketRuleEvent](ctx, db, `UPDATE market_rule_events mre SET
	 notification_status=$2,delivery_outcome='not_attempted',notification_error='',notification_source_reconciled=FALSE,
	 previous_value=$4,current_value=$5,note=$6,details=$3::jsonb,
	 notification_source_audit=(SELECT COALESCE(jsonb_agg(value ORDER BY ord),'[]'::jsonb)
	  FROM jsonb_array_elements(mre.notification_source_audit||jsonb_build_array(jsonb_build_object(
	   'at',NOW(),'previous_reason',mre.notification_error,'reason','canonical complete trade revision reconfirmed before delivery',
	   'previous_ready_id',mre.details#>'{trade_projection,ready_id}','ready_id',$3::jsonb#>'{trade_projection,ready_id}'))) WITH ORDINALITY item(value,ord)
	  WHERE ord>GREATEST(0,jsonb_array_length(mre.notification_source_audit)+1-16))
	 FROM market_events me,market_rules mr,market_projects mp
	 WHERE mre.id=$1 AND mre.market_event_id=me.id AND mr.id=mre.market_rule_id AND mp.id=mr.market_project_id
	  AND mre.notification_status='skipped' AND mre.delivery_outcome IN('','cancelled')
	  AND RIGHT(mre.notification_error,LENGTH('source event was removed by a chain reorganization'))='source event was removed by a chain reorganization'
	  AND mre.notification_payload='{}'::jsonb AND mre.notification_snapshot_id IS NULL
	  AND mre.details ? 'trade_projection'
	  AND (`+marketDeliveryRouteValidSQL("realtime", "mre")+`)
	  AND mre.notification_send_started_at IS NULL AND mre.notification_message_id IS NULL AND mre.notification_attempts=0
	  AND NOT EXISTS(SELECT 1 FROM market_stage_window_events l JOIN market_stage_windows w ON w.id=l.market_stage_window_id
	    WHERE l.market_rule_event_id=mre.id AND(w.closed_at IS NOT NULL OR w.ends_at<=NOW() OR w.notification_payload<>'{}'::jsonb
	      OR w.notification_send_started_at IS NOT NULL OR w.notification_status IN('sent','sending','skipped') OR w.delivery_outcome='unknown'
	      OR NOT COALESCE((`+marketDeliveryRouteValidSQL("stage", "w")+`),FALSE)))
	  AND NOT EXISTS(SELECT 1 FROM market_combination_trigger_events l JOIN market_combination_windows w ON w.id=l.market_combination_window_id
	    WHERE l.source_type='market' AND l.market_rule_event_id=mre.id AND(w.closed_at IS NOT NULL OR w.ends_at<=NOW() OR w.notification_payload<>'{}'::jsonb
	      OR w.notification_send_started_at IS NOT NULL OR w.notification_status IN('sent','sending','skipped') OR w.delivery_outcome='unknown'
	      OR NOT COALESCE((`+marketDeliveryRouteValidSQL("combination", "w")+`),FALSE)))
	  AND (mre.notification_attempted_at IS NULL OR mre.notification_lease_token>0)
	  AND mr.enabled=1 AND mr.run_status='active' AND mp.status='active'
	  AND ($7::timestamptz IS NULL OR (me.occurred_at>=$7 AND mre.created_at>=$7))
	  AND ($3::jsonb ? 'trade_projection') AND (`+marketDeliveryEventValidSQL("($3::jsonb)", "me")+`)
	 RETURNING `+qualifiedMarketRuleEventColumns("mre"),
		existing.ID, params.NotificationStatus, normalizedJSON(params.Details), params.PreviousValue, params.CurrentValue, truncate(params.Note, 2000), cutoff)
}

func qualifiedMarketRuleEventColumns(alias string) string {
	parts := strings.Split(marketRuleEventColumns, ",")
	for i, part := range parts {
		parts[i] = alias + "." + strings.TrimSpace(part)
	}
	return strings.Join(parts, ",")
}

func normalizeInitialMarketNotificationStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "staged":
		return "staged"
	case "combined":
		return "combined"
	case "skipped":
		return "skipped"
	default:
		return "pending"
	}
}

func (s *Store) UpdateMarketRuleEventNotification(
	ctx context.Context,
	ruleEventID int64,
	status string,
	messageID *string,
	notificationError string,
) (MarketRuleEvent, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	switch status {
	case "pending", "sending", "sent", "failed", "skipped":
	default:
		return MarketRuleEvent{}, ErrInvalidNotificationStatus
	}
	value, err := collectOne[MarketRuleEvent](ctx, s.db, `
		UPDATE market_rule_events
		SET notification_status = $1,
		    delivery_outcome = CASE WHEN $1='sent' THEN 'sent' ELSE delivery_outcome END,
		    notification_message_id = COALESCE($2, notification_message_id),
		    notification_error = $3,
		    notification_attempts = notification_attempts + 1,
		    notification_attempted_at = NOW(),
		    notification_sent_at = CASE WHEN $1 = 'sent' THEN NOW() ELSE notification_sent_at END
		WHERE id = $4 AND notification_lease_token=0 AND delivery_outcome<>'unknown'
		  AND notification_status<>'sent'
		RETURNING `+marketRuleEventColumns,
		status,
		messageID,
		truncate(notificationError, 2000),
		ruleEventID,
	)
	if isNoRows(err) {
		return MarketRuleEvent{}, ErrNotFound
	}
	if err != nil {
		return MarketRuleEvent{}, fmt.Errorf("update market rule event notification: %w", err)
	}
	return value, nil
}

func normalizeMarketRuleParams(params *CreateMarketRuleParams) {
	params.DeBoxUserID = strings.TrimSpace(params.DeBoxUserID)
	params.DeploymentScope = strings.ToLower(strings.TrimSpace(params.DeploymentScope))
	if params.DeploymentScope != "selected" {
		params.DeploymentScope = "all"
	}
	params.PoolScope = strings.ToLower(strings.TrimSpace(params.PoolScope))
	switch params.PoolScope {
	case "primary", "all", "selected":
	default:
		if params.MarketPoolID != nil || len(params.MarketProjectPoolIDs) > 0 {
			params.PoolScope = "selected"
		} else {
			params.PoolScope = "primary"
		}
	}
	params.CooldownScope = strings.ToLower(strings.TrimSpace(params.CooldownScope))
	if params.CooldownScope != "project" {
		params.CooldownScope = "chain"
	}
	params.RuleType = strings.ToLower(strings.TrimSpace(params.RuleType))
	if strings.TrimSpace(params.ThresholdValue) == "" {
		params.ThresholdValue = "0"
	}
	params.ThresholdUnit = strings.ToLower(strings.TrimSpace(params.ThresholdUnit))
	switch params.ThresholdUnit {
	case "usd", "token", "percent", "ratio", "count", "progress":
	default:
		params.ThresholdUnit = "usd"
	}
	params.Sensitivity = strings.ToLower(strings.TrimSpace(params.Sensitivity))
	switch params.Sensitivity {
	case "sensitive", "stable", "custom":
	default:
		params.Sensitivity = "balanced"
	}
	if params.CooldownSeconds < 0 {
		params.CooldownSeconds = 0
	}
	params.RuleScope = normalizeRuleScope(params.RuleScope)
	params.DeliveryMode = normalizeDeliveryMode(params.DeliveryMode)
	params.CycleType = normalizeCycleType(params.CycleType)
	if params.CycleMinutes <= 0 {
		params.CycleMinutes = 60
	}
	if params.TriggerCountThreshold <= 0 {
		params.TriggerCountThreshold = 1
	}
	params.NotificationChatType = strings.ToLower(strings.TrimSpace(params.NotificationChatType))
	if params.NotificationChatType != "group" {
		params.NotificationChatType = "private"
	}
	params.NotificationChatID = strings.TrimSpace(params.NotificationChatID)
	if params.NotificationChatID == "" {
		params.NotificationChatID = params.DeBoxUserID
	}
	params.NotificationLanguage = normalizeLanguage(params.NotificationLanguage)
}

func normalizeMarketEventParams(params *CreateMarketEventParams) error {
	params.ChainKey = strings.ToLower(strings.TrimSpace(params.ChainKey))
	params.EventType = strings.ToLower(strings.TrimSpace(params.EventType))
	params.EventKey = strings.TrimSpace(params.EventKey)
	params.Source = strings.ToLower(strings.TrimSpace(params.Source))
	tokenAddress, err := normalizeMarketAddress(params.TokenAddress)
	if err != nil {
		return ErrInvalidMarketEvent
	}
	params.TokenAddress = tokenAddress
	if params.ChainKey == "" || params.ChainID <= 0 ||
		params.EventType == "" || params.EventKey == "" || params.Source == "" {
		return ErrInvalidMarketEvent
	}
	if params.WalletAddress, err = normalizeOptionalMarketAddress(params.WalletAddress); err != nil {
		return ErrInvalidMarketEvent
	}
	for _, value := range []*string{params.TransactionHash, params.BlockHash} {
		if value != nil {
			*value = strings.ToLower(strings.TrimSpace(*value))
			if len(*value) != 66 || !strings.HasPrefix(*value, "0x") {
				return ErrInvalidMarketEvent
			}
		}
	}
	if strings.TrimSpace(params.Confidence) == "" {
		params.Confidence = "1"
	}
	params.OccurredAt = params.OccurredAt.UTC()
	if params.OccurredAt.IsZero() {
		params.OccurredAt = time.Now().UTC()
	}
	return nil
}
