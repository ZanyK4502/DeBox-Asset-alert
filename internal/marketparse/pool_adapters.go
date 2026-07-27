package marketparse

import (
	"fmt"
	"math/big"
)

func parsePoolAdapter(pool Pool, log Log) (*swapLeg, []Event, bool, error) {
	topic := log.Topics[0]
	switch pool.Adapter {
	case AdapterV2:
		return parseV2(pool, log, topic)
	case AdapterV3:
		return parseV3(pool, log, topic)
	case AdapterAlgebra:
		return parseAlgebra(pool, log, topic)
	case AdapterSolidly:
		return parseSolidly(pool, log, topic)
	case AdapterInfinityCL:
		return parseInfinityCL(pool, log, topic)
	case AdapterInfinityBin:
		return parseInfinityBin(pool, log, topic)
	default:
		return nil, nil, false, fmt.Errorf("unsupported pool adapter %s", pool.Adapter)
	}
}

func parseAlgebra(pool Pool, log Log, topic string) (*swapLeg, []Event, bool, error) {
	// Algebra legacy pools deliberately retain the Uniswap V3 event ABI.
	switch topic {
	case topicV3Initialize, topicV3Swap, topicV3Mint, topicV3Burn:
		return parseV3(pool, log, topic)
	case topicAlgebraSwapIntegral:
		if len(log.Topics) != 3 {
			return nil, nil, true, fmt.Errorf("Algebra Swap requires 3 topics")
		}
		values, err := exactWords(log.Data, 7)
		if err != nil {
			return nil, nil, true, fmt.Errorf("Algebra Swap: %w", err)
		}
		sender, err := topicAddress(log.Topics[1])
		if err != nil {
			return nil, nil, true, err
		}
		recipient, err := topicAddress(log.Topics[2])
		if err != nil {
			return nil, nil, true, err
		}
		leg, err := makeSwapLeg(
			pool, log, sender, recipient, signed(values[0]), signed(values[1]),
		)
		return leg, nil, true, err
	case topicAlgebraBurnIntegral:
		if len(log.Topics) != 4 {
			return nil, nil, true, fmt.Errorf("Algebra Burn requires 4 topics")
		}
		values, err := exactWords(log.Data, 4)
		if err != nil {
			return nil, nil, true, fmt.Errorf("Algebra Burn: %w", err)
		}
		owner, err := topicAddress(log.Topics[1])
		if err != nil {
			return nil, nil, true, err
		}
		return nil, poolAmountEvents(
			pool, log, EventLiquidityRemoved, owner,
			unsigned(values[1]), unsigned(values[2]),
		), true, nil
	default:
		return nil, nil, false, nil
	}
}

func parseSolidly(pool Pool, log Log, topic string) (*swapLeg, []Event, bool, error) {
	switch topic {
	case topicSolidlySwap:
		if len(log.Topics) != 3 {
			return nil, nil, true, fmt.Errorf("Solidly Swap requires 3 topics")
		}
		values, err := exactWords(log.Data, 4)
		if err != nil {
			return nil, nil, true, fmt.Errorf("Solidly Swap: %w", err)
		}
		sender, err := topicAddress(log.Topics[1])
		if err != nil {
			return nil, nil, true, err
		}
		recipient, err := topicAddress(log.Topics[2])
		if err != nil {
			return nil, nil, true, err
		}
		amount0 := new(big.Int).Sub(unsigned(values[0]), unsigned(values[2]))
		amount1 := new(big.Int).Sub(unsigned(values[1]), unsigned(values[3]))
		leg, err := makeSwapLeg(pool, log, sender, recipient, amount0, amount1)
		return leg, nil, true, err
	case topicSolidlyMint:
		if len(log.Topics) != 2 {
			return nil, nil, true, fmt.Errorf("Solidly Mint requires 2 topics")
		}
		values, err := exactWords(log.Data, 2)
		if err != nil {
			return nil, nil, true, fmt.Errorf("Solidly Mint: %w", err)
		}
		sender, err := topicAddress(log.Topics[1])
		if err != nil {
			return nil, nil, true, err
		}
		return nil, poolAmountEvents(
			pool, log, EventLiquidityAdded, sender,
			unsigned(values[0]), unsigned(values[1]),
		), true, nil
	case topicSolidlyBurn:
		if len(log.Topics) != 3 {
			return nil, nil, true, fmt.Errorf("Solidly Burn requires 3 topics")
		}
		values, err := exactWords(log.Data, 2)
		if err != nil {
			return nil, nil, true, fmt.Errorf("Solidly Burn: %w", err)
		}
		sender, err := topicAddress(log.Topics[1])
		if err != nil {
			return nil, nil, true, err
		}
		recipient, err := topicAddress(log.Topics[2])
		if err != nil {
			return nil, nil, true, err
		}
		events := poolAmountEvents(
			pool, log, EventLiquidityRemoved, sender,
			unsigned(values[0]), unsigned(values[1]),
		)
		for index := range events {
			events[index].RecipientAddress = recipient
		}
		return nil, events, true, nil
	default:
		return nil, nil, false, nil
	}
}

