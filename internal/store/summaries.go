package store

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type DailySummarySettings struct {
	Enabled      bool
	PushTime     string
	TimezoneName string
	ChatType     string
	ChatID       string
	Label        string
	Language     string
	Targets      []DailySummaryTarget
}

func (s *Store) UpdateDailySummarySettings(
	ctx context.Context,
	deboxUserID string,
	settings DailySummarySettings,
) (Subscription, error) {
	return withTxValue(ctx, s.db, func(tx DBTX) (Subscription, error) {
		enabled := int32(0)
		if settings.Enabled {
			enabled = 1
		}
		subscription, err := collectOne[Subscription](ctx, tx, `
			UPDATE subscriptions
			SET daily_summary_enabled = $1,
			    daily_summary_time = $2,
			    daily_summary_timezone = $3,
			    daily_summary_chat_type = $4,
			    daily_summary_chat_id = $5,
			    daily_summary_label = $6,
			    daily_summary_language = $7
			WHERE id = (
				SELECT id
				FROM subscriptions
				WHERE debox_user_id = $8
				  AND status = 'active'
				  AND (is_permanent = 1 OR expires_at > NOW())
				ORDER BY CASE plan_code WHEN 'professional' THEN 2 WHEN 'standard' THEN 1 ELSE 0 END DESC,
				         is_permanent DESC,
				         expires_at DESC
				LIMIT 1
			)
			RETURNING `+subscriptionColumns,
			enabled,
			settings.PushTime,
			settings.TimezoneName,
			settings.ChatType,
			settings.ChatID,
			settings.Label,
			normalizeLanguage(settings.Language),
			deboxUserID,
		)
		if isNoRows(err) {
			return Subscription{}, ErrNotFound
		}
		if err != nil {
			return Subscription{}, fmt.Errorf("update daily summary settings: %w", err)
		}
		if _, err := tx.Exec(
			ctx,
			"DELETE FROM daily_summary_targets WHERE subscription_id = $1",
			subscription.ID,
		); err != nil {
			return Subscription{}, fmt.Errorf("replace daily summary targets: %w", err)
		}
		targets := settings.Targets
		if settings.Enabled && len(targets) == 0 {
			chatType := settings.ChatType
			chatID := settings.ChatID
			if chatType == "private" {
				chatID = deboxUserID
			}
			if chatID != "" {
				targets = []DailySummaryTarget{{
					SubscriptionID: subscription.ID,
					ChatType:       chatType,
					ChatID:         chatID,
				}}
			}
		}
		for _, target := range targets {
			if _, err := tx.Exec(ctx, `
				INSERT INTO daily_summary_targets (subscription_id, chat_type, chat_id)
				VALUES ($1, $2, $3)
			`, subscription.ID, target.ChatType, target.ChatID); err != nil {
				return Subscription{}, fmt.Errorf("insert daily summary target: %w", err)
			}
		}
		return subscription, nil
	})
}

func (s *Store) ListDailySummaryTargets(
	ctx context.Context,
	subscriptionID int64,
) ([]DailySummaryTarget, error) {
	targets, err := collectMany[DailySummaryTarget](ctx, s.db, `
		SELECT id, subscription_id, chat_type, chat_id, created_at
		FROM daily_summary_targets
		WHERE subscription_id = $1
		ORDER BY CASE WHEN chat_type = 'private' THEN 0 ELSE 1 END, id ASC
	`, subscriptionID)
	if err != nil {
		return nil, fmt.Errorf("list daily summary targets: %w", err)
	}
	return targets, nil
}

func (s *Store) ListPendingDailySummaryTargets(
	ctx context.Context,
	subscriptionID int64,
	periodEnd time.Time,
) ([]DailySummaryTarget, error) {
	targets, err := collectMany[DailySummaryTarget](ctx, s.db, `
		SELECT t.id, t.subscription_id, t.chat_type, t.chat_id, t.created_at
		FROM daily_summary_targets t
		LEFT JOIN daily_summary_deliveries d
		  ON d.subscription_id = t.subscription_id
		 AND d.period_end_at = $2
		 AND d.chat_type = t.chat_type
		 AND d.chat_id = t.chat_id
		WHERE t.subscription_id = $1
		  AND d.id IS NULL
		ORDER BY CASE WHEN t.chat_type = 'private' THEN 0 ELSE 1 END, t.id ASC
	`, subscriptionID, periodEnd.UTC())
	if err != nil {
		return nil, fmt.Errorf("list pending daily summary targets: %w", err)
	}
	return targets, nil
}

