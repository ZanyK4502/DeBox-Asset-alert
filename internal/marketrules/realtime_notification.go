package marketrules

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationfmt"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func marketRealtimeText(delivery store.MarketNotificationDelivery) string {
	if delivery.Rule == nil || delivery.Event == nil {
		return "<b>⚠️ Market alert</b>"
	}
	switch delivery.Rule.RuleType {
	case plans.MarketPriceAbove,
		plans.MarketPriceBelow,
		plans.MarketPriceIncrease,
		plans.MarketPriceDecrease:
		return priceRealtimeText(delivery)
	case plans.MarketLiquidityBelow,
		plans.MarketLiquidityDecrease,
		plans.MarketLiquidityAdded,
		plans.MarketLiquidityRemoved,
		plans.MarketNewPool:
		return liquidityRealtimeText(delivery)
	case plans.MarketVolumeAbove,
		plans.MarketVolumeSpike,
		plans.MarketTradeImbalance:
		return volumeRealtimeText(delivery)
	case plans.MarketLargeBuy,
		plans.MarketLargeSell,
		plans.MarketConsecutiveLargeBuy,
		plans.MarketConsecutiveLargeSell:
		return largeTradeRealtimeText(delivery)
	case plans.MarketHolderIncrease,
		plans.MarketHolderDecrease,
		plans.MarketHolderRankEntered,
		plans.MarketHolderRankExited:
		return holderRealtimeText(delivery)
	case plans.MarketFourMemeLargeTrade,
		plans.MarketFourMemeProgress,
		plans.MarketFourMemeMigration:
		return fourMemeRealtimeText(delivery)
	default:
		return fallbackMarketRealtimeText(delivery)
	}
}

func priceRealtimeText(delivery store.MarketNotificationDelivery) string {
	rule := delivery.Rule
	english := marketRealtimeEnglish(delivery)
	token := escape(marketRealtimeToken(delivery.Project, english))
	actual := marketRealtimeActual(delivery)
	currentPrice := marketRealtimeCurrentPrice(delivery)
	var title string
	lines := make([]string, 0, 7)

	switch rule.RuleType {
	case plans.MarketPriceAbove:
		if english {
			title = "📈 " + token + " price moved above your level"
		} else {
			title = "📈 " + token + " 价格已突破上限"
		}
		lines = marketRealtimeStart(title, delivery, english)
		appendMarketLine(&lines, marketLabel("当前价格", "Current price", english),
			marketPrice(actual), english)
		appendMarketLine(&lines, marketLabel("你的条件", "Your condition", english),
			"≥ "+marketPriceThreshold(rule), english)
		appendMarketLine(&lines, marketLabel("高于阈值", "Above threshold", english),
			marketDifference(actual, rule.ThresholdValue, "price", false,
				delivery.Project.TokenSymbol, true, english), english)
	case plans.MarketPriceBelow:
		if english {
			title = "📉 " + token + " price moved below your level"
		} else {
			title = "📉 " + token + " 价格已跌破下限"
		}
		lines = marketRealtimeStart(title, delivery, english)
		appendMarketLine(&lines, marketLabel("当前价格", "Current price", english),
			marketPrice(actual), english)
		appendMarketLine(&lines, marketLabel("你的条件", "Your condition", english),
			"≤ "+marketPriceThreshold(rule), english)
		appendMarketLine(&lines, marketLabel("低于阈值", "Below threshold", english),
			marketDifference(actual, rule.ThresholdValue, "price", true,
				delivery.Project.TokenSymbol, true, english), english)
	case plans.MarketPriceIncrease, plans.MarketPriceDecrease:
		increase := rule.RuleType == plans.MarketPriceIncrease
		change := marketPercent(actual)
		window := marketWindow(rule.WindowMinutes, english)
		if english {
			direction := "rose "
			icon := "🚀 "
			if !increase {
				direction, icon = "fell ", "🔻 "
			}
			title = icon + token + " " + direction + change + " in " + window
		} else {
			direction := "上涨 "
			icon := "🚀 "
			if !increase {
				direction, icon = "下跌 ", "🔻 "
			}
			title = icon + token + " 在" + window + "内" + direction + change
		}
		lines = marketRealtimeStart(title, delivery, english)
		if transition := marketPriceTransition(delivery.PreviousValue, currentPrice, english); transition != "" {
			appendMarketLine(&lines, marketLabel("价格变化", "Price move", english),
				transition, english)
		}
		appendMarketLine(&lines, marketLabel("你的条件", "Your condition", english),
			"≥ "+marketThreshold(rule, delivery.Project.TokenSymbol), english)
		appendMarketLine(&lines, marketLabel("超出阈值", "Exceeded by", english),
			marketDifference(actual, rule.ThresholdValue, "percent", false,
				delivery.Project.TokenSymbol, true, english), english)
	}
	return finishMarketRealtime(lines, delivery, english)
}

