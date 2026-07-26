package marketview

import (
	"context"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
)

type RuleDefinition struct {
	Code             string   `json:"code"`
	NameZH           string   `json:"name_zh"`
	NameEN           string   `json:"name_en"`
	DescriptionZH    string   `json:"description_zh"`
	DescriptionEN    string   `json:"description_en"`
	Units            []string `json:"units"`
	DefaultUnit      string   `json:"default_unit"`
	DefaultThreshold string   `json:"default_threshold"`
	DefaultWindow    int32    `json:"default_window_minutes"`
	RequiresPool     bool     `json:"requires_pool"`
	Professional     bool     `json:"professional"`
	Allowed          bool     `json:"allowed"`
}

type CatalogResult struct {
	Plan               plans.Plan       `json:"plan"`
	Rules              []RuleDefinition `json:"rules"`
	MonitoringGoals    []string         `json:"monitoring_goals"`
	SupportedChain     string           `json:"supported_chain"`
	SupportedProtocols []string         `json:"supported_protocols"`
}

func (s *Service) Catalog(
	ctx context.Context,
	deboxUserID string,
) (CatalogResult, error) {
	plan, err := s.deps.Entitlements.ActivePlan(ctx, deboxUserID)
	if err != nil {
		return CatalogResult{}, err
	}
	allowed := make(map[string]struct{}, len(plan.AllowedMarketRules))
	for _, code := range plan.AllowedMarketRules {
		allowed[code] = struct{}{}
	}
	definitions := make([]RuleDefinition, 0)
	for _, definition := range marketRuleDefinitions {
		_, enabled := allowed[definition.Code]
		definition.Allowed = enabled
		definitions = append(definitions, definition)
	}
	return CatalogResult{
		Plan:  plan,
		Rules: definitions,
		MonitoringGoals: []string{
			"price", "liquidity", "volume", "large_trade", "holder", "four_meme",
		},
		SupportedChain: "bsc",
		SupportedProtocols: []string{
			"PancakeSwap V2", "PancakeSwap V3", "PancakeSwap Infinity CL",
			"PancakeSwap Infinity Bin", "Four.meme",
		},
	}, nil
}

