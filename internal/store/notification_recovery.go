package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrNotificationBoundaryConflict = errors.New("notification recovery boundary changed or would move backwards")

type NotificationRecoveryBoundary struct {
	CutoffAt     time.Time `db:"cutoff_at" json:"cutoff_at"`
	Revision     int64     `db:"revision" json:"revision"`
	Reason       string    `db:"reason" json:"reason"`
	ConfiguredAt time.Time `db:"configured_at" json:"configured_at"`
}

func (s *Store) GetNotificationRecoveryBoundary(ctx context.Context) (*NotificationRecoveryBoundary, error) {
	return collectOptional[NotificationRecoveryBoundary](ctx, s.db, `SELECT cutoff_at,revision,reason,configured_at FROM notification_recovery_boundaries WHERE singleton=TRUE`)
}

func notificationSourceCutoff(ctx context.Context, db DBTX) (*time.Time, error) {
	boundary, err := newWithDB(db).GetNotificationRecoveryBoundary(ctx)
	if err != nil || boundary == nil {
		return nil, err
	}
	at := boundary.CutoffAt
	return &at, nil
}

func (s *Store) GetNotificationSourceCutoff(ctx context.Context) (*time.Time, error) {
	return notificationSourceCutoff(ctx, s.db)
}

// Explicit control-plane operation, never called by migration or a worker.
// Coordinating in-flight workers and reviewing queue counts happen before this.
func (s *Store) ConfigureNotificationRecoveryBoundary(ctx context.Context, cutoff time.Time, expectedRevision int64, reason string) (NotificationRecoveryBoundary, error) {
	reason = strings.TrimSpace(reason)
	if cutoff.IsZero() || expectedRevision < 0 || reason == "" || len(reason) > 1000 {
		return NotificationRecoveryBoundary{}, ErrInvalidNotificationStatus
	}
	cutoff = cutoff.UTC().Truncate(time.Microsecond)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return withTxValue(ctx, s.db, func(tx DBTX) (NotificationRecoveryBoundary, error) {
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '2s'`); err != nil {
			return NotificationRecoveryBoundary{}, err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('notification-recovery-boundary'))`); err != nil {
			return NotificationRecoveryBoundary{}, err
		}
		old, err := newWithDB(tx).GetNotificationRecoveryBoundary(ctx)
		if err != nil {
			return NotificationRecoveryBoundary{}, err
		}
		if old == nil && expectedRevision != 0 || old != nil && (old.Revision != expectedRevision || cutoff.Before(old.CutoffAt)) {
			return NotificationRecoveryBoundary{}, ErrNotificationBoundaryConflict
		}
		if old != nil && old.CutoffAt.Equal(cutoff) {
			return *old, nil
		}
		value, err := collectOne[NotificationRecoveryBoundary](ctx, tx, `INSERT INTO notification_recovery_boundaries(singleton,cutoff_at,revision,reason)
		 VALUES(TRUE,$1,$2,$3) ON CONFLICT(singleton) DO UPDATE SET cutoff_at=EXCLUDED.cutoff_at,
		 revision=EXCLUDED.revision,reason=EXCLUDED.reason,configured_at=NOW()
		 RETURNING cutoff_at,revision,reason,configured_at`, cutoff, expectedRevision+1, reason)
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO notification_recovery_boundary_history(revision,cutoff_at,reason,configured_at) VALUES($1,$2,$3,$4)`, value.Revision, value.CutoffAt, value.Reason, value.ConfiguredAt)
		}
		return value, err
	})
}

type NotificationRecoveryGap struct {
	ID           int64      `db:"id" json:"id"`
	ChainID      int64      `db:"chain_id" json:"chain_id"`
	ChainKey     string     `db:"chain_key" json:"chain_key"`
	FromBlock    int64      `db:"from_block" json:"from_block"`
	ThroughBlock int64      `db:"through_block" json:"through_block"`
	FromAt       *time.Time `db:"from_at" json:"from_at,omitempty"`
	ThroughAt    time.Time  `db:"through_at" json:"through_at"`
	CutoffAt     time.Time  `db:"cutoff_at" json:"cutoff_at"`
	Reason       string     `db:"reason" json:"reason"`
}

func (s *Store) RecordNotificationRecoveryGap(ctx context.Context, gap NotificationRecoveryGap) (NotificationRecoveryGap, error) {
	gap.ChainKey = strings.ToLower(strings.TrimSpace(gap.ChainKey))
	gap.Reason = strings.TrimSpace(gap.Reason)
	if gap.ChainID < 1 || gap.ChainKey == "" || len(gap.ChainKey) > 32 || gap.FromBlock < 1 || gap.ThroughBlock < gap.FromBlock ||
		gap.ThroughAt.IsZero() || gap.CutoffAt.IsZero() || gap.ThroughAt.After(gap.CutoffAt) || gap.FromAt != nil && gap.ThroughAt.Before(*gap.FromAt) || gap.Reason == "" || len(gap.Reason) > 1000 {
		return NotificationRecoveryGap{}, ErrInvalidMarketCursor
	}
	gap.ThroughAt = gap.ThroughAt.UTC().Truncate(time.Microsecond)
	gap.CutoffAt = gap.CutoffAt.UTC().Truncate(time.Microsecond)
	if gap.FromAt != nil {
		at := gap.FromAt.UTC().Truncate(time.Microsecond)
		gap.FromAt = &at
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	value, err := withTxValue(ctx, s.db, func(tx DBTX) (NotificationRecoveryGap, error) {
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '2s'`); err != nil {
			return NotificationRecoveryGap{}, err
		}
		var configured bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notification_recovery_boundary_history WHERE cutoff_at=$1)`, gap.CutoffAt).Scan(&configured); err != nil {
			return NotificationRecoveryGap{}, err
		}
		if !configured {
			return NotificationRecoveryGap{}, ErrNotificationBoundaryConflict
		}
		value, err := collectOne[NotificationRecoveryGap](ctx, tx, `INSERT INTO notification_recovery_gaps(chain_id,chain_key,from_block,through_block,from_at,through_at,cutoff_at,reason)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(chain_id,from_block,through_block,cutoff_at) DO NOTHING
		 RETURNING id,chain_id,chain_key,from_block,through_block,from_at,through_at,cutoff_at,reason`,
			gap.ChainID, gap.ChainKey, gap.FromBlock, gap.ThroughBlock, gap.FromAt, gap.ThroughAt, gap.CutoffAt, gap.Reason)
		if isNoRows(err) {
			value, err = collectOne[NotificationRecoveryGap](ctx, tx, `SELECT id,chain_id,chain_key,from_block,through_block,from_at,through_at,cutoff_at,reason
			 FROM notification_recovery_gaps WHERE chain_id=$1 AND from_block=$2 AND through_block=$3 AND cutoff_at=$4`, gap.ChainID, gap.FromBlock, gap.ThroughBlock, gap.CutoffAt)
			if err == nil && (value.ChainKey != gap.ChainKey || value.Reason != gap.Reason || !value.ThroughAt.Equal(gap.ThroughAt) || !sameOptionalTime(value.FromAt, gap.FromAt)) {
				return NotificationRecoveryGap{}, ErrNotificationBoundaryConflict
			}
		}
		return value, err
	})
	if err != nil {
		return value, fmt.Errorf("record explicit notification recovery gap: %w", err)
	}
	return value, nil
}

func sameOptionalTime(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}
