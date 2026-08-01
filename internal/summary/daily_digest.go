package summary

import (
	"fmt"
	"html"
	"math/big"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationfmt"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const maxDailyDigestHighlights = 3

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
	group := strings.EqualFold(
		strings.TrimSpace(subscription.DailySummaryChatType),
		"group",
	)
	dataGap := dailySummaryDataGap(statistics, marketSummaries, english)
	notificationFailures := statistics.FailedNotificationCount +
		statistics.MarketFailedNotificationCount
	hasActivity := dailySummaryHasActivity(statistics)

	lines := []string{
		dailySummaryTitle(subscription, group, english),
		dailySummaryKeyValue(
			dailySummaryLabel("今日结论", "Today's conclusion", english),
			dailySummaryConclusion(
				statistics,
				notificationFailures,
				dataGap != "",
				hasActivity,
				english,
			),
			english,
		),
	}

	if hasActivity || notificationFailures > 0 {
		highlights := dailySummaryHighlights(
			statistics,
			events,
			marketEvents,
			marketSummaries,
			group,
			english,
		)
		for index, highlight := range highlights {
			lines = append(lines, dailySummaryKeyValue(
				fmt.Sprintf(
					dailySummaryLabel("重点 %d", "Highlight %d", english),
					index+1,
				),
				highlight,
				english,
			))
		}
		lines = append(
			lines,
			dailySummaryRiskOverview(statistics, notificationFailures, english),
			dailySummaryFundsDirection(statistics, english),
		)
	}

	lines = append(
		lines,
		dailySummaryRuntime(statistics, english),
		dataGap,
		dailySummaryKeyValue(
			dailySummaryLabel("统计周期", "Period", english),
			html.EscapeString(periodLabel(
				periodStart,
				periodEnd,
				subscription.DailySummaryTimezone,
				english,
			)),
			english,
		),
	)
	return notificationfmt.JoinBlocks(lines...)
}

func dailySummaryTitle(
	subscription store.Subscription,
	group bool,
	english bool,
) string {
	title := "📊 每日摘要"
	if group {
		title = "📊 群组每日摘要"
	}
	if english {
		title = "📊 Daily Summary"
		if group {
			title = "📊 Group Daily Summary"
		}
	}
	if label := strings.TrimSpace(subscription.DailySummaryLabel); label != "" {
		title += " · " + html.EscapeString(label)
	}
	return "<b>" + title + "</b>"
}

func dailySummaryConclusion(
	statistics store.SummaryStatistics,
	notificationFailures int64,
	hasDataGap bool,
	hasActivity bool,
	english bool,
) string {
	if notificationFailures > 0 {
		if english {
			if notificationFailures == 1 {
				return "🔴 1 notification delivery needs attention."
			}
			return fmt.Sprintf(
				"🔴 %d notification deliveries need attention.",
				notificationFailures,
			)
		}
		return fmt.Sprintf("🔴 有 %d 条通知发送失败，需要处理。", notificationFailures)
	}
	if statistics.AddressRiskEventCount > 0 || statistics.MarketAnomalyCount > 0 {
		parts := make([]string, 0, 2)
		if statistics.AddressRiskEventCount > 0 {
			if english {
				parts = append(parts, dailySummaryCount(
					statistics.AddressRiskEventCount,
					"address risk",
					"address risks",
				))
			} else {
				parts = append(parts, fmt.Sprintf(
					"%d 个地址风险",
					statistics.AddressRiskEventCount,
				))
			}
		}
		if statistics.MarketAnomalyCount > 0 {
			if english {
				parts = append(parts, dailySummaryCount(
					statistics.MarketAnomalyCount,
					"market anomaly",
					"market anomalies",
				))
			} else {
				parts = append(parts, fmt.Sprintf(
					"%d 个市场异常",
					statistics.MarketAnomalyCount,
				))
			}
		}
		if english {
			return "🟠 <b>Attention needed</b>: " + strings.Join(parts, " and ") + "."
		}
		return "🟠 <b>需要关注</b>：" + strings.Join(parts, "、") + "。"
	}
	if hasDataGap {
		if english {
			return "🟡 No confirmed anomaly, but part of the market data was unavailable."
		}
		return "🟡 暂未发现明确异常，但部分市场数据缺失。"
	}
	if statistics.WalletCount == 0 &&
		statistics.MarketProjectCount == 0 &&
		statistics.RuleCount == 0 &&
		statistics.MarketRuleCount == 0 {
		if english {
			return "⚪ No monitoring items are enabled, so no checks were run today."
		}
		return "⚪ 暂无启用的监控项，今日未执行监控。"
	}
	if hasActivity {
		if english {
			return "🟢 Activity was recorded with no triggered risk alert."
		}
		return "🟢 今日有监控动态，未触发风险提醒。"
	}
	if english {
		return "🟢 All monitored items were normal today."
	}
	return "🟢 今日监控正常，无需处理。"
}

