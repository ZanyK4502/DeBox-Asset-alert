package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s *Store) ExpireActiveSubscriptions(ctx context.Context) (int64, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (int64, error) {
		return expireSubscriptions(ctx, tx, "")
	})
}

func (s *Store) GetActiveSubscription(ctx context.Context, deboxUserID string) (*Subscription, error) {
	if _, err := s.ExpireActiveSubscriptions(ctx); err != nil {
		return nil, err
	}
	subscription, err := collectOptional[Subscription](ctx, s.db, `
		SELECT `+subscriptionColumns+`
		FROM subscriptions
		WHERE debox_user_id = $1
		  AND status = 'active'
		  AND (is_permanent = 1 OR expires_at > NOW())
		ORDER BY CASE plan_code WHEN 'professional' THEN 2 WHEN 'standard' THEN 1 ELSE 0 END DESC,
		         is_permanent DESC,
		         expires_at DESC
		LIMIT 1
	`, deboxUserID)
	if err != nil {
		return nil, fmt.Errorf("get active subscription: %w", err)
	}
	return subscription, nil
}

func (s *Store) HasUsedPlan(ctx context.Context, deboxUserID, planCode string) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM subscriptions
			WHERE debox_user_id = $1 AND plan_code = $2
		)
	`, deboxUserID, planCode).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check used plan: %w", err)
	}
	return exists, nil
}

func (s *Store) HasPaidSubscriptionHistory(ctx context.Context, deboxUserID string) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM subscriptions
			WHERE debox_user_id = $1
			  AND plan_code <> 'free'
			  AND is_permanent = 0
		)
	`, deboxUserID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check paid subscription history: %w", err)
	}
	return exists, nil
}

func (s *Store) ActivateSubscription(
	ctx context.Context,
	deboxUserID string,
	planCode string,
	days int,
) (Subscription, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (Subscription, error) {
		return activateSubscription(ctx, tx, deboxUserID, planCode, days, true)
	})
}