func liquidityRealtimeText(delivery store.MarketNotificationDelivery) string {
	rule := delivery.Rule
	english := marketRealtimeEnglish(delivery)
	token := escape(marketRealtimeToken(delivery.Project, english))
	actual := marketRealtimeActual(delivery)
	currentLiquidity := marketRealtimeLiquidity(delivery)
	var title string
	lines := make([]string, 0, 8)

	switch rule.RuleType {
	case plans.MarketLiquidityBelow:
		value := marketUSD(actual)
		if english {
			title = "⚠️ " + token + " liquidity fell to " + value
		} else {
			title = "⚠️ " + token + " 流动性降至 " + value
		}
		lines = marketRealtimeStart(title, delivery, english)
		appendMarketLine(&lines, marketLabel("当前流动性", "Current liquidity", english),
			value, english)
		appendMarketLine(&lines, marketLabel("你的条件", "Your condition", english),
			"≤ "+marketThreshold(rule, delivery.Project.TokenSymbol), english)
		appendMarketLine(&lines, marketLabel("低于阈值", "Below threshold", english),
			marketDifference(actual, rule.ThresholdValue, "usd", true,
				delivery.Project.TokenSymbol, false, english), english)
		appendMarketLine(&lines, marketLabel("这意味着", "What this means", english),
			marketPlainText(
				"池子变浅，大额交易更容易造成明显价格波动。",
				"The pool is thinner, so large trades may move the price more.",
				english,
			), english)
	case plans.MarketLiquidityDecrease:
		change := marketPercent(actual)
		window := marketWindow(rule.WindowMinutes, english)
		if english {
			title = "⚠️ " + token + " liquidity dropped " + change + " in " + window
		} else {
			title = "⚠️ " + token + " 流动性在" + window + "内下降 " + change
		}
		lines = marketRealtimeStart(title, delivery, english)
		if transition := marketMoneyTransition(delivery.PreviousValue, currentLiquidity, english); transition != "" {
			appendMarketLine(&lines, marketLabel("流动性变化", "Liquidity move", english),
				transition, english)
		}
		appendMarketLine(&lines, marketLabel("你的条件", "Your condition", english),
			"≥ "+marketThreshold(rule, delivery.Project.TokenSymbol), english)
		appendMarketLine(&lines, marketLabel("超出阈值", "Exceeded by", english),
			marketDifference(actual, rule.ThresholdValue, "percent", false,
				delivery.Project.TokenSymbol, true, english), english)
		appendMarketLine(&lines, marketLabel("这意味着", "What this means", english),
			marketPlainText(
				"可交易资金正在减少，滑点和价格波动风险正在上升。",
				"Tradable liquidity is shrinking, increasing slippage and price risk.",
				english,
			), english)
	case plans.MarketLiquidityAdded, plans.MarketLiquidityRemoved:
		added := rule.RuleType == plans.MarketLiquidityAdded
		amount := marketEventHeadlineAmount(delivery)
		if english {
			action := "liquidity added"
			icon := "💧 "
			if !added {
				action, icon = "liquidity removed", "🚨 "
			}
			title = icon + token + " " + action + " · " + amount
		} else {
			action := "新增流动性"
			icon := "💧 "
			if !added {
				action, icon = "发生撤池", "🚨 "
			}
			title = icon + token + " " + action + " · " + amount
		}
		lines = marketRealtimeStart(title, delivery, english)
		appendMarketLine(&lines, marketLabel("变动金额", "Liquidity change", english),
			marketEventAmountSummary(delivery), english)
		appendMarketLine(&lines, marketLabel("你的条件", "Your condition", english),
			"≥ "+marketThreshold(rule, delivery.Project.TokenSymbol), english)
		appendMarketLine(&lines, marketLabel("超出阈值", "Exceeded by", english),
			marketDifference(actual, rule.ThresholdValue, rule.ThresholdUnit, false,
				delivery.Project.TokenSymbol, false, english), english)
		appendMarketLine(&lines, marketLabel("交易池", "Pool", english),
			escape(poolDisplay(delivery.Pool)), english)
		explanationZH := "池子变深，同等金额交易通常更不容易引起大幅波动。"
		explanationEN := "The pool is deeper, so similar trades usually have less price impact."
		if !added {
			explanationZH = "资金被撤出池子，后续交易的滑点和价格波动风险可能升高。"
			explanationEN = "Funds left the pool, which may increase slippage and price volatility."
		}
		appendMarketLine(&lines, marketLabel("这意味着", "What this means", english),
			marketPlainText(explanationZH, explanationEN, english), english)
	case plans.MarketNewPool:
		if english {
			title = "🆕 New " + token + " trading pool detected"
		} else {
			title = "🆕 发现 " + token + " 新交易池"
		}
		lines = marketRealtimeStart(title, delivery, english)
		appendMarketLine(&lines, marketLabel("交易池", "Pool", english),
			escape(poolDisplay(delivery.Pool)), english)
		appendMarketLine(&lines, "DEX", escape(dexDisplay(delivery.Pool)), english)
		appendMarketLine(&lines, marketLabel("当前池流动性", "Current pool liquidity", english),
			marketUSD(currentLiquidity), english)
		appendMarketLine(&lines, marketLabel("你的条件", "Your condition", english),
			marketPlainText(
				"发现 ≥ "+marketThreshold(rule, delivery.Project.TokenSymbol)+" 个新池",
				"Detect ≥ "+marketThreshold(rule, delivery.Project.TokenSymbol)+" new pool(s)",
				english,
			), english)
		appendMarketLine(&lines, marketLabel("实际结果", "Actual result", english),
			marketPlainText("已发现 1 个", "1 detected", english), english)
		appendMarketLine(&lines, marketLabel("超出阈值", "Exceeded by", english),
			marketDifference(stringPointer("1"), rule.ThresholdValue, "count", false,
				delivery.Project.TokenSymbol, false, english), english)
		appendMarketLine(&lines, marketLabel("这意味着", "What this means", english),
			marketPlainText(
				"代币新增了交易场所，请留意新池深度和是否为官方池。",
				"A new venue is available; check its depth and whether it is official.",
				english,
			), english)
	}
	return finishMarketRealtime(lines, delivery, english)
}

