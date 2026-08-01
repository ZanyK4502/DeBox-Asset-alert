package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"strings"
	"time"

	boxbotapi "github.com/debox-pro/debox-chat-go-sdk/boxbotapi"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/subscription"
)

func menuText(language string) string {
	if normalizeLanguage(language) == "en" {
		return "🤖 <b>DeBox Asset Alert</b><br/>" +
			"Monitor on-chain addresses and EVM multi-chain token markets, and receive notifications through Monitor Bot.<br/><br/>" +
			"✨ <b>Core Features:</b><br/><br/>" +
			"🌐 <b>Multi-chain Monitoring:</b> Covers major blockchains and L2 networks<br/><br/>" +
			"📍 <b>Address Monitoring:</b> Track balances, incoming and outgoing transfers, approval changes, and interactions with specified addresses<br/><br/>" +
			"📈 <b>Token Monitoring:</b> Track price, liquidity, volume, large trades, holders, and Four.meme activity<br/><br/>" +
			"⚡ <b>Real-time Alerts:</b> Key changes delivered in seconds, with stage alerts<br/><br/>" +
			"📊 <b>Professional Mode:</b> Supports combination rule configuration<br/><br/>" +
			"📢 <b>Multi-channel Notifications:</b> Supports group notifications and daily summaries<br/><br/>" +
			"👉 <b>Quick Start:</b><br/>" +
			"Open the personal monitoring dashboard and securely sign in with your wallet signature 🔐"
	}
	return "🤖 <b>DeBox Asset Alert</b><br/>" +
		"监控链上地址与 EVM 多链代币行情，通过 Monitor Bot 接收通知。<br/><br/>" +
		"✨ <b>核心功能支持：</b><br/><br/>" +
		"🌐 <b>多链监控：</b>覆盖主流公链与 L2<br/><br/>" +
		"📍 <b>地址监控：</b>跟踪地址余额、资金转入转出、授权变化和指定地址交互<br/><br/>" +
		"📈 <b>代币监控：</b>价格、流动性、成交量、大额买卖、大户与 Four.meme 动态<br/><br/>" +
		"⚡ <b>实时提醒：</b>关键变动秒级推送与阶段提醒<br/><br/>" +
		"📊 <b>专业模式：</b>支持组合规则配置<br/><br/>" +
		"📢 <b>多端通知：</b>支持群通知与每日摘要推送<br/><br/>" +
		"👉 <b>快速开始：</b><br/>" +
		"打开个人监控面板后，通过钱包签名完成安全登录 🔐"
}

