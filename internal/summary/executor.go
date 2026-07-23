package summary

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const (
	defaultTimezone  = "Asia/Shanghai"
	defaultPageSize  = 100
	recentEventLimit = 5
)

var (
	ruleTypeLabels = map[string]string{
		"balance_change":      "余额变化",
		"incoming":            "转入",
		"outgoing":            "转出",
		"balance_threshold":   "余额阈值",
		"approval_change":     "授权变化",
		"address_interaction": "指定地址交互",
	}
	ruleTypeLabelsEN = map[string]string{
		"balance_change":      "Balance change",
		"incoming":            "Incoming transfer",
		"outgoing":            "Outgoing transfer",
		"balance_threshold":   "Balance threshold",
		"approval_change":     "Approval change",
		"address_interaction": "Specified address interaction",
	}
)

type Repository interface {
	ListDueScheduledSubscriptions(context.Context, int64, int) ([]store.Subscription, error)
	GetScheduledSubscription(context.Context, int64) (*store.Subscription, error)
	GetNotificationGroup(context.Context, string, string) (*store.NotificationGroup, error)
	DailySummaryStatistics(context.Context, string, time.Time, time.Time) (store.SummaryStatistics, error)
	ListSummaryRecentEvents(context.Context, string, time.Time, time.Time, int) ([]store.SummaryEvent, error)
	MarkScheduledPushSent(context.Context, int64, string, time.Time) error
}

type NotificationService interface {
	SendNotification(string, string, string) (string, error)
}

type Lock interface {
	Unlock(context.Context) error
}

type TryLockFunc func(context.Context, int64) (Lock, bool, error)

type Dependencies struct {
	Repository    Repository
	Notifications NotificationService
	TryLock       TryLockFunc
	Now           func() time.Time
}

type Executor struct {
	deps Dependencies
}

func New(dependencies Dependencies) *Executor {
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Executor{deps: dependencies}
}

type ErrorResult struct {
	SubscriptionID int64  `json:"subscription_id"`
	Error          string `json:"error"`
}

type CycleResult struct {
	Sent    int64         `json:"sent"`
	Skipped int64         `json:"skipped"`
	Locked  int64         `json:"locked"`
	Errors  []ErrorResult `json:"errors"`
}

func (e *Executor) SendDue(ctx context.Context, limit int) (CycleResult, error) {
	result := CycleResult{Errors: make([]ErrorResult, 0)}
	afterID := int64(0)
	pageSize := clamp(limit, 1, 1000)

	for {
		subscriptions, err := e.deps.Repository.ListDueScheduledSubscriptions(
			ctx,
			afterID,
			pageSize,
		)
		if err != nil {
			return result, err
		}
		if len(subscriptions) == 0 {
			return result, nil
		}
		afterID = subscriptions[len(subscriptions)-1].ID

		for _, candidate := range subscriptions {
			itemStatus, itemErr := e.processCandidate(ctx, candidate.ID)
			switch itemStatus {
			case "sent":
				result.Sent++
			case "skipped":
				result.Skipped++
			case "locked":
				result.Locked++
			}
			if itemErr != nil {
				result.Errors = append(result.Errors, ErrorResult{
					SubscriptionID: candidate.ID,
					Error:          itemErr.Error(),
				})
			}
		}
	}
}

func (e *Executor) processCandidate(ctx context.Context, subscriptionID int64) (string, error) {
	if e.deps.TryLock == nil {
		return "error", errors.New("summary lock is not configured")
	}
	lock, acquired, err := e.deps.TryLock(ctx, subscriptionID)
	if err != nil {
		return "error", err
	}
	if !acquired {
		return "locked", nil
	}

	status, processErr := e.processLocked(ctx, subscriptionID)
	unlockErr := lock.Unlock(ctx)
	if processErr != nil && unlockErr != nil {
		return status, errors.Join(processErr, unlockErr)
	}
	if processErr != nil {
		return status, processErr
	}
	if unlockErr != nil {
		return status, unlockErr
	}
	return status, nil
}

func (e *Executor) processLocked(ctx context.Context, subscriptionID int64) (string, error) {
	subscription, err := e.deps.Repository.GetScheduledSubscription(ctx, subscriptionID)
	if err != nil {
		return "error", err
	}
	if subscription == nil {
		return "skipped", nil
	}

	due, localDate, periodEnd := summaryDue(*subscription, e.deps.Now())
	if !due {
		return "skipped", nil
	}
	chatID, chatType, err := e.summaryTarget(ctx, *subscription)
	if err != nil {
		return "error", err
	}
	periodStart, periodEnd := summaryPeriod(*subscription, periodEnd)
	text, err := e.summaryText(ctx, *subscription, periodStart, periodEnd)
	if err != nil {
		return "error", err
	}
	if _, err := e.deps.Notifications.SendNotification(chatID, chatType, text); err != nil {
		return "error", err
	}
	if err := e.deps.Repository.MarkScheduledPushSent(
		ctx,
		subscription.ID,
		localDate,
		periodEnd,
	); err != nil {
		return "error", err
	}
	return "sent", nil
}

