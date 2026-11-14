package management

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

var ErrSummaryMaintenanceChange = errors.New("维护期间只能关闭现有每日摘要，不能新增或修改配置。")

// DisableSummarySettings stops every existing summary target without changing
// its configuration or delivery progress. The HTTP caller must separately check
// that the request explicitly asks to stop: a bool cannot distinguish an omitted
// enabled field from an explicit false.
func (s *Service) DisableSummarySettings(
	ctx context.Context,
	deboxUserID string,
	input SummarySettingsInput,
) (SummarySettingsResult, error) {
	if input.Enabled {
		return SummarySettingsResult{}, ErrSummaryMaintenanceChange
	}
	for _, target := range input.Targets {
		if target.Enabled != nil && *target.Enabled {
			return SummarySettingsResult{}, ErrSummaryMaintenanceChange
		}
	}
	deboxUserID = strings.TrimSpace(deboxUserID)
	entitlement, err := s.deps.Entitlements.Entitlement(ctx, deboxUserID)
	if err != nil {
		return SummarySettingsResult{}, err
	}
	subscription := entitlement.Subscription
	if subscription == nil || subscription.DeBoxUserID != deboxUserID {
		return SummarySettingsResult{}, store.ErrNotFound
	}
	if err := unchangedSummaryMaintenanceFields(
		[3]string{"push_time", input.PushTime, subscription.DailySummaryTime},
		[3]string{"timezone", input.Timezone, subscription.DailySummaryTimezone},
		[3]string{"chat_type", input.ChatType, subscription.DailySummaryChatType},
		[3]string{"chat_id", input.ChatID, subscription.DailySummaryChatID},
		[3]string{"label", input.Label, subscription.DailySummaryLabel},
		[3]string{"language", input.Language, subscription.DailySummaryLanguage},
	); err != nil {
		return SummarySettingsResult{}, err
	}
	for _, requested := range input.Targets {
		chatType := strings.TrimSpace(requested.ChatType)
		chatID := strings.TrimSpace(requested.ChatID)
		if chatType == "private" && chatID == "" {
			chatID = deboxUserID
		}
		var existing *store.DailySummaryTarget
		for index := range entitlement.SummarySettings.Targets {
			target := &entitlement.SummarySettings.Targets[index]
			if target.ChatType == chatType && target.ChatID == chatID {
				existing = target
				break
			}
		}
		if existing == nil {
			return SummarySettingsResult{}, fmt.Errorf("%w 未找到现有推送对象。", ErrSummaryMaintenanceChange)
		}
		if err := unchangedSummaryMaintenanceFields(
			[3]string{"target.push_time", requested.PushTime, existing.PushTime},
			[3]string{"target.timezone", requested.Timezone, existing.Timezone},
			[3]string{"target.label", requested.Label, existing.Label},
			[3]string{"target.language", requested.Language, existing.Language},
		); err != nil {
			return SummarySettingsResult{}, err
		}
	}
	updated, err := s.deps.Repository.DisableDailySummarySettings(ctx, deboxUserID, subscription.ID)
	if err != nil {
		return SummarySettingsResult{}, err
	}
	entitlement, err = s.deps.Entitlements.Entitlement(ctx, deboxUserID)
	if err != nil {
		return SummarySettingsResult{}, err
	}
	return SummarySettingsResult{Subscription: updated, Entitlement: entitlement}, nil
}

func unchangedSummaryMaintenanceFields(fields ...[3]string) error {
	for _, field := range fields {
		if value := strings.TrimSpace(field[1]); value != "" && value != field[2] {
			return fmt.Errorf("%w 不允许更改 %s。", ErrSummaryMaintenanceChange, field[0])
		}
	}
	return nil
}