func featuresText(language string) string {
	if normalizeLanguage(language) == "en" {
		return "<b>Monitoring</b><br/><br/>" +
			"Supported networks: BNB Chain, Ethereum, Base, Polygon, Arbitrum, and Optimism.<br/><br/>" +
			"Monitor native asset balances, or enter an ERC-20 contract to monitor a token balance.<br/><br/>" +
			"- Rule types:<br/>" +
			"• Balance change: alerts when the change reaches the configured amount<br/>" +
			"• Incoming and outgoing transfers: alerts when the transfer reaches the configured amount<br/>" +
			"• Low balance threshold: alerts once when the balance reaches or falls below the threshold; it alerts again only after recovery above it and another drop<br/>" +
			"• High balance threshold: alerts once when the balance reaches or rises above the threshold; it alerts again only after falling below it and another rise<br/>" +
			"• Approval change<br/>" +
			"• Specified address interaction (Professional)<br/><br/>" +
			"- EVM multi-chain token monitoring:<br/>" +
			"• Free users can search basic token information; identity verification and pool discovery require Standard or Professional<br/>" +
			"• Standard supports one token, its primary pool, and price/liquidity/volume rules<br/>" +
			"• Professional supports five tokens, multiple pools, large trades, holder changes, Four.meme, group alerts, and combination rules<br/>" +
			"• Supported protocols: PancakeSwap V2/V3/Infinity and Four.meme<br/><br/>" +
			"- Delivery modes:<br/>" +
			"• Real-time: sends after each trigger<br/>" +
			"• Stage alert (Standard and Professional): counts events in a user-defined cycle, sends once when the configured count is reached, then resets for the next cycle<br/>" +
			"• Combination rule (Professional): uses at least two dedicated member rules; it sends one combined alert after every member reaches its own count in the same cycle. Members do not send individual alerts.<br/><br/>" +
			"Stage and combination events remain available in the dashboard for 30 days.<br/><br/>" +
			"Each summary covers the previous scheduled cutoff through the current cutoff; the first covers the previous 24 hours.<br/><br/>" +
			"If a summary group is unbound, only that group is removed from the delivery targets. The daily summary is turned off if no targets remain."
	}
	return "<b>监控能力</b><br/><br/>" +
		"支持 BNB Chain、Ethereum、Base、Polygon、Arbitrum、Optimism。<br/><br/>" +
		"可监控原生资产余额，也可填写 ERC20 合约监控代币余额。<br/><br/>" +
		"- 规则包括：<br/>" +
		"• 余额变化：变化量达到设定金额时提醒<br/>" +
		"• 转入与转出：金额达到设定值时提醒<br/>" +
		"• 低余额阈值：余额达到或低于阈值时提醒一次；持续低于不重复，恢复至阈值以上后再次跌破才重新提醒<br/>" +
		"• 高余额阈值：余额达到或高于阈值时提醒一次；持续高于不重复，回落至阈值以下后再次突破才重新提醒<br/>" +
		"• 授权变化<br/>" +
		"• 指定地址交互（专业版）<br/><br/>" +
		"- EVM 多链代币监控：<br/>" +
		"• 免费版可搜索代币基础信息；同币验证与交易池查询需标准版或专业版<br/>" +
		"• 标准版可持续监控 1 个代币、主交易池及价格/流动性/成交量规则<br/>" +
		"• 专业版可监控 5 个代币、多交易池、大额买卖、大户变化、Four.meme、群通知和组合规则<br/>" +
		"• 支持 PancakeSwap V2/V3/Infinity 与 Four.meme<br/><br/>" +
		"- 通知模式：<br/>" +
		"• 实时提醒：每次触发后发送<br/>" +
		"• 阶段提醒（标准版、专业版）：按用户设置的周期累计事件，达到设定次数后发送一次，进入下一周期后重新计数<br/>" +
		"• 组合规则（专业版）：至少包含两条专用成员规则；同一周期内所有成员分别达到设定次数后发送一条总通知，成员不会单独通知<br/><br/>" +
		"阶段提醒和组合规则事件会在个人监控面板保留 30 天。<br/><br/>" +
		"每期摘要统计上一次计划推送时间至本次计划推送时间；首次统计此前 24 小时。<br/><br/>" +
		"解绑摘要群后，只会从推送对象中移除该群；如果没有剩余推送对象，每日摘要会自动关闭。"
}

