package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdetail"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type walletObservationSource struct {
	SourceOccurredAt *time.Time
	SourceTimeBasis  string
}

type walletEnqueueOnlyContextKey struct{}
type walletObservationPhaseKey struct{}
type walletObservationPhase struct{ started bool }

func (e *Executor) commitWalletObservation(ctx context.Context, rule store.WatchRule, plan plans.Plan, current, note, symbol string, matched bool, noTriggerStatus string, source ...walletObservationSource) (RuleResult, error) {
	rule.EffectivePlanCode = plan.Code
	params := store.RecordWalletObservationParams{Rule: rule, CurrentValue: current, Note: note, TokenSymbol: symbol, Matched: matched, NoTriggerStatus: noTriggerStatus}
	if balance, ok := ctx.Value(walletBalanceReadContextKey{}).(walletBalanceReadMetadata); ok {
		params.BalanceReadSource = balance.source
		decimals := balance.decimals
		params.BalanceReadDecimals = &decimals
	}
	if reads, ok := ctx.Value(walletCycleReadsKey{}).(*walletCycleReads); ok && reads.lastObservedAt != nil {
		at := *reads.lastObservedAt
		params.ReadObservedAt = &at
	}
	if len(source) > 0 {
		params.SourceOccurredAt = source[0].SourceOccurredAt
		params.SourceTimeBasis = source[0].SourceTimeBasis
	}
	if plan.Code == plans.Free && plan.DailyAlertLimit != nil {
		limit := int64(*plan.DailyAlertLimit)
		params.DailyAlertLimit = &limit
	}
	params.BuildNotification = func(result store.WalletObservationResult) (store.CreateNotificationDetailSnapshotParams, store.WalletNotificationPayload, error) {
		return e.buildWalletNotification(result, symbol, note)
	}
	if phase, ok := ctx.Value(walletObservationPhaseKey{}).(*walletObservationPhase); ok {
		phase.started = true
	}
	observation, err := e.deps.Repository.RecordWalletObservation(ctx, params)
	if errors.Is(err, store.ErrWalletObservationStale) {
		return RuleResult{RuleID: rule.ID, Status: "stale_observation", Value: current}, nil
	}
	if err != nil {
		if errors.Is(err, store.ErrWalletNotificationCapacity) || errors.Is(err, notificationdelivery.ErrCapacity) ||
			errors.Is(err, store.ErrWalletSourceTimeUnavailable) || errors.Is(err, store.ErrWalletBalanceSourceUnavailable) ||
			errors.Is(err, store.ErrWalletBalanceSourceTransitionRequired) {
			if deferErr := e.deps.Repository.DeferWalletObservation(ctx, rule.ID, time.Minute); deferErr != nil {
				return RuleResult{}, errors.Join(err, deferErr)
			}
		}
		return RuleResult{}, err
	}
	result := RuleResult{RuleID: rule.ID, Status: observation.Status, Value: current, Limit: observation.Limit, Used: observation.Used, Event: observation.Event, ReadObservedAt: observation.ReadObservedAt}
	if observation.Stage != nil {
		result.TriggerCount = observation.Stage.TotalTriggerCount
		result.TriggerThreshold = observation.Stage.TriggerCountThreshold
	}
	if observation.Combination != nil {
		result.CombinationID = observation.Combination.CombinationRuleID
		result.MemberProgress = observation.Combination.MemberProgress
	}
	if observation.Outbox == nil {
		return result, nil
	}
	result.OutboxID = observation.Outbox.ID
	if enqueueOnly, _ := ctx.Value(walletEnqueueOnlyContextKey{}).(bool); enqueueOnly {
		return result, nil
	}
	for _, delivery := range e.deliverWalletNotifications(ctx, observation.Outbox.ID, 1) {
		if delivery.Error != nil {
			return RuleResult{}, delivery.Error
		}
		if delivery.Row.Status != "sent" {
			result.Status = delivery.Row.Status
			return result, nil
		}
		result.Status = "alerted"
		if result.Event != nil {
			result.Event.NotificationStatus = "sent"
			result.Event.NotificationMessageID = delivery.Row.MessageID
		}
		if observation.Stage != nil && observation.Stage.Notification != nil {
			result.Aggregate = observation.Stage.Notification
		}
		if observation.Combination != nil && observation.Combination.Notification != nil {
			result.Aggregate = observation.Combination.Notification
		}
		if result.Aggregate != nil {
			result.Aggregate.NotificationStatus = "sent"
			result.Aggregate.NotificationMessageID = delivery.Row.MessageID
		}
	}
	return result, nil
}

