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
}

func (s *Store) UpdateDailySummarySettings(
	ctx context.Context,
	deboxUserID string,
	settings DailySummarySettings,
) (Subscription, error) {
	enabled := int32(0)
	if settings.Enabled {
		enabled = 1
	}
	subscription, err := collectOne[Subscription](ctx, s.db, `
		UPDATE subscriptions
		SET daily_summary_enabled = $1,
		    daily_summary_time = $2,
		    daily_summary_timezone = $3,
		    daily_summary_chat_type = $4,
		    daily_summary_chat_id = $5,
		    daily_summary_label = $6,
		    daily_summary_language = $7
		WHERE debox_user_id = $8 AND status = 'active' AND expires_at > NOW()
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
	return subscription, nil
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
		  AND expires_at > NOW()
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
		  AND expires_at > NOW()
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
	return nil
}