func dailySummaryHighlights(
	statistics store.SummaryStatistics,
	events []store.SummaryEvent,
	marketEvents []store.MarketSummaryEvent,
	marketSummaries []store.MarketProjectChainSummary,
	group bool,
	english bool,
) []string {
	highlights := make([]string, 0, maxDailyDigestHighlights)
	appendHighlight := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || len(highlights) >= maxDailyDigestHighlights {
			return
		}
		for _, existing := range highlights {
			if existing == value {
				return
			}
		}
		highlights = append(highlights, value)
	}

	failures := statistics.FailedNotificationCount +
		statistics.MarketFailedNotificationCount
	if failures > 0 {
		if english {
			appendHighlight(
				"Notification delivery failed " +
					dailySummaryCount(failures, "time", "times") +
					"; check the delivery channel.",
			)
		} else {
			appendHighlight(fmt.Sprintf(
				"通知发送失败 %d 次，请检查接收渠道。",
				failures,
			))
		}
	}
	appendHighlight(dailySummaryAddressRiskHighlight(
		statistics,
		events,
		group,
		english,
	))
	appendHighlight(dailySummaryLargestPriceMove(marketSummaries, english))
	appendHighlight(dailySummaryLargestTrade(marketEvents, english))

	if statistics.MarketAnomalyCount > 0 {
		if english {
			appendHighlight(
				"Market rules triggered " + dailySummaryCount(
					statistics.MarketAnomalyCount,
					"anomaly alert",
					"anomaly alerts",
				) + ".",
			)
		} else {
			appendHighlight(fmt.Sprintf(
				"市场规则共触发 %d 个异常提醒。",
				statistics.MarketAnomalyCount,
			))
		}
	}
	if statistics.LiquidityEventCount > 0 || statistics.HolderEventCount > 0 {
		parts := make([]string, 0, 2)
		if statistics.LiquidityEventCount > 0 {
			if english {
				parts = append(parts, dailySummaryCount(
					statistics.LiquidityEventCount,
					"liquidity change",
					"liquidity changes",
				))
			} else {
				parts = append(parts, fmt.Sprintf(
					"%d 次流动性变化",
					statistics.LiquidityEventCount,
				))
			}
		}
		if statistics.HolderEventCount > 0 {
			if english {
				parts = append(parts, dailySummaryCount(
					statistics.HolderEventCount,
					"large-holder change",
					"large-holder changes",
				))
			} else {
				parts = append(parts, fmt.Sprintf(
					"%d 次大户变化",
					statistics.HolderEventCount,
				))
			}
		}
		if english {
			appendHighlight("<b>Market activity</b>: " + strings.Join(parts, ", ") + ".")
		} else {
			appendHighlight("<b>市场动态</b>：" + strings.Join(parts, "，") + "。")
		}
	}
	if statistics.EventCount > 0 {
		if english {
			appendHighlight(
				"Address monitoring recorded " + dailySummaryCount(
					statistics.EventCount,
					"event",
					"events",
				) + ".",
			)
		} else {
			appendHighlight(fmt.Sprintf(
				"地址监控共记录 %d 个事件。",
				statistics.EventCount,
			))
		}
	}
	if statistics.MarketEventCount > 0 {
		if english {
			appendHighlight(
				"Market monitoring recorded " + dailySummaryCount(
					statistics.MarketEventCount,
					"event",
					"events",
				) + ".",
			)
		} else {
			appendHighlight(fmt.Sprintf(
				"市场监控共记录 %d 个事件。",
				statistics.MarketEventCount,
			))
		}
	}

	return highlights
}