func volumeRealtimeText(delivery store.MarketNotificationDelivery) string {
	rule := delivery.Rule
	english := marketRealtimeEnglish(delivery)
	token := escape(marketRealtimeToken(delivery.Project, english))
	actual := marketRealtimeActual(delivery)
	window := marketWindow(rule.WindowMinutes, english)
	var title string
	lines := make([]string, 0, 8)

	switch rule.RuleType {
	case plans.MarketVolumeAbove:
		value := marketUSD(actual)
		if english {
			title = "📊 " + token + " volume reached " + value + " in " + window
		} else {
			title = "📊 " + token + " " + window + "成交量达到 " + value
		}
		lines = marketRealtimeStart(title, delivery, english)
		appendMarketLine(&lines, marketLabel("实际成交量", "Actual volume", english),
			value, english)
		appendMarketLine(&lines, marketLabel("你的条件", "Your condition", english),
			"≥ "+marketThreshold(rule, delivery.Project.TokenSymbol), english)
		appendMarketLine(&lines, marketLabel("超出阈值", "Exceeded by", english),
			marketDifference(actual, rule.ThresholdValue, "usd", false,
				delivery.Project.TokenSymbol, false, english), english)
	case plans.MarketVolumeSpike:
		ratio := marketRatio(actual)
		if english {
			title = "🔥 " + token + " volume spiked to " + ratio
		} else {
			title = "🔥 " + token + " 成交量放大至 " + ratio
		}
		lines = marketRealtimeStart(title, delivery, english)
		appendMarketLine(&lines, marketLabel(window+"成交量", window+" volume", english),
			marketUSD(marketRealtimeVolume(delivery)), english)
		appendMarketLine(&lines, marketLabel("历史平均", "Historical average", english),
			marketUSD(delivery.PreviousValue), english)
		appendMarketLine(&lines, marketLabel("实际倍数", "Actual multiple", english),
			ratio, english)
		appendMarketLine(&lines, marketLabel("你的条件", "Your condition", english),
			"≥ "+marketThreshold(rule, delivery.Project.TokenSymbol), english)
		appendMarketLine(&lines, marketLabel("超出阈值", "Exceeded by", english),
			marketDifference(actual, rule.ThresholdValue, "ratio", false,
				delivery.Project.TokenSymbol, false, english), english)
	case plans.MarketTradeImbalance:
		direction, buys, sells := marketTradeDirection(delivery)
		directionText := marketPlainText("买方", "buyers", english)
		icon := "🟢 "
		if direction == "sell" {
			directionText = marketPlainText("卖方", "sellers", english)
			icon = "🔴 "
		}
		if english {
			title = icon + token + " trades skewed to " + directionText + " · " +
				marketPercent(actual)
		} else {
			title = icon + token + " 成交明显偏向" + directionText + " · " +
				marketPercent(actual)
		}
		lines = marketRealtimeStart(title, delivery, english)
		appendMarketLine(&lines, marketLabel("实际占比", "Actual share", english),
			directionText+" "+marketPercent(actual), english)
		if buys >= 0 && sells >= 0 {
			counts := fmt.Sprintf("%d / %d", buys, sells)
			appendMarketLine(&lines, marketLabel("买入 / 卖出", "Buys / sells", english),
				counts, english)
		}
		appendMarketLine(&lines, marketLabel("统计周期", "Window", english),
			window, english)
		appendMarketLine(&lines, marketLabel("你的条件", "Your condition", english),
			"≥ "+marketThreshold(rule, delivery.Project.TokenSymbol), english)
		appendMarketLine(&lines, marketLabel("超出阈值", "Exceeded by", english),
			marketDifference(actual, rule.ThresholdValue, "percent", false,
				delivery.Project.TokenSymbol, true, english), english)
	}
	return finishMarketRealtime(lines, delivery, english)
}

