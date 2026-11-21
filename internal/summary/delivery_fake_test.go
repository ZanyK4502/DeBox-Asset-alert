package summary

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func (f *fakeRepository) CheckDailySummaryLegacyReviews(context.Context, int64, int64, time.Time) (bool, error) {
	return false, nil
}

func (f *fakeRepository) ListDailySummaryDispatchTargets(ctx context.Context, subscriptionID, after int64, limit int) ([]store.DailySummaryTarget, error) {
	targets, err := f.ListDailySummaryTargets(ctx, subscriptionID)
	if err != nil {
		return nil, err
	}
	result := []store.DailySummaryTarget{}
	for i, target := range targets {
		if target.ID == 0 {
			target.ID = subscriptionID*10000 + int64(i) + 1
		}
		if progress, ok := f.targetProgress[target.ID]; ok {
			target.LastSentDate, target.LastPeriodEndAt, target.LastSentAt = progress.LastSentDate, progress.LastPeriodEndAt, progress.LastSentAt
		}
		if target.ID > after {
			result = append(result, target)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (f *fakeRepository) GetOpenDailySummaryDispatch(_ context.Context, subscriptionID int64, chatType, chatID string) (*store.DailySummaryDispatch, error) {
	for _, dispatch := range f.dispatches {
		if dispatch.SubscriptionID == subscriptionID && dispatch.ChatType == chatType && dispatch.ChatID == chatID && (dispatch.Status == "pending" || dispatch.Status == "sending" || dispatch.Status == "failed" || dispatch.Status == "unknown") {
			copy := *dispatch
			return &copy, nil
		}
	}
	return nil, nil
}

func (f *fakeRepository) PrepareDailySummaryDispatch(ctx context.Context, params store.PrepareDailySummaryDispatchParams) (*store.DailySummaryDispatch, error) {
	if open, _ := f.GetOpenDailySummaryDispatch(ctx, params.SubscriptionID, params.Snapshot.NotificationChatType, params.Snapshot.NotificationChatID); open != nil {
		return open, nil
	}
	if len(params.Snapshot.NotificationText) > notificationdelivery.MaxPayloadBytes || len(params.Snapshot.Details) > notificationdelivery.MaxSnapshotBytes {
		return nil, notificationdelivery.ErrCapacity
	}
	snapshot, err := f.CreateNotificationDetailSnapshot(ctx, params.Snapshot)
	if err != nil {
		return nil, err
	}
	payload, err := params.BuildPayload(snapshot)
	if err != nil {
		return nil, err
	}
	if f.dispatches == nil {
		f.dispatches = map[int64]*store.DailySummaryDispatch{}
	}
	if f.dispatchSnapshots == nil {
		f.dispatchSnapshots = map[int64]store.NotificationDetailSnapshot{}
	}
	id := int64(len(f.dispatches) + 1)
	f.dispatches[id] = &store.DailySummaryDispatch{ID: id, SubscriptionID: params.SubscriptionID, TargetID: params.TargetID, ChatType: payload.ChatType, ChatID: payload.ChatID, PeriodStartAt: params.PeriodStartAt, PeriodEndAt: params.PeriodEndAt, LocalDate: params.LocalDate, SnapshotID: &snapshot.ID, Payload: payload, Status: "pending", RetryAt: params.NotBefore}
	f.dispatchSnapshots[id] = snapshot
	copy := *f.dispatches[id]
	return &copy, nil
}

func (f *fakeRepository) ClaimDailySummaryDispatch(_ context.Context, id int64, now time.Time) (*store.DailySummaryDispatchClaim, error) {
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	d := f.dispatches[id]
	if d == nil {
		return nil, nil
	}
	if d.Status == "sending" {
		if d.LeaseUntil != nil && !d.LeaseUntil.After(now) {
			d.Status = "unknown"
			d.ClaimToken = nil
			d.LeaseUntil = nil
		}
		return nil, nil
	}
	if d.Status != "pending" && d.Status != "failed" || d.Status == "failed" && d.RetryAt == nil || d.RetryAt != nil && now.Before(*d.RetryAt) || d.Attempts >= notificationdelivery.MaxAttempts {
		return nil, nil
	}
	d.Attempts++
	token := fmt.Sprintf("claim-%d-%d", id, d.Attempts)
	lease := now.Add(store.DailySummaryClaimLease)
	d.Status, d.ClaimToken, d.LeaseUntil, d.AttemptedAt = "sending", &token, &lease, &now
	d.LastClaimToken = &token
	return &store.DailySummaryDispatchClaim{Dispatch: *d, Snapshot: f.dispatchSnapshots[id]}, nil
}

func (f *fakeRepository) CompleteDailySummaryDispatch(ctx context.Context, id int64, token, messageID string, now time.Time) error {
	if f.completeFailOnce {
		f.completeFailOnce = false
		return errors.New("transient finalize failure")
	}
	d := f.dispatches[id]
	if d != nil && d.Status == "sent" && d.MessageID != nil && *d.MessageID == messageID && d.LastClaimToken != nil && *d.LastClaimToken == token {
		return nil
	}
	if d == nil || d.Status != "sending" || d.ClaimToken == nil || *d.ClaimToken != token {
		return store.ErrDailySummaryDispatchConflict
	}
	if f.targetMarkErr != nil {
		return f.targetMarkErr
	}
	if f.markErr != nil {
		return f.markErr
	}
	target := store.DailySummaryTarget{ID: d.TargetID, SubscriptionID: d.SubscriptionID, ChatType: d.ChatType, ChatID: d.ChatID, LastSentDate: d.LocalDate, LastPeriodEndAt: &d.PeriodEndAt, LastSentAt: &now}
	if err := f.MarkDailySummaryTargetSent(ctx, d.SubscriptionID, d.PeriodEndAt, target); err != nil {
		return err
	}
	if err := f.MarkScheduledPushSent(ctx, d.SubscriptionID, d.LocalDate, d.PeriodEndAt); err != nil {
		return err
	}
	if f.targetProgress == nil {
		f.targetProgress = map[int64]store.DailySummaryTarget{}
	}
	f.targetProgress[d.TargetID] = target
	d.Status, d.ClaimToken, d.LeaseUntil, d.MessageID, d.SentAt = "sent", nil, nil, &messageID, &now
	return nil
}

func (f *fakeRepository) FailDailySummaryDispatch(_ context.Context, id int64, token, status, reason string, retryAt *time.Time, _ time.Time, acceptedMessageID ...string) error {
	d := f.dispatches[id]
	if d == nil || d.Status != "sending" || d.ClaimToken == nil || *d.ClaimToken != token {
		return store.ErrDailySummaryDispatchConflict
	}
	d.Status, d.ClaimToken, d.LeaseUntil, d.RetryAt, d.LastError = status, nil, nil, retryAt, reason
	if len(acceptedMessageID) > 0 {
		id := acceptedMessageID[0]
		d.MessageID = &id
	}
	return nil
}

func (f *fakeRepository) SupersedeDailySummaryDispatch(_ context.Context, id int64, newEnd, _ time.Time) (bool, error) {
	d := f.dispatches[id]
	if d == nil || (d.Status != "pending" && d.Status != "failed") || !d.PeriodEndAt.Before(newEnd) {
		return false, nil
	}
	d.Status = "superseded"
	return true, nil
}