func summaryDue(subscription store.Subscription, now time.Time) (bool, string, time.Time) {
	location, _ := loadLocation(subscription.DailySummaryTimezone)
	localNow := now.In(location)
	localDate := localNow.Format("2006-01-02")
	hour, minute := parsePushTime(subscription.DailySummaryTime)
	periodEnd := time.Date(
		localNow.Year(),
		localNow.Month(),
		localNow.Day(),
		hour,
		minute,
		0,
		0,
		location,
	)
	if subscription.DailySummaryLastSentDate == localDate {
		return false, localDate, periodEnd.UTC()
	}
	return !localNow.Before(periodEnd), localDate, periodEnd.UTC()
}

func summaryPeriod(subscription store.Subscription, periodEnd time.Time) (time.Time, time.Time) {
	periodStart := subscription.DailySummaryLastPeriodEndAt
	if periodStart == nil || !periodStart.Before(periodEnd) {
		return periodEnd.Add(-24 * time.Hour), periodEnd
	}
	return periodStart.UTC(), periodEnd
}

func (e *Executor) summaryTarget(
	ctx context.Context,
	subscription store.Subscription,
) (string, string, error) {
	userID := strings.TrimSpace(subscription.DeBoxUserID)
	if userID == "" {
		return "", "", errors.New("摘要订阅缺少 DeBox 用户 ID。")
	}
	chatType := strings.ToLower(strings.TrimSpace(subscription.DailySummaryChatType))
	if chatType == "" || chatType == "private" {
		return userID, "private", nil
	}
	if chatType != "group" {
		return "", "", errors.New("摘要通知目标类型无效。")
	}
	chatID := strings.TrimSpace(subscription.DailySummaryChatID)
	if chatID == "" {
		return "", "", errors.New("摘要目标群已解绑或不可用。")
	}
	group, err := e.deps.Repository.GetNotificationGroup(ctx, userID, chatID)
	if err != nil {
		return "", "", err
	}
	if group == nil {
		return "", "", errors.New("摘要目标群已解绑或不可用。")
	}
	return chatID, "group", nil
}

func (e *Executor) summaryText(
	ctx context.Context,
	subscription store.Subscription,
	periodStart time.Time,
	periodEnd time.Time,
) (string, error) {
	statistics, err := e.deps.Repository.DailySummaryStatistics(
		ctx,
		subscription.DeBoxUserID,
		periodStart,
		periodEnd,
	)
	if err != nil {
		return "", err
	}
	events, err := e.deps.Repository.ListSummaryRecentEvents(
		ctx,
		subscription.DeBoxUserID,
		periodStart,
		periodEnd,
		recentEventLimit,
	)
	if err != nil {
		return "", err
	}
	return buildSummaryText(subscription, periodStart, periodEnd, statistics, events), nil
}