var marketRuleDefinitions = []RuleDefinition{
	rule(plans.MarketPriceAbove, "价格高于", "Price above", "价格达到或高于目标值时提醒。", "Alert when price reaches or rises above the target.", []string{"usd"}, "usd", "1", 0, true),
	rule(plans.MarketPriceBelow, "价格低于", "Price below", "价格达到或低于目标值时提醒。", "Alert when price reaches or falls below the target.", []string{"usd"}, "usd", "0.1", 0, true),
	rule(plans.MarketPriceIncrease, "价格涨幅", "Price increase", "统计周期内涨幅达到阈值时提醒。", "Alert when the price increase reaches the threshold in the window.", []string{"percent"}, "percent", "5", 60, true),
	rule(plans.MarketPriceDecrease, "价格跌幅", "Price decrease", "统计周期内跌幅达到阈值时提醒。", "Alert when the price decrease reaches the threshold in the window.", []string{"percent"}, "percent", "5", 60, true),
	rule(plans.MarketLiquidityBelow, "流动性低于", "Liquidity below", "主交易池流动性低于阈值时提醒。", "Alert when pool liquidity falls below the threshold.", []string{"usd"}, "usd", "10000", 0, true),
	rule(plans.MarketLiquidityDecrease, "流动性下降", "Liquidity decrease", "统计周期内流动性下降达到阈值时提醒。", "Alert when liquidity decreases by the threshold in the window.", []string{"percent"}, "percent", "10", 60, true),
	rule(plans.MarketVolumeAbove, "成交量超过", "Volume above", "统计周期成交量超过阈值时提醒。", "Alert when volume exceeds the threshold in the window.", []string{"usd"}, "usd", "10000", 60, true),
	rule(plans.MarketVolumeSpike, "成交量突增", "Volume spike", "成交量相对基准放大到指定倍数时提醒。", "Alert when volume reaches the configured multiple of its baseline.", []string{"ratio"}, "ratio", "2", 60, true),
	rule(plans.MarketTradeImbalance, "买卖失衡", "Trade imbalance", "买入或卖出笔数占比达到阈值时提醒。", "Alert when the buy or sell share reaches the threshold.", []string{"percent"}, "percent", "75", 60, true),
	rule(plans.MarketLargeBuy, "大额买入", "Large buy", "单笔买入达到阈值时提醒。", "Alert when a single buy reaches the threshold.", []string{"usd", "token", "percent"}, "usd", "1000", 15, true),
	rule(plans.MarketLargeSell, "大额卖出", "Large sell", "单笔卖出达到阈值时提醒。", "Alert when a single sell reaches the threshold.", []string{"usd", "token", "percent"}, "usd", "1000", 15, true),
	rule(plans.MarketConsecutiveLargeBuy, "连续大额买入", "Consecutive large buys", "周期内连续出现大额买入时提醒。", "Alert on consecutive large buys in the window.", []string{"usd", "token", "percent"}, "usd", "1000", 15, true),
	rule(plans.MarketConsecutiveLargeSell, "连续大额卖出", "Consecutive large sells", "周期内连续出现大额卖出时提醒。", "Alert on consecutive large sells in the window.", []string{"usd", "token", "percent"}, "usd", "1000", 15, true),
	rule(plans.MarketLiquidityAdded, "大额加池", "Liquidity added", "新增流动性达到阈值时提醒。", "Alert when added liquidity reaches the threshold.", []string{"usd", "percent"}, "usd", "5000", 0, true),
	rule(plans.MarketLiquidityRemoved, "大额撤池", "Liquidity removed", "撤出流动性达到阈值时提醒。", "Alert when removed liquidity reaches the threshold.", []string{"usd", "percent"}, "usd", "5000", 0, true),
	rule(plans.MarketNewPool, "新交易池", "New pool", "发现该币新交易池时提醒。", "Alert when a new pool is discovered for the token.", []string{"count"}, "count", "1", 0, false),
	rule(plans.MarketHolderIncrease, "大户增持", "Holder increase", "已跟踪大户增持达到阈值时提醒。", "Alert when a tracked holder increases its position.", []string{"usd", "token", "percent"}, "percent", "5", 0, false),
	rule(plans.MarketHolderDecrease, "大户减持", "Holder decrease", "已跟踪大户减持达到阈值时提醒。", "Alert when a tracked holder decreases its position.", []string{"usd", "token", "percent"}, "percent", "5", 0, false),
	rule(plans.MarketHolderRankEntered, "进入大户榜", "Entered holder ranking", "地址进入指定大户排名时提醒。", "Alert when an address enters the configured holder ranking.", []string{"count"}, "count", "20", 0, false),
	rule(plans.MarketHolderRankExited, "退出大户榜", "Exited holder ranking", "地址退出指定大户排名时提醒。", "Alert when an address exits the configured holder ranking.", []string{"count"}, "count", "20", 0, false),
	rule(plans.MarketFourMemeLargeTrade, "Four.meme 大额交易", "Four.meme large trade", "Four.meme 内盘出现大额买卖时提醒。", "Alert on a large Four.meme bonding-curve trade.", []string{"usd", "token", "percent"}, "usd", "1000", 0, false),
	rule(plans.MarketFourMemeProgress, "Four.meme 进度", "Four.meme progress", "内盘进度达到阈值时提醒。", "Alert when Four.meme progress reaches the threshold.", []string{"progress", "percent"}, "percent", "80", 0, false),
	rule(plans.MarketFourMemeMigration, "Four.meme 迁移", "Four.meme migration", "项目迁移到外盘时提醒。", "Alert when the token migrates to an external pool.", []string{"count"}, "count", "1", 0, false),
}

func rule(
	code, nameZH, nameEN, descriptionZH, descriptionEN string,
	units []string,
	defaultUnit, threshold string,
	window int32,
	requiresPool bool,
) RuleDefinition {
	return RuleDefinition{
		Code:             code,
		NameZH:           nameZH,
		NameEN:           nameEN,
		DescriptionZH:    descriptionZH,
		DescriptionEN:    descriptionEN,
		Units:            units,
		DefaultUnit:      defaultUnit,
		DefaultThreshold: threshold,
		DefaultWindow:    window,
		RequiresPool:     requiresPool,
		Professional:     isProfessionalRule(code),
	}
}

func isProfessionalRule(code string) bool {
	for _, standard := range []string{
		plans.MarketPriceAbove,
		plans.MarketPriceBelow,
		plans.MarketPriceIncrease,
		plans.MarketPriceDecrease,
		plans.MarketLiquidityBelow,
		plans.MarketLiquidityDecrease,
		plans.MarketVolumeAbove,
		plans.MarketVolumeSpike,
		plans.MarketTradeImbalance,
	} {
		if code == standard {
			return false
		}
	}
	return true
}
