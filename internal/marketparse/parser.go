package marketparse

import (
	"fmt"
	"sort"
	"strings"
)

type Parser struct {
	poolsByAddress map[string][]Pool
	emitters       map[string][]Emitter
}

func NewParser(pools []Pool, emitters []Emitter) (*Parser, error) {
	result := &Parser{
		poolsByAddress: make(map[string][]Pool),
		emitters:       make(map[string][]Emitter),
	}
	seenPools := make(map[string]struct{})
	for _, candidate := range pools {
		pool, err := normalizePool(candidate)
		if err != nil {
			return nil, fmt.Errorf("pool %q: %w", candidate.PoolKey, err)
		}
		key := fmt.Sprintf("%d|%s|%s|%s", pool.ChainID, pool.Adapter, pool.LogAddress, pool.PoolKey)
		if _, exists := seenPools[key]; exists {
			return nil, fmt.Errorf("duplicate pool registration %s", pool.PoolKey)
		}
		seenPools[key] = struct{}{}
		result.poolsByAddress[pool.LogAddress] = append(result.poolsByAddress[pool.LogAddress], pool)
	}
	for _, candidate := range emitters {
		emitter, err := normalizeEmitter(candidate)
		if err != nil {
			return nil, fmt.Errorf("emitter %q: %w", candidate.Address, err)
		}
		duplicate := false
		for _, existing := range result.emitters[emitter.Address] {
			if existing.Adapter == emitter.Adapter {
				duplicate = true
				break
			}
		}
		if duplicate {
			return nil, fmt.Errorf("duplicate emitter registration %s/%s", emitter.Address, emitter.Adapter)
		}
		result.emitters[emitter.Address] = append(result.emitters[emitter.Address], emitter)
	}
	return result, nil
}

// NewBSCParser enables both official Four.meme event generations. Event topics
// disambiguate the trade formats; registering both also preserves the shared
// TokenCreate and TradeStop lifecycle events across upgrades.
func NewBSCParser(pools []Pool) (*Parser, error) {
	return NewParser(pools, []Emitter{
		{Address: BSCPancakeV2Factory, Protocol: "pancakeswap", Version: "v2", Adapter: AdapterV2},
		{Address: BSCPancakeV3Factory, Protocol: "pancakeswap", Version: "v3", Adapter: AdapterV3},
		{Address: BSCInfinityCLManager, Protocol: "pancakeswap_infinity", Version: "cl", Adapter: AdapterInfinityCL},
		{Address: BSCInfinityBinManager, Protocol: "pancakeswap_infinity", Version: "bin", Adapter: AdapterInfinityBin},
		{Address: BSCFourMemeTokenManager, Protocol: "four_meme", Version: "v2", Adapter: AdapterFourMemeV2},
		{Address: BSCFourMemeTokenManager, Protocol: "four_meme", Version: "v1", Adapter: AdapterFourMemeV1},
	})
}

func (p *Parser) Parse(receipt Receipt, targetTokens []string) ([]Event, error) {
	targets, err := normalizeTargets(targetTokens)
	if err != nil {
		return nil, err
	}
	if receipt.TransactionHash != "" {
		receipt.TransactionHash = strings.ToLower(receipt.TransactionHash)
		if !hashPattern.MatchString(receipt.TransactionHash) {
			return nil, fmt.Errorf("invalid receipt transaction hash")
		}
	}
	if receipt.From != "" {
		receipt.From, err = normalizeAddress(receipt.From)
		if err != nil {
			return nil, fmt.Errorf("receipt from: %w", err)
		}
	}
	if receipt.To != "" {
		receipt.To, err = normalizeAddress(receipt.To)
		if err != nil {
			return nil, fmt.Errorf("receipt to: %w", err)
		}
	}

	logs := append([]Log(nil), receipt.Logs...)
	sort.SliceStable(logs, func(i, j int) bool { return logs[i].LogIndex < logs[j].LogIndex })
	legs := make([]swapLeg, 0)
	lifecycle := make([]Event, 0)
	seen := make(map[string]string)
	for index := range logs {
		log := logs[index]
		if log.Removed {
			continue
		}
		log.Address = strings.ToLower(log.Address)
		for topicIndex := range log.Topics {
			log.Topics[topicIndex] = strings.ToLower(log.Topics[topicIndex])
		}
		if log.TransactionHash == "" {
			log.TransactionHash = receipt.TransactionHash
		} else {
			log.TransactionHash = strings.ToLower(log.TransactionHash)
			if receipt.TransactionHash != "" && log.TransactionHash != receipt.TransactionHash {
				return nil, fmt.Errorf("log %d belongs to a different transaction", log.LogIndex)
			}
		}
		if log.BlockHash == "" {
			log.BlockHash = receipt.BlockHash
		} else {
			log.BlockHash = strings.ToLower(log.BlockHash)
			if receipt.BlockHash != "" && log.BlockHash != strings.ToLower(receipt.BlockHash) {
				return nil, fmt.Errorf("log %d belongs to a different block", log.LogIndex)
			}
		}
		if log.BlockNumber == 0 {
			log.BlockNumber = receipt.BlockNumber
		} else if receipt.BlockNumber != 0 && log.BlockNumber != receipt.BlockNumber {
			return nil, fmt.Errorf("log %d has a mismatched block number", log.LogIndex)
		}
		if log.TransactionIndex == 0 {
			log.TransactionIndex = receipt.TransactionIndex
		}
		if err := validateLog(log); err != nil {
			return nil, fmt.Errorf("log %d: %w", log.LogIndex, err)
		}
		if len(log.Topics) == 0 {
			continue
		}
		fingerprint := log.Address + "|" + fmt.Sprint(log.LogIndex)
		content := strings.Join(log.Topics, ",") + "|" + strings.ToLower(log.Data)
		if previous, exists := seen[fingerprint]; exists {
			if previous != content {
				return nil, fmt.Errorf("conflicting duplicate log %d", log.LogIndex)
			}
			continue
		}
		seen[fingerprint] = content

		transferEvents, _, err := parseTokenTransfer(log, targets)
		if err != nil {
			return nil, fmt.Errorf("log %d: %w", log.LogIndex, err)
		}
		lifecycle = append(lifecycle, transferEvents...)

		poolLegs, poolEvents, err := p.parsePoolLog(receipt.ChainID, log)
		if err != nil {
			return nil, fmt.Errorf("log %d: %w", log.LogIndex, err)
		}
		legs = append(legs, poolLegs...)
		lifecycle = append(lifecycle, filterLifecycle(poolEvents, targets)...)

		fourEvents, err := p.parseEmitterLog(log)
		if err != nil {
			return nil, fmt.Errorf("log %d: %w", log.LogIndex, err)
		}
		lifecycle = append(lifecycle, filterLifecycle(fourEvents, targets)...)
	}

	trades := aggregateTrades(receipt, legs, targets)
	fourTrades := make([]Event, 0)
	remaining := lifecycle[:0]
	for _, event := range lifecycle {
		if event.Type == EventBuy || event.Type == EventSell {
			event = fillReceipt(event, receipt)
			fourTrades = append(fourTrades, event)
		} else {
			remaining = append(remaining, fillReceipt(event, receipt))
		}
	}
	result := append(trades, fourTrades...)
	result = append(result, remaining...)
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].LogIndex != result[j].LogIndex {
			return result[i].LogIndex < result[j].LogIndex
		}
		return result[i].Type < result[j].Type
	})
	return result, nil
}