func (e *Executor) buildWalletNotification(r store.WalletObservationResult, symbol, note string) (store.CreateNotificationDetailSnapshotParams, store.WalletNotificationPayload, error) {
	rule := r.Rule
	p := store.CreateNotificationDetailSnapshotParams{DeBoxUserID: rule.DeBoxUserID, RuleID: &rule.ID, RuleType: rule.RuleType, RuleName: addressSnapshotRuleName(rule.RuleType, rule.NotificationLanguage), RuleThreshold: rule.Threshold, ActualValue: r.Value, NotificationChatID: rule.NotificationChatID, NotificationChatType: rule.NotificationChatType, NotificationLanguage: rule.NotificationLanguage, NotificationLabel: rule.NotificationLabel}
	data := addressNotificationSnapshotPayload{SchemaVersion: 1, Rule: &rule, SourceOccurredAt: r.SourceOccurredAt, SourceTimeBasis: r.SourceTimeBasis, ObservedFromAt: rule.LastCheckedAt, ReadObservedAt: r.ReadObservedAt, BalanceReadSource: r.BalanceReadSource, BalanceReadDecimals: r.BalanceReadDecimals}
	actionZH, actionEN := "查看详情", "View details"
	switch {
	case r.Event != nil:
		p.SourceKey = fmt.Sprintf("address_realtime:%d", r.Event.ID)
		p.NotificationKind = store.NotificationKindAddressRealtime
		p.SourceType = "alert_event"
		p.SourceID = &r.Event.ID
		at := r.Event.CreatedAt
		if at.IsZero() {
			at = time.Now()
		}
		p.NotificationText = alertText(rule, r.Event.PreviousValue, stringValue(r.Event.CurrentValue), symbol, note, at)
		data.AlertEvent = r.Event
		data.TokenSymbol = strings.TrimSpace(symbol)
		data.Note = strings.TrimSpace(note)
		data.OccurredAt = &at
		data.ObservedThroughAt = &at
	case r.Stage != nil && r.Stage.Notification != nil:
		v := r.Stage
		n := v.Notification
		p.SourceKey = fmt.Sprintf("address_stage:%d", n.ID)
		p.NotificationKind = store.NotificationKindAddressStage
		p.SourceType = "aggregate_notification"
		p.SourceID = &n.ID
		p.NotificationChatID = n.NotificationChatID
		p.NotificationChatType = n.NotificationChatType
		p.NotificationLanguage = n.NotificationLanguage
		p.NotificationLabel = n.NotificationLabel
		if v.TriggerCountThreshold > 0 {
			p.RuleThreshold = appendSnapshotCondition(p.RuleThreshold, fmt.Sprintf("send_after=%d", v.TriggerCountThreshold))
		}
		p.ActualValue = addressStageSnapshotActual(*v)
		p.NotificationText = stageAlertText(rule, *v)
		data.Stage = v
		actionZH, actionEN = "查看全部事件", "View all events"
	case r.Combination != nil && r.Combination.Notification != nil:
		v := r.Combination
		n := v.Notification
		p.SourceKey = fmt.Sprintf("address_combination:%d", n.ID)
		p.NotificationKind = store.NotificationKindAddressCombination
		p.SourceType = "aggregate_notification"
		p.SourceID = &n.ID
		p.RuleID = &v.CombinationRuleID
		p.RuleType = "combination"
		p.RuleName = "地址组合提醒"
		if normalizeLanguage(n.NotificationLanguage) == "en" {
			p.RuleName = "Address combination alert"
		}
		p.NotificationChatID = n.NotificationChatID
		p.NotificationChatType = n.NotificationChatType
		p.NotificationLanguage = n.NotificationLanguage
		p.NotificationLabel = n.NotificationLabel
		p.RuleThreshold = addressCombinationSnapshotThreshold(v.MemberProgress)
		p.ActualValue = addressCombinationSnapshotActual(v.MemberProgress)
		p.NotificationText = combinationAlertText(*v)
		data.Combination = v
		actionZH, actionEN = "查看完整分析", "View full analysis"
	default:
		return p, store.WalletNotificationPayload{}, store.ErrInvalidNotificationSnapshot
	}
	details, err := json.Marshal(data)
	if err != nil {
		return p, store.WalletNotificationPayload{}, err
	}
	p.Details = details
	payload := store.WalletNotificationPayload{Payload: notificationdelivery.Payload{ChatID: p.NotificationChatID, ChatType: p.NotificationChatType, Text: p.NotificationText}, Language: p.NotificationLanguage, ReadObservedAt: r.ReadObservedAt, BalanceReadSource: r.BalanceReadSource, BalanceReadDecimals: r.BalanceReadDecimals}
	_, action := e.deps.Notifications.(ActionNotificationService)
	_, contextAction := e.deps.Notifications.(notificationdelivery.ContextActionSender)
	if action || contextAction {
		payload.ActionURL = notificationdetail.NotificationURL(e.deps.PublicAppURL, store.WalletNotificationIDPlaceholder)
		if payload.ActionURL != "" {
			payload.ActionText = actionZH
			if normalizeLanguage(p.NotificationLanguage) == "en" {
				payload.ActionText = actionEN
			}
		}
	}
	return p, payload, nil
}

