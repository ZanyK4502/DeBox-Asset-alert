package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
)

type notificationAdmissionRow struct {
	Reservations json.RawMessage `db:"reservations"`
	BlockedUntil *time.Time      `db:"blocked_until"`
}

func validNotificationAdmissionIdentity(botID string) bool {
	return botID != "" && len(botID) <= 128 && botID == strings.TrimSpace(botID)
}

func notificationAdmissionTx[T any](ctx context.Context, db DBTX, fn func(context.Context, DBTX) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return withTxValue(ctx, db, func(tx DBTX) (value T, err error) {
		if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='2s'`); err != nil {
			return value, err
		}
		return fn(ctx, tx)
	})
}

func lockNotificationAdmission(ctx context.Context, db DBTX, botID string) (notificationAdmissionRow, time.Time, error) {
	if _, err := db.Exec(ctx, `INSERT INTO notification_send_admission(bot_id) VALUES($1) ON CONFLICT(bot_id) DO NOTHING`, botID); err != nil {
		return notificationAdmissionRow{}, time.Time{}, err
	}
	row, err := collectOne[notificationAdmissionRow](ctx, db, `SELECT reservations,blocked_until FROM notification_send_admission WHERE bot_id=$1 FOR UPDATE`, botID)
	if err != nil {
		return row, time.Time{}, err
	}
	var now time.Time
	// clock_timestamp is sampled after the lock, not at transaction start.
	err = db.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return row, now.UTC(), err
}

func liveNotificationReservations(raw json.RawMessage, botID string, now time.Time) ([]notificationdelivery.AdmissionPermit, error) {
	var all []notificationdelivery.AdmissionPermit
	if err := json.Unmarshal(raw, &all); err != nil || len(all) > notificationdelivery.AdmissionDefaultLimit {
		return nil, errors.New("invalid persisted notification admission state")
	}
	live := make([]notificationdelivery.AdmissionPermit, 0, len(all))
	seen := make(map[string]bool, len(all))
	for _, permit := range all {
		if permit.BotID != botID || permit.Token == "" || len(permit.Token) > 80 || permit.GrantedAt.IsZero() || !permit.ExpiresAt.Equal(permit.GrantedAt.Add(notificationdelivery.AdmissionWindow)) || seen[permit.Token] {
			return nil, errors.New("invalid persisted notification admission permit")
		}
		seen[permit.Token] = true
		if permit.ExpiresAt.After(now) {
			live = append(live, permit)
		}
	}
	return live, nil
}

func (s *Store) ReserveNotificationAdmission(ctx context.Context, botID string, limit int) (notificationdelivery.AdmissionPermit, error) {
	if !validNotificationAdmissionIdentity(botID) || limit != 5 && limit != 10 && limit != notificationdelivery.AdmissionDefaultLimit {
		return notificationdelivery.AdmissionPermit{}, errors.New("invalid notification admission policy")
	}
	return notificationAdmissionTx(ctx, s.db, func(ctx context.Context, tx DBTX) (notificationdelivery.AdmissionPermit, error) {
		row, now, err := lockNotificationAdmission(ctx, tx, botID)
		if err != nil {
			return notificationdelivery.AdmissionPermit{}, err
		}
		live, err := liveNotificationReservations(row.Reservations, botID, now)
		if err != nil {
			return notificationdelivery.AdmissionPermit{}, err
		}
		if row.BlockedUntil != nil && row.BlockedUntil.After(now) {
			return notificationdelivery.AdmissionPermit{}, &notificationdelivery.AdmissionDeferred{RetryAt: *row.BlockedUntil, Reason: "server_retry_after"}
		}
		if len(live) >= limit {
			at := live[0].ExpiresAt
			for _, permit := range live[1:] {
				if permit.ExpiresAt.Before(at) {
					at = permit.ExpiresAt
				}
			}
			return notificationdelivery.AdmissionPermit{}, &notificationdelivery.AdmissionDeferred{RetryAt: at, Reason: "rolling_budget_full"}
		}
		var random [20]byte
		if _, err := rand.Read(random[:]); err != nil {
			return notificationdelivery.AdmissionPermit{}, err
		}
		permit := notificationdelivery.AdmissionPermit{BotID: botID, Token: hex.EncodeToString(random[:]), GrantedAt: now, ExpiresAt: now.Add(notificationdelivery.AdmissionWindow)}
		live = append(live, permit)
		body, err := json.Marshal(live)
		if err != nil {
			return notificationdelivery.AdmissionPermit{}, err
		}
		_, err = tx.Exec(ctx, `UPDATE notification_send_admission SET reservations=$2::jsonb,updated_at=$3 WHERE bot_id=$1`, botID, body, now)
		return permit, err
	})
}

func (s *Store) CheckNotificationAdmission(ctx context.Context, permit notificationdelivery.AdmissionPermit) error {
	return s.checkNotificationHTTPAdmission(ctx, permit, "")
}

func (s *Store) CheckNotificationHTTPAdmission(ctx context.Context, permit notificationdelivery.AdmissionPermit, scopeHash string) error {
	if len(scopeHash) != 64 || strings.Trim(scopeHash, "0123456789abcdef") != "" {
		return errors.New("invalid notification HTTP scope")
	}
	return s.checkNotificationHTTPAdmission(ctx, permit, scopeHash)
}

func (s *Store) checkNotificationHTTPAdmission(ctx context.Context, permit notificationdelivery.AdmissionPermit, scopeHash string) error {
	if !validNotificationAdmissionIdentity(permit.BotID) || permit.Token == "" || len(permit.Token) > 80 {
		return errors.New("invalid notification admission permit")
	}
	_, err := notificationAdmissionTx(ctx, s.db, func(ctx context.Context, tx DBTX) (struct{}, error) {
		var rawPath json.RawMessage
		if scopeHash != "" {
			// All combined grants lock path first, then bot. Reserve/release and
			// retry-after operations only lock bot, so two bots sharing a key
			// cannot introduce an opposite lock order.
			if _, err := tx.Exec(ctx, `INSERT INTO notification_send_path_admission(scope_hash) VALUES($1) ON CONFLICT(scope_hash) DO NOTHING`, scopeHash); err != nil {
				return struct{}{}, err
			}
			if err := tx.QueryRow(ctx, `SELECT admissions FROM notification_send_path_admission WHERE scope_hash=$1 FOR UPDATE`, scopeHash).Scan(&rawPath); err != nil {
				return struct{}{}, err
			}
		}
		row, now, err := lockNotificationAdmission(ctx, tx, permit.BotID)
		if err != nil {
			return struct{}{}, err
		}
		if row.BlockedUntil != nil && row.BlockedUntil.After(now) {
			return struct{}{}, &notificationdelivery.AdmissionDeferred{RetryAt: *row.BlockedUntil, Reason: "server_retry_after"}
		}
		live, err := liveNotificationReservations(row.Reservations, permit.BotID, now)
		if err != nil {
			return struct{}{}, err
		}
		var pathSlots []time.Time
		if scopeHash != "" {
			var all []time.Time
			if err := json.Unmarshal(rawPath, &all); err != nil || len(all) > notificationdelivery.AdmissionPathLimit {
				return struct{}{}, errors.New("invalid persisted notification HTTP admission state")
			}
			for _, at := range all {
				if at.IsZero() || at.After(now) {
					return struct{}{}, errors.New("invalid persisted notification HTTP admission timestamp")
				}
				if at.Add(notificationdelivery.AdmissionPathWindow).After(now) {
					pathSlots = append(pathSlots, at)
				}
			}
			if len(pathSlots) >= notificationdelivery.AdmissionPathLimit {
				at := pathSlots[0].Add(notificationdelivery.AdmissionPathWindow)
				for _, slot := range pathSlots[1:] {
					if candidate := slot.Add(notificationdelivery.AdmissionPathWindow); candidate.Before(at) {
						at = candidate
					}
				}
				return struct{}{}, &notificationdelivery.AdmissionDeferred{RetryAt: at, Reason: "path_second_budget_full"}
			}
		}
		for i, saved := range live {
			if saved == permit && !saved.Consumed {
				// Anchor the rolling window at the last transport boundary,
				// not the earlier worker preflight reservation. A consumed slot
				// is durable, so a copied permit cannot authorize another HTTP.
				live[i].Consumed = true
				live[i].GrantedAt = now
				live[i].ExpiresAt = now.Add(notificationdelivery.AdmissionWindow)
				body, marshalErr := json.Marshal(live)
				if marshalErr != nil {
					return struct{}{}, marshalErr
				}
				if _, err := tx.Exec(ctx, `UPDATE notification_send_admission SET reservations=$2::jsonb,updated_at=$3 WHERE bot_id=$1`, permit.BotID, body, now); err != nil {
					return struct{}{}, err
				}
				if scopeHash != "" {
					pathSlots = append(pathSlots, now)
					encoded, err := json.Marshal(pathSlots)
					if err != nil {
						return struct{}{}, err
					}
					if _, err = tx.Exec(ctx, `UPDATE notification_send_path_admission SET admissions=$2::jsonb,updated_at=$3 WHERE scope_hash=$1`, scopeHash, encoded, now); err != nil {
						return struct{}{}, err
					}
				}
				return struct{}{}, nil
			}
		}
		return struct{}{}, &notificationdelivery.AdmissionDeferred{RetryAt: now, Reason: "permit_expired_or_released"}
	})
	return err
}

func (s *Store) ReleaseNotificationAdmission(ctx context.Context, permit notificationdelivery.AdmissionPermit) error {
	if !validNotificationAdmissionIdentity(permit.BotID) || permit.Token == "" || len(permit.Token) > 80 {
		return errors.New("invalid notification admission permit")
	}
	_, err := notificationAdmissionTx(ctx, s.db, func(ctx context.Context, tx DBTX) (struct{}, error) {
		row, now, err := lockNotificationAdmission(ctx, tx, permit.BotID)
		if err != nil {
			return struct{}{}, err
		}
		live, err := liveNotificationReservations(row.Reservations, permit.BotID, now)
		if err != nil {
			return struct{}{}, err
		}
		kept := make([]notificationdelivery.AdmissionPermit, 0, len(live))
		for _, saved := range live {
			if saved != permit || saved.Consumed {
				kept = append(kept, saved)
			}
		}
		body, err := json.Marshal(kept)
		if err != nil {
			return struct{}{}, err
		}
		_, err = tx.Exec(ctx, `UPDATE notification_send_admission SET reservations=$2::jsonb,updated_at=$3 WHERE bot_id=$1`, permit.BotID, body, now)
		return struct{}{}, err
	})
	return err
}

func (s *Store) BlockNotificationAdmission(ctx context.Context, botID string, delay time.Duration) error {
	if !validNotificationAdmissionIdentity(botID) || delay <= 0 {
		return errors.New("invalid notification admission block")
	}
	microseconds := delay.Microseconds()
	if delay%time.Microsecond != 0 {
		microseconds++
	}
	_, err := notificationAdmissionTx(ctx, s.db, func(ctx context.Context, tx DBTX) (struct{}, error) {
		_, err := tx.Exec(ctx, `INSERT INTO notification_send_admission(bot_id,blocked_until) VALUES($1,clock_timestamp()+$2::bigint*INTERVAL '1 microsecond') ON CONFLICT(bot_id) DO UPDATE SET blocked_until=GREATEST(notification_send_admission.blocked_until,EXCLUDED.blocked_until),updated_at=clock_timestamp()`, botID, microseconds)
		return struct{}{}, err
	})
	return err
}