func dailySummaryAddressRiskHighlight(
	statistics store.SummaryStatistics,
	events []store.SummaryEvent,
	group bool,
	english bool,
) string {
	if statistics.AddressRiskEventCount == 0 {
		return ""
	}
	if group {
		if english {
			return fmt.Sprintf(
				"Address monitoring detected %s; wallet details are hidden in group summaries.",
				dailySummaryCount(
					statistics.AddressRiskEventCount,
					"risk event",
					"risk events",
				),
			)
		}
		return fmt.Sprintf(
			"地址监控发现 %d 个风险事件；群组摘要已隐藏钱包信息。",
			statistics.AddressRiskEventCount,
		)
	}
	for _, event := range events {
		if !dailySummaryRiskEvent(event.EventType) {
			continue
		}
		wallet := shortAddress(event.WalletAddress)
		if wallet == "-" {
			break
		}
		label := dailySummaryRuleLabel(event.EventType, english)
		if english {
			return fmt.Sprintf(
				"%s triggered for wallet %s.",
				html.EscapeString(label),
				html.EscapeString(wallet),
			)
		}
		return fmt.Sprintf(
			"钱包 %s 触发“%s”。",
			html.EscapeString(wallet),
			html.EscapeString(label),
		)
	}
	if english {
		return fmt.Sprintf(
			"Address monitoring detected %s.",
			dailySummaryCount(
				statistics.AddressRiskEventCount,
				"risk event",
				"risk events",
			),
		)
	}
	return fmt.Sprintf(
		"地址监控发现 %d 个风险事件。",
		statistics.AddressRiskEventCount,
	)
}

func dailySummaryLargestPriceMove(
	summaries []store.MarketProjectChainSummary,
	english bool,
) string {
	var selected *store.MarketProjectChainSummary
	var selectedChange *big.Rat
	for index := range summaries {
		item := &summaries[index]
		change, ok := dailySummaryPriceChange(item.StartPriceUSD, item.EndPriceUSD)
		if !ok {
			continue
		}
		absolute := new(big.Rat).Abs(new(big.Rat).Set(change))
		if selectedChange == nil ||
			absolute.Cmp(new(big.Rat).Abs(new(big.Rat).Set(selectedChange))) > 0 {
			selected = item
			selectedChange = change
		}
	}
	if selected == nil || selectedChange == nil || selectedChange.Sign() == 0 {
		return ""
	}
	token := dailySummaryTokenName(*selected)
	changeText := selectedChange.FloatString(2)
	if selectedChange.Sign() > 0 {
		changeText = "+" + changeText
	}
	if english {
		return fmt.Sprintf(
			"<b>Largest price move</b>: %s on %s, %s%%.",
			html.EscapeString(token),
			html.EscapeString(marketChainName(selected.ChainKey)),
			changeText,
		)
	}
	return fmt.Sprintf(
		"<b>最大价格波动</b>：%s（%s）%s%%。",
		html.EscapeString(token),
		html.EscapeString(marketChainName(selected.ChainKey)),
		changeText,
	)
}