func largeTradeRealtimeText(delivery store.MarketNotificationDelivery) string {
	rule := delivery.Rule
	event := delivery.Event
	english := marketRealtimeEnglish(delivery)
	token := escape(marketRealtimeToken(delivery.Project, english))
	actual := marketRealtimeActual(delivery)
	buy := rule.RuleType == plans.MarketLargeBuy ||
		rule.RuleType == plans.MarketConsecutiveLargeBuy
	consecutive := rule.RuleType == plans.MarketConsecutiveLargeBuy ||
		rule.RuleType == plans.MarketConsecutiveLargeSell
	amount := marketEventHeadlineAmount(delivery)
	var title string
	if english {
		action := "large buy"
		icon := "🟢 "
		if !buy {
			action, icon = "large sell", "🔴 "
		}
		if consecutive {
			action = "consecutive " + action + "s"
		}
		title = icon + token + " " + action + " · " + amount
	} else {
		action := "大额买入"
		icon := "🟢 "
		if !buy {
			action, icon = "大额卖出", "🔴 "
		}
		if consecutive {
			action = "连续" + action
		}
		title = icon + token + " " + action + " · " + amount
	}
	lines := marketRealtimeStart(title, delivery, english)
	if consecutive {
		count := pointerValue(actual)
		required := fmt.Sprintf("%d", rule.TriggerCountThreshold)
		if count != "" && rule.TriggerCountThreshold > 0 {
			appendMarketLine(&lines, marketLabel("连续笔数", "Consecutive trades", english),
				marketPlainText(count+" 笔（要求 "+required+" 笔）",
					count+" (required "+required+")", english), english)
		}
		appendMarketLine(&lines, marketLabel("最新一笔", "Latest trade", english),
			marketEventAmountSummary(delivery), english)
		appendMarketLine(&lines, marketLabel("单笔条件", "Per-trade condition", english),
			"≥ "+marketThreshold(rule, delivery.Project.TokenSymbol), english)
		eventActual := marketEventRuleValue(delivery)
		appendMarketLine(&lines, marketLabel("最新一笔超出", "Latest exceeded by", english),
			marketDifference(eventActual, rule.ThresholdValue, rule.ThresholdUnit,
				false, delivery.Project.TokenSymbol, false, english), english)
	} else {
		appendMarketLine(&lines, marketLabel("成交详情", "Trade details", english),
			marketTradeDetails(delivery, english), english)
		appendMarketLine(&lines, marketLabel("你的条件", "Your condition", english),
			"≥ "+marketThreshold(rule, delivery.Project.TokenSymbol), english)
		appendMarketLine(&lines, marketLabel("超出阈值", "Exceeded by", english),
			marketDifference(actual, rule.ThresholdValue, rule.ThresholdUnit,
				false, delivery.Project.TokenSymbol, false, english), english)
	}
	appendMarketLine(&lines, marketLabel("相关钱包", "Wallet", english),
		marketWallet(delivery), english)
	if impact := metadataDisplay(event.Metadata, "price_impact_percent", "%"); impact != "" {
		appendMarketLine(&lines, marketLabel("价格影响", "Price impact", english),
			escape(impact), english)
	}
	return finishMarketRealtime(lines, delivery, english)
}

func holderRealtimeText(delivery store.MarketNotificationDelivery) string {
	rule := delivery.Rule
	event := delivery.Event
	english := marketRealtimeEnglish(delivery)
	token := escape(marketRealtimeToken(delivery.Project, english))
	actual := marketRealtimeActual(delivery)
	increase := rule.RuleType == plans.MarketHolderIncrease
	rankEvent := rule.RuleType == plans.MarketHolderRankEntered ||
		rule.RuleType == plans.MarketHolderRankExited
	var title string
	lines := make([]string, 0, 8)

	if rankEvent {
		entered := rule.RuleType == plans.MarketHolderRankEntered
		if english {
			action := "entered the top-holder list"
			icon := "🏅 "
			if !entered {
				action, icon = "left the top-holder list", "↘️ "
			}
			title = icon + token + " holder " + action
		} else {
			action := "进入大户榜"
			icon := "🏅 "
			if !entered {
				action, icon = "退出大户榜", "↘️ "
			}
			title = icon + token + " 地址" + action
		}
		lines = marketRealtimeStart(title, delivery, english)
		appendMarketLine(&lines, marketLabel("相关地址", "Holder", english),
			marketWallet(delivery), english)
		oldRank := metadataDecimal(event.Metadata, "old_rank")
		newRank := metadataDecimal(event.Metadata, "new_rank")
		appendMarketLine(&lines, marketLabel("排名变化", "Rank move", english),
			marketRankTransition(oldRank, newRank, english), english)
		appendMarketLine(&lines, marketLabel("你的条件", "Your condition", english),
			marketPlainText("Top "+notificationfmt.Percentage(rule.ThresholdValue),
				"Top "+notificationfmt.Percentage(rule.ThresholdValue), english), english)
		resultZH := "当前排名已进入设定范围"
		resultEN := "Current rank is inside your selected range"
		if !entered {
			resultZH = "已从设定范围内退出"
			resultEN = "The holder moved outside your selected range"
		}
		appendMarketLine(&lines, marketLabel("实际结果", "Actual result", english),
			marketPlainText(resultZH, resultEN, english), english)
		return finishMarketRealtime(lines, delivery, english)
	}

	if english {
		action := "increased holdings"
		icon := "🐋 "
		if !increase {
			action, icon = "reduced holdings", "🐋 "
		}
		title = icon + token + " large holder " + action
	} else {
		action := "大户增持"
		if !increase {
			action = "大户减持"
		}
		title = "🐋 " + token + " " + action
	}
	lines = marketRealtimeStart(title, delivery, english)
	appendMarketLine(&lines, marketLabel("相关地址", "Holder", english),
		marketWallet(delivery), english)
	appendMarketLine(&lines, marketLabel("持仓变化", "Balance move", english),
		marketBalanceTransition(event.Metadata, delivery.Project.TokenSymbol, english), english)
	appendMarketLine(&lines, marketLabel("触发值", "Actual trigger value", english),
		marketUnitValue(actual, rule.ThresholdUnit, delivery.Project.TokenSymbol, false), english)
	appendMarketLine(&lines, marketLabel("你的条件", "Your condition", english),
		"≥ "+marketThreshold(rule, delivery.Project.TokenSymbol), english)
	appendMarketLine(&lines, marketLabel("超出阈值", "Exceeded by", english),
		marketDifference(actual, rule.ThresholdValue, rule.ThresholdUnit, false,
			delivery.Project.TokenSymbol, false, english), english)
	return finishMarketRealtime(lines, delivery, english)
}

