package marketparse

import (
	"math/big"
	"sort"
	"strings"
)

type tradeGroup struct {
	target     string
	direction  string
	legs       []swapLeg
	tokenTotal *big.Int
	quoteTotal *big.Int
	quote      string
	mixedQuote bool
}

func aggregateTrades(receipt Receipt, legs []swapLeg, targets map[string]struct{}) []Event {
	groups := make(map[string]*tradeGroup)
	order := make([]string, 0)
	add := func(leg swapLeg, target, direction, quote string, tokenAmount, quoteAmount *big.Int) {
		key := target + "|" + direction
		group := groups[key]
		if group == nil {
			group = &tradeGroup{
				target:     target,
				direction:  direction,
				tokenTotal: new(big.Int),
				quoteTotal: new(big.Int),
				quote:      quote,
			}
			groups[key] = group
			order = append(order, key)
		} else if group.quote != quote {
			group.mixedQuote = true
		}
		group.legs = append(group.legs, leg)
		group.tokenTotal.Add(group.tokenTotal, tokenAmount)
		if !group.mixedQuote {
			group.quoteTotal.Add(group.quoteTotal, quoteAmount)
		}
	}
	for _, leg := range legs {
		if _, exists := targets[leg.tokenOut.Address]; exists {
			add(leg, leg.tokenOut.Address, EventBuy, leg.tokenIn.Address, leg.amountOut, leg.amountIn)
		}
		if _, exists := targets[leg.tokenIn.Address]; exists {
			add(leg, leg.tokenIn.Address, EventSell, leg.tokenOut.Address, leg.amountIn, leg.amountOut)
		}
	}

	result := make([]Event, 0, len(groups))
	for _, key := range order {
		group := groups[key]
		sort.SliceStable(group.legs, func(i, j int) bool {
			return group.legs[i].log.LogIndex < group.legs[j].log.LogIndex
		})
		first := group.legs[0]
		event := Event{
			Type:             group.direction,
			Protocol:         first.pool.Protocol,
			Version:          first.pool.Version,
			Adapter:          first.pool.Adapter,
			PoolID:           first.pool.ID,
			PoolKey:          first.pool.PoolKey,
			TokenAddress:     group.target,
			QuoteAddress:     group.quote,
			WalletAddress:    receipt.From,
			TokenAmountRaw:   group.tokenTotal.String(),
			QuoteAmountRaw:   group.quoteTotal.String(),
			TransactionHash:  first.log.TransactionHash,
			TransactionIndex: first.log.TransactionIndex,
			LogIndex:         first.log.LogIndex,
			BlockNumber:      first.log.BlockNumber,
			BlockHash:        first.log.BlockHash,
			Source:           "onchain_log",
			Confidence:       "1.0000",
			Metadata:         map[string]string{},
		}
		protocols := map[string]struct{}{first.pool.Protocol: {}}
		pools := map[string]struct{}{first.pool.PoolKey: {}}
		indices := make([]uint64, 0, len(group.legs))
		for _, leg := range group.legs {
			tokenAmount, quoteAmount := leg.amountOut, leg.amountIn
			quote := leg.tokenIn.Address
			if group.direction == EventSell {
				tokenAmount, quoteAmount = leg.amountIn, leg.amountOut
				quote = leg.tokenOut.Address
			}
			wallet := receipt.From
			if wallet == "" {
				wallet = leg.sender
			}
			event.TradeLegs = append(event.TradeLegs, TradeLeg{
				PoolID: leg.pool.ID, PoolKey: leg.pool.PoolKey, LogIndex: leg.log.LogIndex,
				Protocol: leg.pool.Protocol, Version: leg.pool.Version, Adapter: leg.pool.Adapter,
				QuoteAddress: quote, TokenAmountRaw: tokenAmount.String(), QuoteAmountRaw: quoteAmount.String(),
				Amount0DeltaRaw: leg.amount0.String(), Amount1DeltaRaw: leg.amount1.String(),
				WalletAddress: wallet, RecipientAddress: leg.recipient,
				Metadata: map[string]string{},
			})
			protocols[leg.pool.Protocol] = struct{}{}
			pools[leg.pool.PoolKey] = struct{}{}
			indices = append(indices, leg.log.LogIndex)
			if event.WalletAddress == "" {
				event.WalletAddress = leg.sender
			}
			if group.direction == EventBuy && leg.tokenOut.Address == group.target && leg.recipient != "" {
				event.RecipientAddress = leg.recipient
			}
			if group.direction == EventSell && leg.tokenIn.Address == group.target && leg.recipient != "" {
				event.RecipientAddress = leg.recipient
			}
		}
		event.LogIndices = sortedUnique(indices)
		event.Metadata["route_log_count"] = big.NewInt(int64(len(legs))).String()
		event.Metadata["target_leg_count"] = big.NewInt(int64(len(group.legs))).String()
		event.Metadata["aggregation"] = "transaction_target_direction"
		if len(group.legs) == 1 {
			event.Amount0DeltaRaw = first.amount0.String()
			event.Amount1DeltaRaw = first.amount1.String()
		}
		if len(protocols) > 1 {
			event.Protocol = "mixed"
			event.Version = ""
			event.Adapter = "multi_protocol"
		}
		if len(pools) > 1 {
			event.PoolID = 0
			event.PoolKey = ""
		}
		if group.mixedQuote {
			event.QuoteAddress = ""
			event.QuoteAmountRaw = ""
			event.Confidence = "0.9000"
			event.Metadata["quote_resolution"] = "mixed"
		}
		if strings.TrimSpace(event.WalletAddress) == "" {
			event.Confidence = "0.9500"
		}
		event = fillReceipt(event, receipt)
		result = append(result, event)
	}
	return result
}