func activateSubscription(
	ctx context.Context,
	db DBTX,
	deboxUserID string,
	planCode string,
	days int,
	allowRenewal bool,
) (Subscription, error) {
	if _, err := db.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", deboxUserID); err != nil {
		return Subscription{}, fmt.Errorf("lock subscription: %w", err)
	}
	if _, err := expireSubscriptions(ctx, db, deboxUserID); err != nil {
		return Subscription{}, err
	}

	active, err := collectOptional[Subscription](ctx, db, `
		SELECT `+subscriptionColumns+`
		FROM subscriptions
		WHERE debox_user_id = $1
		  AND status = 'active'
		  AND is_permanent = 0
		  AND expires_at > NOW()
		ORDER BY expires_at DESC
		LIMIT 1
		FOR UPDATE
	`, deboxUserID)
	if err != nil {
		return Subscription{}, fmt.Errorf("select active subscription: %w", err)
	}
	if active == nil {
		var paidHistory bool
		if err := db.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM subscriptions
				WHERE debox_user_id = $1
				  AND plan_code <> 'free'
				  AND is_permanent = 0
			)
		`, deboxUserID).Scan(&paidHistory); err != nil {
			return Subscription{}, fmt.Errorf("check prior paid subscription: %w", err)
		}
		if paidHistory {
			var freeWatchRuleID *int64
			if err := db.QueryRow(ctx, `
				SELECT (
					SELECT free_watch_rule_id
					FROM user_preferences
					WHERE debox_user_id = $1
				)
			`, deboxUserID).Scan(&freeWatchRuleID); err != nil {
				return Subscription{}, fmt.Errorf("read free fallback rule: %w", err)
			}
			if err := pauseExpiredUserResources(
				ctx,
				db,
				deboxUserID,
				freeWatchRuleID,
			); err != nil {
				return Subscription{}, err
			}
		}
	}
	if active != nil && active.PlanCode == planCode && allowRenewal {
		subscription, err := collectOne[Subscription](ctx, db, `
			UPDATE subscriptions
			SET expires_at = expires_at + make_interval(days => $1)
			WHERE id = $2
			RETURNING `+subscriptionColumns,
			days,
			active.ID,
		)
		if err != nil {
			return Subscription{}, fmt.Errorf("renew subscription: %w", err)
		}
		return subscription, nil
	}
	if active != nil && active.PlanCode == "free" && planCode != "free" {
		if _, err := db.Exec(ctx,
			"UPDATE subscriptions SET status = 'upgraded' WHERE id = $1",
			active.ID,
		); err != nil {
			return Subscription{}, fmt.Errorf("upgrade free subscription: %w", err)
		}
	} else if active != nil {
		return Subscription{}, ErrActiveSubscriptionConflict
	}

	start := time.Now().UTC()
	subscription, err := collectOne[Subscription](ctx, db, `
		INSERT INTO subscriptions (
			debox_user_id, plan_code, status, starts_at, expires_at,
			daily_summary_enabled, daily_summary_chat_type,
			daily_summary_chat_id, daily_summary_label
		)
		VALUES ($1, $2, 'active', $3, $4, 0, 'private', $1, '私聊摘要')
		RETURNING `+subscriptionColumns,
		deboxUserID,
		planCode,
		start,
		start.AddDate(0, 0, days),
	)
	if err != nil {
		return Subscription{}, fmt.Errorf("insert subscription: %w", err)
	}
	return subscription, nil
}

func expireSubscriptions(ctx context.Context, db DBTX, deboxUserID string) (int64, error) {
	rows, err := db.Query(ctx, `
		SELECT id
		FROM subscriptions
		WHERE status = 'active'
		  AND is_permanent = 0
		  AND expires_at < NOW()
		  AND ($1 = '' OR debox_user_id = $1)
		ORDER BY id
		FOR UPDATE
	`, deboxUserID)
	if err != nil {
		return 0, fmt.Errorf("list expired subscriptions: %w", err)
	}
	subscriptionIDs, err := collectInt64Rows(rows)
	if err != nil {
		return 0, fmt.Errorf("read expired subscriptions: %w", err)
	}
	if len(subscriptionIDs) == 0 {
		return 0, nil
	}
	if _, err := db.Exec(ctx, `
		DELETE FROM daily_summary_deliveries
		WHERE subscription_id = ANY($1::bigint[])
	`, subscriptionIDs); err != nil {
		return 0, fmt.Errorf("delete expired daily summary deliveries: %w", err)
	}
	if _, err := db.Exec(ctx, `
		DELETE FROM daily_summary_targets
		WHERE subscription_id = ANY($1::bigint[])
	`, subscriptionIDs); err != nil {
		return 0, fmt.Errorf("delete expired daily summary targets: %w", err)
	}
	tag, err := db.Exec(ctx, `
		UPDATE subscriptions
		SET status = 'expired',
		    daily_summary_enabled = 0,
		    daily_summary_time = '20:00',
		    daily_summary_timezone = 'Asia/Shanghai',
		    daily_summary_chat_type = 'private',
		    daily_summary_chat_id = debox_user_id,
		    daily_summary_label = '私聊摘要',
		    daily_summary_language = 'zh',
		    daily_summary_last_sent_date = '',
		    scheduled_push_last_sent_at = NULL,
		    daily_summary_last_period_end_at = NULL
		WHERE id = ANY($1::bigint[])
	`, subscriptionIDs)
	if err != nil {
		return 0, fmt.Errorf("expire subscriptions: %w", err)
	}
	return tag.RowsAffected(), nil
}

type PermanentPlanBinding struct {
	Subscription        *Subscription `json:"subscription"`
	PreviousDeBoxUserID string        `json:"previous_debox_user_id,omitempty"`
	Changed             bool          `json:"changed"`
}

func (s *Store) BindPermanentPlan(
	ctx context.Context,
	deboxUserID string,
	walletAddress string,
) (PermanentPlanBinding, error) {
	deboxUserID = strings.TrimSpace(deboxUserID)
	walletAddress = strings.ToLower(strings.TrimSpace(walletAddress))
	return withTxValue(ctx, s.db, func(tx DBTX) (PermanentPlanBinding, error) {
		lockKey := "permanent-plan:" + walletAddress
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", lockKey); err != nil {
			return PermanentPlanBinding{}, fmt.Errorf("lock permanent plan: %w", err)
		}
		if err := lockUser(ctx, tx, deboxUserID); err != nil {
			return PermanentPlanBinding{}, err
		}

		var planCode string
		var boundUserID *string
		var subscriptionID *int64
		previousDeBoxUserID := ""
		err := tx.QueryRow(ctx, `
			SELECT plan_code, debox_user_id, subscription_id
			FROM permanent_plan_allowlist
			WHERE wallet_address = $1
			FOR UPDATE
		`, walletAddress).Scan(&planCode, &boundUserID, &subscriptionID)
		if isNoRows(err) {
			return PermanentPlanBinding{}, nil
		}
		if err != nil {
			return PermanentPlanBinding{}, fmt.Errorf("read permanent plan allowlist: %w", err)
		}

		if boundUserID != nil && *boundUserID != "" && *boundUserID != deboxUserID {
			previousDeBoxUserID = *boundUserID
			if subscriptionID != nil {
				if _, err := tx.Exec(ctx,
					"DELETE FROM daily_summary_targets WHERE subscription_id = $1",
					*subscriptionID,
				); err != nil {
					return PermanentPlanBinding{}, fmt.Errorf("remove previous permanent summary targets: %w", err)
				}
				if _, err := tx.Exec(ctx, `
					UPDATE subscriptions
					SET status = 'revoked', daily_summary_enabled = 0
					WHERE id = $1 AND is_permanent = 1
				`, *subscriptionID); err != nil {
					return PermanentPlanBinding{}, fmt.Errorf("revoke previous permanent subscription: %w", err)
				}
			}
			subscriptionID = nil
		}

		if boundUserID != nil && *boundUserID == deboxUserID && subscriptionID != nil {
			existing, err := collectOptional[Subscription](ctx, tx, `
				SELECT `+subscriptionColumns+`
				FROM subscriptions
				WHERE id = $1
				  AND debox_user_id = $2
				  AND is_permanent = 1
				  AND entitlement_wallet_address = $3
				FOR UPDATE
			`, *subscriptionID, deboxUserID, walletAddress)
			if err != nil {
				return PermanentPlanBinding{}, fmt.Errorf("read permanent subscription: %w", err)
			}
			if existing != nil &&
				existing.Status == "active" &&
				existing.PlanCode == planCode &&
				existing.EntitlementSource == "permanent_allowlist" {
				return PermanentPlanBinding{Subscription: existing}, nil
			}
			if existing != nil {
				updated, err := collectOne[Subscription](ctx, tx, `
					UPDATE subscriptions
					SET plan_code = $1,
					    status = 'active',
					    is_permanent = 1,
					    entitlement_source = 'permanent_allowlist',
					    entitlement_wallet_address = $2
					WHERE id = $3
					RETURNING `+subscriptionColumns,
					planCode,
					walletAddress,
					existing.ID,
				)
				if err != nil {
					return PermanentPlanBinding{}, fmt.Errorf("refresh permanent subscription: %w", err)
				}
				return PermanentPlanBinding{Subscription: &updated, Changed: true}, nil
			}
		}

		now := time.Now().UTC()
		subscription, err := collectOne[Subscription](ctx, tx, `
			INSERT INTO subscriptions (
				debox_user_id, plan_code, status, starts_at, expires_at,
				daily_summary_enabled, daily_summary_chat_type,
				daily_summary_chat_id, daily_summary_label,
				is_permanent, entitlement_source, entitlement_wallet_address
			)
			VALUES (
				$1, $2, 'active', $3, $3,
				0, 'private', $1, '私聊摘要',
				1, 'permanent_allowlist', $4
			)
			RETURNING `+subscriptionColumns,
			deboxUserID,
			planCode,
			now,
			walletAddress,
		)
		if err != nil {
			return PermanentPlanBinding{}, fmt.Errorf("create permanent subscription: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE permanent_plan_allowlist
			SET debox_user_id = $1,
			    subscription_id = $2,
			    bound_at = NOW(),
			    updated_at = NOW()
			WHERE wallet_address = $3
		`, deboxUserID, subscription.ID, walletAddress); err != nil {
			return PermanentPlanBinding{}, fmt.Errorf("bind permanent plan allowlist: %w", err)
		}
		return PermanentPlanBinding{
			Subscription:        &subscription,
			PreviousDeBoxUserID: previousDeBoxUserID,
			Changed:             true,
		}, nil
	})
}

func IsActiveSubscriptionConflict(err error) bool {
	return errors.Is(err, ErrActiveSubscriptionConflict)
}