func fourMemeRealtimeText(delivery store.MarketNotificationDelivery) string {
	rule := delivery.Rule
	event := delivery.Event
	english := marketRealtimeEnglish(delivery)
	token := escape(marketRealtimeToken(delivery.Project, english))
	actual := marketRealtimeActual(delivery)
	var title string
	lines := make([]string, 0, 8)

	switch rule.RuleType {
	case plans.MarketFourMemeLargeTrade:
		buy := event.EventType == "buy"
		if english {
			action := "large internal-market buy"
			icon := "🟢 "
			if !buy {
				action, icon = "large internal-market sell", "🔴 "
			}
			title = icon + token + " Four.meme " + action + " · " +
				marketEventHeadlineAmount(delivery)
		} else {
			action := "内盘大额买入"
			icon := "🟢 "
			if !buy {
				action, icon = "内盘大额卖出", "🔴 "
			}
			title = icon + token + " Four.meme " + action + " · " +
				marketEventHeadlineAmount(delivery)
		}
		lines = marketRealtimeStart(title, delivery, english)
		appendMarketLine(&lines, marketLabel("成交详情", "Trade details", english),
			marketTradeDetails(delivery, english), english)
		appendMarketLine(&lines, marketLabel("你的条件", "Your condition", english),
			"≥ "+marketThreshold(rule, delivery.Project.TokenSymbol), english)
		appendMarketLine(&lines, marketLabel("超出阈值", "Exceeded by", english),
			marketDifference(actual, rule.ThresholdValue, rule.ThresholdUnit, false,
				delivery.Project.TokenSymbol, false, english), english)
		appendMarketLine(&lines, marketLabel("相关钱包", "Wallet", english),
			marketWallet(delivery), english)
		if progress := metadataDecimal(event.Metadata, "progress_percent"); progress != nil {
			appendMarketLine(&lines, marketLabel("当前进度", "Current progress", english),
				marketPercent(progress), english)
		}
	case plans.MarketFourMemeProgress:
		progress := marketPercent(actual)
		if english {
			title = "🚀 " + token + " Four.meme progress reached " + progress
		} else {
			title = "🚀 " + token + " Four.meme 进度达到 " + progress
		}
		lines = marketRealtimeStart(title, delivery, english)
		appendMarketLine(&lines, marketLabel("当前进度", "Current progress", english),
			progress, english)
		appendMarketLine(&lines, marketLabel("你的条件", "Your condition", english),
			"≥ "+marketThreshold(rule, delivery.Project.TokenSymbol), english)
		appendMarketLine(&lines, marketLabel("超出阈值", "Exceeded by", english),
			marketDifference(actual, rule.ThresholdValue, "percent", false,
				delivery.Project.TokenSymbol, true, english), english)
		appendMarketLine(&lines, marketLabel("距离 100%", "Remaining to 100%", english),
			marketProgressRemaining(actual), english)
		appendMarketLine(&lines, marketLabel("这意味着", "What this means", english),
			marketPlainText(
				"内盘筹资进度正在推进，越接近 100% 越接近毕业迁移。",
				"Internal-market funding is advancing; 100% is the migration milestone.",
				english,
			), english)
	case plans.MarketFourMemeMigration:
		if english {
			title = "🎓 " + token + " graduated from Four.meme"
		} else {
			title = "🎓 " + token + " 已从 Four.meme 毕业迁移"
		}
		lines = marketRealtimeStart(title, delivery, english)
		appendMarketLine(&lines, marketLabel("你的条件", "Your condition", english),
			marketPlainText(
				"检测到 ≥ "+marketThreshold(rule, delivery.Project.TokenSymbol)+" 次迁移",
				"Detect ≥ "+marketThreshold(rule, delivery.Project.TokenSymbol)+" migration(s)",
				english,
			), english)
		appendMarketLine(&lines, marketLabel("实际结果", "Actual result", english),
			marketPlainText("检测到 1 次迁移",
				"1 migration detected", english), english)
		appendMarketLine(&lines, marketLabel("超出阈值", "Exceeded by", english),
			marketDifference(stringPointer("1"), rule.ThresholdValue, "count", false,
				delivery.Project.TokenSymbol, false, english), english)
		appendMarketLine(&lines, marketLabel("迁移去向", "Destination pool", english),
			escape(poolDisplay(delivery.Pool)), english)
		appendMarketLine(&lines, "DEX", escape(dexDisplay(delivery.Pool)), english)
		appendMarketLine(&lines, marketLabel("这意味着", "What this means", english),
			marketPlainText(
				"代币已从 Four.meme 内盘进入外部 DEX 流动性池交易。",
				"The token moved from Four.meme's internal market to an external DEX pool.",
				english,
			), english)
	}
	return finishMarketRealtime(lines, delivery, english)
}

