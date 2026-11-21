package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// DisableDailySummarySettings changes only enabled flags on the owner's exact
// active subscription and its existing targets. It never inserts, deletes, or
// resets a target, a schedule, or a delivery watermark.
func (s *Store) DisableDailySummarySettings(
	ctx context.Context,
	deboxUserID string,
	subscriptionID int64,
) (Subscription, error) {
	deboxUserID = strings.TrimSpace(deboxUserID)
	if deboxUserID == "" || subscriptionID < 1 {
		return Subscription{}, ErrNotFound
	}
	return withTxValue(ctx, s.db, func(tx DBTX) (Subscription, error) {
		subscription, err := collectOne[Subscription](ctx, tx, `
			UPDATE subscriptions
			SET daily_summary_enabled = 0
			WHERE id = $1 AND debox_user_id = $2
			  AND status = 'active'
			  AND (is_permanent = 1 OR expires_at > NOW())
			RETURNING `+subscriptionColumns, subscriptionID, deboxUserID)
		if isNoRows(err) {
			return Subscription{}, ErrNotFound
		}
		if err != nil {
			return Subscription{}, fmt.Errorf("disable daily summary subscription: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE daily_summary_targets
			SET enabled = 0
			WHERE subscription_id = $1
			  AND EXISTS (
				SELECT 1 FROM subscriptions
				WHERE id = $1 AND debox_user_id = $2
			  )
		`, subscriptionID, deboxUserID); err != nil {
			return Subscription{}, fmt.Errorf("disable existing daily summary targets: %w", err)
		}
		if _, err := cancelInvalidDailySummaryDispatches(ctx, tx, deboxUserID, time.Now().UTC()); err != nil {
			return Subscription{}, fmt.Errorf("invalidate stopped daily summaries: %w", err)
		}
		return subscription, nil
	})
}