func (s *Service) plansText(language string) string {
	standard, _ := s.deps.Catalog.Get(plans.Standard)
	professional, _ := s.deps.Catalog.Get(plans.Professional)
	standardMonthly, _ := standard.BillingOption(plans.Monthly)
	standardQuarterly, _ := standard.BillingOption(plans.Quarterly)
	standardAnnual, _ := standard.BillingOption(plans.Annual)
	professionalMonthly, _ := professional.BillingOption(plans.Monthly)
	professionalQuarterly, _ := professional.BillingOption(plans.Quarterly)
	professionalAnnual, _ := professional.BillingOption(plans.Annual)
	if normalizeLanguage(language) == "en" {
		return "<b>Plans</b><br/><br/>" +
			"Free: 1 address, 1 basic real-time rule, no expiration, up to 5 alerts per day, private alerts only.<br/><br/>" +
			fmt.Sprintf(
				"Standard: %s %s / %d days, %s %s / %d days, or %s %s / %d days; 3 addresses, 10 total rules, real-time or stage alerts, 1 token with primary-pool price/liquidity/volume monitoring, private delivery, and a unified private daily summary.<br/><br/>",
				standardMonthly.Price,
				standard.Asset,
				standardMonthly.Days,
				standardQuarterly.Price,
				standard.Asset,
				standardQuarterly.Days,
				standardAnnual.Price,
				standard.Asset,
				standardAnnual.Days,
			) +
			fmt.Sprintf(
				"Professional: %s %s / %d days, %s %s / %d days, or %s %s / %d days; 20 addresses, 100 total rules, 5 tokens, multi-pool monitoring, large trades, holders and Four.meme, combination rules, alerts to 5 groups, and unified daily summaries. Combination members use the rule quota.<br/><br/>",
				professionalMonthly.Price,
				professional.Asset,
				professionalMonthly.Days,
				professionalQuarterly.Price,
				professional.Asset,
				professionalQuarterly.Days,
				professionalAnnual.Price,
				professional.Asset,
				professionalAnnual.Days,
			) +
			"While a paid plan is active, only the same plan can be renewed, with any billing period; choose another plan after it expires.<br/><br/>" +
			"Pay with USDT on BNB Chain. The subscription activates after 3 block confirmations; failed verification does not activate it.<br/><br/>" +
			"Subscriptions take effect immediately. Digital service purchases are non-refundable, so please review the plan before purchase."
	}
	return "<b>订阅方案</b><br/><br/>" +
		"免费版：1 个地址，1 条基础实时规则，永久有效，每日最多 5 次提醒，仅私聊通知。<br/><br/>" +
		fmt.Sprintf(
			"标准版：%s %s / %d 天、%s %s / %d 天或 %s %s / %d 天；3 个地址，10 条总规则，支持实时或阶段提醒、1 个代币及主交易池的价格/流动性/成交量监控、私聊通知和统一每日摘要。<br/><br/>",
			standardMonthly.Price,
			standard.Asset,
			standardMonthly.Days,
			standardQuarterly.Price,
			standard.Asset,
			standardQuarterly.Days,
			standardAnnual.Price,
			standard.Asset,
			standardAnnual.Days,
		) +
		fmt.Sprintf(
			"专业版：%s %s / %d 天、%s %s / %d 天或 %s %s / %d 天；20 个地址，100 条总规则，支持 5 个代币、多交易池、大额买卖、大户与 Four.meme、组合规则、5 个群通知和统一每日摘要；组合成员会占用规则额度。<br/><br/>",
			professionalMonthly.Price,
			professional.Asset,
			professionalMonthly.Days,
			professionalQuarterly.Price,
			professional.Asset,
			professionalQuarterly.Days,
			professionalAnnual.Price,
			professional.Asset,
			professionalAnnual.Days,
		) +
		"付费套餐有效期内只能续费同一套餐，可选择任意付费周期并顺延到期时间；套餐到期后才能选择其他套餐。<br/><br/>" +
		"使用 BNB Chain USDT 支付，交易达到 3 个区块确认后开通订阅；支付验证失败不会开通。<br/><br/>" +
		"订阅开通后立即生效，虚拟服务类权益不支持退款，请确认套餐内容后再购买。"
}

func groupEntryText(message *boxbotapi.Message) string {
	userName := ""
	if message != nil && message.From != nil {
		userName = firstNonEmpty(message.From.Name, message.From.UserId)
	}
	prefix := ""
	if userName != "" {
		prefix = "@" + escapeText(userName) + " "
	}
	return prefix +
		"我是 DeBox Asset Alert 链上监控助理，请私聊 Bot 发送 /start 查看使用详情或打开个人监控面板。<br/><br/>" +
		"I'm the DeBox Asset Alert on-chain monitoring assistant. " +
		"Message the Bot and send /start to view usage details, or open your personal monitoring dashboard."
}