func fallbackMarketRealtimeText(delivery store.MarketNotificationDelivery) string {
	english := marketRealtimeEnglish(delivery)
	token := escape(marketRealtimeToken(delivery.Project, english))
	title := "📊 " + token + " 市场提醒"
	if english {
		title = "📊 " + token + " market alert"
	}
	lines := marketRealtimeStart(title, delivery, english)
	if value := pointerValue(delivery.CurrentValue); value != "" {
		appendMarketLine(&lines, marketLabel("实际值", "Actual value", english),
			escape(value), english)
	}
	return finishMarketRealtime(lines, delivery, english)
}

func marketRealtimeStart(
	title string,
	delivery store.MarketNotificationDelivery,
	english bool,
) []string {
	lines := []string{"<b>" + title + "</b>"}
	if delivery.Rule != nil {
		appendMarketLine(&lines, marketLabel("规则", "Rule", english),
			escape(ruleDisplay(delivery.Rule.RuleType, english)), english)
	}
	return lines
}

func finishMarketRealtime(
	lines []string,
	delivery store.MarketNotificationDelivery,
	english bool,
) string {
	appendMarketLine(&lines, marketLabel("发生于", "Occurred", english),
		marketRealtimeContext(delivery), english)
	return notificationfmt.JoinBlocks(lines...)
}

func appendMarketLine(lines *[]string, label, value string, english bool) {
	if line := notificationfmt.KeyValue(label, value, english); line != "" {
		*lines = append(*lines, line)
	}
}

func marketRealtimeEnglish(delivery store.MarketNotificationDelivery) bool {
	return notificationLanguage(delivery.NotificationLanguage) == "en"
}

func marketRealtimeToken(project store.MarketProject, english bool) string {
	if symbol := strings.TrimSpace(project.TokenSymbol); symbol != "" {
		return symbol
	}
	if name := strings.TrimSpace(project.TokenName); name != "" {
		return name
	}
	if english {
		return "Token"
	}
	return "代币"
}

func marketRealtimeContext(delivery store.MarketNotificationDelivery) string {
	event := delivery.Event
	if event == nil {
		return ""
	}
	parts := make([]string, 0, 2)
	if chainName := notificationfmt.ChainName(event.ChainKey); chainName != "" {
		parts = append(parts, escape(chainName))
	}
	if occurred := notificationfmt.LocalTime(
		event.OccurredAt,
		delivery.Timezone,
		"2006-01-02 15:04",
	); occurred != "" {
		parts = append(parts, escape(occurred))
	}
	return strings.Join(parts, " · ")
}

func marketRealtimeActual(delivery store.MarketNotificationDelivery) *string {
	if pointerValue(delivery.CurrentValue) != "" {
		return delivery.CurrentValue
	}
	if delivery.Rule == nil || delivery.Event == nil {
		return nil
	}
	rule := delivery.Rule
	switch rule.RuleType {
	case plans.MarketPriceAbove, plans.MarketPriceBelow:
		if delivery.Snapshot != nil {
			return delivery.Snapshot.PriceUSD
		}
		return delivery.Event.PriceUSD
	case plans.MarketLiquidityBelow:
		if delivery.Snapshot != nil {
			return delivery.Snapshot.LiquidityUSD
		}
	case plans.MarketVolumeAbove:
		if delivery.Snapshot != nil {
			return snapshotVolume(*delivery.Snapshot, rule.WindowMinutes)
		}
	case plans.MarketFourMemeProgress:
		return metadataDecimal(delivery.Event.Metadata, "progress_percent")
	case plans.MarketNewPool, plans.MarketFourMemeMigration:
		return stringPointer("1")
	}
	snapshots := make([]store.MarketSnapshot, 0, 1)
	if delivery.Snapshot != nil {
		snapshots = append(snapshots, *delivery.Snapshot)
	}
	return eventValue(*rule, delivery.Project, snapshots, *delivery.Event)
}

