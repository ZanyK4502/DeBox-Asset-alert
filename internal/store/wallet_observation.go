package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const WalletNotificationPayloadLimit = 64 << 10
const WalletNotificationIDPlaceholder = "nd_0000000000000000000000000000000000000000"

var (
	ErrWalletObservationStale      = errors.New("wallet observation baseline or configuration changed")
	ErrWalletNotificationCapacity  = errors.New("wallet notification capacity exceeded")
	ErrWalletSourceTimeUnavailable = errors.New("wallet interaction source time is unavailable at recovery boundary")
)

// The builder is pure: it may format the locked rule and the committed source
// ingredients, but must not perform network calls or write outside this transaction.
type WalletNotificationBuilder func(WalletObservationResult) (CreateNotificationDetailSnapshotParams, WalletNotificationPayload, error)

type RecordWalletObservationParams struct {
	Rule                WatchRule
	CurrentValue        string
	TokenSymbol         string
	Note                string
	Matched             bool
	NoTriggerStatus     string
	DailyAlertLimit     *int64
	BuildNotification   WalletNotificationBuilder
	SourceOccurredAt    *time.Time
	SourceTimeBasis     string
	ReadObservedAt      *time.Time
	BalanceReadSource   string
	BalanceReadDecimals *int
}

type WalletObservationResult struct {
	Rule                WatchRule
	Status              string
	Value               string
	Limit               int64
	Used                int64
	Event               *AlertEvent
	Stage               *StageTriggerResult
	Combination         *CombinationTriggerResult
	Outbox              *WalletNotificationOutbox
	SourceOccurredAt    *time.Time
	SourceTimeBasis     string
	ReadObservedAt      *time.Time
	BalanceReadSource   string
	BalanceReadDecimals *int
}

