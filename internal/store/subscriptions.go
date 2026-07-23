package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s *Store) ExpireActiveSubscriptions(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE subscriptions
		SET status = 'expired'
		WHERE status = 'active' AND expires_at < NOW()
	`)
	if err != nil {
		return 0, fmt.Errorf("expire active subscriptions: %w", err)
	}
	return tag.RowsAffected(), nil
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
		  AND expires_at > NOW()
		ORDER BY expires_at DESC
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
			WHERE debox_user_id = $1 AND plan_code <> 'free'
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
	if _, err := db.Exec(ctx, `
		UPDATE subscriptions
		SET status = 'expired'
		WHERE debox_user_id = $1 AND status = 'active' AND expires_at < NOW()
	`, deboxUserID); err != nil {
		return Subscription{}, fmt.Errorf("expire user subscriptions: %w", err)
	}

	active, err := collectOptional[Subscription](ctx, db, `
		SELECT `+subscriptionColumns+`
		FROM subscriptions
		WHERE debox_user_id = $1
		  AND status = 'active'
		  AND expires_at > NOW()
		ORDER BY expires_at DESC
		LIMIT 1
		FOR UPDATE
	`, deboxUserID)
	if err != nil {
		return Subscription{}, fmt.Errorf("select active subscription: %w", err)
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

func (s *Store) GetComplimentaryGrant(
	ctx context.Context,
	walletAddress string,
) (*ComplimentaryGrant, error) {
	grant, err := collectOptional[ComplimentaryGrant](ctx, s.db, `
		SELECT `+complimentaryGrantColumns+`
		FROM complimentary_grants
		WHERE LOWER(wallet_address) = LOWER($1)
	`, walletAddress)
	if err != nil {
		return nil, fmt.Errorf("get complimentary grant: %w", err)
	}
	return grant, nil
}

type ComplimentaryActivation struct {
	Subscription Subscription       `json:"subscription"`
	Grant        ComplimentaryGrant `json:"grant"`
}

func (s *Store) ActivateComplimentarySubscription(
	ctx context.Context,
	deboxUserID string,
	walletAddress string,
	planCode string,
	days int,
) (ComplimentaryActivation, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (ComplimentaryActivation, error) {
		lockKey := "complimentary:" + strings.ToLower(walletAddress)
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", lockKey); err != nil {
			return ComplimentaryActivation{}, fmt.Errorf("lock complimentary grant: %w", err)
		}
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM complimentary_grants
				WHERE LOWER(wallet_address) = LOWER($1)
			)
		`, walletAddress).Scan(&exists); err != nil {
			return ComplimentaryActivation{}, fmt.Errorf("check complimentary grant: %w", err)
		}
		if exists {
			return ComplimentaryActivation{}, ErrComplimentaryAlreadyUsed
		}

		subscription, err := activateSubscription(
			ctx,
			tx,
			deboxUserID,
			planCode,
			days,
			false,
		)
		if err != nil {
			return ComplimentaryActivation{}, err
		}
		grant, err := collectOne[ComplimentaryGrant](ctx, tx, `
			INSERT INTO complimentary_grants (
				wallet_address, debox_user_id, plan_code, starts_at, expires_at
			)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING `+complimentaryGrantColumns,
			strings.ToLower(walletAddress),
			deboxUserID,
			planCode,
			subscription.StartsAt,
			subscription.ExpiresAt,
		)
		if err != nil {
			if isUniqueViolation(err) {
				return ComplimentaryActivation{}, ErrComplimentaryAlreadyUsed
			}
			return ComplimentaryActivation{}, fmt.Errorf("insert complimentary grant: %w", err)
		}
		return ComplimentaryActivation{Subscription: subscription, Grant: grant}, nil
	})
}

func IsActiveSubscriptionConflict(err error) bool {
	return errors.Is(err, ErrActiveSubscriptionConflict)
}