func (s *Service) subscriptionText(
	ctx context.Context,
	userID string,
	language string,
) (string, error) {
	english := normalizeLanguage(language) == "en"
	if strings.TrimSpace(userID) == "" {
		if english {
			return "We could not identify your DeBox User ID. Open the monitoring dashboard to view your subscription.", nil
		}
		return "暂时无法识别你的 DeBox 用户 ID，请打开个人监控面板查看订阅。", nil
	}
	current, err := s.deps.Subscriptions.Entitlement(ctx, userID)
	if err != nil {
		return "", err
	}
	planName := escapeText(localizedPlanName(current, language))
	if current.Plan.Code == plans.Free {
		if english {
			return fmt.Sprintf(
				"<b>Subscription</b><br/>"+
					"Current plan: %s<br/>"+
					"Valid through: No expiration<br/>"+
					"Monitoring rules: %d / %d<br/>"+
					"Group alerts: %d / %d<br/>"+
					"Token monitoring: %d / %d",
				planName,
				current.RuleCount,
				current.Plan.RuleLimit,
				current.GroupCount,
				current.Plan.GroupLimit,
				current.MarketProjectCount,
				current.Plan.MarketProjectLimit,
			), nil
		}
		return fmt.Sprintf(
			"<b>订阅有效期</b><br/>"+
				"当前方案：%s<br/>"+
				"有效期：永久有效<br/>"+
				"监控规则：%d / %d<br/>"+
				"群通知：%d / %d<br/>"+
				"代币：%d / %d",
			planName,
			current.RuleCount,
			current.Plan.RuleLimit,
			current.GroupCount,
			current.Plan.GroupLimit,
			current.MarketProjectCount,
			current.Plan.MarketProjectLimit,
		), nil
	}
	if current.Permanent {
		if english {
			return fmt.Sprintf(
				"<b>Subscription</b><br/>"+
					"Current plan: %s<br/>"+
					"Valid through: No expiration<br/>"+
					"Monitoring rules: %d / %d<br/>"+
					"Group alerts: %d / %d<br/>"+
					"Token monitoring: %d / %d",
				planName,
				current.RuleCount,
				current.Plan.RuleLimit,
				current.GroupCount,
				current.Plan.GroupLimit,
				current.MarketProjectCount,
				current.Plan.MarketProjectLimit,
			), nil
		}
		return fmt.Sprintf(
			"<b>订阅状态</b><br/>"+
				"当前方案：%s<br/>"+
				"有效期：永久有效<br/>"+
				"监控规则：%d / %d<br/>"+
				"群通知：%d / %d<br/>"+
				"代币：%d / %d",
			planName,
			current.RuleCount,
			current.Plan.RuleLimit,
			current.GroupCount,
			current.Plan.GroupLimit,
			current.MarketProjectCount,
			current.Plan.MarketProjectLimit,
		), nil
	}

	expiresAt := "-"
	if current.Subscription != nil {
		expiresAt = formatUTCDateTime(current.Subscription.ExpiresAt, english)
	}
	if english {
		return fmt.Sprintf(
			"<b>Subscription</b><br/>"+
				"Current plan: %s<br/>"+
				"Days remaining: %d<br/>"+
				"Expires at: %s<br/>"+
				"Monitoring rules: %d / %d<br/>"+
				"Group alerts: %d / %d<br/>"+
				"Token monitoring: %d / %d",
			planName,
			current.DaysRemaining,
			escapeText(expiresAt),
			current.RuleCount,
			current.Plan.RuleLimit,
			current.GroupCount,
			current.Plan.GroupLimit,
			current.MarketProjectCount,
			current.Plan.MarketProjectLimit,
		), nil
	}
	return fmt.Sprintf(
		"<b>订阅有效期</b><br/>"+
			"当前方案：%s<br/>"+
			"剩余天数：%d 天<br/>"+
			"到期时间：%s<br/>"+
			"监控规则：%d / %d<br/>"+
			"群通知：%d / %d<br/>"+
			"代币：%d / %d",
		planName,
		current.DaysRemaining,
		escapeText(expiresAt),
		current.RuleCount,
		current.Plan.RuleLimit,
		current.GroupCount,
		current.Plan.GroupLimit,
		current.MarketProjectCount,
		current.Plan.MarketProjectLimit,
	), nil
}

func formatUTCDateTime(value time.Time, english bool) string {
	formatted := value.UTC().Format("2006-01-02 15:04:05")
	if english {
		return formatted + " (UTC)"
	}
	return formatted + "（UTC）"
}

