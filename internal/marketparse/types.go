package marketparse

import (
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
)

const (
	AdapterV2          = "evm_v2"
	AdapterV3          = "evm_v3"
	AdapterAlgebra     = "algebra"
	AdapterSolidly     = "solidly"
	AdapterInfinityCL  = "pancakeswap_infinity_cl"
	AdapterInfinityBin = "pancakeswap_infinity_bin"
	AdapterFourMemeV1  = "four_meme_v1"
	AdapterFourMemeV2  = "four_meme_v2"

	EventBuy              = "buy"
	EventSell             = "sell"
	EventLiquidityAdded   = "liquidity_added"
	EventLiquidityRemoved = "liquidity_removed"
	EventPoolInitialized  = "pool_initialized"
	EventTokenCreated     = "token_created"
	EventTradingStopped   = "trading_stopped"
	EventMigrated         = "migrated"
	EventTokenTransfer    = "token_transfer"

	NativeTokenAddress = "0x0000000000000000000000000000000000000000"

	BSCFourMemeTokenManager = "0x5c952063c7fc8610ffdb798152d69f0b9550762b"
	BSCInfinityCLManager    = "0xa0ffb9c1ce1fe56963b0321b32e7a0302114058b"
	BSCInfinityBinManager   = "0xc697d2898e0d09264376196696c51d7abbbaa4a9"
	BSCInfinityVault        = "0x238a358808379702088667322f80ac48bad5e6c4"
	BSCPancakeV2Factory     = "0xca143ce32fe78f1f7019d7d551a6402fc5350c73"
	BSCPancakeV3Factory     = "0x0bfbcf9fa4f9c56b0f40a671ad40e0805a091865"
)

var (
	addressPattern = regexp.MustCompile(`^0x[0-9a-f]{40}$`)
	hashPattern    = regexp.MustCompile(`^0x[0-9a-f]{64}$`)
)

func ERC20TransferTopic() string {
	return topicERC20Transfer
}

type Token struct {
	Address  string
	Symbol   string
	Decimals uint8
}

// Pool describes the immutable information required to decode a pool's logs.
// For Infinity, LogAddress is the shared PoolManager and PoolKey is the bytes32
// PoolId. For V2/V3, LogAddress and PoolKey are normally the pool address.
type Pool struct {
	ID         int64
	ChainID    uint64
	Protocol   string
	Version    string
	Adapter    string
	PoolKey    string
	LogAddress string
	Token0     Token
	Token1     Token
}

type Emitter struct {
	Address  string
	Protocol string
	Version  string
	Adapter  string
}

type Log struct {
	Address          string
	Topics           []string
	Data             string
	BlockNumber      uint64
	TransactionHash  string
	TransactionIndex uint64
	BlockHash        string
	LogIndex         uint64
	Removed          bool
}

type Receipt struct {
	ChainID          uint64
	TransactionHash  string
	From             string
	To               string
	BlockNumber      uint64
	BlockHash        string
	TransactionIndex uint64
	Logs             []Log
}

// Event is protocol-neutral and contains only exact integer quantities.
// Human-readable decimals and USD valuation are deliberately deferred until
// token metadata and a quote snapshot are available.
type Event struct {
	Type             string
	Protocol         string
	Version          string
	Adapter          string
	PoolID           int64
	PoolKey          string
	TokenAddress     string
	QuoteAddress     string
	WalletAddress    string
	RecipientAddress string
	TokenAmountRaw   string
	QuoteAmountRaw   string
	Amount0DeltaRaw  string
	Amount1DeltaRaw  string
	FeeRaw           string
	TransactionHash  string
	TransactionIndex uint64
	LogIndex         uint64
	LogIndices       []uint64
	BlockNumber      uint64
	BlockHash        string
	Source           string
	Confidence       string
	Metadata         map[string]string
}

type swapLeg struct {
	pool      Pool
	tokenIn   Token
	tokenOut  Token
	amountIn  *big.Int
	amountOut *big.Int
	amount0   *big.Int
	amount1   *big.Int
	sender    string
	recipient string
	log       Log
}

func LogFromRPC(value chain.RPCLog) (Log, error) {
	blockNumber, err := parseHexUint64(value.BlockNumber)
	if err != nil {
		return Log{}, fmt.Errorf("block number: %w", err)
	}
	transactionIndex, err := parseHexUint64(value.TransactionIndex)
	if err != nil {
		return Log{}, fmt.Errorf("transaction index: %w", err)
	}
	logIndex, err := parseHexUint64(value.LogIndex)
	if err != nil {
		return Log{}, fmt.Errorf("log index: %w", err)
	}
	result := Log{
		Address:          strings.ToLower(value.Address),
		Topics:           append([]string(nil), value.Topics...),
		Data:             value.Data,
		BlockNumber:      blockNumber,
		TransactionHash:  strings.ToLower(value.TransactionHash),
		TransactionIndex: transactionIndex,
		BlockHash:        strings.ToLower(value.BlockHash),
		LogIndex:         logIndex,
		Removed:          value.Removed,
	}
	if err := validateLog(result); err != nil {
		return Log{}, err
	}
	return result, nil
}