// RecordWalletObservation commits the baseline, quota reservation, trigger and
// immutable notification together. Delivery never occurs while the transaction is open.
func (s *Store) RecordWalletObservation(ctx context.Context, params RecordWalletObservationParams) (WalletObservationResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return withTxValue(ctx, s.db, func(tx DBTX) (WalletObservationResult, error) {
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '2s'`); err != nil {
			return WalletObservationResult{}, err
		}
		if err := lockUser(ctx, tx, params.Rule.DeBoxUserID); err != nil {
			return WalletObservationResult{}, err
		}
		var combinationID *int64
		if params.Rule.RuleScope == "combination" {
			var id int64
			err := tx.QueryRow(ctx, `SELECT cr.id FROM combination_rules cr JOIN combination_rule_members crm ON crm.combination_rule_id=cr.id WHERE crm.watch_rule_id=$1 AND cr.debox_user_id=$2 AND cr.enabled=1 AND cr.run_status='active' FOR UPDATE OF cr`, params.Rule.ID, params.Rule.DeBoxUserID).Scan(&id)
			if isNoRows(err) {
				return WalletObservationResult{}, ErrWalletObservationStale
			}
			if err != nil {
				return WalletObservationResult{}, fmt.Errorf("lock wallet combination: %w", err)
			}
			combinationID = &id
		}
		rule, err := collectOne[WatchRule](ctx, tx, `SELECT `+watchRuleColumns+` FROM watch_rules WHERE id=$1 AND debox_user_id=$2 FOR UPDATE`, params.Rule.ID, params.Rule.DeBoxUserID)
		if isNoRows(err) {
			return WalletObservationResult{}, ErrWalletObservationStale
		}
		if err != nil {
			return WalletObservationResult{}, fmt.Errorf("lock wallet observation rule: %w", err)
		}
		if rule.Enabled != 1 || rule.RunStatus != "active" || rule.NotificationGeneration != params.Rule.NotificationGeneration || !sameWalletBaseline(rule, params.Rule) ||
			watchRuleConfigurationFingerprint(createWatchRuleParamsFromRule(rule), true) != watchRuleConfigurationFingerprint(createWatchRuleParamsFromRule(params.Rule), true) {
			return WalletObservationResult{}, ErrWalletObservationStale
		}
		plan, eligible, err := walletRuleCurrentEligibility(ctx, tx, rule)
		if err != nil {
			return WalletObservationResult{}, err
		}
		if !eligible || plan != params.Rule.EffectivePlanCode {
			return WalletObservationResult{}, ErrWalletObservationStale
		}
		rule.EffectivePlanCode = plan
		result := WalletObservationResult{Rule: rule, Status: params.NoTriggerStatus, Value: params.CurrentValue, SourceOccurredAt: params.SourceOccurredAt, SourceTimeBasis: params.SourceTimeBasis, ReadObservedAt: params.ReadObservedAt, BalanceReadSource: params.BalanceReadSource, BalanceReadDecimals: params.BalanceReadDecimals}
		local := newWithDB(tx)
		matched := params.Matched
		balanceSource, sourceRebased, err := prepareWalletBalanceSource(ctx, tx, rule, params)
		if err != nil {
			return WalletObservationResult{}, err
		}
		if sourceRebased {
			matched = false
			result.Status = "source_rebased"
		}
		crossed, err := recordWalletBalanceRecoveryObservation(ctx, tx, rule, params.CurrentValue)
		if err != nil {
			return WalletObservationResult{}, err
		}
		if crossed {
			matched = false
			if !sourceRebased {
				result.Status = "recovery_skipped"
			}
		}
		if params.Matched && rule.RuleType == "address_interaction" && rule.LastValue != nil {
			skipped, err := recordWalletInteractionRecoveryDecision(ctx, tx, rule, params.CurrentValue, params.SourceOccurredAt, params.SourceTimeBasis)
			if err != nil {
				return WalletObservationResult{}, err
			}
			if skipped {
				matched = false
				result.Status = "recovery_skipped"
			}
		}
		if matched && plan == "free" && params.DailyAlertLimit != nil && *params.DailyAlertLimit > 0 {
			used, err := local.CountDailyAlertEvents(ctx, rule.DeBoxUserID, "Asia/Shanghai")
			if err != nil {
				return WalletObservationResult{}, err
			}
			if used >= *params.DailyAlertLimit {
				matched = false
				result.Status = "daily_limit"
				result.Used = used
				result.Limit = *params.DailyAlertLimit
			}
		}
		if matched {
			result.Status = "counted"
			switch {
			case rule.RuleScope == "combination":
				v, err := local.RecordCombinationTrigger(ctx, RecordCombinationTriggerParams{WatchRuleID: rule.ID, DeBoxUserID: rule.DeBoxUserID, PreviousValue: rule.LastValue, CurrentValue: &params.CurrentValue, Note: params.Note, TokenSymbol: params.TokenSymbol, SourceOccurredAt: params.SourceOccurredAt, SourceTimeBasis: params.SourceTimeBasis, ReadObservedAt: params.ReadObservedAt})
				if err != nil {
					return WalletObservationResult{}, err
				}
				result.Combination = &v
				if v.SkippedReason != "" {
					result.Status = "recovery_skipped"
				}
			case rule.DeliveryMode == "stage":
				v, err := local.RecordStageTrigger(ctx, RecordStageTriggerParams{WatchRuleID: rule.ID, DeBoxUserID: rule.DeBoxUserID, PreviousValue: rule.LastValue, CurrentValue: &params.CurrentValue, Note: params.Note, TokenSymbol: params.TokenSymbol, SourceOccurredAt: params.SourceOccurredAt, SourceTimeBasis: params.SourceTimeBasis, ReadObservedAt: params.ReadObservedAt})
				if err != nil {
					return WalletObservationResult{}, err
				}
				result.Stage = &v
				if v.SkippedReason != "" {
					result.Status = "recovery_skipped"
				}
			default:
				v, err := local.CreateAlertEvent(ctx, CreateAlertEventParams{WatchRuleID: rule.ID, EventType: rule.RuleType, PreviousValue: rule.LastValue, CurrentValue: &params.CurrentValue, NotificationStatus: "pending"})
				if err != nil {
					return WalletObservationResult{}, err
				}
				result.Event = &v
			}
			due := result.Event != nil || result.Stage != nil && result.Stage.NotificationDue || result.Combination != nil && result.Combination.NotificationDue
			if due {
				if params.BuildNotification == nil {
					return WalletObservationResult{}, ErrInvalidNotificationSnapshot
				}
				snapshotParams, payload, err := params.BuildNotification(result)
				if err != nil {
					return WalletObservationResult{}, err
				}
				snapshot, err := local.CreateNotificationDetailSnapshot(ctx, snapshotParams)
				if err != nil {
					return WalletObservationResult{}, err
				}
				// Always copy the actual immutable snapshot, including when source_key already exists.
				payload.ChatID = snapshot.NotificationChatID
				payload.ChatType = snapshot.NotificationChatType
				payload.Text = snapshot.NotificationText
				payload.Language = snapshot.NotificationLanguage
				payload.PublicID = snapshot.PublicID
				payload.SourceOccurredAt = params.SourceOccurredAt
				payload.SourceTimeBasis = params.SourceTimeBasis
				payload.ReadObservedAt = params.ReadObservedAt
				payload.BalanceReadSource = params.BalanceReadSource
				payload.BalanceReadDecimals = params.BalanceReadDecimals
				payload.ActionURL = strings.ReplaceAll(payload.ActionURL, WalletNotificationIDPlaceholder, snapshot.PublicID)
				if err := payload.Payload.Validate(); err != nil {
					return WalletObservationResult{}, err
				}
				encoded, err := json.Marshal(payload)
				if err != nil {
					return WalletObservationResult{}, err
				}
				if len(encoded) > WalletNotificationPayloadLimit {
					return WalletObservationResult{}, ErrWalletNotificationCapacity
				}
				sourceID := snapshot.SourceID
				if sourceID == nil || *sourceID < 1 {
					return WalletObservationResult{}, ErrInvalidNotificationSnapshot
				}
				outbox, err := collectOne[WalletNotificationOutbox](ctx, tx, `INSERT INTO wallet_notification_outbox(source_key,source_type,source_id,watch_rule_id,combination_rule_id,debox_user_id,snapshot_id,payload,source_generation) VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9) ON CONFLICT(source_key) DO UPDATE SET source_key=wallet_notification_outbox.source_key RETURNING `+walletNotificationOutboxColumns, snapshot.SourceKey, snapshot.SourceType, *sourceID, rule.ID, combinationID, rule.DeBoxUserID, snapshot.ID, string(encoded), rule.NotificationGeneration)
				if err != nil {
					var check *pgconn.PgError
					if errors.As(err, &check) && check.Code == "23514" && check.ConstraintName == "wallet_notification_outbox_payload_check" {
						return WalletObservationResult{}, fmt.Errorf("%w: %w", ErrWalletNotificationCapacity, err)
					}
					return WalletObservationResult{}, fmt.Errorf("queue wallet notification: %w", err)
				}
				result.Outbox = &outbox
				result.Status = "queued"
			}
		}
		if balanceSource {
			var checkedAt time.Time
			if err := tx.QueryRow(ctx, `UPDATE watch_rules SET last_value=$1,last_checked_at=GREATEST(clock_timestamp(),COALESCE(last_checked_at,'-infinity'::timestamptz)+INTERVAL '1 microsecond'),next_observation_at=NULL WHERE id=$2 RETURNING last_checked_at`, params.CurrentValue, rule.ID).Scan(&checkedAt); err != nil {
				return WalletObservationResult{}, fmt.Errorf("commit wallet observation baseline: %w", err)
			}
			if err := commitWalletBalanceSource(ctx, tx, rule, params, checkedAt); err != nil {
				return WalletObservationResult{}, err
			}
		} else if _, err := tx.Exec(ctx, `UPDATE watch_rules SET last_value=$1,last_checked_at=GREATEST(clock_timestamp(),COALESCE(last_checked_at,'-infinity'::timestamptz)+INTERVAL '1 microsecond'),next_observation_at=NULL WHERE id=$2`, params.CurrentValue, rule.ID); err != nil {
			return WalletObservationResult{}, fmt.Errorf("commit wallet observation baseline: %w", err)
		}
		return result, nil
	})
}

func sameWalletBaseline(a, b WatchRule) bool {
	if (a.LastValue == nil) != (b.LastValue == nil) || (a.LastCheckedAt == nil) != (b.LastCheckedAt == nil) {
		return false
	}
	if a.LastValue != nil && *a.LastValue != *b.LastValue {
		return false
	}
	return a.LastCheckedAt == nil || a.LastCheckedAt.UTC().Truncate(time.Microsecond).Equal(b.LastCheckedAt.UTC().Truncate(time.Microsecond))
}

func walletRuleCurrentEligibility(ctx context.Context, db DBTX, rule WatchRule) (string, bool, error) {
	var plan string
	var eligible bool
	err := db.QueryRow(ctx, `WITH current_plan AS (SELECT COALESCE((SELECT plan_code FROM subscriptions WHERE debox_user_id=$1 AND status='active' AND (is_permanent=1 OR expires_at>NOW()) ORDER BY CASE plan_code WHEN 'professional' THEN 2 WHEN 'standard' THEN 1 ELSE 0 END DESC,is_permanent DESC,expires_at DESC LIMIT 1),'free') AS code) SELECT code, code<>'free' OR ((EXISTS(SELECT 1 FROM subscriptions WHERE debox_user_id=$1 AND status='active' AND plan_code='free' AND (is_permanent=1 OR expires_at>NOW())) OR NOT EXISTS(SELECT 1 FROM subscriptions WHERE debox_user_id=$1 AND plan_code<>'free' AND is_permanent=0)) AND EXISTS(SELECT 1 FROM user_preferences WHERE debox_user_id=$1 AND free_watch_rule_id=$2)) FROM current_plan`, rule.DeBoxUserID, rule.ID).Scan(&plan, &eligible)
	if err != nil {
		return "", false, fmt.Errorf("wallet observation entitlement: %w", err)
	}
	return plan, eligible, nil
}