func (s *Store) MarkDailySummaryTargetSent(
	ctx context.Context,
	subscriptionID int64,
	periodEnd time.Time,
	target DailySummaryTarget,
) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO daily_summary_deliveries (
			subscription_id, period_end_at, chat_type, chat_id
		)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (subscription_id, period_end_at, chat_type, chat_id)
		DO NOTHING
	`, subscriptionID, periodEnd.UTC(), target.ChatType, target.ChatID)
	if err != nil {
		return fmt.Errorf("mark daily summary target sent: %w", err)
	}
	return nil
}

func (s *Store) ListDueScheduledSubscriptions(
	ctx context.Context,
	afterID int64,
	limit int,
) ([]Subscription, error) {
	subscriptions, err := collectMany[Subscription](ctx, s.db, `
		SELECT `+subscriptionColumns+`
		FROM subscriptions
		WHERE status = 'active'
		  AND (is_permanent = 1 OR expires_at > NOW())
		  AND daily_summary_enabled = 1
		  AND id > $1
		ORDER BY id ASC
		LIMIT $2
	`, max(afterID, 0), clamp(limit, 1, 1000))
	if err != nil {
		return nil, fmt.Errorf("list due scheduled subscriptions: %w", err)
	}
	return subscriptions, nil
}

func (s *Store) GetScheduledSubscription(
	ctx context.Context,
	subscriptionID int64,
) (*Subscription, error) {
	subscription, err := collectOptional[Subscription](ctx, s.db, `
		SELECT `+subscriptionColumns+`
		FROM subscriptions
		WHERE id = $1
		  AND status = 'active'
		  AND (is_permanent = 1 OR expires_at > NOW())
		  AND daily_summary_enabled = 1
		LIMIT 1
	`, subscriptionID)
	if err != nil {
		return nil, fmt.Errorf("get scheduled subscription: %w", err)
	}
	return subscription, nil
}

type SummaryLock struct {
	conn           *pgxpool.Conn
	subscriptionID int64
	once           sync.Once
}

func (s *Store) TryScheduledSummaryLock(
	ctx context.Context,
	subscriptionID int64,
) (*SummaryLock, bool, error) {
	if s.pool == nil {
		return nil, false, ErrPoolRequired
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("acquire scheduled summary connection: %w", err)
	}
	var acquired bool
	if err := conn.QueryRow(
		ctx,
		"SELECT pg_try_advisory_lock($1, $2)",
		summaryLockNamespace,
		int32(subscriptionID),
	).Scan(&acquired); err != nil {
		conn.Release()
		return nil, false, fmt.Errorf("try scheduled summary lock: %w", err)
	}
	if !acquired {
		conn.Release()
		return nil, false, nil
	}
	return &SummaryLock{
		conn:           conn,
		subscriptionID: subscriptionID,
	}, true, nil
}

func (lock *SummaryLock) Unlock(ctx context.Context) (err error) {
	lock.once.Do(func() {
		baseContext := context.Background()
		if ctx != nil {
			baseContext = context.WithoutCancel(ctx)
		}
		unlockContext, cancel := context.WithTimeout(baseContext, 5*time.Second)
		defer cancel()

		var unlocked bool
		if scanErr := lock.conn.QueryRow(
			unlockContext,
			"SELECT pg_advisory_unlock($1, $2)",
			summaryLockNamespace,
			int32(lock.subscriptionID),
		).Scan(&unlocked); scanErr != nil {
			rawConn := lock.conn.Hijack()
			closeContext, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer closeCancel()
			_ = rawConn.Close(closeContext)
			err = fmt.Errorf("unlock scheduled summary: %w", scanErr)
			return
		}
		lock.conn.Release()
		if !unlocked {
			err = fmt.Errorf("unlock scheduled summary: lock was not held")
		}
	})
	return err
}

func (s *Store) MarkScheduledPushSent(
	ctx context.Context,
	subscriptionID int64,
	sentDate string,
	periodEnd time.Time,
) error {
	_, err := s.db.Exec(ctx, `
		UPDATE subscriptions
		SET daily_summary_last_sent_date = $1,
		    scheduled_push_last_sent_at = NOW(),
		    daily_summary_last_period_end_at = $2
		WHERE id = $3
	`, sentDate, periodEnd, subscriptionID)
	if err != nil {
		return fmt.Errorf("mark scheduled push sent: %w", err)
	}
	if _, err := s.db.Exec(ctx, `
		DELETE FROM daily_summary_deliveries
		WHERE subscription_id = $1 AND period_end_at < $2
	`, subscriptionID, periodEnd.AddDate(0, 0, -35)); err != nil {
		return fmt.Errorf("prune daily summary deliveries: %w", err)
	}
	return nil
}
