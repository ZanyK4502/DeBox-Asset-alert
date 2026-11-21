package store

import (
	"context"
	"fmt"
	"time"
)

// Shared with the legacy manual list. Scheduling changes neither eligibility
// nor the original last_checked_at/id commit order.
const walletObservationEligibilitySQL = `
	FROM watch_rules wr
	LEFT JOIN LATERAL (
		SELECT sub.plan_code FROM subscriptions sub
		WHERE sub.debox_user_id=wr.debox_user_id AND sub.status='active'
		  AND (sub.is_permanent=1 OR sub.expires_at>NOW())
		ORDER BY CASE sub.plan_code WHEN 'professional' THEN 2 WHEN 'standard' THEN 1 ELSE 0 END DESC,
		         sub.is_permanent DESC, sub.expires_at DESC
		LIMIT 1
	) active_subscription ON TRUE
	WHERE wr.enabled=1 AND wr.run_status='active'
	  AND (
		active_subscription.plan_code <> 'free'
		OR (
		  (active_subscription.plan_code='free' OR NOT EXISTS (
			SELECT 1 FROM subscriptions history WHERE history.debox_user_id=wr.debox_user_id
			  AND history.plan_code<>'free' AND history.is_permanent=0
		  ))
		  AND EXISTS (SELECT 1 FROM user_preferences up
			WHERE up.debox_user_id=wr.debox_user_id AND up.free_watch_rule_id=wr.id)
		)
	  )
`

// A transient traversal key, never a successful-observation watermark.
type WalletObservationCursor struct {
	LastCheckedAt *time.Time
	RuleID        int64
	MaxRuleID     int64
}

func (s *Store) listScheduledWatchRules(ctx context.Context, limit int, minimumInterval time.Duration) ([]WatchRule, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	query := `SELECT ` + watchRuleColumnsQualified + `,
		COALESCE(active_subscription.plan_code,'free') AS effective_plan_code
	` + walletObservationEligibilitySQL + `
	  AND (wr.next_observation_at IS NULL OR wr.next_observation_at<=NOW())`
	args := []any{clamp(limit, 1, 1000)}
	if minimumInterval > 0 {
		query += ` AND (wr.last_checked_at IS NULL OR wr.last_checked_at<=NOW()-($2::bigint*INTERVAL '1 microsecond'))`
		args = append(args, walletObservationIntervalMicros(minimumInterval))
	}
	query += ` ORDER BY wr.last_checked_at NULLS FIRST,wr.id ASC LIMIT $1`
	rules, err := collectMany[WatchRule](ctx, s.db, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list enabled watch rules: %w", err)
	}
	return rules, nil
}

// ListDueWatchRules reads at most one bounded page. The minimum period is based
// on the last committed observation, independently of failure backoff.
func (s *Store) ListDueWatchRules(ctx context.Context, limit int, minimumInterval time.Duration) ([]WatchRule, error) {
	if minimumInterval <= 0 {
		minimumInterval = time.Minute
	}
	return s.ListDueWatchRulesPage(ctx, limit, minimumInterval, nil, nil)
}

func (s *Store) ListDueWatchRulesPage(ctx context.Context, limit int, minimumInterval time.Duration, after, through *WalletObservationCursor) ([]WatchRule, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	args := []any{clamp(limit, 1, 200), walletObservationIntervalMicros(minimumInterval)}
	query := `SELECT ` + watchRuleColumnsQualified + `,
	 COALESCE(active_subscription.plan_code,'free') AS effective_plan_code
	` + walletObservationEligibilitySQL + `
	 AND (wr.next_observation_at IS NULL OR wr.next_observation_at<=NOW())
	 AND (wr.last_checked_at IS NULL OR wr.last_checked_at<=NOW()-($2::bigint*INTERVAL '1 microsecond'))`
	query += walletObservationKeyBound(after, true, &args)
	query += walletObservationKeyBound(through, false, &args)
	if through != nil && through.MaxRuleID > 0 {
		args = append(args, through.MaxRuleID)
		query += fmt.Sprintf(` AND wr.id<=$%d`, len(args))
	}
	query += ` ORDER BY wr.last_checked_at NULLS FIRST,wr.id ASC LIMIT $1`
	rows, err := collectMany[WatchRule](ctx, s.db, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list due wallet observation page: %w", err)
	}
	return rows, nil
}

