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

// PoolFactsVersion identifies the chain-read and whitelist contract. Facts are
// reusable on this exact chain and pool. Collector instances may retain only
// separately factory-bound immutable facts in a short-lived bounded cache;
// project membership and protocol classification still run for every consumer.
const PoolFactsVersion = 1

// PoolFacts deliberately contains no project token or Supported conclusion.
// ClassifyPairWithFacts must check each consumer's membership independently.
type PoolFacts struct {
	Version        int
	ChainKey       string
	ChainID        int64
	PoolAddress    string
	Token0Address  string
	Token1Address  string
	FactoryAddress string
	FailureReason  string
	ReadFailed     bool
}

func ReadPoolFacts(ctx context.Context, reader PoolReader, chainKey string, pair marketdata.Pair) PoolFacts {
	facts := PoolFacts{Version: PoolFactsVersion}
	profile, err := chain.ChainProfile(chainKey, "")
	if err != nil {
		facts.FailureReason = "不支持所选链。"
		return facts
	}
	facts.ChainKey, facts.ChainID = profile.Key, profile.ChainID
	if chain.NormalizeChainKey(pair.ChainID, "") != profile.Key {
		facts.FailureReason = "交易池所属链与当前选择不一致，已阻止跨链混用。"
		return facts
	}
	facts.PoolAddress, err = chain.ValidateAddress(pair.PairAddress)
	if err != nil {
		facts.FailureReason = "交易池地址无效，不能进行链上验证。"
		return facts
	}
	if reader == nil {
		facts.FailureReason = "链上验证服务不可用，只能查看行情。"
		return facts
	}
	token0, token1, err := reader.PoolTokens(ctx, facts.PoolAddress, profile.Key, profile.Key)
	if err != nil {
		facts.ReadFailed = true
		facts.FailureReason = boundedVerificationError("无法读取交易池代币组成", err)
		return facts
	}
	facts.Token0Address, err = chain.ValidateAddress(token0)
	if err == nil {
		facts.Token1Address, err = chain.ValidateAddress(token1)
	}
	if err != nil || facts.Token0Address == facts.Token1Address {
		facts.Token0Address, facts.Token1Address = "", ""
		facts.FailureReason = "无法读取有效的交易池代币组成。"
		return facts
	}
	factory, err := reader.PoolFactory(ctx, facts.PoolAddress, profile.Key, profile.Key)
	if err != nil {
		facts.ReadFailed = true
		facts.FailureReason = boundedVerificationError("无法读取交易池工厂", err)
		return facts
	}
	facts.FactoryAddress, err = chain.ValidateAddress(factory)
	if err != nil {
		facts.FailureReason = "无法读取有效的交易池工厂。"
	}
	return facts
}

func boundedVerificationError(prefix string, err error) string {
	message := err.Error()
	if len(message) > 512 {
		message = message[:512]
	}
	return fmt.Sprintf("%s：%s", prefix, message)
}

func VerifyPair(
	ctx context.Context,
	reader PoolReader,
	chainKey, tokenAddress string,
	pair marketdata.Pair,
) Classification {
	return ClassifyPairWithFacts(chainKey, tokenAddress, pair, ReadPoolFacts(ctx, reader, chainKey, pair))
}

func ClassifyPairWithFacts(chainKey, tokenAddress string, pair marketdata.Pair, facts PoolFacts) Classification {
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
		result.UnsupportedReason = "代币合约地址无效。"
		return result
	}
	pool, err := chain.ValidateAddress(pair.PairAddress)
	if err != nil {
		result.UnsupportedReason = "交易池地址无效，不能进行链上验证。"
		return result
	}
	if facts.Version != PoolFactsVersion || facts.ChainKey != profile.Key || facts.ChainID != profile.ChainID || facts.PoolAddress != pool {
		result.UnsupportedReason = "交易池验证事实与当前链或池不匹配。"
		return result
	}
	if facts.FailureReason != "" {
		result.UnsupportedReason = facts.FailureReason
		return result
	}
	token0, token1 := facts.Token0Address, facts.Token1Address
	result.Token0Address = token0
	result.Token1Address = token1
	if token0 != token && token1 != token {
		result.UnsupportedReason = "链上交易池不包含当前代币，已按可疑池处理。"
		return result
	}

	// A provider's conflicting composition cannot borrow another pair's proof.
	base, baseErr := chain.ValidateAddress(pair.BaseToken.Address)
	quote, quoteErr := chain.ValidateAddress(pair.QuoteToken.Address)
	if baseErr == nil && quoteErr == nil && !((base == token0 && quote == token1) || (base == token1 && quote == token0)) {
		result.UnsupportedReason = "行情来源的代币组成与链上交易池不一致。"
		return result
	}
	factory := facts.FactoryAddress
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
