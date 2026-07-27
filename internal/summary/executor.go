package summary

import (
	"context"
	"errors"
	"fmt"
	"html"
	"math/big"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const (
	defaultTimezone  = "Asia/Shanghai"
	defaultPageSize  = 100
	recentEventLimit = 5
)

var (
	ruleTypeLabels = map[string]string{
		"balance_change":         "余额变化",
		"incoming":               "转入",
		"outgoing":               "转出",
		"balance_threshold":      "低余额阈值",
		"balance_threshold_high": "高余额阈值",
		"approval_change":        "授权变化",
		"address_interaction":    "指定地址交互",
	}
	ruleTypeLabelsEN = map[string]string{
		"balance_change":         "Balance change",
		"incoming":               "Incoming transfer",
		"outgoing":               "Outgoing transfer",
		"balance_threshold":      "Low balance threshold",
		"balance_threshold_high": "High balance threshold",
		"approval_change":        "Approval change",
		"address_interaction":    "Specified address interaction",
	}
	marketEventLabels = map[string]string{
		"buy": "买入", "sell": "卖出",
		"liquidity_added": "加池", "liquidity_removed": "撤池",
		"holder_increase": "大户增持", "holder_decrease": "大户减持",
		"holder_rank_entered": "进入大户榜", "holder_rank_exited": "退出大户榜",
		"pool_initialized": "新交易池", "migrated": "迁移外盘",
	}
	marketEventLabelsEN = map[string]string{
		"buy": "Buy", "sell": "Sell",
		"liquidity_added": "Liquidity added", "liquidity_removed": "Liquidity removed",
		"holder_increase": "Holder increase", "holder_decrease": "Holder decrease",
		"holder_rank_entered": "Entered holder ranking", "holder_rank_exited": "Exited holder ranking",
		"pool_initialized": "New pool", "migrated": "Migration",
	}
)

type Repository interface {
	ListDueScheduledSubscriptions(context.Context, int64, int) ([]store.Subscription, error)
	GetScheduledSubscription(context.Context, int64) (*store.Subscription, error)
	ListDailySummaryTargets(context.Context, int64) ([]store.DailySummaryTarget, error)
	ListPendingDailySummaryTargets(context.Context, int64, time.Time) ([]store.DailySummaryTarget, error)
	MarkDailySummaryTargetSent(context.Context, int64, time.Time, store.DailySummaryTarget) error
	DailySummaryStatistics(context.Context, string, time.Time, time.Time) (store.SummaryStatistics, error)
	ListSummaryRecentEvents(context.Context, string, time.Time, time.Time, int) ([]store.SummaryEvent, error)
	ListSummaryRecentMarketEvents(context.Context, string, time.Time, time.Time, int) ([]store.MarketSummaryEvent, error)
	ListDailyMarketProjectChainSummaries(context.Context, string, time.Time, time.Time) ([]store.MarketProjectChainSummary, error)
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
	periodStart, periodEnd := summaryPeriod(*subscription, periodEnd)
	text, err := e.summaryText(ctx, *subscription, periodStart, periodEnd)
	if err != nil {
		return "error", err
	}
	targets, err := e.deps.Repository.ListDailySummaryTargets(ctx, subscription.ID)
	if err != nil {
		return "error", err
	}
	if len(targets) == 0 {
		return "error", errors.New("每日摘要没有可用的推送对象。")
	}
	pendingTargets, err := e.deps.Repository.ListPendingDailySummaryTargets(
		ctx,
		subscription.ID,
		periodEnd,
	)
	if err != nil {
		return "error", err
	}
	for _, target := range pendingTargets {
		if _, err := e.deps.Notifications.SendNotification(
			target.ChatID,
			target.ChatType,
			text,
		); err != nil {
			return "error", err
		}
		if err := e.deps.Repository.MarkDailySummaryTargetSent(
			ctx,
			subscription.ID,
			periodEnd,
			target,
		); err != nil {
			return "error", err
		}
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
	marketEvents, err := e.deps.Repository.ListSummaryRecentMarketEvents(
		ctx,
		subscription.DeBoxUserID,
		periodStart,
		periodEnd,
		recentEventLimit,
	)
	if err != nil {
		return "", err
	}
	marketSummaries, err := e.deps.Repository.ListDailyMarketProjectChainSummaries(
		ctx,
		subscription.DeBoxUserID,
		periodStart,
		periodEnd,
	)
	if err != nil {
		return "", err
	}
	return buildSummaryText(
		subscription,
		periodStart,
		periodEnd,
		statistics,
		events,
		marketEvents,
		marketSummaries,
	), nil
}

func buildSummaryText(
	subscription store.Subscription,
	periodStart time.Time,
	periodEnd time.Time,
	statistics store.SummaryStatistics,
	events []store.SummaryEvent,
	marketEvents []store.MarketSummaryEvent,
	marketSummaries []store.MarketProjectChainSummary,
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
	recentMarketText := recentMarketEventsText(marketEvents, english)
	marketProjectsText := marketProjectSummariesText(marketSummaries, english)

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
				"Market monitoring: %d projects, %d running rules<br/>"+
				"Market trades: %d buys ($%s), %d sells ($%s), net buy $%s<br/>"+
				"Market events: %d total, %d liquidity, %d holder changes<br/>"+
				"Risk notice: %s<br/><br/>"+
				"<b>Recent address events</b><br/>%s<br/><br/>"+
				"%s<br/><br/>"+
				"<b>Recent market events</b><br/>%s",
			title,
			html.EscapeString(periodText),
			statistics.EventCount,
			statistics.FailedNotificationCount+statistics.MarketFailedNotificationCount,
			statistics.WalletCount,
			statistics.RuleCount,
			statistics.AssetRuleCount,
			statistics.ApprovalRuleCount,
			statistics.InteractionRuleCount,
			statistics.AssetEventCount,
			statistics.ApprovalEventCount,
			statistics.InteractionEventCount,
			statistics.MarketProjectCount,
			statistics.MarketRuleCount,
			statistics.MarketBuyCount,
			moneyText(statistics.MarketBuyUSD),
			statistics.MarketSellCount,
			moneyText(statistics.MarketSellUSD),
			signedMoneyText(statistics.MarketNetBuyUSD),
			statistics.MarketEventCount,
			statistics.LiquidityEventCount,
			statistics.HolderEventCount,
			html.EscapeString(alertHint),
			recentText,
			marketProjectsText,
			recentMarketText,
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
			"市场监控：%d 个项目币，%d 条运行规则<br/>"+
			"市场成交：买入 %d 笔（$%s），卖出 %d 笔（$%s），净买入 $%s<br/>"+
			"市场事件：共 %d 条，流动性 %d 条，大户变化 %d 条<br/>"+
			"异常提醒：%s<br/><br/>"+
			"<b>最近地址事件</b><br/>%s<br/><br/>"+
			"%s<br/><br/>"+
			"<b>最近市场事件</b><br/>%s",
		title,
		html.EscapeString(periodText),
		statistics.EventCount,
		statistics.FailedNotificationCount+statistics.MarketFailedNotificationCount,
		statistics.WalletCount,
		statistics.RuleCount,
		statistics.AssetRuleCount,
		statistics.ApprovalRuleCount,
		statistics.InteractionRuleCount,
		statistics.AssetEventCount,
		statistics.ApprovalEventCount,
		statistics.InteractionEventCount,
		statistics.MarketProjectCount,
		statistics.MarketRuleCount,
		statistics.MarketBuyCount,
		moneyText(statistics.MarketBuyUSD),
		statistics.MarketSellCount,
		moneyText(statistics.MarketSellUSD),
		signedMoneyText(statistics.MarketNetBuyUSD),
		statistics.MarketEventCount,
		statistics.LiquidityEventCount,
		statistics.HolderEventCount,
		html.EscapeString(alertHint),
		recentText,
		marketProjectsText,
		recentMarketText,
	)
}

func marketProjectSummariesText(
	summaries []store.MarketProjectChainSummary,
	english bool,
) string {
	if len(summaries) == 0 {
		if english {
			return "<b>Token project reports</b><br/>No active token projects."
		}
		return "<b>项目币日报</b><br/>暂无启用中的项目币。"
	}
	lines := make([]string, 0, len(summaries)*5)
	currentProjectID := int64(0)
	for _, item := range summaries {
		if item.MarketProjectID != currentProjectID {
			if currentProjectID != 0 {
				lines = append(lines, "")
			}
			currentProjectID = item.MarketProjectID
			name := strings.TrimSpace(item.TokenName)
			if name == "" {
				name = item.TokenSymbol
			}
			title := name + " 项目币日报"
			if english {
				title = name + " token report"
			}
			lines = append(lines, "<b>"+html.EscapeString(title)+"</b>")
		}
		chainName := marketChainName(item.ChainKey)
		lines = append(lines, fmt.Sprintf(
			"<b>%s</b> · %s",
			html.EscapeString(chainName),
			html.EscapeString(item.TokenAddress),
		))
		if english {
			lines = append(lines,
				"Price change: "+html.EscapeString(priceChangeText(item.StartPriceUSD, item.EndPriceUSD)),
				fmt.Sprintf(
					"Volume: $%s (buys %d / sells %d)",
					moneyText(item.TradeVolumeUSD),
					item.BuyCount,
					item.SellCount,
				),
				fmt.Sprintf("Large trades: %d", item.LargeTradeCount),
				fmt.Sprintf(
					"Holder changes: increase %d, decrease %d, entered %d, exited %d",
					item.HolderIncreaseCount,
					item.HolderDecreaseCount,
					item.HolderRankEnterCount,
					item.HolderRankExitCount,
				),
			)
		} else {
			lines = append(lines,
				"价格变化："+html.EscapeString(priceChangeText(item.StartPriceUSD, item.EndPriceUSD)),
				fmt.Sprintf(
					"成交量：$%s（买入 %d 笔 / 卖出 %d 笔）",
					moneyText(item.TradeVolumeUSD),
					item.BuyCount,
					item.SellCount,
				),
				fmt.Sprintf("大额买卖：%d 笔", item.LargeTradeCount),
				fmt.Sprintf(
					"大户变化：增持 %d，减持 %d，进榜 %d，退榜 %d",
					item.HolderIncreaseCount,
					item.HolderDecreaseCount,
					item.HolderRankEnterCount,
					item.HolderRankExitCount,
				),
			)
		}
	}
	return strings.Join(lines, "<br/>")
}

func priceChangeText(start, end *string) string {
	if start == nil || end == nil ||
		strings.TrimSpace(*start) == "" || strings.TrimSpace(*end) == "" {
		return "-"
	}
	startValue, startOK := new(big.Rat).SetString(*start)
	endValue, endOK := new(big.Rat).SetString(*end)
	if !startOK || !endOK {
		return "$" + moneyText(*start) + " → $" + moneyText(*end)
	}
	result := "$" + moneyText(*start) + " → $" + moneyText(*end)
	if startValue.Sign() == 0 {
		return result
	}
	change := new(big.Rat).Sub(endValue, startValue)
	change.Quo(change, startValue)
	change.Mul(change, big.NewRat(100, 1))
	changeText := change.FloatString(2)
	if change.Sign() > 0 {
		changeText = "+" + changeText
	}
	return result + " (" + changeText + "%)"
}

func recentMarketEventsText(events []store.MarketSummaryEvent, english bool) string {
	if len(events) == 0 {
		if english {
			return "No market events were recorded this period."
		}
		return "本期暂无市场事件。"
	}
	labels := marketEventLabels
	if english {
		labels = marketEventLabelsEN
	}
	lines := make([]string, 0, min(len(events), recentEventLimit))
	for _, event := range events[:min(len(events), recentEventLimit)] {
		label := labels[event.EventType]
		if label == "" {
			label = event.EventType
		}
		detail := ""
		if event.USDValue != nil && strings.TrimSpace(*event.USDValue) != "" {
			detail = " $" + moneyText(*event.USDValue)
		} else if event.TokenAmount != nil && strings.TrimSpace(*event.TokenAmount) != "" {
			detail = " " + *event.TokenAmount
		}
		wallet := ""
		if event.WalletAddress != nil && strings.TrimSpace(*event.WalletAddress) != "" {
			wallet = " · " + shortAddress(*event.WalletAddress)
		}
		lines = append(lines, fmt.Sprintf(
			"- %s · %s · %s · %s%s%s",
			html.EscapeString(event.TokenSymbol),
			html.EscapeString(marketChainName(event.ChainKey)),
			html.EscapeString(marketPoolSummary(event)),
			html.EscapeString(label),
			html.EscapeString(detail),
			html.EscapeString(wallet),
		))
	}
	return strings.Join(lines, "<br/>")
}

func marketPoolSummary(event store.MarketSummaryEvent) string {
	dex := strings.TrimSpace(event.Protocol + " " + event.ProtocolVersion)
	pool := valueOrDash(event.PoolAddress)
	if dex == "" {
		return pool
	}
	return dex + " / " + pool
}

func marketChainName(chainKey string) string {
	if strings.TrimSpace(chainKey) == "" {
		return "-"
	}
	profile, err := chain.ChainProfile(chainKey, "")
	if err == nil {
		return profile.Name
	}
	return chainKey
}

func moneyText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "0"
	}
	number, ok := new(big.Rat).SetString(value)
	if !ok {
		return value
	}
	text := number.FloatString(2)
	return strings.TrimRight(strings.TrimRight(text, "0"), ".")
}

func signedMoneyText(value string) string {
	value = moneyText(value)
	if value != "0" && !strings.HasPrefix(value, "-") {
		return "+" + value
	}
	return value
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
