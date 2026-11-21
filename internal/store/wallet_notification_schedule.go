package store

import (
	"context"
	"fmt"
	"time"
)

// NextWalletNotificationAt is a read-only wakeup hint. Each branch reads one
// ordered index head; ClaimWalletNotifications remains the authority for
// eligibility, retries and recovery of an expired sending lease. notBefore
// gates ordinary pending work without delaying lease recovery.
func (s *Store) NextWalletNotificationAt(ctx context.Context, notBefore time.Time) (*time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var at *time.Time
	err := s.db.QueryRow(ctx, `SELECT MIN(next_at) FROM (
  (SELECT GREATEST(pending.next_attempt_at,$1::timestamptz) AS next_at FROM (
    SELECT o.next_attempt_at FROM wallet_notification_outbox o
    WHERE o.status IN ('pending','retry') AND o.attempts<5 AND `+walletOutboxRuleCurrentSQL("o")+`
    ORDER BY o.next_attempt_at,o.id LIMIT 1
   ) pending)
  UNION ALL
  (SELECT o.lease_expires_at AS next_at FROM wallet_notification_outbox o
   WHERE o.status='sending' AND o.lease_expires_at IS NOT NULL
   ORDER BY o.lease_expires_at,o.id LIMIT 1)
 ) heads`, notBefore).Scan(&at)
	if err != nil {
		return nil, fmt.Errorf("read next wallet notification: %w", err)
	}
	return at, nil
}
