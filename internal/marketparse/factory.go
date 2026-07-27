package marketparse

import "fmt"

func parseFactory(emitter Emitter, log Log) ([]Event, bool, error) {
	switch emitter.Adapter {
	case AdapterV2:
		if log.Topics[0] != topicV2PairCreated {
			return nil, false, nil
		}
		if len(log.Topics) != 3 {
			return nil, true, fmt.Errorf("V2 PairCreated requires 3 topics")
		}
		values, err := exactWords(log.Data, 2)
		if err != nil {
			return nil, true, fmt.Errorf("V2 PairCreated: %w", err)
		}
		token0, err := topicAddress(log.Topics[1])
		if err != nil {
			return nil, true, err
		}
		token1, err := topicAddress(log.Topics[2])
		if err != nil {
			return nil, true, err
		}
		pool, err := wordAddress(values[0])
		if err != nil {
			return nil, true, err
		}
		events := factoryEvents(emitter, log, token0, token1, pool)
		for index := range events {
			events[index].Metadata["pair_count"] = unsigned(values[1]).String()
		}
		return events, true, nil
	case AdapterV3:
		if log.Topics[0] != topicV3PoolCreated {
			return nil, false, nil
		}
		if len(log.Topics) != 4 {
			return nil, true, fmt.Errorf("V3 PoolCreated requires 4 topics")
		}
		values, err := exactWords(log.Data, 2)
		if err != nil {
			return nil, true, fmt.Errorf("V3 PoolCreated: %w", err)
		}
		token0, err := topicAddress(log.Topics[1])
		if err != nil {
			return nil, true, err
		}
		token1, err := topicAddress(log.Topics[2])
		if err != nil {
			return nil, true, err
		}
		pool, err := wordAddress(values[1])
		if err != nil {
			return nil, true, err
		}
		events := factoryEvents(emitter, log, token0, token1, pool)
		fee := unsignedTopic(log.Topics[3])
		for index := range events {
			events[index].Metadata["fee"] = fee
			events[index].Metadata["tick_spacing"] = signed(values[0]).String()
		}
		return events, true, nil
	case AdapterAlgebra:
		if log.Topics[0] != topicAlgebraPoolCreated {
			return nil, false, nil
		}
		if len(log.Topics) != 3 {
			return nil, true, fmt.Errorf("Algebra Pool requires 3 topics")
		}
		values, err := exactWords(log.Data, 1)
		if err != nil {
			return nil, true, fmt.Errorf("Algebra Pool: %w", err)
		}
		token0, err := topicAddress(log.Topics[1])
		if err != nil {
			return nil, true, err
		}
		token1, err := topicAddress(log.Topics[2])
		if err != nil {
			return nil, true, err
		}
		pool, err := wordAddress(values[0])
		if err != nil {
			return nil, true, err
		}
		return factoryEvents(emitter, log, token0, token1, pool), true, nil
	case AdapterSolidly:
		if log.Topics[0] != topicSolidlyPoolCreated {
			return nil, false, nil
		}
		if len(log.Topics) != 4 {
			return nil, true, fmt.Errorf("Solidly PoolCreated requires 4 topics")
		}
		values, err := exactWords(log.Data, 2)
		if err != nil {
			return nil, true, fmt.Errorf("Solidly PoolCreated: %w", err)
		}
		token0, err := topicAddress(log.Topics[1])
		if err != nil {
			return nil, true, err
		}
		token1, err := topicAddress(log.Topics[2])
		if err != nil {
			return nil, true, err
		}
		pool, err := wordAddress(values[0])
		if err != nil {
			return nil, true, err
		}
		events := factoryEvents(emitter, log, token0, token1, pool)
		stable := unsignedTopic(log.Topics[3]) != "0"
		for index := range events {
			events[index].Metadata["stable"] = fmt.Sprint(stable)
			events[index].Metadata["pool_count"] = unsigned(values[1]).String()
		}
		return events, true, nil
	default:
		return nil, false, nil
	}
}

func factoryEvents(
	emitter Emitter,
	log Log,
	token0, token1, pool string,
) []Event {
	base := Event{
		Type:             EventPoolInitialized,
		Protocol:         emitter.Protocol,
		Version:          emitter.Version,
		Adapter:          emitter.Adapter,
		PoolKey:          pool,
		TransactionHash:  log.TransactionHash,
		TransactionIndex: log.TransactionIndex,
		LogIndex:         log.LogIndex,
		LogIndices:       []uint64{log.LogIndex},
		BlockNumber:      log.BlockNumber,
		BlockHash:        log.BlockHash,
		Source:           "onchain_log",
		Confidence:       "1.0000",
		Metadata: map[string]string{
			"factory_address": log.Address,
			"pool_address":    pool,
			"token0_address":  token0,
			"token1_address":  token1,
		},
	}
	first := base
	first.TokenAddress = token0
	first.QuoteAddress = token1
	second := base
	second.Metadata = map[string]string{
		"factory_address": log.Address,
		"pool_address":    pool,
		"token0_address":  token0,
		"token1_address":  token1,
	}
	second.TokenAddress = token1
	second.QuoteAddress = token0
	return []Event{first, second}
}

func unsignedTopic(topic string) string {
	raw, err := decodeHex(topic)
	if err != nil {
		return ""
	}
	return unsigned(raw).String()
}
