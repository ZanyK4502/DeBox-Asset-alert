package marketparse

import (
	"fmt"
)

func parseFourMeme(emitter Emitter, log Log) ([]Event, bool, error) {
	switch emitter.Adapter {
	case AdapterFourMemeV1:
		return parseFourMemeV1(emitter, log)
	case AdapterFourMemeV2:
		return parseFourMemeV2(emitter, log)
	default:
		return nil, false, nil
	}
}

func parseFourMemeV1(emitter Emitter, log Log) ([]Event, bool, error) {
	switch log.Topics[0] {
	case topicFourPurchaseV1, topicFourSaleV1:
		if len(log.Topics) != 1 {
			return nil, true, fmt.Errorf("Four.meme V1 trade requires 1 topic")
		}
		values, err := exactWords(log.Data, 5)
		if err != nil {
			return nil, true, fmt.Errorf("Four.meme V1 trade: %w", err)
		}
		token, err := wordAddress(values[0])
		if err != nil {
			return nil, true, err
		}
		account, err := wordAddress(values[1])
		if err != nil {
			return nil, true, err
		}
		eventType := EventBuy
		if log.Topics[0] == topicFourSaleV1 {
			eventType = EventSell
		}
		event := fourBase(emitter, log, eventType, token, account)
		event.QuoteAddress = NativeTokenAddress
		event.TokenAmountRaw = unsigned(values[2]).String()
		event.QuoteAmountRaw = unsigned(values[3]).String()
		event.FeeRaw = unsigned(values[4]).String()
		event.Metadata["quote_source"] = "event_ether_amount"
		return []Event{event}, true, nil
	case topicFourTokenCreateV1:
		events, err := parseFourTokenCreate(emitter, log)
		return events, true, err
	case topicFourTradeStop:
		events, err := parseFourTradeStop(emitter, log)
		return events, true, err
	default:
		return nil, false, nil
	}
}

func parseFourMemeV2(emitter Emitter, log Log) ([]Event, bool, error) {
	switch log.Topics[0] {
	case topicFourPurchaseV2, topicFourSaleV2:
		if len(log.Topics) != 1 {
			return nil, true, fmt.Errorf("Four.meme V2 trade requires 1 topic")
		}
		values, err := exactWords(log.Data, 8)
		if err != nil {
			return nil, true, fmt.Errorf("Four.meme V2 trade: %w", err)
		}
		token, err := wordAddress(values[0])
		if err != nil {
			return nil, true, err
		}
		account, err := wordAddress(values[1])
		if err != nil {
			return nil, true, err
		}
		eventType := EventBuy
		if log.Topics[0] == topicFourSaleV2 {
			eventType = EventSell
		}
		event := fourBase(emitter, log, eventType, token, account)
		event.TokenAmountRaw = unsigned(values[3]).String()
		event.QuoteAmountRaw = unsigned(values[4]).String()
		event.FeeRaw = unsigned(values[5]).String()
		event.Metadata["price_raw"] = unsigned(values[2]).String()
		event.Metadata["offers_raw"] = unsigned(values[6]).String()
		event.Metadata["funds_raw"] = unsigned(values[7]).String()
		event.Metadata["quote_source"] = "resolve_from_token_template"
		event.Confidence = "0.9500"
		return []Event{event}, true, nil
	case topicFourTokenCreateV2:
		events, err := parseFourTokenCreate(emitter, log)
		return events, true, err
	case topicFourTradeStop:
		events, err := parseFourTradeStop(emitter, log)
		return events, true, err
	case topicFourLiquidity:
		if len(log.Topics) != 1 {
			return nil, true, fmt.Errorf("Four.meme LiquidityAdded requires 1 topic")
		}
		values, err := exactWords(log.Data, 4)
		if err != nil {
			return nil, true, fmt.Errorf("Four.meme LiquidityAdded: %w", err)
		}
		base, err := wordAddress(values[0])
		if err != nil {
			return nil, true, err
		}
		quote, err := wordAddress(values[2])
		if err != nil {
			return nil, true, err
		}
		event := fourBase(emitter, log, EventMigrated, base, "")
		event.QuoteAddress = quote
		event.TokenAmountRaw = unsigned(values[1]).String()
		event.QuoteAmountRaw = unsigned(values[3]).String()
		event.Metadata["lifecycle"] = "liquidity_added"
		return []Event{event}, true, nil
	default:
		return nil, false, nil
	}
}

func parseFourTokenCreate(emitter Emitter, log Log) ([]Event, error) {
	if len(log.Topics) != 1 {
		return nil, fmt.Errorf("Four.meme TokenCreate requires 1 topic")
	}
	data, err := decodeHex(log.Data)
	if err != nil {
		return nil, err
	}
	values, err := words(log.Data, 8)
	if err != nil {
		return nil, fmt.Errorf("Four.meme TokenCreate: %w", err)
	}
	creator, err := wordAddress(values[0])
	if err != nil {
		return nil, err
	}
	token, err := wordAddress(values[1])
	if err != nil {
		return nil, err
	}
	name, err := dynamicString(data, values[3])
	if err != nil {
		return nil, fmt.Errorf("Four.meme token name: %w", err)
	}
	symbol, err := dynamicString(data, values[4])
	if err != nil {
		return nil, fmt.Errorf("Four.meme token symbol: %w", err)
	}
	event := fourBase(emitter, log, EventTokenCreated, token, creator)
	event.TokenAmountRaw = unsigned(values[5]).String()
	event.FeeRaw = unsigned(values[7]).String()
	event.Metadata["request_id"] = unsigned(values[2]).String()
	event.Metadata["name"] = name
	event.Metadata["symbol"] = symbol
	event.Metadata["launch_time"] = unsigned(values[6]).String()
	return []Event{event}, nil
}

func parseFourTradeStop(emitter Emitter, log Log) ([]Event, error) {
	if len(log.Topics) != 1 {
		return nil, fmt.Errorf("Four.meme TradeStop requires 1 topic")
	}
	values, err := exactWords(log.Data, 1)
	if err != nil {
		return nil, fmt.Errorf("Four.meme TradeStop: %w", err)
	}
	token, err := wordAddress(values[0])
	if err != nil {
		return nil, err
	}
	event := fourBase(emitter, log, EventTradingStopped, token, "")
	event.Metadata["lifecycle"] = "graduating"
	return []Event{event}, nil
}

func fourBase(emitter Emitter, log Log, eventType, token, wallet string) Event {
	return Event{
		Type:             eventType,
		Protocol:         emitter.Protocol,
		Version:          emitter.Version,
		Adapter:          emitter.Adapter,
		TokenAddress:     token,
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
}