func parseV2(pool Pool, log Log, topic string) (*swapLeg, []Event, bool, error) {
	switch topic {
	case topicV2Swap:
		if len(log.Topics) != 3 {
			return nil, nil, true, fmt.Errorf("V2 Swap requires 3 topics")
		}
		values, err := exactWords(log.Data, 4)
		if err != nil {
			return nil, nil, true, fmt.Errorf("V2 Swap: %w", err)
		}
		sender, err := topicAddress(log.Topics[1])
		if err != nil {
			return nil, nil, true, fmt.Errorf("V2 Swap sender: %w", err)
		}
		recipient, err := topicAddress(log.Topics[2])
		if err != nil {
			return nil, nil, true, fmt.Errorf("V2 Swap recipient: %w", err)
		}
		amount0 := new(big.Int).Sub(unsigned(values[0]), unsigned(values[2]))
		amount1 := new(big.Int).Sub(unsigned(values[1]), unsigned(values[3]))
		leg, err := makeSwapLeg(pool, log, sender, recipient, amount0, amount1)
		return leg, nil, true, err
	case topicV2Mint:
		if len(log.Topics) != 2 {
			return nil, nil, true, fmt.Errorf("V2 Mint requires 2 topics")
		}
		values, err := exactWords(log.Data, 2)
		if err != nil {
			return nil, nil, true, fmt.Errorf("V2 Mint: %w", err)
		}
		sender, err := topicAddress(log.Topics[1])
		if err != nil {
			return nil, nil, true, err
		}
		return nil, poolAmountEvents(pool, log, EventLiquidityAdded, sender, unsigned(values[0]), unsigned(values[1])), true, nil
	case topicV2Burn:
		if len(log.Topics) != 3 {
			return nil, nil, true, fmt.Errorf("V2 Burn requires 3 topics")
		}
		values, err := exactWords(log.Data, 2)
		if err != nil {
			return nil, nil, true, fmt.Errorf("V2 Burn: %w", err)
		}
		sender, err := topicAddress(log.Topics[1])
		if err != nil {
			return nil, nil, true, err
		}
		recipient, err := topicAddress(log.Topics[2])
		if err != nil {
			return nil, nil, true, err
		}
		events := poolAmountEvents(pool, log, EventLiquidityRemoved, sender, unsigned(values[0]), unsigned(values[1]))
		for index := range events {
			events[index].RecipientAddress = recipient
		}
		return nil, events, true, nil
	default:
		return nil, nil, false, nil
	}
}