func dailySummaryLargestTrade(
	events []store.MarketSummaryEvent,
	english bool,
) string {
	var selected *store.MarketSummaryEvent
	var selectedValue *big.Rat
	for index := range events {
		event := &events[index]
		if event.EventType != "buy" && event.EventType != "sell" ||
			event.USDValue == nil {
			continue
		}
		value, ok := new(big.Rat).SetString(strings.TrimSpace(*event.USDValue))
		if !ok || value.Sign() <= 0 {
			continue
		}
		if selectedValue == nil || value.Cmp(selectedValue) > 0 {
			selected = event
			selectedValue = value
		}
	}
	if selected == nil || selectedValue == nil {
		return ""
	}
	token := strings.TrimSpace(selected.TokenSymbol)
	if token == "" {
		token = strings.TrimSpace(selected.TokenName)
	}
	if token == "" {
		token = "Token"
	}
	action := "买入"
	if selected.EventType == "sell" {
		action = "卖出"
	}
	if english {
		action = "buy"
		if selected.EventType == "sell" {
			action = "sell"
		}
		return fmt.Sprintf(
			"<b>Largest trade</b>: %s %s worth $%s.",
			html.EscapeString(token),
			action,
			notificationfmt.Money(selectedValue.FloatString(8)),
		)
	}
	return fmt.Sprintf(
		"<b>最大成交</b>：%s %s $%s。",
		html.EscapeString(token),
		action,
		notificationfmt.Money(selectedValue.FloatString(8)),
	)
}

