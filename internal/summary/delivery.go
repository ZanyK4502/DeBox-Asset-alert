package summary

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdetail"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type dailySummaryDispatchRepository interface {
	CheckDailySummaryLegacyReviews(context.Context, int64, int64, time.Time) (bool, error)
	ListDailySummaryDispatchTargets(context.Context, int64, int64, int) ([]store.DailySummaryTarget, error)
	GetOpenDailySummaryDispatch(context.Context, int64, string, string) (*store.DailySummaryDispatch, error)
	PrepareDailySummaryDispatch(context.Context, store.PrepareDailySummaryDispatchParams) (*store.DailySummaryDispatch, error)
	ClaimDailySummaryDispatch(context.Context, int64, time.Time) (*store.DailySummaryDispatchClaim, error)
	CompleteDailySummaryDispatch(context.Context, int64, string, string, time.Time) error
	FailDailySummaryDispatch(context.Context, int64, string, string, string, *time.Time, time.Time, ...string) error
	SupersedeDailySummaryDispatch(context.Context, int64, time.Time, time.Time) (bool, error)
}

func (e *Executor) processSummaryTarget(ctx context.Context, repository dailySummaryDispatchRepository, subscription store.Subscription, target store.DailySummaryTarget) (bool, error) {
	now := e.deps.Now().UTC()
	due, date, periodEnd := summaryTargetDue(target, now)
	if !due && target.LastSentDate == date {
		return false, nil
	}
	open, err := repository.GetOpenDailySummaryDispatch(ctx, subscription.ID, target.ChatType, target.ChatID)
	if err != nil {
		return false, err
	}
	if open == nil && !due {
		return false, nil
	}
	if open != nil && open.Status == store.DailySummaryDispatchUnknown {
		return false, nil
	}
	if open != nil && open.Status == store.DailySummaryDispatchSending {
		_, err := repository.ClaimDailySummaryDispatch(ctx, open.ID, now)
		return false, err
	}
	if blocked, err := repository.CheckDailySummaryLegacyReviews(ctx, subscription.ID, target.ID, now); err != nil || blocked {
		return false, err
	}
	var notBefore *time.Time
	if open != nil {
		if open.Status == store.DailySummaryDispatchUnknown {
			return false, nil
		}
		if due && periodEnd.After(open.PeriodEndAt) && (open.Status == store.DailySummaryDispatchPending || open.Status == store.DailySummaryDispatchFailed) {
			if open.RetryAt != nil && open.RetryAt.After(now) {
				value := *open.RetryAt
				notBefore = &value
			}
			replaced, err := repository.SupersedeDailySummaryDispatch(ctx, open.ID, periodEnd, now)
			if err != nil {
				return false, err
			}
			if !replaced {
				return false, nil
			}
			open = nil
		}
	}
	if open == nil {
		if !due {
			return false, nil
		}
		start, end := summaryTargetPeriod(target, periodEnd)
		summarySubscription := summarySubscriptionForTarget(subscription, target)
		content, err := e.loadDailySummaryContent(ctx, summarySubscription, start, end)
		if err != nil {
			return false, err
		}
		text := buildDailySummaryReport(summarySubscription, start, end, content)
		params, err := dailySummarySnapshotParams(subscription, target, start, end, text, content)
		if err != nil {
			return false, err
		}
		open, err = repository.PrepareDailySummaryDispatch(ctx, store.PrepareDailySummaryDispatchParams{SubscriptionID: subscription.ID, TargetID: target.ID, PeriodStartAt: start, PeriodEndAt: end, LocalDate: date, NotBefore: notBefore, Snapshot: params, BuildPayload: e.frozenSummaryPayload})
		if err != nil {
			return false, err
		}
		if open == nil {
			return false, nil
		}
	}
	return e.sendFrozenSummary(ctx, repository, open, now)
}

// sendFrozenSummary consumes an existing immutable dispatch. It never reads
// statistics, creates a snapshot, or changes the report's frozen period.
func (e *Executor) sendFrozenSummary(ctx context.Context, repository dailySummaryDispatchRepository, open *store.DailySummaryDispatch, now time.Time) (bool, error) {
	return e.sendFrozenSummaryNext(ctx, repository, open, now, nil)
}

