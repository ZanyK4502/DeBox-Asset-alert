package marketprotocol

import (
	"context"
	"fmt"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
)

type PoolReader interface {
	PoolTokens(context.Context, string, string, string) (string, string, error)
	PoolFactory(context.Context, string, string, string) (string, error)
}

type Classification struct {
	ChainKey          string
	ChainID           int64
	Protocol          string
	ProtocolVersion   string
	ParserAdapter     string
	FactoryAddress    string
	Token0Address     string
	Token1Address     string
	FactoryVerified   bool
	Supported         bool
	MonitoringLevel   string
	SupportedFeature  []string
	UnsupportedReason string
}

func VerifyPair(
	ctx context.Context,
	reader PoolReader,
	chainKey, tokenAddress string,
	pair marketdata.Pair,
) Classification {
	result := Classification{
		Protocol:          providerProtocol(pair),
		ProtocolVersion:   providerVersion(pair),
		MonitoringLevel:   MonitoringQuoteOnly,
		UnsupportedReason: "该交易池仅支持行情查看，尚未通过链上协议验证。",
	}
	profile, err := chain.ChainProfile(chainKey, "")
	if err != nil {
		result.UnsupportedReason = "不支持所选链。"
		return result
	}
	result.ChainKey = profile.Key
	result.ChainID = profile.ChainID

	if pairChain := chain.NormalizeChainKey(pair.ChainID, ""); pairChain != profile.Key {
		result.UnsupportedReason = "交易池所属链与当前选择不一致，已阻止跨链混用。"
		return result
	}
	token, err := chain.ValidateAddress(tokenAddress)
	if err != nil {
		result.UnsupportedReason = "项目币合约地址无效。"
		return result
	}
	pool, err := chain.ValidateAddress(pair.PairAddress)
	if err != nil {
		result.UnsupportedReason = "交易池地址无效，不能进行链上验证。"
		return result
	}
	if reader == nil {
		result.UnsupportedReason = "链上验证服务不可用，只能查看行情。"
		return result
	}

	token0, token1, err := reader.PoolTokens(ctx, pool, profile.Key, profile.Key)
	if err != nil {
		result.UnsupportedReason = fmt.Sprintf("无法读取交易池代币组成：%v", err)
		return result
	}
	result.Token0Address = token0
	result.Token1Address = token1
	if token0 != token && token1 != token {
		result.UnsupportedReason = "链上交易池不包含当前项目币，已按可疑池处理。"
		return result
	}

	factory, err := reader.PoolFactory(ctx, pool, profile.Key, profile.Key)
	if err != nil {
		result.UnsupportedReason = fmt.Sprintf("无法读取交易池工厂：%v", err)
		return result
	}
	result.FactoryAddress = factory
	deployment, trusted := Lookup(profile.Key, factory)
	if !trusted {
		result.UnsupportedReason = "交易池工厂不在该链的可信协议名单中，只能查看行情。"
		return result
	}

	result.Protocol = deployment.Protocol
	result.ProtocolVersion = deployment.Version
	result.ParserAdapter = deployment.Adapter
	result.FactoryVerified = true
	result.Supported = true
	result.MonitoringLevel = MonitoringFull
	result.SupportedFeature = append([]string(nil), FullMonitoringFeatures...)
	result.UnsupportedReason = ""
	return result
}

func providerProtocol(pair marketdata.Pair) string {
	value := strings.ToLower(strings.TrimSpace(pair.DexID))
	if value == "" {
		return "unknown"
	}
	return value
}

func providerVersion(pair marketdata.Pair) string {
	return strings.ToLower(strings.TrimSpace(strings.Join(pair.Labels, " ")))
}