func buildSummaryText(
	subscription store.Subscription,
	periodStart time.Time,
	periodEnd time.Time,
	statistics store.SummaryStatistics,
	events []store.SummaryEvent,
) string {
	english := normalizeLanguage(subscription.DailySummaryLanguage) == "en"
	periodText := periodLabel(
		periodStart,
		periodEnd,
		subscription.DailySummaryTimezone,
		english,
	)
	summaryLabel := strings.TrimSpace(subscription.DailySummaryLabel)
	recentText := recentEventsText(events, english)

	if english {
		title := "DeBox Asset Alert Daily Summary"
		if summaryLabel != "" {
			title += " · " + html.EscapeString(summaryLabel)
		}
		alertHint := "None"
		if statistics.EventCount > 0 {
			alertHint = fmt.Sprintf(
				"%d alerts were triggered this period. Review the recent events below.",
				statistics.EventCount,
			)
		}
		return fmt.Sprintf(
			"<b>%s</b><br/>"+
				"Period: %s<br/>"+
				"Alerts this period: %d<br/>"+
				"Notification failures: %d<br/>"+
				"Monitored wallets: %d<br/>"+
				"Running rules: %d<br/>"+
				"Rules: Assets %d, approvals %d, interactions %d<br/>"+
				"Events: Assets %d, approvals %d, interactions %d<br/>"+
				"Risk notice: %s<br/><br/>"+
				"<b>Recent events</b><br/>%s",
			title,
			html.EscapeString(periodText),
			statistics.EventCount,
			statistics.FailedNotificationCount,
			statistics.WalletCount,
			statistics.RuleCount,
			statistics.AssetRuleCount,
			statistics.ApprovalRuleCount,
			statistics.InteractionRuleCount,
			statistics.AssetEventCount,
			statistics.ApprovalEventCount,
			statistics.InteractionEventCount,
			html.EscapeString(alertHint),
			recentText,
		)
	}

	title := "DeBox Asset Alert 每日摘要"
	if summaryLabel != "" {
		title += " · " + html.EscapeString(summaryLabel)
	}
	alertHint := "无"
	if statistics.EventCount > 0 {
		alertHint = fmt.Sprintf(
			"本期共触发 %d 次提醒，请查看下方最近事件。",
			statistics.EventCount,
		)
	}
	return fmt.Sprintf(
		"<b>%s</b><br/>"+
			"统计周期：%s<br/>"+
			"本期触发次数：%d<br/>"+
			"通知失败次数：%d<br/>"+
			"监控钱包数：%d<br/>"+
			"运行规则数：%d<br/>"+
			"资产规则：%d，授权规则：%d，交互规则：%d<br/>"+
			"事件概览：资产 %d，授权 %d，交互 %d<br/>"+
			"异常提醒：%s<br/><br/>"+
			"<b>最近事件</b><br/>%s",
		title,
		html.EscapeString(periodText),
		statistics.EventCount,
		statistics.FailedNotificationCount,
		statistics.WalletCount,
		statistics.RuleCount,
		statistics.AssetRuleCount,
		statistics.ApprovalRuleCount,
		statistics.InteractionRuleCount,
		statistics.AssetEventCount,
		statistics.ApprovalEventCount,
		statistics.InteractionEventCount,
		html.EscapeString(alertHint),
		recentText,
	)
}

func recentEventsText(events []store.SummaryEvent, english bool) string {
	if len(events) == 0 {
		if english {
			return "No alerts were triggered this period."
		}
		return "本期暂无触发事件。"
	}
	labels := ruleTypeLabels
	separator := "："
	if english {
		labels = ruleTypeLabelsEN
		separator = ": "
	}
	lines := make([]string, 0, min(len(events), recentEventLimit))
	for _, event := range events[:min(len(events), recentEventLimit)] {
		label := labels[event.EventType]
		if label == "" {
			label = event.EventType
		}
		lines = append(lines, fmt.Sprintf(
			"- %s%s%s %s -> %s",
			html.EscapeString(label),
			separator,
			html.EscapeString(shortAddress(event.WalletAddress)),
			html.EscapeString(valueOrDash(event.PreviousValue)),
			html.EscapeString(valueOrDash(event.CurrentValue)),
		))
	}
	return strings.Join(lines, "<br/>")
}

func periodLabel(
	periodStart time.Time,
	periodEnd time.Time,
	timezoneName string,
	english bool,
) string {
	location, normalizedName := loadLocation(timezoneName)
	startText := periodStart.In(location).Format("2006-01-02 15:04")
	endText := periodEnd.In(location).Format("2006-01-02 15:04")
	if english {
		return fmt.Sprintf("%s to %s (%s)", startText, endText, normalizedName)
	}
	return fmt.Sprintf("%s 至 %s（%s）", startText, endText, normalizedName)
}

func loadLocation(timezoneName string) (*time.Location, string) {
	timezoneName = strings.TrimSpace(timezoneName)
	if timezoneName == "" {
		timezoneName = defaultTimezone
	}
	location, err := time.LoadLocation(timezoneName)
	if err == nil {
		return location, timezoneName
	}
	location, _ = time.LoadLocation(defaultTimezone)
	return location, defaultTimezone
}

func parsePushTime(value string) (int, int) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) == 2 {
		hour, hourErr := strconv.Atoi(parts[0])
		minute, minuteErr := strconv.Atoi(parts[1])
		if hourErr == nil && minuteErr == nil &&
			hour >= 0 && hour <= 23 && minute >= 0 && minute <= 59 {
			return hour, minute
		}
	}
	return 20, 0
}

func normalizeLanguage(value string) string {
	if strings.ToLower(strings.TrimSpace(value)) == "en" {
		return "en"
	}
	return "zh"
}

func shortAddress(value string) string {
	if value == "" {
		return "-"
	}
	if len(value) <= 16 {
		return value
	}
	return value[:8] + "..." + value[len(value)-6:]
}

func valueOrDash(value *string) string {
	if value == nil || *value == "" {
		return "-"
	}
	return *value
}

func clamp(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}