func localizedPlanName(current subscription.Entitlement, language string) string {
	if normalizeLanguage(language) != "en" {
		if current.Plan.Name == "" {
			return "未开通"
		}
		return current.Plan.Name
	}
	switch current.Plan.Code {
	case plans.Free:
		return "Free"
	case plans.Standard:
		return "Standard"
	case plans.Professional:
		return "Professional"
	case "":
		return "Not active"
	default:
		return current.Plan.Name
	}
}

func (s *Service) balanceText(
	ctx context.Context,
	userID string,
	language string,
) (string, error) {
	english := normalizeLanguage(language) == "en"
	if strings.TrimSpace(userID) == "" {
		if english {
			return "We could not identify your DeBox User ID. Open the monitoring dashboard to check your balance.", nil
		}
		return "暂时无法识别你的 DeBox 用户 ID，请打开个人监控面板查询余额。", nil
	}
	profile, err := s.deps.DeBox.UserInfo(ctx, userID, "")
	if err != nil {
		return "", err
	}
	address := strings.TrimSpace(extractAddress(profile))
	if address == "" {
		if english {
			return "No wallet address was found in your DeBox profile. Connect a wallet in the monitoring dashboard first.", nil
		}
		return "没有从 DeBox 用户资料中识别到钱包地址，请在个人监控面板连接钱包后查询。", nil
	}
	token, err := s.deps.Chain.Balance(
		ctx,
		address,
		s.deps.Settings.SubscriptionTokenAddress,
		"bsc",
		s.deps.Settings.DefaultChainKey,
	)
	if err != nil {
		return "", err
	}
	gas, err := s.deps.Chain.Balance(
		ctx,
		address,
		"",
		"bsc",
		s.deps.Settings.DefaultChainKey,
	)
	if err != nil {
		return "", err
	}
	wallet := shortAddress(address)
	if english {
		return fmt.Sprintf(
			"<b>Balance</b><br/>"+
				"Wallet: %s<br/>"+
				"Network: %s<br/>"+
				"Balance: %s %s<br/>"+
				"Gas balance: %s %s",
			escapeText(wallet),
			escapeText(token.ChainName),
			escapeText(token.Value),
			escapeText(token.Symbol),
			escapeText(gas.Value),
			escapeText(gas.Symbol),
		), nil
	}
	return fmt.Sprintf(
		"<b>余额查询</b><br/>"+
			"钱包：%s<br/>"+
			"网络：%s<br/>"+
			"余额：%s %s<br/>"+
			"Gas 费余额：%s %s",
		escapeText(wallet),
		escapeText(token.ChainName),
		escapeText(token.Value),
		escapeText(token.Symbol),
		escapeText(gas.Value),
		escapeText(gas.Symbol),
	), nil
}

func (s *Service) callbackText(
	ctx context.Context,
	data string,
	userID string,
	language string,
) (string, error) {
	switch {
	case data == "alert:intro", strings.HasPrefix(data, "alert:language:"):
		return menuText(language), nil
	case data == "alert:features":
		return featuresText(language), nil
	case data == "alert:plans":
		return s.plansText(language), nil
	case data == "alert:subscription":
		return s.subscriptionText(ctx, userID, language)
	case data == "alert:balance":
		return s.balanceText(ctx, userID, language)
	case data == "alert:aggregate-details":
		if normalizeLanguage(language) == "en" {
			return "<b>Summary Details</b><br/><br/>View more event details for stage alerts from individual rules and combination rules.", nil
		}
		return "<b>汇总类通知详情</b><br/><br/>单条规则的阶段提醒事件与组合规则中的更多事件详情。", nil
	case data == "alert:market":
		if normalizeLanguage(language) == "en" {
			return "<b>Token Monitoring</b><br/><br/>Query a BNB Chain token, choose its pools, and create price, liquidity, volume, large-trade, holder, or Four.meme rules in the dashboard.", nil
		}
		return "<b>代币监控</b><br/><br/>在监控面板输入 BNB Chain 代币合约，选择交易池后即可创建价格、流动性、成交量、大额买卖、大户或 Four.meme 规则。", nil
	case data == "alert:swap":
		if normalizeLanguage(language) == "en" {
			return "<b>Swap</b><br/>Swap assets for USDT on BSC", nil
		}
		return "<b>闪兑</b><br/>将资产兑换为 BSC 链 USDT", nil
	case data == "alert:renew":
		if normalizeLanguage(language) == "en" {
			if s.publicAppURL != "" {
				return "Open the monitoring dashboard to renew: " + escapeText(s.publicAppURL), nil
			}
			return "Please renew in the H5 app.", nil
		}
		if s.publicAppURL != "" {
			return "请打开个人监控面板续费：" + escapeText(s.publicAppURL), nil
		}
		return "请在 H5 中续费。", nil
	default:
		return menuText(language), nil
	}
}