func normalizePool(value Pool) (Pool, error) {
	var err error
	value.Adapter, err = normalizeAdapter(value.Adapter)
	if err != nil {
		return Pool{}, err
	}
	value.LogAddress, err = normalizeAddress(value.LogAddress)
	if err != nil {
		return Pool{}, fmt.Errorf("pool log address: %w", err)
	}
	value.Token0.Address, err = normalizeAddress(value.Token0.Address)
	if err != nil {
		return Pool{}, fmt.Errorf("token0: %w", err)
	}
	value.Token1.Address, err = normalizeAddress(value.Token1.Address)
	if err != nil {
		return Pool{}, fmt.Errorf("token1: %w", err)
	}
	if value.Token0.Address == value.Token1.Address {
		return Pool{}, fmt.Errorf("pool tokens must differ")
	}
	value.PoolKey = strings.ToLower(strings.TrimSpace(value.PoolKey))
	switch value.Adapter {
	case AdapterInfinityCL, AdapterInfinityBin:
		if !hashPattern.MatchString(value.PoolKey) {
			return Pool{}, fmt.Errorf("Infinity pool key must be bytes32")
		}
	default:
		if value.PoolKey == "" {
			value.PoolKey = value.LogAddress
		}
		if !addressPattern.MatchString(value.PoolKey) && !hashPattern.MatchString(value.PoolKey) {
			return Pool{}, fmt.Errorf("invalid pool key")
		}
	}
	value.Protocol = strings.TrimSpace(value.Protocol)
	if value.Protocol == "" {
		value.Protocol = defaultProtocol(value.Adapter)
	}
	return value, nil
}

func normalizeEmitter(value Emitter) (Emitter, error) {
	var err error
	value.Address, err = normalizeAddress(value.Address)
	if err != nil {
		return Emitter{}, err
	}
	value.Adapter, err = normalizeAdapter(value.Adapter)
	if err != nil {
		return Emitter{}, err
	}
	if value.Adapter != AdapterFourMemeV1 && value.Adapter != AdapterFourMemeV2 &&
		value.Adapter != AdapterV2 && value.Adapter != AdapterV3 &&
		value.Adapter != AdapterAlgebra && value.Adapter != AdapterSolidly &&
		value.Adapter != AdapterInfinityCL && value.Adapter != AdapterInfinityBin {
		return Emitter{}, fmt.Errorf("unsupported emitter adapter")
	}
	if value.Protocol == "" {
		value.Protocol = defaultProtocol(value.Adapter)
	}
	return value, nil
}

func normalizeAdapter(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case AdapterV2, "v2", "uniswap_v2", "pancakeswap_v2", "pancake_v2":
		return AdapterV2, nil
	case AdapterV3, "v3", "uniswap_v3", "pancakeswap_v3", "pancake_v3":
		return AdapterV3, nil
	case AdapterAlgebra, "algebra_v3", "algebra_integral":
		return AdapterAlgebra, nil
	case AdapterSolidly, "solidly_v2", "aerodrome", "velodrome":
		return AdapterSolidly, nil
	case AdapterInfinityCL, "infinity_cl", "pancake_infinity_cl":
		return AdapterInfinityCL, nil
	case AdapterInfinityBin, "infinity_bin", "pancake_infinity_bin":
		return AdapterInfinityBin, nil
	case AdapterFourMemeV1, "fourmeme_v1":
		return AdapterFourMemeV1, nil
	case AdapterFourMemeV2, "fourmeme_v2", "four_meme":
		return AdapterFourMemeV2, nil
	default:
		return "", fmt.Errorf("unsupported parser adapter %q", value)
	}
}

func defaultProtocol(adapter string) string {
	switch adapter {
	case AdapterInfinityCL, AdapterInfinityBin:
		return "pancakeswap_infinity"
	case AdapterFourMemeV1, AdapterFourMemeV2:
		return "four_meme"
	case AdapterAlgebra:
		return "algebra"
	case AdapterSolidly:
		return "solidly"
	default:
		return adapter
	}
}

func normalizeAddress(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if !addressPattern.MatchString(value) {
		return "", fmt.Errorf("invalid address")
	}
	return value, nil
}

func validateLog(value Log) error {
	if _, err := normalizeAddress(value.Address); err != nil {
		return fmt.Errorf("log address: %w", err)
	}
	if value.TransactionHash != "" && !hashPattern.MatchString(strings.ToLower(value.TransactionHash)) {
		return fmt.Errorf("invalid transaction hash")
	}
	if value.BlockHash != "" && !hashPattern.MatchString(strings.ToLower(value.BlockHash)) {
		return fmt.Errorf("invalid block hash")
	}
	for _, topic := range value.Topics {
		if !hashPattern.MatchString(strings.ToLower(topic)) {
			return fmt.Errorf("invalid log topic")
		}
	}
	if _, err := decodeHex(value.Data); err != nil {
		return fmt.Errorf("invalid log data: %w", err)
	}
	return nil
}

func parseHexUint64(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	if len(value) < 3 || !strings.HasPrefix(value, "0x") {
		return 0, fmt.Errorf("invalid hex quantity")
	}
	result, err := strconv.ParseUint(value[2:], 16, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid hex quantity")
	}
	return result, nil
}

func sortedUnique(values []uint64) []uint64 {
	if len(values) == 0 {
		return nil
	}
	result := append([]uint64(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	write := 1
	for read := 1; read < len(result); read++ {
		if result[read] != result[write-1] {
			result[write] = result[read]
			write++
		}
	}
	return result[:write]
}