func parseV3(pool Pool, log Log, topic string) (*swapLeg, []Event, bool, error) {
	switch topic {
	case topicV3Swap, topicPancakeV3Swap:
		if len(log.Topics) != 3 {
			return nil, nil, true, fmt.Errorf("V3 Swap requires 3 topics")
		}
		wordCount := 5
		if topic == topicPancakeV3Swap {
			wordCount = 7
		}
		values, err := exactWords(log.Data, wordCount)
		if err != nil {
			return nil, nil, true, fmt.Errorf("V3 Swap: %w", err)
		}
		sender, err := topicAddress(log.Topics[1])
		if err != nil {
			return nil, nil, true, err
		}
		recipient, err := topicAddress(log.Topics[2])
		if err != nil {
			return nil, nil, true, err
		}
		leg, err := makeSwapLeg(pool, log, sender, recipient, signed(values[0]), signed(values[1]))
		return leg, nil, true, err
	case topicV3Initialize:
		if len(log.Topics) != 1 {
			return nil, nil, true, fmt.Errorf("V3 Initialize requires 1 topic")
		}
		if _, err := exactWords(log.Data, 2); err != nil {
			return nil, nil, true, fmt.Errorf("V3 Initialize: %w", err)
		}
		return nil, poolLifecycleEvents(pool, log, EventPoolInitialized, ""), true, nil
	case topicV3Mint:
		if len(log.Topics) != 4 {
			return nil, nil, true, fmt.Errorf("V3 Mint requires 4 topics")
		}
		values, err := exactWords(log.Data, 4)
		if err != nil {
			return nil, nil, true, fmt.Errorf("V3 Mint: %w", err)
		}
		sender, err := wordAddress(values[0])
		if err != nil {
			return nil, nil, true, err
		}
		return nil, poolAmountEvents(pool, log, EventLiquidityAdded, sender, unsigned(values[2]), unsigned(values[3])), true, nil
	case topicV3Burn:
		if len(log.Topics) != 4 {
			return nil, nil, true, fmt.Errorf("V3 Burn requires 4 topics")
		}
		values, err := exactWords(log.Data, 3)
		if err != nil {
			return nil, nil, true, fmt.Errorf("V3 Burn: %w", err)
		}
		owner, err := topicAddress(log.Topics[1])
		if err != nil {
			return nil, nil, true, err
		}
		return nil, poolAmountEvents(pool, log, EventLiquidityRemoved, owner, unsigned(values[1]), unsigned(values[2])), true, nil
	default:
		return nil, nil, false, nil
	}
}

func parseInfinityCL(pool Pool, log Log, topic string) (*swapLeg, []Event, bool, error) {
	switch topic {
	case topicInfinityCLSwap:
		if len(log.Topics) != 3 {
			return nil, nil, true, fmt.Errorf("Infinity CL Swap requires 3 topics")
		}
		values, err := exactWords(log.Data, 7)
		if err != nil {
			return nil, nil, true, fmt.Errorf("Infinity CL Swap: %w", err)
		}
		sender, err := topicAddress(log.Topics[2])
		if err != nil {
			return nil, nil, true, err
		}
		leg, err := makeSwapLeg(pool, log, sender, "", signed(values[0]), signed(values[1]))
		return leg, nil, true, err
	case topicInfinityCLModify:
		if len(log.Topics) != 3 {
			return nil, nil, true, fmt.Errorf("Infinity CL ModifyLiquidity requires 3 topics")
		}
		values, err := exactWords(log.Data, 4)
		if err != nil {
			return nil, nil, true, fmt.Errorf("Infinity CL ModifyLiquidity: %w", err)
		}
		sender, err := topicAddress(log.Topics[2])
		if err != nil {
			return nil, nil, true, err
		}
		delta := signed(values[2])
		if delta.Sign() == 0 {
			return nil, nil, true, nil
		}
		eventType := EventLiquidityAdded
		if delta.Sign() < 0 {
			eventType = EventLiquidityRemoved
		}
		events := poolLifecycleEvents(pool, log, eventType, sender)
		for index := range events {
			events[index].Metadata["liquidity_delta_raw"] = delta.String()
		}
		return nil, events, true, nil
	default:
		return nil, nil, false, nil
	}
}

