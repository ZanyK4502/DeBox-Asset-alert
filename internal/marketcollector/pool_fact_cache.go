package marketcollector

import (
	"context"
	"reflect"
	"sync"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketprotocol"
)

const (
	immutablePoolFactsTTL      = 15 * time.Minute
	immutablePoolFactsMaxKeys  = 4096
	immutablePoolFactsMaxBytes = 2 << 20 // bounded identity payload estimate, not RSS
	immutablePoolProbeBackoff  = time.Minute
)

type immutablePoolBindingReader interface {
	PoolFactsCacheIdentity() any
	ImmutablePoolBinding(context.Context, string, string, string, string, string, string) (bool, error)
}

type immutablePoolFactEntry struct {
	facts     marketprotocol.PoolFacts
	fetchedAt time.Time
	bytes     int
}

type immutablePoolFactCache struct {
	mu         sync.Mutex
	source     any // retain actual pointer: pointer reuse cannot impersonate an old client
	entries    map[discoveryFactKey]immutablePoolFactEntry
	bytes      int
	probeAfter map[int64]time.Time // at most six supported chains, not one failure per pool
}

func (service *Service) readDiscoveryPoolFacts(ctx context.Context, pair marketdata.Pair) marketprotocol.PoolFacts {
	reader, cacheCapable := service.chain.(immutablePoolBindingReader)
	var source any
	if cacheCapable {
		source = reader.PoolFactsCacheIdentity()
		v := reflect.ValueOf(source)
		cacheCapable = v.IsValid() && v.Kind() == reflect.Pointer && !v.IsNil()
	}
	pool, validPool := chain.ValidateAddress(pair.PairAddress)
	key := discoveryFactKey{chainID: service.settings.ChainID, pool: pool, version: marketprotocol.PoolFactsVersion}
	if cacheCapable && validPool == nil && chain.NormalizeChainKey(pair.ChainID, "") == service.settings.ChainKey {
		if facts, ok := service.poolFacts.get(source, key, service.now()); ok {
			return facts
		}
	}
	started := service.now()
	facts := marketprotocol.ReadPoolFacts(ctx, service.chain, service.settings.ChainKey, pair)
	if !cacheCapable || ctx.Err() != nil || facts.ReadFailed || facts.FailureReason != "" || facts.Version != key.version || facts.ChainID != key.chainID || facts.PoolAddress != key.pool {
		return facts
	}
	deployment, trusted := marketprotocol.Lookup(service.settings.ChainKey, facts.FactoryAddress)
	if !trusted || (deployment.Protocol != "uniswap" && deployment.Protocol != "pancakeswap") || (deployment.Version != "v2" && deployment.Version != "v3") {
		return facts
	}
	if !service.poolFacts.probeAllowed(source, key.chainID, service.now()) {
		return facts
	}
	bound, err := reader.ImmutablePoolBinding(ctx, facts.PoolAddress, facts.Token0Address, facts.Token1Address, facts.FactoryAddress, deployment.Version, service.settings.ChainKey)
	if err == nil && bound && ctx.Err() == nil {
		service.poolFacts.put(source, key, facts, started, service.now())
	} else if ctx.Err() == nil {
		service.poolFacts.deferProbe(source, key.chainID, service.now())
	}
	// A failed cache-admission probe does not broaden or narrow the original
	// protocol classification. Fresh facts still follow the original checks.
	return facts
}

func (cache *immutablePoolFactCache) get(source any, key discoveryFactKey, now time.Time) (marketprotocol.PoolFacts, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.source != source {
		cache.resetSource(source)
		return marketprotocol.PoolFacts{}, false
	}
	entry, ok := cache.entries[key]
	if !ok {
		return marketprotocol.PoolFacts{}, false
	}
	age := now.Sub(entry.fetchedAt)
	if age < 0 || age >= immutablePoolFactsTTL {
		cache.bytes -= entry.bytes
		delete(cache.entries, key)
		return marketprotocol.PoolFacts{}, false
	}
	return entry.facts, true
}

func (cache *immutablePoolFactCache) resetSource(source any) {
	cache.source = source
	cache.entries = make(map[discoveryFactKey]immutablePoolFactEntry)
	cache.probeAfter = make(map[int64]time.Time)
	cache.bytes = 0
}

func (cache *immutablePoolFactCache) probeAllowed(source any, chainID int64, now time.Time) bool {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.source != source {
		cache.resetSource(source)
	}
	return !now.Before(cache.probeAfter[chainID])
}

func (cache *immutablePoolFactCache) deferProbe(source any, chainID int64, now time.Time) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.source != source {
		cache.resetSource(source)
	}
	if len(cache.probeAfter) < 6 || !cache.probeAfter[chainID].IsZero() {
		cache.probeAfter[chainID] = now.Add(immutablePoolProbeBackoff)
	}
}

func immutablePoolFactBytes(key discoveryFactKey, facts marketprotocol.PoolFacts) int {
	return len(key.pool) + len(facts.ChainKey) + len(facts.PoolAddress) + len(facts.Token0Address) + len(facts.Token1Address) + len(facts.FactoryAddress) + 128
}

func (cache *immutablePoolFactCache) put(source any, key discoveryFactKey, facts marketprotocol.PoolFacts, started, now time.Time) {
	if now.Before(started) || now.Sub(started) >= immutablePoolFactsTTL {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.source != source {
		cache.resetSource(source)
	}
	for oldKey, entry := range cache.entries {
		if now.Before(entry.fetchedAt) || now.Sub(entry.fetchedAt) >= immutablePoolFactsTTL {
			cache.bytes -= entry.bytes
			delete(cache.entries, oldKey)
		}
	}
	if previous, ok := cache.entries[key]; ok {
		cache.bytes -= previous.bytes
		delete(cache.entries, key)
	}
	size := immutablePoolFactBytes(key, facts)
	if size > immutablePoolFactsMaxBytes {
		return
	}
	for len(cache.entries) >= immutablePoolFactsMaxKeys || cache.bytes+size > immutablePoolFactsMaxBytes {
		var oldestKey discoveryFactKey
		var oldest time.Time
		found := false
		for candidate, entry := range cache.entries {
			if !found || entry.fetchedAt.Before(oldest) {
				oldestKey, oldest, found = candidate, entry.fetchedAt, true
			}
		}
		if !found {
			return
		}
		cache.bytes -= cache.entries[oldestKey].bytes
		delete(cache.entries, oldestKey)
	}
	cache.entries[key] = immutablePoolFactEntry{facts: facts, fetchedAt: started, bytes: size}
	cache.bytes += size
}