func marketRealtimeCurrentPrice(delivery store.MarketNotificationDelivery) *string {
	if delivery.Event != nil && pointerValue(delivery.Event.PriceUSD) != "" {
		return delivery.Event.PriceUSD
	}
	if delivery.Snapshot != nil {
		return delivery.Snapshot.PriceUSD
	}
	return nil
}

func marketRealtimeLiquidity(delivery store.MarketNotificationDelivery) *string {
	if delivery.Snapshot == nil {
		return nil
	}
	return delivery.Snapshot.LiquidityUSD
}

func marketRealtimeVolume(delivery store.MarketNotificationDelivery) *string {
	if delivery.Snapshot == nil || delivery.Rule == nil {
		return nil
	}
	return snapshotVolume(*delivery.Snapshot, delivery.Rule.WindowMinutes)
}

func marketThreshold(rule *store.MarketRule, symbol string) string {
	if rule == nil {
		return ""
	}
	return marketUnitValue(
		stringPointer(rule.ThresholdValue),
		rule.ThresholdUnit,
		symbol,
		false,
	)
}

func marketUnitValue(value *string, unit, symbol string, compact bool) string {
	raw := pointerValue(value)
	if raw == "" {
		return ""
	}
	switch unit {
	case "usd":
		if compact {
			return "$" + escape(notificationfmt.CompactMoney(raw))
		}
		return "$" + escape(notificationfmt.Money(raw))
	case "price":
		return "$" + escape(notificationfmt.Price(raw))
	case "percent", "progress":
		return escape(notificationfmt.Percentage(raw)) + "%"
	case "ratio":
		return escape(notificationfmt.Percentage(raw)) + "×"
	case "token":
		amount := notificationfmt.FullTokenAmount(raw)
		if compact {
			amount = notificationfmt.TokenAmount(raw)
		}
		if strings.TrimSpace(symbol) != "" {
			amount += " " + strings.TrimSpace(symbol)
		}
		return escape(amount)
	case "count":
		return escape(notificationfmt.Percentage(raw))
	default:
		return escape(raw)
	}
}

func marketUSD(value *string) string {
	return marketUnitValue(value, "usd", "", false)
}

func marketPrice(value *string) string {
	return marketUnitValue(value, "price", "", false)
}

func marketPriceThreshold(rule *store.MarketRule) string {
	if rule == nil {
		return ""
	}
	return marketUnitValue(stringPointer(rule.ThresholdValue), "price", "", false)
}

func marketPercent(value *string) string {
	return marketUnitValue(value, "percent", "", false)
}

func marketRatio(value *string) string {
	return marketUnitValue(value, "ratio", "", false)
}

func marketDifference(
	actual *string,
	threshold string,
	unit string,
	below bool,
	symbol string,
	percentagePoints bool,
	english bool,
) string {
	actualValue, actualOK := pointerRat(actual)
	thresholdValue, thresholdOK := rat(threshold)
	if !actualOK || !thresholdOK {
		return ""
	}
	difference := new(big.Rat)
	if below {
		difference.Sub(thresholdValue, actualValue)
	} else {
		difference.Sub(actualValue, thresholdValue)
	}
	if difference.Sign() < 0 {
		return ""
	}
	if difference.Sign() == 0 {
		return ""
	}
	formatted := marketUnitValue(
		stringPointer(decimalString(difference)),
		unit,
		symbol,
		false,
	)
	if formatted == "" {
		return ""
	}
	if unit == "percent" && percentagePoints {
		return "+" + strings.TrimSuffix(formatted, "%") + marketPlainText(
			" 个百分点",
			" pp",
			english,
		)
	}
	if strings.HasPrefix(formatted, "$") {
		return "+$" + strings.TrimPrefix(formatted, "$")
	}
	return "+" + formatted
}

func marketPriceTransition(previous, current *string, english bool) string {
	if pointerValue(previous) == "" || pointerValue(current) == "" {
		return ""
	}
	arrow := " → "
	if english {
		arrow = " → "
	}
	return marketPrice(previous) + arrow + marketPrice(current)
}

func marketMoneyTransition(previous, current *string, english bool) string {
	if pointerValue(previous) == "" || pointerValue(current) == "" {
		return ""
	}
	return marketUSD(previous) + " → " + marketUSD(current)
}

func marketWindow(window *int32, english bool) string {
	minutes := defaultWindow(window, 60)
	if english {
		switch {
		case minutes%1440 == 0:
			return fmt.Sprintf("%dd", minutes/1440)
		case minutes%60 == 0:
			return fmt.Sprintf("%dh", minutes/60)
		default:
			return fmt.Sprintf("%dm", minutes)
		}
	}
	switch {
	case minutes%1440 == 0:
		return fmt.Sprintf("%d天", minutes/1440)
	case minutes%60 == 0:
		return fmt.Sprintf("%d小时", minutes/60)
	default:
		return fmt.Sprintf("%d分钟", minutes)
	}
}