type walletDispatchResult struct {
	Row   store.WalletNotificationOutbox
	Error error
}

func (e *Executor) deliverWalletNotifications(ctx context.Context, onlyID int64, limit int) []walletDispatchResult {
	if limit > notificationdelivery.MaxBatch {
		limit = notificationdelivery.MaxBatch
	}
	results := make([]walletDispatchResult, 0, limit)
	for n := 0; n < limit && ctx.Err() == nil; n++ {
		rows, err := e.deps.Repository.ClaimWalletNotifications(ctx, onlyID, 1)
		if err != nil {
			results = append(results, walletDispatchResult{Error: err})
			break
		}
		if len(rows) == 0 {
			break
		}
		row := rows[0]
		p := store.FinishWalletNotificationParams{ID: row.ID, LeaseToken: stringValue(row.LeaseToken)}
		var frozen store.WalletNotificationPayload
		decodeErr := json.Unmarshal(row.Payload, &frozen)
		rule, eligible, eligibilityErr := e.deps.Repository.WalletNotificationRule(ctx, row)
		var sendErr error
		if decodeErr != nil || frozen.Payload.Validate() != nil {
			p.Status = "failed"
			p.Error = "invalid frozen wallet notification payload"
		} else if eligibilityErr != nil {
			sendErr = &notificationdelivery.Failure{Kind: notificationdelivery.Retryable, Code: "eligibility_check_failed_before_send", Cause: eligibilityErr}
		} else if !eligible {
			p.Status = "skipped"
			p.Error = "wallet rule, entitlement, or destination no longer eligible"
		} else {
			plan, err := e.plan(rule.EffectivePlanCode)
			if err != nil {
				sendErr = &notificationdelivery.Failure{Kind: notificationdelivery.Retryable, Code: "plan_check_failed_before_send", Cause: err}
			} else if ruleAllowedByPlan(rule, plan) != nil {
				p.Status = "skipped"
				p.Error = "wallet rule no longer allowed by current plan"
			} else {
				_, contextSender := e.deps.Notifications.(notificationdelivery.ContextSender)
				_, contextAction := e.deps.Notifications.(notificationdelivery.ContextActionSender)
				if onlyID == 0 && !contextSender && !(frozen.ActionURL != "" && contextAction) {
					sendErr = &notificationdelivery.Failure{Kind: notificationdelivery.Retryable, Code: "background_sender_requires_cancellation"}
				} else {
					sendContext, reservation, admissionErr := notificationdelivery.Acquire(ctx, e.deps.Notifications)
					if retryAt, waiting := notificationdelivery.AdmissionRetryAt(admissionErr); waiting {
						updated, deferErr := e.deferWalletAdmission(ctx, row, retryAt)
						results = append(results, walletDispatchResult{Row: updated, Error: deferErr})
						break
					}
					started, startErr := e.deps.Repository.StartWalletNotificationAttempt(ctx, row.ID, p.LeaseToken)
					if startErr != nil {
						releaseWalletAdmission(ctx, reservation)
						results = append(results, walletDispatchResult{Row: row, Error: startErr})
						if onlyID != 0 {
							break
						}
						continue
					}
					row = started
					id, err := notificationdelivery.Send(sendContext, e.deps.Notifications, frozen.Payload)
					releaseWalletAdmission(ctx, reservation)
					if retryAt, waiting := notificationdelivery.AdmissionRetryAt(err); waiting {
						updated, deferErr := e.deferWalletAdmission(ctx, row, retryAt)
						results = append(results, walletDispatchResult{Row: updated, Error: deferErr})
						break
					}
					sendErr = err
					if err == nil {
						p.Status = "sent"
						p.MessageID = &id
					}
				}
			}
		}
		if sendErr != nil {
			failure := notificationdelivery.Classify(sendErr)
			p.Error = sendErr.Error()
			switch failure.Kind {
			case notificationdelivery.Retryable:
				p.Status = "failed"
				if row.Attempts < notificationdelivery.MaxAttempts {
					p.Status = "retry"
					at := time.Now().Add(notificationdelivery.RetryDelay(int(row.Attempts), failure.RetryAfter))
					p.RetryAt = &at
				}
			case notificationdelivery.Rejected:
				p.Status = "failed"
			default:
				p.Status = "unknown"
			}
		}
		finishContext, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		updated, finishErr := e.deps.Repository.FinishWalletNotification(finishContext, p)
		finishCancel()
		if finishErr != nil {
			if p.Status == "sent" && p.MessageID != nil {
				slog.WarnContext(context.WithoutCancel(ctx), "wallet notification accepted but acknowledgement persistence failed", "outbox_id", row.ID, "message_id", *p.MessageID, "error", finishErr)
				finishErr = fmt.Errorf("wallet outbox %d accepted message %s but acknowledgement persistence failed: %w", row.ID, *p.MessageID, finishErr)
			}
			results = append(results, walletDispatchResult{Row: row, Error: errors.Join(sendErr, finishErr)})
		} else {
			results = append(results, walletDispatchResult{Row: updated, Error: sendErr})
		}
		if onlyID != 0 {
			break
		}
	}
	return results
}

func releaseWalletAdmission(ctx context.Context, reservation *notificationdelivery.Reservation) {
	if reservation == nil {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_ = reservation.Release(cleanupCtx)
}

func (e *Executor) deferWalletAdmission(ctx context.Context, row store.WalletNotificationOutbox, at time.Time) (store.WalletNotificationOutbox, error) {
	repository, ok := e.deps.Repository.(interface {
		DeferWalletNotificationAdmission(context.Context, int64, string, time.Time) (store.WalletNotificationOutbox, error)
	})
	if !ok {
		return row, errors.New("wallet admission deferral repository unavailable")
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	return repository.DeferWalletNotificationAdmission(cleanupCtx, row.ID, stringValue(row.LeaseToken), at)
}