func dailySummaryRiskOverview(
	statistics store.SummaryStatistics,
	notificationFailures int64,
	english bool,
) string {
	parts := make([]string, 0, 3)
	if statistics.AddressRiskEventCount > 0 {
		if english {
			parts = append(parts, fmt.Sprintf(
				"address %d",
				statistics.AddressRiskEventCount,
			))
		} else {
			parts = append(parts, fmt.Sprintf(
				"地址风险 %d",
				statistics.AddressRiskEventCount,
			))
		}
	}
	if statistics.MarketAnomalyCount > 0 {
		if english {
			parts = append(parts, fmt.Sprintf(
				"market %d",
				statistics.MarketAnomalyCount,
			))
		} else {
			parts = append(parts, fmt.Sprintf(
				"市场异常 %d",
				statistics.MarketAnomalyCount,
			))
		}
	}
	if notificationFailures > 0 {
		if english {
			parts = append(parts, fmt.Sprintf(
				"delivery failures %d",
				notificationFailures,
			))
		} else {
			parts = append(parts, fmt.Sprintf(
				"发送失败 %d",
				notificationFailures,
			))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return dailySummaryKeyValue(
		dailySummaryLabel("风险概览", "Risk overview", english),
		strings.Join(parts, dailySummarySeparator(english)),
		english,
	)
}

func dailySummaryFundsDirection(
	statistics store.SummaryStatistics,
	english bool,
) string {
	if statistics.MarketBuyCount > 0 || statistics.MarketSellCount > 0 {
		net, ok := new(big.Rat).SetString(strings.TrimSpace(
			statistics.MarketNetBuyUSD,
		))
		if ok && net.Sign() != 0 {
			value := notificationfmt.Money(
				new(big.Rat).Abs(net).FloatString(8),
			)
			if english {
				direction := "net inflow"
				if net.Sign() < 0 {
					direction = "net outflow"
				}
				return dailySummaryKeyValue(
					"Funds",
					fmt.Sprintf(
						"%s $%s (%s)",
						direction,
						value,
						dailySummaryTradeCounts(statistics, english),
					),
					true,
				)
			}
			direction := "净流入"
			if net.Sign() < 0 {
				direction = "净流出"
			}
			return dailySummaryKeyValue(
				"资金方向",
				fmt.Sprintf(
					"%s $%s（%s）",
					direction,
					value,
					dailySummaryTradeCounts(statistics, english),
				),
				false,
			)
		}
		if english {
			return dailySummaryKeyValue(
				"Funds",
				"buying and selling were broadly balanced ("+
					dailySummaryTradeCounts(statistics, true)+")",
				true,
			)
		}
		return dailySummaryKeyValue(
			"资金方向",
			"买卖基本持平（"+dailySummaryTradeCounts(statistics, false)+"）",
			false,
		)
	}
	if statistics.AddressIncomingCount == 0 &&
		statistics.AddressOutgoingCount == 0 {
		return ""
	}
	parts := make([]string, 0, 2)
	if statistics.AddressIncomingCount > 0 {
		if english {
			parts = append(parts, dailySummaryCount(
				statistics.AddressIncomingCount,
				"incoming transfer",
				"incoming transfers",
			))
		} else {
			parts = append(parts, fmt.Sprintf(
				"转入 %d 次",
				statistics.AddressIncomingCount,
			))
		}
	}
	if statistics.AddressOutgoingCount > 0 {
		if english {
			parts = append(parts, dailySummaryCount(
				statistics.AddressOutgoingCount,
				"outgoing transfer",
				"outgoing transfers",
			))
		} else {
			parts = append(parts, fmt.Sprintf(
				"转出 %d 次",
				statistics.AddressOutgoingCount,
			))
		}
	}
	return dailySummaryKeyValue(
		dailySummaryLabel("资金方向", "Funds", english),
		strings.Join(parts, dailySummarySeparator(english)),
		english,
	)
}

func dailySummaryRuntime(
	statistics store.SummaryStatistics,
	english bool,
) string {
	parts := make([]string, 0, 3)
	if statistics.WalletCount > 0 {
		if english {
			parts = append(parts, dailySummaryCount(
				statistics.WalletCount,
				"wallet",
				"wallets",
			))
		} else {
			parts = append(parts, fmt.Sprintf(
				"%d 个钱包",
				statistics.WalletCount,
			))
		}
	}
	if statistics.MarketProjectCount > 0 {
		if english {
			parts = append(parts, dailySummaryCount(
				statistics.MarketProjectCount,
				"token project",
				"token projects",
			))
		} else {
			parts = append(parts, fmt.Sprintf(
				"%d 个代币项目",
				statistics.MarketProjectCount,
			))
		}
	}
	ruleCount := statistics.RuleCount + statistics.MarketRuleCount
	if ruleCount > 0 {
		if english {
			parts = append(parts, dailySummaryCount(
				ruleCount,
				"active rule",
				"active rules",
			))
		} else {
			parts = append(parts, fmt.Sprintf("%d 条运行规则", ruleCount))
		}
	}
	if len(parts) == 0 {
		if english {
			return dailySummaryKeyValue(
				"Running",
				"No active monitoring items.",
				true,
			)
		}
		return dailySummaryKeyValue("运行中", "暂无启用的监控项。", false)
	}
	return dailySummaryKeyValue(
		dailySummaryLabel("运行中", "Running", english),
		strings.Join(parts, dailySummarySeparator(english)),
		english,
	)
}

func dailySummaryDataGap(
	statistics store.SummaryStatistics,
	summaries []store.MarketProjectChainSummary,
	english bool,
) string {
	if statistics.MarketProjectCount == 0 {
		return ""
	}
	missingPrice := false
	missingLiquidity := false
	missingVolume := false
	if len(summaries) == 0 {
		missingPrice = true
		missingLiquidity = true
		missingVolume = true
	} else {
		for _, item := range summaries {
			if item.SnapshotCount == 0 {
				missingPrice = true
				missingLiquidity = true
				missingVolume = true
				continue
			}
			if item.PriceSampleCount < 2 ||
				item.StartPriceUSD == nil ||
				item.EndPriceUSD == nil {
				missingPrice = true
			}
			if item.LiquiditySampleCount == 0 {
				missingLiquidity = true
			}
			if item.VolumeSampleCount == 0 {
				missingVolume = true
			}
		}
	}
	if !missingPrice && !missingLiquidity && !missingVolume {
		return ""
	}
	missing := make([]string, 0, 3)
	impact := make([]string, 0, 3)
	if missingPrice {
		missing = append(missing, dailySummaryLabel("价格", "price", english))
		impact = append(impact, dailySummaryLabel(
			"涨跌与价格规则",
			"price-change rules",
			english,
		))
	}
	if missingLiquidity {
		missing = append(missing, dailySummaryLabel("流动性", "liquidity", english))
		impact = append(impact, dailySummaryLabel(
			"流动性规则",
			"liquidity rules",
			english,
		))
	}
	if missingVolume {
		missing = append(missing, dailySummaryLabel("成交量", "volume", english))
		impact = append(impact, dailySummaryLabel(
			"成交量规则",
			"volume rules",
			english,
		))
	}
	if english {
		return dailySummaryKeyValue(
			"Data coverage",
			fmt.Sprintf(
				"missing %s; <b>affected</b>: %s.",
				strings.Join(missing, ", "),
				strings.Join(impact, ", "),
			),
			true,
		)
	}
	return dailySummaryKeyValue(
		"数据覆盖",
		fmt.Sprintf(
			"缺少%s；影响%s。",
			strings.Join(missing, "、"),
			strings.Join(impact, "、"),
		),
		false,
	)
}

func dailySummaryHasActivity(statistics store.SummaryStatistics) bool {
	return statistics.EventCount > 0 ||
		statistics.MarketEventCount > 0 ||
		statistics.MarketAnomalyCount > 0 ||
		statistics.LiquidityEventCount > 0 ||
		statistics.HolderEventCount > 0 ||
		statistics.FailedNotificationCount > 0 ||
		statistics.MarketFailedNotificationCount > 0
}

func dailySummaryPriceChange(start, end *string) (*big.Rat, bool) {
	if start == nil || end == nil {
		return nil, false
	}
	startValue, startOK := new(big.Rat).SetString(strings.TrimSpace(*start))
	endValue, endOK := new(big.Rat).SetString(strings.TrimSpace(*end))
	if !startOK || !endOK || startValue.Sign() == 0 {
		return nil, false
	}
	change := new(big.Rat).Sub(endValue, startValue)
	change.Quo(change, startValue)
	change.Mul(change, big.NewRat(100, 1))
	return change, true
}

func dailySummaryTokenName(item store.MarketProjectChainSummary) string {
	if value := strings.TrimSpace(item.TokenSymbol); value != "" {
		return value
	}
	if value := strings.TrimSpace(item.TokenName); value != "" {
		return value
	}
	return "Token"
}

func dailySummaryTradeCounts(
	statistics store.SummaryStatistics,
	english bool,
) string {
	parts := make([]string, 0, 2)
	if statistics.MarketBuyCount > 0 {
		if english {
			parts = append(parts, dailySummaryCount(
				statistics.MarketBuyCount,
				"buy",
				"buys",
			))
		} else {
			parts = append(parts, fmt.Sprintf(
				"买入 %d 笔",
				statistics.MarketBuyCount,
			))
		}
	}
	if statistics.MarketSellCount > 0 {
		if english {
			parts = append(parts, dailySummaryCount(
				statistics.MarketSellCount,
				"sell",
				"sells",
			))
		} else {
			parts = append(parts, fmt.Sprintf(
				"卖出 %d 笔",
				statistics.MarketSellCount,
			))
		}
	}
	return strings.Join(parts, dailySummarySeparator(english))
}

func dailySummaryRuleLabel(eventType string, english bool) string {
	labels := ruleTypeLabels
	if english {
		labels = ruleTypeLabelsEN
	}
	if label := strings.TrimSpace(labels[eventType]); label != "" {
		return label
	}
	return eventType
}

func dailySummaryRiskEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "outgoing", "balance_threshold", "approval_change",
		"address_interaction":
		return true
	default:
		return false
	}
}

func dailySummaryKeyValue(label, value string, english bool) string {
	label = strings.TrimSpace(label)
	value = strings.TrimSpace(value)
	if label == "" || value == "" {
		return ""
	}
	return "<b>" + html.EscapeString(label) + "</b>" +
		dailySummaryColon(english) + value
}

func dailySummaryLabel(chinese, english string, useEnglish bool) string {
	if useEnglish {
		return english
	}
	return chinese
}

func dailySummaryColon(english bool) string {
	if english {
		return ": "
	}
	return "："
}

func dailySummarySeparator(english bool) string {
	if english {
		return ", "
	}
	return "，"
}

func dailySummaryCount(count int64, singular, plural string) string {
	noun := plural
	if count == 1 {
		noun = singular
	}
	return fmt.Sprintf("%d %s", count, noun)
}