func (s *Service) menuMarkup(language string) boxbotapi.InlineKeyboardMarkup {
	english := normalizeLanguage(language) == "en"
	rows := [][]boxbotapi.InlineKeyboardButton{
		boxbotapi.NewInlineKeyboardRow(
			buttonData(
				choice(english, "Monitoring", "监控能力"),
				localizedCallbackData("features", language),
			),
			buttonData(
				choice(english, "Plans", "订阅方案"),
				localizedCallbackData("plans", language),
			),
		),
		boxbotapi.NewInlineKeyboardRow(
			buttonData(
				choice(english, "Subscription", "订阅有效期"),
				localizedCallbackData("subscription", language),
			),
			buttonData(
				choice(english, "Balance", "余额查询"),
				localizedCallbackData("balance", language),
			),
		),
	}
	renewButton := buttonData(
		choice(english, "Renew", "快捷续费"),
		localizedCallbackData("renew", language),
	)
	if s.publicAppURL != "" {
		renewButton = buttonURL(choice(english, "Renew", "快捷续费"), s.publicAppURL+"#renew")
	}
	rows = append(rows, boxbotapi.NewInlineKeyboardRow(
		buttonData(
			choice(english, "Swap", "闪兑"),
			localizedCallbackData("swap", language),
		),
		renewButton,
	))
	marketButton := buttonData(
		choice(english, "Token Monitor", "代币监控"),
		localizedCallbackData("market", language),
	)
	if s.publicAppURL != "" {
		marketButton = buttonURL(
			choice(english, "Token Monitor", "代币监控"),
			s.publicAppURL+"#market",
		)
	}
	rows = append(rows, boxbotapi.NewInlineKeyboardRow(marketButton))
	if s.publicAppURL != "" {
		rows = append(rows, boxbotapi.NewInlineKeyboardRow(
			buttonURL(
				choice(english, "Dashboard", "个人监控面板"),
				s.publicAppURL,
			),
		))
	}
	if english {
		rows = append(rows, boxbotapi.NewInlineKeyboardRow(
			buttonData("中文", "alert:language:zh"),
		))
	} else {
		rows = append(rows, boxbotapi.NewInlineKeyboardRow(
			buttonData("English", "alert:language:en"),
		))
	}
	return boxbotapi.NewInlineKeyboardMarkup(rows...)
}

func (s *Service) backMarkup(language string) boxbotapi.InlineKeyboardMarkup {
	english := normalizeLanguage(language) == "en"
	buttons := []boxbotapi.InlineKeyboardButton{
		buttonData(
			choice(english, "Home", "返回主页"),
			localizedCallbackData("intro", language),
		),
	}
	if s.publicAppURL != "" {
		buttons = append(buttons, buttonURL(
			choice(english, "Dashboard", "个人监控面板"),
			s.publicAppURL,
		))
	}
	return boxbotapi.NewInlineKeyboardMarkup(
		boxbotapi.NewInlineKeyboardRow(buttons...),
	)
}

func (s *Service) groupEntryMarkup(_ string) boxbotapi.InlineKeyboardMarkup {
	buttons := make([]boxbotapi.InlineKeyboardButton, 0, 2)
	if privateURL := s.botPrivateChatURL(); privateURL != "" {
		buttons = append(buttons, buttonURL("私聊 Chat", privateURL))
	}
	if s.publicAppURL != "" {
		buttons = append(buttons, buttonURL("面板 Board", s.publicAppURL))
	}
	if len(buttons) == 0 {
		return boxbotapi.NewInlineKeyboardMarkup()
	}
	return boxbotapi.NewInlineKeyboardMarkup(
		boxbotapi.NewInlineKeyboardRow(buttons...),
	)
}