func parseInfinityBin(pool Pool, log Log, topic string) (*swapLeg, []Event, bool, error) {
	switch topic {
	case topicInfinityBinSwap:
		if len(log.Topics) != 3 {
			return nil, nil, true, fmt.Errorf("Infinity Bin Swap requires 3 topics")
		}
		values, err := exactWords(log.Data, 5)
		if err != nil {
			return nil, nil, true, fmt.Errorf("Infinity Bin Swap: %w", err)
		}
		sender, err := topicAddress(log.Topics[2])
		if err != nil {
			return nil, nil, true, err
		}
		leg, err := makeSwapLeg(pool, log, sender, "", signed(values[0]), signed(values[1]))
		return leg, nil, true, err
	case topicInfinityBinMint:
		if len(log.Topics) != 3 {
			return nil, nil, true, fmt.Errorf("Infinity Bin Mint requires 3 topics")
		}
		data, err := decodeHex(log.Data)
		if err != nil {
			return nil, nil, true, fmt.Errorf("Infinity Bin Mint: %w", err)
		}
		values, err := words(log.Data, 5)
		if err != nil {
			return nil, nil, true, fmt.Errorf("Infinity Bin Mint: %w", err)
		}
		if err := validateWordArray(data, values[0]); err != nil {
			return nil, nil, true, fmt.Errorf("Infinity Bin Mint ids: %w", err)
		}
		if err := validateWordArray(data, values[2]); err != nil {
			return nil, nil, true, fmt.Errorf("Infinity Bin Mint amounts: %w", err)
		}
		sender, err := topicAddress(log.Topics[2])
		if err != nil {
			return nil, nil, true, err
		}
		return nil, poolLifecycleEvents(pool, log, EventLiquidityAdded, sender), true, nil
	case topicInfinityBinBurn:
		if len(log.Topics) != 3 {
			return nil, nil, true, fmt.Errorf("Infinity Bin Burn requires 3 topics")
		}
		data, err := decodeHex(log.Data)
		if err != nil {
			return nil, nil, true, fmt.Errorf("Infinity Bin Burn: %w", err)
		}
		values, err := words(log.Data, 3)
		if err != nil {
			return nil, nil, true, fmt.Errorf("Infinity Bin Burn: %w", err)
		}
		if err := validateWordArray(data, values[0]); err != nil {
			return nil, nil, true, fmt.Errorf("Infinity Bin Burn ids: %w", err)
		}
		if err := validateWordArray(data, values[2]); err != nil {
			return nil, nil, true, fmt.Errorf("Infinity Bin Burn amounts: %w", err)
		}
		sender, err := topicAddress(log.Topics[2])
		if err != nil {
			return nil, nil, true, err
		}
		return nil, poolLifecycleEvents(pool, log, EventLiquidityRemoved, sender), true, nil
	default:
		return nil, nil, false, nil
	}
}

func makeSwapLeg(
	pool Pool,
	log Log,
	sender, recipient string,
	amount0, amount1 *big.Int,
) (*swapLeg, error) {
	if !exactOppositeDeltas(amount0, amount1) {
		return nil, fmt.Errorf("swap must contain one positive input and one negative output")
	}
	result := &swapLeg{
		pool:      pool,
		amount0:   cloneInt(amount0),
		amount1:   cloneInt(amount1),
		sender:    sender,
		recipient: recipient,
		log:       log,
	}
	if amount0.Sign() > 0 {
		result.tokenIn = pool.Token0
		result.tokenOut = pool.Token1
		result.amountIn = cloneInt(amount0)
		result.amountOut = absolute(amount1)
	} else {
		result.tokenIn = pool.Token1
		result.tokenOut = pool.Token0
		result.amountIn = cloneInt(amount1)
		result.amountOut = absolute(amount0)
	}
	return result, nil
}

func poolAmountEvents(
	pool Pool,
	log Log,
	eventType, wallet string,
	amount0, amount1 *big.Int,
) []Event {
	events := poolLifecycleEvents(pool, log, eventType, wallet)
	events[0].TokenAmountRaw = amount0.String()
	events[0].QuoteAmountRaw = amount1.String()
	events[1].TokenAmountRaw = amount1.String()
	events[1].QuoteAmountRaw = amount0.String()
	return events
}

func poolLifecycleEvents(pool Pool, log Log, eventType, wallet string) []Event {
	base := Event{
		Type:             eventType,
		Protocol:         pool.Protocol,
		Version:          pool.Version,
		Adapter:          pool.Adapter,
		PoolID:           pool.ID,
		PoolKey:          pool.PoolKey,
		WalletAddress:    wallet,
		TransactionHash:  log.TransactionHash,
		TransactionIndex: log.TransactionIndex,
		LogIndex:         log.LogIndex,
		LogIndices:       []uint64{log.LogIndex},
		BlockNumber:      log.BlockNumber,
		BlockHash:        log.BlockHash,
		Source:           "onchain_log",
		Confidence:       "1.0000",
		Metadata:         map[string]string{},
	}
	token0 := base
	token0.TokenAddress = pool.Token0.Address
	token0.QuoteAddress = pool.Token1.Address
	token1 := base
	token1.Metadata = map[string]string{}
	token1.TokenAddress = pool.Token1.Address
	token1.QuoteAddress = pool.Token0.Address
	return []Event{token0, token1}
}
