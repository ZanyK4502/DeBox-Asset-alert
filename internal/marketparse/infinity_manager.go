package marketparse

import "fmt"

func parseInfinityManager(emitter Emitter, log Log) ([]Event, bool, error) {
	var requiredWords int
	switch emitter.Adapter {
	case AdapterInfinityCL:
		if log.Topics[0] != topicInfinityCLInitialize {
			return nil, false, nil
		}
		requiredWords = 5
	case AdapterInfinityBin:
		if log.Topics[0] != topicInfinityBinInitialize {
			return nil, false, nil
		}
		requiredWords = 4
	default:
		return nil, false, nil
	}
	if len(log.Topics) != 4 {
		return nil, true, fmt.Errorf("Infinity Initialize requires 4 topics")
	}
	values, err := exactWords(log.Data, requiredWords)
	if err != nil {
		return nil, true, fmt.Errorf("Infinity Initialize: %w", err)
	}
	token0, err := topicAddress(log.Topics[2])
	if err != nil {
		return nil, true, err
	}
	token1, err := topicAddress(log.Topics[3])
	if err != nil {
		return nil, true, err
	}
	hooks, err := wordAddress(values[0])
	if err != nil {
		return nil, true, err
	}
	events := infinityInitializeEvents(emitter, log, token0, token1)
	for index := range events {
		events[index].Metadata["hooks_address"] = hooks
		events[index].Metadata["fee"] = unsigned(values[1]).String()
		events[index].Metadata["parameters"] = "0x" + fmt.Sprintf("%064x", unsigned(values[2]))
		if emitter.Adapter == AdapterInfinityCL {
			events[index].Metadata["sqrt_price_x96"] = unsigned(values[3]).String()
			events[index].Metadata["tick"] = signed(values[4]).String()
		} else {
			events[index].Metadata["active_id"] = unsigned(values[3]).String()
		}
	}
	return events, true, nil
}

func infinityInitializeEvents(
	emitter Emitter,
	log Log,
	token0, token1 string,
) []Event {
	base := Event{
		Type:             EventPoolInitialized,
		Protocol:         emitter.Protocol,
		Version:          emitter.Version,
		Adapter:          emitter.Adapter,
		PoolKey:          log.Topics[1],
		TransactionHash:  log.TransactionHash,
		TransactionIndex: log.TransactionIndex,
		LogIndex:         log.LogIndex,
		LogIndices:       []uint64{log.LogIndex},
		BlockNumber:      log.BlockNumber,
		BlockHash:        log.BlockHash,
		Source:           "onchain_log",
		Confidence:       "1.0000",
		Metadata: map[string]string{
			"manager_address": log.Address,
			"pool_id":         log.Topics[1],
			"token0_address":  token0,
			"token1_address":  token1,
		},
	}
	first := base
	first.TokenAddress = token0
	first.QuoteAddress = token1
	second := base
	second.Metadata = map[string]string{
		"manager_address": log.Address,
		"pool_id":         log.Topics[1],
		"token0_address":  token0,
		"token1_address":  token1,
	}
	second.TokenAddress = token1
	second.QuoteAddress = token0
	return []Event{first, second}
}