func marketTradeDirection(delivery store.MarketNotificationDelivery) (string, int64, int64) {
	if delivery.Snapshot == nil || delivery.Rule == nil {
		return "buy", -1, -1
	}
	buys, sells, ok := snapshotTrades(*delivery.Snapshot, delivery.Rule.WindowMinutes)
	if !ok {
		return "buy", -1, -1
	}
	if sells > buys {
		return "sell", buys, sells
	}
	return "buy", buys, sells
}

func marketEventHeadlineAmount(delivery store.MarketNotificationDelivery) string {
	if delivery.Event == nil {
		return ""
	}
	if value := pointerValue(delivery.Event.USDValue); value != "" {
		return marketUnitValue(delivery.Event.USDValue, "usd", "", true)
	}
	if value := pointerValue(delivery.Event.TokenAmount); value != "" {
		return marketUnitValue(
			delivery.Event.TokenAmount,
			"token",
			delivery.Project.TokenSymbol,
			true,
		)
	}
	return marketPlainText("已触发", "triggered", marketRealtimeEnglish(delivery))
}

func marketEventAmountSummary(delivery store.MarketNotificationDelivery) string {
	if delivery.Event == nil {
		return ""
	}
	parts := make([]string, 0, 2)
	if value := marketUSD(delivery.Event.USDValue); value != "" {
		parts = append(parts, value)
	}
	if value := marketUnitValue(
		delivery.Event.TokenAmount,
		"token",
		delivery.Project.TokenSymbol,
		true,
	); value != "" {
		parts = append(parts, value)
	}
	return strings.Join(parts, " · ")
}

func marketTradeDetails(
	delivery store.MarketNotificationDelivery,
	english bool,
) string {
	if delivery.Event == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	if value := marketUSD(delivery.Event.USDValue); value != "" {
		parts = append(parts, value)
	}
	if value := marketUnitValue(
		delivery.Event.TokenAmount,
		"token",
		delivery.Project.TokenSymbol,
		true,
	); value != "" {
		parts = append(parts, value)
	}
	if value := marketUSD(delivery.Event.PriceUSD); value != "" {
		parts = append(parts, marketPlainText("单价 ", "at ", english)+value)
	}
	return strings.Join(parts, " · ")
}

func marketEventRuleValue(delivery store.MarketNotificationDelivery) *string {
	if delivery.Rule == nil || delivery.Event == nil {
		return nil
	}
	snapshots := make([]store.MarketSnapshot, 0, 1)
	if delivery.Snapshot != nil {
		snapshots = append(snapshots, *delivery.Snapshot)
	}
	return eventValue(*delivery.Rule, delivery.Project, snapshots, *delivery.Event)
}

func marketWallet(delivery store.MarketNotificationDelivery) string {
	var address string
	if delivery.Event != nil {
		address = pointerValue(delivery.Event.WalletAddress)
		if address == "" {
			address = metadataText(delivery.Event.Metadata, "holder_address")
		}
	}
	address = notificationfmt.ShortIdentifier(address)
	label := strings.TrimSpace(delivery.AddressLabel)
	switch {
	case label != "" && address != "":
		return escape(label) + " (" + escape(address) + ")"
	case label != "":
		return escape(label)
	default:
		return escape(address)
	}
}

func marketRankTransition(oldRank, newRank *string, english bool) string {
	oldText := marketRank(oldRank, english)
	newText := marketRank(newRank, english)
	switch {
	case oldText != "" && newText != "":
		return oldText + " → " + newText
	case oldText != "":
		return oldText + " → " + marketPlainText("榜外", "outside list", english)
	case newText != "":
		return marketPlainText("榜外", "outside list", english) + " → " + newText
	default:
		return ""
	}
}

func marketRank(value *string, english bool) string {
	if pointerValue(value) == "" {
		return ""
	}
	if english {
		return "#" + escape(notificationfmt.Percentage(pointerValue(value)))
	}
	return "第 " + escape(notificationfmt.Percentage(pointerValue(value))) + " 名"
}

func marketBalanceTransition(payload []byte, symbol string, english bool) string {
	oldBalance := metadataDecimal(payload, "old_balance")
	newBalance := metadataDecimal(payload, "new_balance")
	if pointerValue(oldBalance) == "" || pointerValue(newBalance) == "" {
		return ""
	}
	return marketUnitValue(oldBalance, "token", symbol, false) + " → " +
		marketUnitValue(newBalance, "token", symbol, false)
}

func marketProgressRemaining(actual *string) string {
	value, ok := pointerRat(actual)
	if !ok {
		return ""
	}
	remaining := new(big.Rat).Sub(big.NewRat(100, 1), value)
	if remaining.Sign() < 0 {
		remaining.SetInt64(0)
	}
	return marketPercent(stringPointer(decimalString(remaining)))
}

func marketLabel(chinese, english string, useEnglish bool) string {
	if useEnglish {
		return english
	}
	return chinese
}

func marketPlainText(chinese, english string, useEnglish bool) string {
	if useEnglish {
		return english
	}
	return chinese
}

func metadataText(payload []byte, key string) string {
	var values map[string]any
	if len(payload) == 0 || json.Unmarshal(payload, &values) != nil {
		return ""
	}
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}
