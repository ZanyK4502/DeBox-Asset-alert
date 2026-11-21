package store

import (
	"context"
	"errors"
	"time"
)

// These methods are only for AdmissionDeferred, which proves that transport
// has not been called. They never refund a real rejection or an unknown send.
func (s *Store) DeferWalletNotificationAdmission(ctx context.Context, id int64, token string, retryAt time.Time) (WalletNotificationOutbox, error) {
	if id < 1 || token == "" || retryAt.IsZero() {
		return WalletNotificationOutbox{}, errors.New("invalid wallet admission deferral")
	}
	return notificationAdmissionTx(ctx, s.db, func(ctx context.Context, tx DBTX) (WalletNotificationOutbox, error) {
		row, err := collectOne[WalletNotificationOutbox](ctx, tx, `UPDATE wallet_notification_outbox SET status='retry',attempts=attempts-CASE WHEN send_started_at IS NOT NULL THEN 1 ELSE 0 END,next_attempt_at=GREATEST(next_attempt_at,$3),send_started_at=NULL,lease_token=NULL,lease_expires_at=NULL,last_error='notification_admission_wait' WHERE id=$1 AND status='sending' AND lease_token=$2 AND lease_expires_at>clock_timestamp() AND (send_started_at IS NULL OR attempts>0) RETURNING `+walletNotificationOutboxColumns, id, token, retryAt.UTC())
		if isNoRows(err) {
			return row, ErrWalletObservationStale
		}
		if err != nil {
			return row, err
		}
		if err := mirrorWalletNotification(ctx, tx, row, "failed", nil, row.LastError); err != nil {
			return row, err
		}
		return row, nil
	})
}

// An empty token defers an unclaimed daily job without creating an attempt.
// A token fences the single claimed-but-not-transported attempt being refunded.
func (s *Store) DeferDailySummaryAdmission(ctx context.Context, id int64, token string, retryAt, now time.Time) error {
	if id < 1 || retryAt.IsZero() || now.IsZero() {
		return errors.New("invalid daily summary admission deferral")
	}
	_, err := notificationAdmissionTx(ctx, s.db, func(ctx context.Context, tx DBTX) (struct{}, error) {
		query := `UPDATE daily_summary_dispatches SET retry_at=GREATEST(retry_at,$2),last_error='notification_admission_wait' WHERE id=$1 AND status IN ('pending','failed')`
		args := []any{id, retryAt.UTC()}
		if token != "" {
			query = `UPDATE daily_summary_dispatches SET status='failed',attempts=attempts-1,retry_at=GREATEST(retry_at,$2),last_error='notification_admission_wait',claim_token=NULL,lease_until=NULL WHERE id=$1 AND status='sending' AND claim_token=$3 AND lease_until>$4 AND attempts>0`
			args = append(args, token, now.UTC())
		}
		tag, err := tx.Exec(ctx, query, args...)
		if err == nil && tag.RowsAffected() != 1 {
			err = ErrDailySummaryDispatchConflict
		}
		return struct{}{}, err
	})
	return err
}
