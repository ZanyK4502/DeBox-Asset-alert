package store

import (
	"context"
	"fmt"
	"strings"
)

type CreateWatchRuleParams struct {
	DeBoxUserID          string
	ChainKey             string
	ChainID              int32
	WalletAddress        string
	TokenAddress         *string
	TargetAddress        *string
	TargetLabel          string
	RuleType             string
	Threshold            string
	NotificationChatID   string
	NotificationChatType string
	NotificationLabel    string
	NotificationLanguage string
	LastValue            *string
}

func (s *Store) CreateWatchRule(
	ctx context.Context,
	params CreateWatchRuleParams,
) (WatchRule, error) {
	rule, err := collectOne[WatchRule](ctx, s.db, `
		INSERT INTO watch_rules (
			debox_user_id, chain_key, chain_id, wallet_address,
			token_address, target_address, target_label, rule_type,
			threshold, notification_chat_id, notification_chat_type,
			notification_label, notification_language, run_status,
			last_value, last_checked_at
		)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12, $13, 'active', $14, NOW()
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
		params.LastValue,
	)
	if err != nil {
		return WatchRule{}, fmt.Errorf("create watch rule: %w", err)
	}
	return rule, nil
}

func (s *Store) DeleteWatchRule(ctx context.Context, ruleID int64, deboxUserID string) (bool, error) {
	tag, err := s.db.Exec(ctx,
		"DELETE FROM watch_rules WHERE id = $1 AND debox_user_id = $2",
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
		WHERE debox_user_id = $1 AND run_status = 'paused'
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
		WHERE id = $2 AND debox_user_id = $3
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
	rule, err := collectOne[WatchRule](ctx, s.db, `
		UPDATE watch_rules
		SET run_status = 'active'
		WHERE id = $1 AND debox_user_id = $2 AND enabled = 1
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
	return rule, nil
}

func (s *Store) CountUserWatchRules(ctx context.Context, deboxUserID string) (int64, error) {
	return queryCount(ctx, s.db, `
		SELECT COUNT(*)
		FROM watch_rules
		WHERE debox_user_id = $1 AND enabled = 1 AND run_status = 'active'
	`, deboxUserID)
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
		WHERE debox_user_id = $1
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
		var eligible bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM watch_rules
				WHERE id = $1
				  AND debox_user_id = $2
				  AND enabled = 1
				  AND notification_chat_type = 'private'
				  AND rule_type IN (
					'balance_change', 'incoming', 'outgoing', 'balance_threshold'
				  )
			)
		`, ruleID, deboxUserID).Scan(&eligible); err != nil {
			return UserPreference{}, fmt.Errorf("check free watch rule: %w", err)
		}
		if !eligible {
			return UserPreference{}, ErrInvalidFreeWatchRule
		}
		if _, err := tx.Exec(ctx, `
			UPDATE watch_rules
			SET run_status = CASE WHEN id = $1 THEN 'active' ELSE 'paused' END
			WHERE debox_user_id = $2 AND enabled = 1
		`, ruleID, deboxUserID); err != nil {
			return UserPreference{}, fmt.Errorf("activate free watch rule: %w", err)
		}
		preferences, err := collectOne[UserPreference](ctx, tx, `
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
	})
}

func (s *Store) PauseUserWatchRules(
	ctx context.Context,
	deboxUserID string,
	exceptRuleID *int64,
) (int64, error) {
	query := `
		UPDATE watch_rules
		SET run_status = 'paused'
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
	return tag.RowsAffected(), nil
}

func (s *Store) ListEnabledWatchRules(ctx context.Context, limit int) ([]WatchRule, error) {
	rules, err := collectMany[WatchRule](ctx, s.db, `
		SELECT `+watchRuleColumnsQualified+`,
		       COALESCE(active_subscription.plan_code, 'free') AS effective_plan_code
		FROM watch_rules wr
		LEFT JOIN LATERAL (
			SELECT sub.plan_code
			FROM subscriptions sub
			WHERE sub.debox_user_id = wr.debox_user_id
			  AND sub.status = 'active'
			  AND sub.expires_at > NOW()
			ORDER BY sub.expires_at DESC
			LIMIT 1
		) active_subscription ON TRUE
		WHERE wr.enabled = 1
		  AND wr.run_status = 'active'
		  AND (
			active_subscription.plan_code <> 'free'
			OR (
			  (
				active_subscription.plan_code = 'free'
				OR NOT EXISTS (
				  SELECT 1 FROM subscriptions history
				  WHERE history.debox_user_id = wr.debox_user_id
				    AND history.plan_code <> 'free'
				)
			  )
			  AND EXISTS (
				SELECT 1
				FROM user_preferences up
				WHERE up.debox_user_id = wr.debox_user_id
				  AND up.free_watch_rule_id = wr.id
			  )
			)
		  )
		ORDER BY wr.last_checked_at NULLS FIRST, wr.id ASC
		LIMIT $1
	`, clamp(limit, 1, 1000))
	if err != nil {
		return nil, fmt.Errorf("list enabled watch rules: %w", err)
	}
	return rules, nil
}

func (s *Store) UpdateWatchRuleValue(
	ctx context.Context,
	ruleID int64,
	value string,
) error {
	_, err := s.db.Exec(ctx, `
		UPDATE watch_rules
		SET last_value = $1, last_checked_at = NOW()
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