func walletObservationKeyBound(key *WalletObservationCursor, after bool, args *[]any) string {
	if key == nil {
		return ""
	}
	n := len(*args) + 1
	if key.LastCheckedAt == nil {
		*args = append(*args, key.RuleID)
		if after {
			return fmt.Sprintf(` AND (wr.last_checked_at IS NOT NULL OR (wr.last_checked_at IS NULL AND wr.id>$%d))`, n)
		}
		return fmt.Sprintf(` AND wr.last_checked_at IS NULL AND wr.id<=$%d`, n)
	}
	*args = append(*args, key.LastCheckedAt.UTC().Truncate(time.Microsecond), key.RuleID)
	if after {
		return fmt.Sprintf(` AND wr.last_checked_at IS NOT NULL AND (wr.last_checked_at,wr.id)>($%d,$%d)`, n, n+1)
	}
	return fmt.Sprintf(` AND (wr.last_checked_at IS NULL OR (wr.last_checked_at,wr.id)<=($%d,$%d))`, n, n+1)
}

func (s *Store) WalletObservationDueFence(ctx context.Context, minimumInterval time.Duration) (*WalletObservationCursor, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var key WalletObservationCursor
	err := s.db.QueryRow(ctx, `SELECT wr.last_checked_at,wr.id,
	 (SELECT newest.id FROM watch_rules newest ORDER BY newest.id DESC LIMIT 1)
	 `+walletObservationEligibilitySQL+`
	 AND (wr.next_observation_at IS NULL OR wr.next_observation_at<=NOW())
	 AND (wr.last_checked_at IS NULL OR wr.last_checked_at<=NOW()-($1::bigint*INTERVAL '1 microsecond'))
	 ORDER BY wr.last_checked_at DESC NULLS LAST,wr.id DESC LIMIT 1
	`, walletObservationIntervalMicros(minimumInterval)).Scan(&key.LastCheckedAt, &key.RuleID, &key.MaxRuleID)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read wallet observation traversal fence: %w", err)
	}
	return &key, nil
}

// NextWalletObservationAt uses two indexed leading-order probes instead of
// materializing all rules and taking MIN(GREATEST(...)). Each branch selects
// the field dominating the due time, preserving arbitrary configured periods.
func (s *Store) NextWalletObservationAt(ctx context.Context, minimumInterval time.Duration) (*time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var at time.Time
	err := s.db.QueryRow(ctx, `
		SELECT next_at FROM (
		  (SELECT COALESCE(wr.last_checked_at+($1::bigint*INTERVAL '1 microsecond'),NOW()) AS next_at
		   `+walletObservationEligibilitySQL+`
		   AND (wr.next_observation_at IS NULL OR (wr.last_checked_at IS NOT NULL
		        AND wr.next_observation_at<=wr.last_checked_at+($1::bigint*INTERVAL '1 microsecond')))
		   ORDER BY wr.last_checked_at NULLS FIRST,wr.id ASC LIMIT 1)
		  UNION ALL
		  (SELECT wr.next_observation_at AS next_at
		   `+walletObservationEligibilitySQL+`
		   AND wr.next_observation_at IS NOT NULL
		   AND (wr.last_checked_at IS NULL OR wr.next_observation_at>wr.last_checked_at+($1::bigint*INTERVAL '1 microsecond'))
		   ORDER BY wr.next_observation_at ASC LIMIT 1)
		) candidates ORDER BY next_at LIMIT 1
	`, walletObservationIntervalMicros(minimumInterval)).Scan(&at)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read next wallet observation: %w", err)
	}
	return &at, nil
}

func walletObservationIntervalMicros(interval time.Duration) int64 {
	if interval <= 0 {
		interval = time.Minute
	}
	micros := int64(interval / time.Microsecond)
	if interval%time.Microsecond != 0 {
		micros++
	}
	return micros
}
