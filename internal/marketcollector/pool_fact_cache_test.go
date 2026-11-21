package marketcollector

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketprotocol"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type immutableDiscoveryChain struct {
	*discoveryTestChain
	identity     *int
	bound        bool
	bindingError error
	probes       int
}

func (c *immutableDiscoveryChain) PoolFactsCacheIdentity() any { return c.identity }
func (c *immutableDiscoveryChain) ImmutablePoolBinding(context.Context, string, string, string, string, string, string) (bool, error) {
	c.probes++
	return c.bound, c.bindingError
}

func TestImmutablePoolFactsReuseAcrossTasksKeepsProjectChecks(t *testing.T) {
	projects := make([]store.MarketProject, 201)
	for i := range projects {
		projects[i] = discoveryProject(int64(i+1), discoveryTokenA)
	}
	pair := discoveryPair(1, "1000")
	s, repo, market, old := discoveryFixture(projects, map[string][]marketdata.Pair{discoveryTokenA: {pair}})
	reader := &immutableDiscoveryChain{discoveryTestChain: old, identity: new(int), bound: true}
	s.chain = reader
	now := time.Now()
	s.now = func() time.Time { return now }
	if err := s.discoverPools(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.discoverPools(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.links) != 201 || reader.tokenCalls[pair.PairAddress] != 1 || reader.factoryCalls[pair.PairAddress] != 1 || reader.probes != 1 || len(s.poolFacts.entries) != 1 {
		t.Fatalf("facts did not survive onlyboundedtaskboundary: links=%d token=%v factory=%v probes=%d", len(repo.links), reader.tokenCalls, reader.factoryCalls, reader.probes)
	}
	for _, p := range projects {
		if repo.selected[p.ID] == 0 {
			t.Fatal("shared facts lost a project's selection", p.ID)
		}
	}
	// A provider cannot borrow another project's membership/classification
	// merely by keeping the same pool address in a new quote response.
	before := len(repo.writes)
	repo.projects = []store.MarketProject{discoveryProject(1001, discoveryTokenC)}
	foreign := pair
	foreign.BaseToken.Address = discoveryTokenC
	market.pairs[discoveryTokenC] = []marketdata.Pair{foreign}
	if err := s.discoverPools(context.Background()); err == nil || len(repo.writes) != before || reader.tokenCalls[pair.PairAddress] != 1 {
		t.Fatal("cached identity bypassed new project's membership check", err)
	}
}

func TestImmutablePoolFactsExpiryFailureAndClientIdentity(t *testing.T) {
	pair := discoveryPair(1, "1000")
	s, repo, _, old := discoveryFixture([]store.MarketProject{discoveryProject(1, discoveryTokenA)}, map[string][]marketdata.Pair{discoveryTokenA: {pair}})
	reader := &immutableDiscoveryChain{discoveryTestChain: old, identity: new(int), bound: true}
	s.chain = reader
	now := time.Now()
	s.now = func() time.Time { return now }
	if err := s.discoverPools(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(immutablePoolFactsTTL)
	reader.err = errors.New("temporary RPC failure")
	before := len(repo.writes)
	if err := s.discoverPools(context.Background()); err == nil || len(repo.writes) != before || len(s.poolFacts.entries) != 0 {
		t.Fatal("expired/failing fact downgraded existing verified pool", err)
	}
	reader.err = nil
	if err := s.discoverPools(context.Background()); err != nil {
		t.Fatal(err)
	}
	previous := reader.tokenCalls[pair.PairAddress]
	reader.identity = new(int)
	if err := s.discoverPools(context.Background()); err != nil || reader.tokenCalls[pair.PairAddress] != previous+1 {
		t.Fatal("new actual RPC client inherited old facts", err)
	}
}

func TestImmutablePoolProbeFailureBackoffDoesNotChangeClassification(t *testing.T) {
	pair := discoveryPair(1, "1000")
	s, _, _, old := discoveryFixture(nil, map[string][]marketdata.Pair{})
	old.tokens[pair.PairAddress] = [2]string{discoveryTokenA, discoveryTokenB}
	reader := &immutableDiscoveryChain{discoveryTestChain: old, identity: new(int), bindingError: errors.New("finalized tag unsupported")}
	s.chain = reader
	now := time.Now()
	s.now = func() time.Time { return now }
	for i := 0; i < 12; i++ {
		candidate := discoveryPair(i, "1000")
		old.tokens[candidate.PairAddress] = [2]string{discoveryTokenA, discoveryTokenB}
		facts := s.readDiscoveryPoolFacts(context.Background(), candidate)
		classification := marketprotocol.ClassifyPairWithFacts("bsc", discoveryTokenA, candidate, facts)
		if !classification.Supported || facts.ReadFailed {
			t.Fatal("cache failure changed original classification", classification)
		}
	}
	if reader.probes != 1 || len(s.poolFacts.entries) != 0 || len(s.poolFacts.probeAfter) != 1 {
		t.Fatalf("failure attached extra RPC per pool: probes=%d cache=%d", reader.probes, len(s.poolFacts.entries))
	}
	now = now.Add(immutablePoolProbeBackoff)
	_ = s.readDiscoveryPoolFacts(context.Background(), pair)
	if reader.probes != 2 {
		t.Fatal("bounded capability backoff never retried")
	}
}

func TestImmutablePoolFactCacheHasFinitePayloadTTLAndVersionScope(t *testing.T) {
	var cache immutablePoolFactCache
	source := new(int)
	now := time.Now()
	for i := 0; i < immutablePoolFactsMaxKeys+17; i++ {
		key := discoveryFactKey{chainID: 56, pool: fmt.Sprintf("0x%040x", i), version: marketprotocol.PoolFactsVersion}
		facts := marketprotocol.PoolFacts{Version: key.version, ChainKey: "bsc", ChainID: 56, PoolAddress: key.pool, Token0Address: discoveryTokenA, Token1Address: discoveryTokenB, FactoryAddress: discoveryTokenC}
		cache.put(source, key, facts, now, now)
	}
	if len(cache.entries) > immutablePoolFactsMaxKeys || cache.bytes > immutablePoolFactsMaxBytes {
		t.Fatalf("cache unbounded keys=%d bytes=%d", len(cache.entries), cache.bytes)
	}
	var key discoveryFactKey
	for key = range cache.entries {
		break
	}
	other := key
	other.version++
	if _, ok := cache.get(source, other, now); ok {
		t.Fatal("fact version crossed cache key")
	}
	other = key
	other.chainID = 1
	if _, ok := cache.get(source, other, now); ok {
		t.Fatal("fact crossed chain")
	}
	if _, ok := cache.get(source, key, now.Add(immutablePoolFactsTTL)); ok {
		t.Fatal("expired fact reused")
	}
	if _, ok := cache.get(new(int), key, now); ok || len(cache.entries) != 0 || cache.bytes != 0 {
		t.Fatal("client identity retained previous cache")
	}
}