func (p *Parser) parsePoolLog(chainID uint64, log Log) ([]swapLeg, []Event, error) {
	candidates := p.poolsByAddress[log.Address]
	if len(candidates) == 0 {
		return nil, nil, nil
	}
	var matched bool
	for _, pool := range candidates {
		if pool.ChainID != 0 && chainID != 0 && pool.ChainID != chainID {
			continue
		}
		if (pool.Adapter == AdapterInfinityCL || pool.Adapter == AdapterInfinityBin) &&
			(len(log.Topics) < 2 || log.Topics[1] != pool.PoolKey) {
			continue
		}
		matched = true
		leg, events, recognized, err := parsePoolAdapter(pool, log)
		if err != nil {
			return nil, nil, err
		}
		if !recognized {
			continue
		}
		if leg != nil {
			return []swapLeg{*leg}, events, nil
		}
		return nil, events, nil
	}
	if matched {
		return nil, nil, nil
	}
	return nil, nil, nil
}

func (p *Parser) parseEmitterLog(log Log) ([]Event, error) {
	for _, emitter := range p.emitters[log.Address] {
		var events []Event
		var recognized bool
		var err error
		switch emitter.Adapter {
		case AdapterV2, AdapterV3, AdapterAlgebra, AdapterSolidly:
			events, recognized, err = parseFactory(emitter, log)
		case AdapterInfinityCL, AdapterInfinityBin:
			events, recognized, err = parseInfinityManager(emitter, log)
		default:
			events, recognized, err = parseFourMeme(emitter, log)
		}
		if err != nil {
			return nil, err
		}
		if recognized {
			return events, nil
		}
	}
	return nil, nil
}

func normalizeTargets(values []string) (map[string]struct{}, error) {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		address, err := normalizeAddress(value)
		if err != nil {
			return nil, fmt.Errorf("target token: %w", err)
		}
		result[address] = struct{}{}
	}
	return result, nil
}

func filterLifecycle(events []Event, targets map[string]struct{}) []Event {
	if len(targets) == 0 {
		return events
	}
	result := make([]Event, 0, len(events))
	for _, event := range events {
		if _, exists := targets[event.TokenAddress]; exists {
			result = append(result, event)
		}
	}
	return result
}

func fillReceipt(event Event, receipt Receipt) Event {
	if event.TransactionHash == "" {
		event.TransactionHash = receipt.TransactionHash
	}
	if event.BlockHash == "" {
		event.BlockHash = receipt.BlockHash
	}
	if event.BlockNumber == 0 {
		event.BlockNumber = receipt.BlockNumber
	}
	if event.TransactionIndex == 0 {
		event.TransactionIndex = receipt.TransactionIndex
	}
	if event.WalletAddress == "" {
		event.WalletAddress = receipt.From
	}
	if event.Confidence == "" {
		event.Confidence = "1.0000"
	}
	if event.Source == "" {
		event.Source = "onchain_log"
	}
	return event
}
