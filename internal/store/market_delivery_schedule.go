package store

import (
	"context"
	"fmt"
	"time"
)

// NextMarketDeliveryAt is a scheduling hint only. Claim and BeginSend remain
// authoritative, including the source, route, entitlement and lease checks.
func (s *Store) NextMarketDeliveryAt(ctx context.Context) (*time.Time, error) {
	return withMarketDeliveryTx(ctx, s.db, func(tx DBTX) (*time.Time, error) {
		cutoff, err := notificationSourceCutoff(ctx, tx)
		if err != nil {
			return nil, err
		}
		var earliest *time.Time
		for _, kind := range []string{"realtime", "stage", "combination"} {
			table, _ := marketDeliveryTable(kind)
			var at *time.Time
			// Match the existing (notification_status,next_attempt_at,id) index
			// with one ordered head per status, rather than sorting their union.
			head := func(status string) string {
				return fmt.Sprintf(`SELECT d.next_attempt_at FROM %s d
 WHERE d.notification_status='%s' AND d.delivery_outcome IN('not_attempted','retryable')
 AND d.notification_attempts<5 AND (%s) AND (%s)
 ORDER BY d.next_attempt_at,d.id LIMIT 1`, table, status, marketDeliveryEligibleSQL(kind, "d", "$1"), marketDeliveryRouteValidSQL(kind, "d"))
			}
			query := `SELECT MIN(next_attempt_at) FROM ((` + head("pending") + `) UNION ALL (` + head("failed") + `)) heads`
			if err = tx.QueryRow(ctx, query, cutoff).Scan(&at); err != nil {
				return nil, err
			}
			if at != nil && (earliest == nil || at.Before(*earliest)) {
				earliest = at
			}
		}
		return earliest, nil
	})
}
