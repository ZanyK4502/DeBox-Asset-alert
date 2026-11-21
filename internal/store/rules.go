package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type CreateWatchRuleParams struct {
	DeBoxUserID           string
	ChainKey              string
	ChainID               int32
	WalletAddress         string
	TokenAddress          *string
	TargetAddress         *string
	TargetLabel           string
	RuleType              string
	Threshold             string
	NotificationChatID    string
	NotificationChatType  string
	NotificationLabel     string
	NotificationLanguage  string
	RuleScope             string
	DeliveryMode          string
	CycleType             string
	CycleMinutes          int32
	TriggerCountThreshold int64
	LastValue             *string
	BalanceReadSource     string
	BalanceReadDecimals   *int
	BalanceReadObservedAt *time.Time
}

func (s *Store) CreateWatchRule(
	ctx context.Context,
	params CreateWatchRuleParams,
) (WatchRule, error) {
	if s.resourceCapacity != nil {
		return withTxValue(ctx, s.db, func(tx DBTX) (WatchRule, error) {
			ctx, cancel, err := s.beginResourceCapacityCheck(ctx, tx, params.DeBoxUserID)
			if err != nil {
				return WatchRule{}, err
			}
			defer cancel()
			if err := lockUser(ctx, tx, params.DeBoxUserID); err != nil {
				return WatchRule{}, err
			}
			rule, err := createWatchRule(ctx, tx, params)
			if err != nil {
				return WatchRule{}, err
			}
			if err := s.checkResourceCapacity(ctx, tx); err != nil {
				return WatchRule{}, err
			}
			return rule, nil
		})
	}
	if params.BalanceReadSource != "" || params.BalanceReadDecimals != nil || params.BalanceReadObservedAt != nil {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		return withTxValue(ctx, s.db, func(tx DBTX) (WatchRule, error) {
			if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '2s'`); err != nil {
				return WatchRule{}, err
			}
			return createWatchRule(ctx, tx, params)
		})
	}
	return createWatchRule(ctx, s.db, params)
}

func createWatchRule(
	ctx context.Context,
	db DBTX,
	params CreateWatchRuleParams,
) (WatchRule, error) {
	ruleScope := normalizeRuleScope(params.RuleScope)
	deliveryMode := normalizeDeliveryMode(params.DeliveryMode)
	cycleType := normalizeCycleType(params.CycleType)
	cycleMinutes := params.CycleMinutes
	if cycleMinutes <= 0 {
		cycleMinutes = 60
	}
	triggerCountThreshold := params.TriggerCountThreshold
	if triggerCountThreshold <= 0 {
		triggerCountThreshold = 1
	}
	rule, err := collectOne[WatchRule](ctx, db, `
		INSERT INTO watch_rules (
			debox_user_id, chain_key, chain_id, wallet_address,
			token_address, target_address, target_label, rule_type,
			threshold, notification_chat_id, notification_chat_type,
			notification_label, notification_language, rule_scope,
			delivery_mode, cycle_type, cycle_minutes, trigger_count_threshold,
			aggregation_anchor_at, run_status, last_value, last_checked_at
		)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12, $13, $14, $15, $16, $17, $18,
			CASE WHEN $15 = 'stage' AND $16 = 'fixed' THEN NOW() ELSE NULL END,
			'active', $19, NOW()
		)
		RETURNING `+watchRuleColumns,
		params.DeBoxUserID,
		params.ChainKey,
		params.ChainID,
		params.WalletAddress,
		params.TokenAddress,
		params.TargetAddress,
		params.TargetLabel,
		params.RuleType,
		params.Threshold,
		params.NotificationChatID,
		params.NotificationChatType,
		params.NotificationLabel,
		normalizeLanguage(params.NotificationLanguage),
		ruleScope,
		deliveryMode,
		cycleType,
		cycleMinutes,
		triggerCountThreshold,
		params.LastValue,
	)
	if err != nil {
		return WatchRule{}, fmt.Errorf("create watch rule: %w", err)
	}
	if params.BalanceReadSource != "" || params.BalanceReadDecimals != nil || params.BalanceReadObservedAt != nil {
		value := "0"
		if params.LastValue != nil {
			value = *params.LastValue
		}
		if !walletBalanceEvidenceValid(rule, params.BalanceReadSource, params.BalanceReadDecimals, params.BalanceReadObservedAt, value) {
			return WatchRule{}, ErrWalletBalanceSourceUnavailable
		}
		var now time.Time
		if err := db.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return WatchRule{}, err
		}
		if params.BalanceReadObservedAt.After(now) {
			return WatchRule{}, ErrWalletBalanceSourceUnavailable
		}
		if params.LastValue != nil {
			if rule.LastCheckedAt == nil {
				return WatchRule{}, ErrWalletBalanceSourceUnavailable
			}
			observation := RecordWalletObservationParams{BalanceReadSource: params.BalanceReadSource, BalanceReadDecimals: params.BalanceReadDecimals, ReadObservedAt: params.BalanceReadObservedAt, CurrentValue: *params.LastValue}
			if err := commitWalletBalanceSource(ctx, db, rule, observation, *rule.LastCheckedAt); err != nil {
				return WatchRule{}, err
			}
		}
	}
	return rule, nil
}

func normalizeRuleScope(value string) string {
	if strings.ToLower(strings.TrimSpace(value)) == "combination" {
		return "combination"
	}
	return "standalone"
}

func normalizeDeliveryMode(value string) string {
	if strings.ToLower(strings.TrimSpace(value)) == "stage" {
		return "stage"
	}
	return "realtime"
}

func normalizeCycleType(value string) string {
	if strings.ToLower(strings.TrimSpace(value)) == "follow" {
		return "follow"
	}
	return "fixed"
}

func (s *Store) DeleteWatchRule(ctx context.Context, ruleID int64, deboxUserID string) (bool, error) {
	tag, err := s.db.Exec(ctx,
		"DELETE FROM watch_rules WHERE id = $1 AND debox_user_id = $2 AND rule_scope = 'standalone'",
		ruleID,
		deboxUserID,
	)
	if err != nil {
		return false, fmt.Errorf("delete watch rule: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *Store) DeletePausedWatchRules(ctx context.Context, deboxUserID string) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		DELETE FROM watch_rules
		WHERE debox_user_id = $1 AND run_status = 'paused' AND rule_scope = 'standalone'
	`, deboxUserID)
	if err != nil {
		return 0, fmt.Errorf("delete paused watch rules: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *Store) GetWatchRule(
	ctx context.Context,
	ruleID int64,
	deboxUserID string,
) (*WatchRule, error) {
	rule, err := collectOptional[WatchRule](ctx, s.db, `
		SELECT `+watchRuleColumns+`
		FROM watch_rules
		WHERE id = $1 AND debox_user_id = $2
	`, ruleID, deboxUserID)
	if err != nil {
		return nil, fmt.Errorf("get watch rule: %w", err)
	}
	return rule, nil
}

func (s *Store) UpdateWatchRuleNotificationLanguage(
	ctx context.Context,
	ruleID int64,
	deboxUserID string,
	language string,
) (WatchRule, error) {
	rule, err := collectOne[WatchRule](ctx, s.db, `
		UPDATE watch_rules
		SET notification_language = $1
		WHERE id = $2 AND debox_user_id = $3 AND rule_scope = 'standalone'
		RETURNING `+watchRuleColumns,
		normalizeLanguage(language),
		ruleID,
		deboxUserID,
	)
	if isNoRows(err) {
		return WatchRule{}, ErrNotFound
	}
	if err != nil {
		return WatchRule{}, fmt.Errorf("update watch rule notification language: %w", err)
	}
	return rule, nil
}

func (s *Store) RestoreWatchRule(
	ctx context.Context,
	ruleID int64,
	deboxUserID string,
) (WatchRule, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (WatchRule, error) {
		ctx, cancel, err := s.beginResourceCapacityCheck(ctx, tx, deboxUserID)
		if err != nil {
			return WatchRule{}, err
		}
		defer cancel()
		if s.resourceCapacity != nil {
			if err := lockUser(ctx, tx, deboxUserID); err != nil {
				return WatchRule{}, err
			}
		}
		rule, err := collectOne[WatchRule](ctx, tx, `
			UPDATE watch_rules
			SET run_status = 'active',
			    pause_reason = '',
			    aggregation_anchor_at = CASE
			      WHEN delivery_mode = 'stage' AND cycle_type = 'fixed' THEN NOW()
			      ELSE NULL
			    END
			WHERE id = $1
			  AND debox_user_id = $2
			  AND enabled = 1
			  AND rule_scope = 'standalone'
			RETURNING `+watchRuleColumns,
			ruleID,
			deboxUserID,
		)
		if isNoRows(err) {
			return WatchRule{}, ErrNotFound
		}
		if err != nil {
			return WatchRule{}, fmt.Errorf("restore watch rule: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE aggregation_windows
			SET closed_at = NOW(), updated_at = NOW()
			WHERE watch_rule_id = $1 AND closed_at IS NULL
		`, ruleID); err != nil {
			return WatchRule{}, fmt.Errorf("reset restored watch rule aggregation: %w", err)
		}
		if err := s.checkResourceCapacity(ctx, tx); err != nil {
			return WatchRule{}, err
		}
		return rule, nil
	})
}

func (s *Store) CountUserWatchRules(ctx context.Context, deboxUserID string) (int64, error) {
	return countActiveRuleSlots(ctx, s.db, deboxUserID)
}

func (s *Store) CountUserWallets(ctx context.Context, deboxUserID string) (int64, error) {
	return queryCount(ctx, s.db, `
		SELECT COUNT(DISTINCT LOWER(wallet_address))
		FROM watch_rules
		WHERE debox_user_id = $1 AND enabled = 1 AND run_status = 'active'
	`, deboxUserID)
}

func (s *Store) WalletIsMonitored(
	ctx context.Context,
	deboxUserID string,
	walletAddress string,
) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM watch_rules
			WHERE debox_user_id = $1
			  AND LOWER(wallet_address) = LOWER($2)
			  AND enabled = 1
			  AND run_status = 'active'
		)
	`, deboxUserID, walletAddress).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check monitored wallet: %w", err)
	}
	return exists, nil
}

func (s *Store) ListUserWatchRules(ctx context.Context, deboxUserID string) ([]WatchRule, error) {
	rules, err := collectMany[WatchRule](ctx, s.db, `
		SELECT `+watchRuleColumns+`
		FROM watch_rules
		WHERE debox_user_id = $1 AND rule_scope = 'standalone'
		ORDER BY created_at DESC
	`, deboxUserID)
	if err != nil {
		return nil, fmt.Errorf("list user watch rules: %w", err)
	}
	return rules, nil
}

func (s *Store) GetUserPreferences(ctx context.Context, deboxUserID string) (UserPreference, error) {
	preferences, err := collectOptional[UserPreference](ctx, s.db, `
		SELECT `+userPreferenceColumns+`
		FROM user_preferences
		WHERE debox_user_id = $1
	`, deboxUserID)
	if err != nil {
		return UserPreference{}, fmt.Errorf("get user preferences: %w", err)
	}
	if preferences == nil {
		return UserPreference{
			DeBoxUserID: deboxUserID,
			BotLanguage: "zh",
		}, nil
	}
	preferences.BotLanguage = normalizeLanguage(preferences.BotLanguage)
	return *preferences, nil
}

func (s *Store) SetBotLanguage(
	ctx context.Context,
	deboxUserID string,
	language string,
) (UserPreference, error) {
	preferences, err := collectOne[UserPreference](ctx, s.db, `
		INSERT INTO user_preferences (debox_user_id, bot_language, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (debox_user_id)
		DO UPDATE SET bot_language = EXCLUDED.bot_language, updated_at = NOW()
		RETURNING `+userPreferenceColumns,
		deboxUserID,
		normalizeLanguage(language),
	)
	if err != nil {
		return UserPreference{}, fmt.Errorf("set bot language: %w", err)
	}
	return preferences, nil
}

func (s *Store) SetFreeWatchRule(
	ctx context.Context,
	deboxUserID string,
	ruleID int64,
) (UserPreference, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (UserPreference, error) {
		ctx, cancel, err := s.beginResourceCapacityCheck(ctx, tx, deboxUserID)
		if err != nil {
			return UserPreference{}, err
		}
		defer cancel()
		if err := lockUser(ctx, tx, deboxUserID); err != nil {
			return UserPreference{}, err
		}
		planCode, err := effectivePlanCode(ctx, tx, deboxUserID)
		if err != nil {
			return UserPreference{}, err
		}
		if planCode != "free" {
			return UserPreference{}, ErrSubscriptionChanged
		}
		preferences, err := setFreeWatchRule(ctx, tx, deboxUserID, ruleID)
		if err != nil {
			return UserPreference{}, err
		}
		if err := s.checkResourceCapacity(ctx, tx); err != nil {
			return UserPreference{}, err
		}
		return preferences, nil
	})
}

func setFreeWatchRule(
	ctx context.Context,
	db DBTX,
	deboxUserID string,
	ruleID int64,
) (UserPreference, error) {
	var eligible bool
	if err := db.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM watch_rules
				WHERE id = $1
				  AND debox_user_id = $2
				  AND enabled = 1
				  AND rule_scope = 'standalone'
				  AND notification_chat_type = 'private'
				  AND delivery_mode = 'realtime'
				  AND rule_type IN (
					'balance_change', 'incoming', 'outgoing',
					'balance_threshold', 'balance_threshold_high'
				  )
			)
	`, ruleID, deboxUserID).Scan(&eligible); err != nil {
		return UserPreference{}, fmt.Errorf("check free watch rule: %w", err)
	}
	if !eligible {
		return UserPreference{}, ErrInvalidFreeWatchRule
	}
	if _, err := db.Exec(ctx, `
			UPDATE watch_rules
			SET run_status = CASE WHEN id = $1 THEN 'active' ELSE 'paused' END,
			    pause_reason = CASE WHEN id = $1 THEN '' ELSE 'free_plan' END,
			    notification_generation = CASE WHEN id <> $1 AND run_status='active' THEN notification_generation+1 ELSE notification_generation END
			WHERE debox_user_id = $2 AND enabled = 1
	`, ruleID, deboxUserID); err != nil {
		return UserPreference{}, fmt.Errorf("activate free watch rule: %w", err)
	}
	if _, err := db.Exec(ctx, `
		UPDATE combination_rules
		SET run_status = 'paused', pause_reason = 'free_plan'
		WHERE debox_user_id = $1 AND enabled = 1 AND run_status = 'active'
	`, deboxUserID); err != nil {
		return UserPreference{}, fmt.Errorf("pause free plan combination rules: %w", err)
	}
	if err := closePausedWalletWindows(ctx, db, deboxUserID); err != nil {
		return UserPreference{}, err
	}
	if _, err := cancelInvalidWalletNotifications(ctx, db, deboxUserID, "wallet rule paused after free rule selection changed"); err != nil {
		return UserPreference{}, err
	}
	if err := cancelMarketDeliveriesForUser(ctx, db, deboxUserID, "free rule selection changed before delivery"); err != nil {
		return UserPreference{}, err
	}
	var lifecycleAt time.Time
	if err := db.QueryRow(ctx, `SELECT NOW()`).Scan(&lifecycleAt); err != nil {
		return UserPreference{}, err
	}
	if _, err := cancelInvalidDailySummaryDispatches(ctx, db, deboxUserID, lifecycleAt); err != nil {
		return UserPreference{}, err
	}
	preferences, err := collectOne[UserPreference](ctx, db, `
			INSERT INTO user_preferences (debox_user_id, free_watch_rule_id, updated_at)
			VALUES ($1, $2, NOW())
			ON CONFLICT (debox_user_id)
			DO UPDATE SET free_watch_rule_id = EXCLUDED.free_watch_rule_id, updated_at = NOW()
			RETURNING `+userPreferenceColumns,
		deboxUserID,
		ruleID,
	)
	if err != nil {
		return UserPreference{}, fmt.Errorf("save free watch rule: %w", err)
	}
	return preferences, nil
}

func (s *Store) PauseUserWatchRules(
	ctx context.Context,
	deboxUserID string,
	exceptRuleID *int64,
) (int64, error) {
	query := `
		UPDATE watch_rules
		SET run_status = 'paused', pause_reason = 'subscription_expired'
		WHERE debox_user_id = $1 AND enabled = 1 AND run_status = 'active'
	`
	args := []any{deboxUserID}
	if exceptRuleID != nil {
		query += " AND id <> $2"
		args = append(args, *exceptRuleID)
	}
	tag, err := s.db.Exec(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("pause user watch rules: %w", err)
	}
	if _, err := s.db.Exec(ctx, `
		UPDATE combination_rules
		SET run_status = 'paused', pause_reason = 'subscription_expired'
		WHERE debox_user_id = $1 AND enabled = 1 AND run_status = 'active'
	`, deboxUserID); err != nil {
		return 0, fmt.Errorf("pause user combination rules: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *Store) ListEnabledWatchRules(ctx context.Context, limit int) ([]WatchRule, error) {
	return s.listScheduledWatchRules(ctx, limit, 0)
}

func (s *Store) UpdateWatchRuleValue(
	ctx context.Context,
	ruleID int64,
	value string,
) error {
	_, err := s.db.Exec(ctx, `
		UPDATE watch_rules
		SET last_value = $1, last_checked_at = NOW(), next_observation_at = NULL
		WHERE id = $2
	`, value, ruleID)
	if err != nil {
		return fmt.Errorf("update watch rule value: %w", err)
	}
	return nil
}

func normalizeLanguage(language string) string {
	if strings.ToLower(strings.TrimSpace(language)) == "en" {
		return "en"
	}
	return "zh"
}