func (s *Service) swapMarkup(language string) boxbotapi.InlineKeyboardMarkup {
	english := normalizeLanguage(language) == "en"
	return boxbotapi.NewInlineKeyboardMarkup(
		boxbotapi.NewInlineKeyboardRow(
			buttonChain(
				choice(english, "Start swap", "开始兑换"),
				swapPayload(s.deps.Settings.SubscriptionTokenAddress),
			),
			buttonData(
				choice(english, "Home", "返回主页"),
				localizedCallbackData("intro", language),
			),
		),
	)
}

func (s *Service) aggregateDetailsMarkup(language string) boxbotapi.InlineKeyboardMarkup {
	english := normalizeLanguage(language) == "en"
	return boxbotapi.NewInlineKeyboardMarkup(
		boxbotapi.NewInlineKeyboardRow(
			buttonURL(
				choice(english, "View", "查看"),
				s.publicAppURL+"#aggregateEventsSection",
			),
			buttonData(
				choice(english, "Home", "返回主页"),
				localizedCallbackData("intro", language),
			),
		),
	)
}

func (s *Service) callbackMarkup(
	data string,
	language string,
) boxbotapi.InlineKeyboardMarkup {
	if data == "alert:intro" || strings.HasPrefix(data, "alert:language:") {
		return s.menuMarkup(language)
	}
	if data == "alert:swap" {
		return s.swapMarkup(language)
	}
	if data == "alert:aggregate-details" {
		return s.aggregateDetailsMarkup(language)
	}
	return s.backMarkup(language)
}

func buttonData(text, data string) boxbotapi.InlineKeyboardButton {
	return boxbotapi.NewInlineKeyboardButtonData(text, data)
}

func buttonURL(text, target string) boxbotapi.InlineKeyboardButton {
	return boxbotapi.NewInlineKeyboardButtonURL(text, target)
}

func buttonChain(text, payload string) boxbotapi.InlineKeyboardButton {
	return boxbotapi.NewInlineKeyboardButtonDataWithColor(
		text,
		payload,
		"debox://wallet/request",
		"",
		"#16C784",
	)
}

func swapPayload(tokenAddress string) string {
	payload := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  []struct {
			FromAddress string `json:"fromAddress"`
			ToAddress   string `json:"toAddress"`
			FromChainID string `json:"fromChainId"`
			ToChainID   string `json:"toChainId"`
		} `json:"params"`
	}{
		JSONRPC: "2.0",
		ID:      106,
		Method:  "swap",
	}
	payload.Params = append(payload.Params, struct {
		FromAddress string `json:"fromAddress"`
		ToAddress   string `json:"toAddress"`
		FromChainID string `json:"fromChainId"`
		ToChainID   string `json:"toChainId"`
	}{
		FromAddress: "0xEeeeeEeeeEeEeeEeEeEeeEEEeeeeEeeeeeeeEEeE",
		ToAddress:   tokenAddress,
		FromChainID: "0x38",
		ToChainID:   "0x38",
	})
	encoded, _ := json.Marshal(payload)
	return string(encoded)
}

func extractAddress(payload map[string]any) string {
	candidates := []map[string]any{payload}
	if nested, ok := payload["data"].(map[string]any); ok {
		candidates = append(candidates, nested)
	}
	for _, candidate := range candidates {
		for _, key := range []string{"address", "walletAddress", "wallet_address"} {
			if value := strings.TrimSpace(fmt.Sprint(candidate[key])); value != "" &&
				value != "<nil>" {
				return value
			}
		}
	}
	return ""
}

func escapeText(value string) string {
	return html.EscapeString(value)
}

func shortAddress(value string) string {
	if len(value) <= 16 {
		return value
	}
	return value[:8] + "..." + value[len(value)-6:]
}

func choice(english bool, englishText, chineseText string) string {
	if english {
		return englishText
	}
	return chineseText
}