func (e *Executor) sendFrozenSummaryNext(ctx context.Context, repository dailySummaryDispatchRepository, open *store.DailySummaryDispatch, now time.Time, next *time.Time) (bool, error) {
	if open.Status != store.DailySummaryDispatchPending && open.Status != store.DailySummaryDispatchFailed || open.RetryAt != nil && now.Before(*open.RetryAt) || open.Attempts >= notificationdelivery.MaxAttempts || open.Status == store.DailySummaryDispatchFailed && open.RetryAt == nil {
		return false, nil
	}
	if notificationdelivery.NextAdmissionAt(e.deps.Notifications).After(now) {
		return false, nil
	}
	sendContext, reservation, admissionErr := notificationdelivery.Acquire(ctx, e.deps.Notifications)
	if retryAt, waiting := notificationdelivery.AdmissionRetryAt(admissionErr); waiting {
		if next != nil {
			*next = retryAt
		}
		return false, deferDailySummaryAdmission(ctx, repository, open.ID, "", retryAt, now)
	}
	defer releaseSummaryAdmission(ctx, reservation)
	claim, err := repository.ClaimDailySummaryDispatch(ctx, open.ID, now)
	if err != nil || claim == nil {
		return false, err
	}
	if claim.Dispatch.ClaimToken == nil {
		return false, store.ErrDailySummaryDispatchConflict
	}
	sendCtx, cancel := context.WithTimeout(sendContext, 25*time.Second)
	messageID, sendErr := notificationdelivery.Send(sendCtx, e.deps.Notifications, claim.Dispatch.Payload)
	cancel()
	finished := e.deps.Now().UTC()
	if retryAt, waiting := notificationdelivery.AdmissionRetryAt(sendErr); waiting {
		if next != nil {
			*next = retryAt
		}
		return false, deferDailySummaryAdmission(ctx, repository, claim.Dispatch.ID, *claim.Dispatch.ClaimToken, retryAt, finished)
	}
	// Persist a result after cancellation using a small independent cleanup
	// budget. A crash or failed result write leaves a durable sending marker;
	// expiry is unknown, never an implicit permission to resend.
	resultCtx, resultCancel := context.WithTimeout(context.WithoutCancel(ctx), 7*time.Second)
	defer resultCancel()
	token := *claim.Dispatch.ClaimToken
	if sendErr == nil {
		if err := repository.CompleteDailySummaryDispatch(resultCtx, claim.Dispatch.ID, token, messageID, finished); err != nil {
			// Retry only the same fenced database finalization, never the SDK.
			if retryErr := repository.CompleteDailySummaryDispatch(resultCtx, claim.Dispatch.ID, token, messageID, finished); retryErr == nil {
				return true, nil
			} else {
				markErr := repository.FailDailySummaryDispatch(resultCtx, claim.Dispatch.ID, token, store.DailySummaryDispatchUnknown, "send_confirmed_record_failed", nil, finished, messageID)
				return false, fmt.Errorf("daily_summary dispatch_id=%d accepted_message_id=%q finalization_unconfirmed: %w", claim.Dispatch.ID, messageID, errors.Join(err, retryErr, markErr))
			}
		}
		return true, nil
	}
	failure := notificationdelivery.Classify(sendErr)
	status := store.DailySummaryDispatchUnknown
	var retryAt *time.Time
	if failure.Kind == notificationdelivery.Retryable || failure.Kind == notificationdelivery.Rejected {
		status = store.DailySummaryDispatchFailed
		if failure.Kind == notificationdelivery.Retryable && claim.Dispatch.Attempts < notificationdelivery.MaxAttempts {
			next := finished.Add(notificationdelivery.RetryDelay(int(claim.Dispatch.Attempts), failure.RetryAfter))
			retryAt = &next
		}
	}
	reason := failure.Code
	if failure.RequestID != "" {
		reason += " [request_id=" + failure.RequestID + "]"
	}
	markErr := repository.FailDailySummaryDispatch(resultCtx, claim.Dispatch.ID, token, status, reason, retryAt, finished)
	return false, errors.Join(sendErr, markErr)
}

func releaseSummaryAdmission(ctx context.Context, reservation *notificationdelivery.Reservation) {
	if reservation == nil {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_ = reservation.Release(cleanupCtx)
}

func deferDailySummaryAdmission(ctx context.Context, repository dailySummaryDispatchRepository, id int64, token string, at, now time.Time) error {
	deferrer, ok := repository.(interface {
		DeferDailySummaryAdmission(context.Context, int64, string, time.Time, time.Time) error
	})
	if !ok {
		return errors.New("daily summary admission deferral repository unavailable")
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	return deferrer.DeferDailySummaryAdmission(cleanupCtx, id, token, at, now)
}

func (e *Executor) frozenSummaryPayload(snapshot store.NotificationDetailSnapshot) (notificationdelivery.Payload, error) {
	payload := notificationdelivery.Payload{ChatID: snapshot.NotificationChatID, ChatType: snapshot.NotificationChatType, Text: snapshot.NotificationText}
	if link := notificationdetail.NotificationURL(e.deps.PublicAppURL, snapshot.PublicID); link != "" {
		payload.ActionURL = link
		payload.ActionText = "查看完整日报"
		if normalizeLanguage(snapshot.NotificationLanguage) == "en" {
			payload.ActionText = "View full report"
		}
	}
	return payload, payload.Validate()
}
