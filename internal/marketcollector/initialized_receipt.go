package marketcollector

import (
	"context"
	"fmt"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketprotocol"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const maxInitializedReceiptPools = 128

// prepareInitializedReceipt extends a private parser scope before any trade
// facts are published. A Factory/PoolManager initialization can precede swaps in
// its new pool in the very same receipt; the initial parser could not see them.
func (service *Service) prepareInitializedReceipt(
	ctx context.Context,
	receipt marketparse.Receipt,
	parserCtx parserContext,
	initialEvents []marketparse.Event,
) (parserContext, []marketparse.Event, map[string]int64, bool, error) {
	type initialization struct {
		event marketparse.Event
		pool  marketparse.Pool
	}
	ordered := make([]initialization, 0)
	seen := make(map[string]marketparse.Pool)
	initialized := make(map[string]int64)
	for _, event := range initialEvents {
		if event.Type != marketparse.EventPoolInitialized {
			continue
		}
		key := event.Adapter + ":" + strings.ToLower(event.PoolKey)
		if event.PoolID > 0 {
			// A registered V3 pool's own Initialize is not a factory event.
			initialized[key] = event.PoolID
			continue
		}
		pool, err := service.initializedReceiptPool(event)
		if err != nil {
			return parserContext{}, nil, nil, false, err
		}
		if previous, exists := seen[key]; exists {
			if previous != pool {
				return parserContext{}, nil, nil, false, fmt.Errorf("conflicting initialized pool %s", key)
			}
			continue
		}
		if len(ordered) >= maxInitializedReceiptPools {
			return parserContext{}, nil, nil, false, fmt.Errorf("receipt exceeds initialized pool capacity")
		}
		seen[key] = pool
		ordered = append(ordered, initialization{event: event, pool: pool})
	}
	if len(ordered) == 0 {
		return parserCtx, initialEvents, initialized, false, nil
	}
	if receipt.ChainID != uint64(service.settings.ChainID) || service.settings.ChainID <= 0 {
		return parserContext{}, nil, nil, false, fmt.Errorf("initialized receipt belongs to another chain")
	}
	updated := cloneInitializedParserContext(parserCtx)
	registered := make(map[string]int, len(updated.pools))
	ids := make(map[int64]string, len(updated.pools))
	for index, pool := range updated.pools {
		key := pool.Adapter + ":" + strings.ToLower(pool.PoolKey)
		registered[key] = index
		ids[pool.ID] = key
	}
	for id := range updated.poolByID {
		if _, registered := ids[id]; !registered {
			delete(updated.poolByID, id)
		}
	}
	reloadNeeded := false
	for _, item := range ordered {
		key := item.event.Adapter + ":" + strings.ToLower(item.event.PoolKey)
		id, selected, err := service.ensureInitializedPoolDetailed(ctx, item.event, updated)
		if err != nil {
			return parserContext{}, nil, nil, false, err
		}
		if id <= 0 {
			return parserContext{}, nil, nil, false, fmt.Errorf("initialized pool has no persistent identity")
		}
		if previous, exists := ids[id]; exists && previous != key {
			return parserContext{}, nil, nil, false, fmt.Errorf("initialized pool ID conflicts with parser scope")
		}
		pool := item.pool
		pool.ID = id
		for _, token := range []*marketparse.Token{&pool.Token0, &pool.Token1} {
			decimals, exists := updated.tokenDecimals[token.Address]
			if !exists || decimals < 0 || decimals > 255 {
				return parserContext{}, nil, nil, false, fmt.Errorf("initialized pool precision is unknown")
			}
			token.Symbol, _ = projectTokenMetadata(updated, token.Address)
			token.Decimals = uint8(decimals)
		}
		if index, exists := registered[key]; exists {
			previous := updated.pools[index]
			if previous.ID != id || previous.Token0.Address != pool.Token0.Address || previous.Token1.Address != pool.Token1.Address {
				return parserContext{}, nil, nil, false, fmt.Errorf("initialized pool conflicts with registered identity")
			}
			updated.pools[index] = pool
		} else {
			registered[key] = len(updated.pools)
			updated.pools = append(updated.pools, pool)
			// Only a selected new pool will be present in the next shared parser
			// scope. An unselected discovery must not cause endless range retries.
			reloadNeeded = reloadNeeded || selected
		}
		ids[id] = key
		initialized[key] = id
		var address *string
		if pool.Adapter != marketparse.AdapterInfinityCL && pool.Adapter != marketparse.AdapterInfinityBin {
			value := pool.LogAddress
			address = &value
		}
		// This records actual parser coverage, not a claim that a user selected
		// the new pool. Rules retain their separate user-selected scope.
		updated.poolByID[id] = store.MarketCollectionTarget{
			ChainKey: service.settings.ChainKey, ChainID: service.settings.ChainID,
			TokenAddress: item.event.TokenAddress, MarketPoolID: id,
			Protocol: pool.Protocol, ProtocolVersion: pool.Version, PoolKey: pool.PoolKey,
			PoolAddress: address, ParserAdapter: pool.Adapter,
			Token0Address: pool.Token0.Address, Token0Symbol: pool.Token0.Symbol, Token0Decimals: int32(pool.Token0.Decimals),
			Token1Address: pool.Token1.Address, Token1Symbol: pool.Token1.Symbol, Token1Decimals: int32(pool.Token1.Decimals),
		}
	}
	parser, err := marketparse.NewParser(updated.pools, marketprotocol.FactoryEmitters(service.settings.ChainKey))
	if err != nil {
		return parserContext{}, nil, nil, false, fmt.Errorf("register initialized receipt pools: %w", err)
	}
	// Parser normalization lowercases topic strings; do not mutate the caller's
	// receipt while producing the expanded parse.
	reparse := receipt
	reparse.Logs = append([]marketparse.Log(nil), receipt.Logs...)
	for index := range reparse.Logs {
		reparse.Logs[index].Topics = append([]string(nil), receipt.Logs[index].Topics...)
	}
	events, err := parser.Parse(reparse, updated.targetTokens)
	if err != nil {
		return parserContext{}, nil, nil, false, fmt.Errorf("reparse complete initialized receipt: %w", err)
	}
	for index := range events {
		if events[index].Type == marketparse.EventPoolInitialized {
			if id, exists := initialized[events[index].Adapter+":"+strings.ToLower(events[index].PoolKey)]; exists {
				events[index].PoolID = id
			}
		}
	}
	updated.parser = parser
	return updated, events, initialized, reloadNeeded, nil
}

func cloneInitializedParserContext(original parserContext) parserContext {
	result := original
	result.pools = append([]marketparse.Pool(nil), original.pools...)
	result.targetTokens = append([]string(nil), original.targetTokens...)
	result.tokenDecimals = make(map[string]int, len(original.tokenDecimals)+2)
	for token, decimals := range original.tokenDecimals {
		result.tokenDecimals[token] = decimals
	}
	result.poolByID = make(map[int64]store.MarketCollectionTarget, len(original.poolByID))
	for id, pool := range original.poolByID {
		result.poolByID[id] = pool
	}
	result.projectsByToken = make(map[string][]store.MarketProject, len(original.projectsByToken))
	for token, projects := range original.projectsByToken {
		result.projectsByToken[token] = append([]store.MarketProject(nil), projects...)
		if _, exists := result.tokenDecimals[token]; !exists && len(projects) > 0 {
			result.tokenDecimals[token] = int(projects[0].TokenDecimals)
		}
	}
	return result
}

func (service *Service) initializedReceiptPool(event marketparse.Event) (marketparse.Pool, error) {
	pool := marketparse.Pool{
		ChainID: uint64(service.settings.ChainID), Adapter: event.Adapter,
		PoolKey: strings.ToLower(strings.TrimSpace(event.PoolKey)),
	}
	var factory string
	switch event.Adapter {
	case marketparse.AdapterV2, marketparse.AdapterV3, marketparse.AdapterAlgebra, marketparse.AdapterSolidly:
		address, err := chain.ValidateAddress(pool.PoolKey)
		if err != nil {
			return marketparse.Pool{}, err
		}
		pool.LogAddress = address
		factory = event.Metadata["factory_address"]
	case marketparse.AdapterInfinityCL:
		pool.LogAddress = marketparse.BSCInfinityCLManager
		factory = event.Metadata["manager_address"]
	case marketparse.AdapterInfinityBin:
		pool.LogAddress = marketparse.BSCInfinityBinManager
		factory = event.Metadata["manager_address"]
	default:
		return marketparse.Pool{}, fmt.Errorf("unsupported initialized pool adapter")
	}
	trusted := false
	for _, emitter := range marketprotocol.FactoryEmitters(service.settings.ChainKey) {
		if strings.EqualFold(emitter.Address, factory) && emitter.Adapter == event.Adapter {
			pool.Protocol, pool.Version = emitter.Protocol, emitter.Version
			trusted = true
			break
		}
	}
	if !trusted {
		return marketparse.Pool{}, fmt.Errorf("initialized pool factory is not trusted on this chain")
	}
	var err error
	pool.Token0.Address, err = chain.ValidateAddress(event.Metadata["token0_address"])
	if err != nil {
		return marketparse.Pool{}, err
	}
	pool.Token1.Address, err = chain.ValidateAddress(event.Metadata["token1_address"])
	if err != nil || pool.Token0.Address == pool.Token1.Address {
		return marketparse.Pool{}, fmt.Errorf("initialized pool tokens are invalid")
	}
	// Validate the full registration shape before any metadata write.
	if _, err := marketparse.NewParser([]marketparse.Pool{pool}, nil); err != nil {
		return marketparse.Pool{}, err
	}
	return pool, nil
}
