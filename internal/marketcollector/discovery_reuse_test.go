package marketcollector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const (
	discoveryTokenA = "0x1111111111111111111111111111111111111111"
	discoveryTokenB = "0x2222222222222222222222222222222222222222"
	discoveryTokenC = "0x3333333333333333333333333333333333333333"
)

type discoveryTestRepository struct {
	*fakeRepository
	projects []store.MarketProject
	pages    []store.MarketDiscoveryCursor
	limits   []int
	writes   []store.UpsertMarketPoolParams
	links    []store.EnsureMarketProjectPoolParams
	poolIDs  map[string]int64
	selected map[int64]int64
	writeErr error
}

func discoveryCursorLess(a, b store.MarketDiscoveryCursor) bool {
	if a.TokenAddress != b.TokenAddress {
		return a.TokenAddress < b.TokenAddress
	}
	if a.ProjectID != b.ProjectID {
		return a.ProjectID < b.ProjectID
	}
	return a.ProjectDeploymentID < b.ProjectDeploymentID
}
func (r *discoveryTestRepository) ListMarketDiscoveryProjectsPage(_ context.Context, _ int64, after store.MarketDiscoveryCursor, limit int) ([]store.MarketProject, error) {
	r.pages = append(r.pages, after)
	r.limits = append(r.limits, limit)
	var result []store.MarketProject
	for _, project := range r.projects {
		if discoveryCursorLess(after, discoveryCursor(project)) {
			result = append(result, project)
		}
		if len(result) == limit {
			break
		}
	}
	return result, nil
}
func (r *discoveryTestRepository) UpsertMarketPool(_ context.Context, params store.UpsertMarketPoolParams) (store.MarketPool, error) {
	r.writes = append(r.writes, params)
	if r.poolIDs == nil {
		r.poolIDs = map[string]int64{}
	}
	id := r.poolIDs[params.PoolKey]
	if id == 0 {
		id = int64(len(r.poolIDs) + 1)
		r.poolIDs[params.PoolKey] = id
	}
	return store.MarketPool{ID: id}, r.writeErr
}
func (r *discoveryTestRepository) EnsureMarketProjectPool(_ context.Context, params store.EnsureMarketProjectPoolParams) (store.MarketProjectPool, error) {
	r.links = append(r.links, params)
	if r.selected == nil {
		r.selected = map[int64]int64{}
	}
	if params.SelectIfNone && r.selected[params.MarketProjectID] == 0 {
		r.selected[params.MarketProjectID] = params.MarketPoolID
	}
	return store.MarketProjectPool{}, nil
}

type discoveryTestMarket struct {
	fakeMarket
	pairs  map[string][]marketdata.Pair
	errors map[string]error
	calls  map[string]int
	quotes int
}

func (m *discoveryTestMarket) DiscoverPools(_ context.Context, _, token string) ([]marketdata.Pair, error) {
	if m.calls == nil {
		m.calls = map[string]int{}
	}
	m.calls[token]++
	return m.pairs[token], m.errors[token]
}
func (m *discoveryTestMarket) PairsByAddresses(context.Context, string, []string) ([]marketdata.Pair, error) {
	m.quotes++
	return nil, nil
}

type discoveryTestChain struct {
	*fakeChain
	tokens                   map[string][2]string
	tokenCalls, factoryCalls map[string]int
	err                      error
	decimalsCalls            int
	decimalsErr              error
	blockPools               map[string]bool
	beforeRead               func(string)
}

func (c *discoveryTestChain) PoolTokens(ctx context.Context, pool, _, _ string) (string, string, error) {
	if c.tokenCalls == nil {
		c.tokenCalls = map[string]int{}
	}
	c.tokenCalls[pool]++
	if c.beforeRead != nil {
		c.beforeRead(pool)
	}
	if c.blockPools[pool] {
		<-ctx.Done()
		return "", "", ctx.Err()
	}
	value := c.tokens[pool]
	return value[0], value[1], c.err
}
func (c *discoveryTestChain) PoolFactory(_ context.Context, pool, _, _ string) (string, error) {
	if c.factoryCalls == nil {
		c.factoryCalls = map[string]int{}
	}
	c.factoryCalls[pool]++
	return marketparse.BSCPancakeV2Factory, c.err
}
func (c *discoveryTestChain) TokenDecimals(context.Context, string, string, string) (int32, error) {
	c.decimalsCalls++
	return 6, c.decimalsErr
}

func discoveryProject(id int64, token string) store.MarketProject {
	deployment := id + 1000
	return store.MarketProject{ID: id, DeBoxUserID: fmt.Sprint(id), ChainKey: "bsc", ChainID: 56, TokenAddress: token, TokenDecimals: 6, MarketProjectDeploymentID: &deployment}
}
func discoveryPair(id int, liquidity string) marketdata.Pair {
	return marketdata.Pair{ChainID: "bsc", DexID: "pancakeswap", PairAddress: fmt.Sprintf("0x%040x", id+100), Labels: []string{"v2"}, BaseToken: marketdata.Token{Address: discoveryTokenA, Symbol: "A"}, QuoteToken: marketdata.Token{Address: discoveryTokenB, Symbol: "B"}, Liquidity: marketdata.Liquidity{USD: marketdata.Decimal(liquidity)}}
}
func discoveryFixture(projects []store.MarketProject, pairs map[string][]marketdata.Pair) (*Service, *discoveryTestRepository, *discoveryTestMarket, *discoveryTestChain) {
	sort.Slice(projects, func(i, j int) bool {
		return discoveryCursorLess(discoveryCursor(projects[i]), discoveryCursor(projects[j]))
	})
	repo := &discoveryTestRepository{fakeRepository: &fakeRepository{}, projects: projects}
	provider := &discoveryTestMarket{pairs: pairs, errors: map[string]error{}}
	client := &discoveryTestChain{fakeChain: &fakeChain{}, tokens: map[string][2]string{}}
	for _, rows := range pairs {
		for _, pair := range rows {
			client.tokens[pair.PairAddress] = [2]string{discoveryTokenA, discoveryTokenB}
		}
	}
	service := New(Dependencies{Repository: repo, Market: provider, Chain: client}, Settings{Enabled: true, ChainKey: "bsc", ChainID: 56})
	return service, repo, provider, client
}

func TestDiscoverySharesReadsNotProjectSelectionOrTokenClassification(t *testing.T) {
	low, high := discoveryPair(1, "10"), discoveryPair(2, "100")
	fetched := time.Date(2026, 11, 15, 1, 2, 3, 123000, time.UTC)
	low.QuoteQuality = marketquality.Quote{Status: marketquality.StatusCached, FetchedAt: &fetched}
	high.QuoteQuality = low.QuoteQuality.Clone()
	projects := []store.MarketProject{discoveryProject(1, discoveryTokenA), discoveryProject(2, discoveryTokenA), discoveryProject(3, discoveryTokenB), discoveryProject(4, discoveryTokenC)}
	s, r, m, c := discoveryFixture(projects, map[string][]marketdata.Pair{discoveryTokenA: {low, high}, discoveryTokenB: {low, high}, discoveryTokenC: {low, high}})
	r.selected = map[int64]int64{1: 999}
	if err := s.discoverPools(context.Background()); err == nil {
		t.Fatal("third-token mismatch was hidden")
	}
	for _, token := range []string{discoveryTokenA, discoveryTokenB, discoveryTokenC} {
		if m.calls[token] != 1 {
			t.Fatalf("token discovery calls=%v", m.calls)
		}
	}
	for _, pair := range []marketdata.Pair{low, high} {
		if c.tokenCalls[pair.PairAddress] != 1 || c.factoryCalls[pair.PairAddress] != 1 {
			t.Fatalf("duplicate facts: %v/%v", c.tokenCalls, c.factoryCalls)
		}
	}
	if len(r.writes) != 2 || len(r.links) != 6 || r.selected[1] != 999 || r.selected[2] != r.poolIDs[high.PairAddress] || r.selected[3] != r.poolIDs[high.PairAddress] || r.selected[4] != 0 {
		t.Fatalf("project scope/selection changed: writes=%d links=%+v selected=%v", len(r.writes), r.links, r.selected)
	}
	for _, params := range r.writes {
		var saved marketdata.Pair
		if err := json.Unmarshal(params.Metadata, &saved); err != nil {
			t.Fatal(err)
		}
		if saved.QuoteQuality.Status != marketquality.StatusCached || saved.QuoteQuality.FetchedAt == nil || !saved.QuoteQuality.FetchedAt.Equal(fetched) {
			t.Fatal("identity reuse refreshed a quote timestamp")
		}
	}
	if m.pairs[discoveryTokenA][0].PairAddress != low.PairAddress {
		t.Fatal("task sorted the provider's cache slice in place")
	}
	// A subsequent task must reverify facts instead of retaining a global cache.
	_ = s.discoverPools(context.Background())
	if c.tokenCalls[high.PairAddress] != 2 {
		t.Fatal("facts survived task lifetime")
	}
}

func TestDiscoveryKeysetContinuesBeyondBatchWithoutLoadingAllProjects(t *testing.T) {
	var projects []store.MarketProject
	for id := int64(1); id <= 405; id++ {
		projects = append(projects, discoveryProject(id, discoveryTokenA))
	}
	s, r, m, c := discoveryFixture(projects, map[string][]marketdata.Pair{discoveryTokenA: {discoveryPair(1, "1")}})
	for i := 0; i < 3; i++ {
		if err := s.discoverPools(context.Background()); err != nil {
			t.Fatal(err)
		}
		if s.discoveryPending() != (i < 2) {
			t.Fatalf("wrong continuation after batch %d", i)
		}
	}
	if len(r.links) != 405 || len(r.pages) != 3 || r.pages[0].ProjectID != 0 || r.pages[1].ProjectID != 200 || r.pages[2].ProjectID != 400 {
		t.Fatalf("keyset omitted/duplicated consumers: links=%d cursors=%+v", len(r.links), r.pages)
	}
	for _, limit := range r.limits {
		if limit != 201 {
			t.Fatalf("unbounded project input %d", limit)
		}
	}
	if m.calls[discoveryTokenA] != 3 || c.tokenCalls[discoveryPair(1, "1").PairAddress] != 3 {
		t.Fatal("same-key reads were not scoped once per bounded task")
	}
}

func TestDiscoveryCandidateAndBatchCapacityContinueRatherThanStall(t *testing.T) {
	var pairs []marketdata.Pair
	for i := 0; i < discoveryMaxPairsPerToken; i++ {
		pairs = append(pairs, discoveryPair(i, "1"))
	}
	s, r, _, _ := discoveryFixture([]store.MarketProject{discoveryProject(1, discoveryTokenA), discoveryProject(2, discoveryTokenA)}, map[string][]marketdata.Pair{discoveryTokenA: pairs})
	if err := s.discoverPools(context.Background()); !errors.Is(err, ErrDiscoveryCapacity) || !s.discoveryPending() || s.discovery.after.ProjectID != 1 {
		t.Fatalf("batch did not preserve resumable cursor: %v %+v", err, s.discovery.after)
	}
	if err := s.discoverPools(context.Background()); err != nil || s.discoveryPending() || len(r.links) != 2*len(pairs) {
		t.Fatalf("next task stalled: err=%v links=%d", err, len(r.links))
	}
	// An individual oversized token is explicitly failed, not truncated and
	// not allowed to permanently pin the cursor in front of all other tokens.
	pairs = append(pairs, discoveryPair(999, "1"))
	s, r, m, _ := discoveryFixture([]store.MarketProject{discoveryProject(1, discoveryTokenA), discoveryProject(2, discoveryTokenB)}, map[string][]marketdata.Pair{discoveryTokenA: pairs, discoveryTokenB: {discoveryPair(900, "1")}})
	if err := s.discoverPools(context.Background()); !errors.Is(err, ErrDiscoveryCapacity) || s.discoveryPending() || len(r.links) != 1 || m.calls[discoveryTokenB] != 1 {
		t.Fatalf("oversized token blocked healthy token: %v links=%d calls=%v", err, len(r.links), m.calls)
	}
}

func TestDiscoveryFailureIsIsolatedAndDoesNotRevalidateSuccessEverySnapshotTick(t *testing.T) {
	s, r, m, _ := discoveryFixture([]store.MarketProject{discoveryProject(1, discoveryTokenA), discoveryProject(2, discoveryTokenB)}, map[string][]marketdata.Pair{discoveryTokenB: {discoveryPair(1, "1")}})
	failure := errors.New("token temporarily unavailable")
	m.errors[discoveryTokenA] = failure
	now := time.Date(2026, 11, 15, 1, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	if err := s.RefreshMarketData(context.Background()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if len(r.links) != 1 {
		t.Fatal("bad token blocked healthy token")
	}
	if err := s.RefreshMarketData(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.calls[discoveryTokenA] != 1 || m.calls[discoveryTokenB] != 1 {
		t.Fatal("successful items repeated each 20-second snapshot tick")
	}
	now = now.Add(DefaultDiscoveryInterval)
	if err := s.RefreshMarketData(context.Background()); !errors.Is(err, failure) || m.calls[discoveryTokenA] != 2 {
		t.Fatal("failed token never retried on the original discovery schedule", err)
	}
}

func TestDiscoveryCanceledTaskNeverAdvancesOrUsesCachedSuccess(t *testing.T) {
	s, r, m, _ := discoveryFixture([]store.MarketProject{discoveryProject(1, discoveryTokenA)}, map[string][]marketdata.Pair{discoveryTokenA: {discoveryPair(1, "1")}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.discoverPools(ctx); !errors.Is(err, context.Canceled) || len(r.links) != 0 || len(m.calls) != 0 || s.discovery.after.ProjectID != 0 || !s.discoveryPending() {
		t.Fatalf("cancellation changed state: %v %+v", err, s.discovery.after)
	}
}

func TestDiscoveryInternalTimeoutLetsNextTokenRunAndRetriesFailedProjectNextTraversal(t *testing.T) {
	fast, slow, other := discoveryPair(1, "100"), discoveryPair(2, "50"), discoveryPair(3, "1")
	s, r, _, c := discoveryFixture([]store.MarketProject{discoveryProject(1, discoveryTokenA), discoveryProject(2, discoveryTokenB)}, map[string][]marketdata.Pair{discoveryTokenA: {fast, slow}, discoveryTokenB: {other}})
	c.blockPools = map[string]bool{slow.PairAddress: true}
	if err := s.discoverPoolsWithin(context.Background(), 20*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) || s.discovery.after.ProjectID != 1 || !s.discoveryPending() {
		t.Fatalf("internal timeout pinned project: %v %+v", err, s.discovery.after)
	}
	selected := r.selected[1]
	if selected == 0 || len(r.links) != 1 {
		t.Fatal("completed first link was rolled back or slow candidate marked complete")
	}
	if err := s.discoverPools(context.Background()); err != nil || s.discoveryPending() || r.selected[2] == 0 {
		t.Fatalf("later healthy token was starved: %v", err)
	}
	if err := s.discoverPoolsWithin(context.Background(), 20*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) || c.tokenCalls[slow.PairAddress] != 2 || r.selected[1] != selected {
		t.Fatalf("failed project not retried or selection changed: %v calls=%v selected=%v", err, c.tokenCalls, r.selected)
	}
}

func TestDiscoveryOuterCancellationMidProjectKeepsItsCursorAndExistingSelection(t *testing.T) {
	fast, slow := discoveryPair(1, "100"), discoveryPair(2, "50")
	s, r, _, c := discoveryFixture([]store.MarketProject{discoveryProject(1, discoveryTokenA)}, map[string][]marketdata.Pair{discoveryTokenA: {fast, slow}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.blockPools = map[string]bool{slow.PairAddress: true}
	c.beforeRead = func(pool string) {
		if pool == slow.PairAddress {
			cancel()
		}
	}
	if err := s.discoverPools(ctx); !errors.Is(err, context.Canceled) || s.discovery.after.ProjectID != 0 || !s.discoveryPending() || len(r.links) != 1 || r.selected[1] == 0 {
		t.Fatalf("outer cancel advanced/rolled back state: %v after=%+v links=%d", err, s.discovery.after, len(r.links))
	}
}

func TestDiscoveryPrecisionFailureIsBoundedAndNeverBecomesVerifiedDefault(t *testing.T) {
	s, r, m, c := discoveryFixture([]store.MarketProject{discoveryProject(1, discoveryTokenA), discoveryProject(2, discoveryTokenA)}, map[string][]marketdata.Pair{discoveryTokenA: {discoveryPair(1, "100")}})
	failure := errors.New("unknown token decimals")
	c.decimalsErr = failure
	if err := s.discoverPools(context.Background()); !errors.Is(err, failure) || c.decimalsCalls != 1 || len(r.writes) != 0 || len(r.links) != 0 || m.calls[discoveryTokenA] != 1 {
		t.Fatalf("precision failure retried per-user or invented defaults: err=%v decimals=%d writes=%d", err, c.decimalsCalls, len(r.writes))
	}
	_ = s.discoverPools(context.Background())
	if c.decimalsCalls != 2 {
		t.Fatal("precision failure persisted past task lifetime")
	}
}

func TestDiscoveryTemporaryRPCFailureKeepsVerifiedPoolAndNextTaskCanRecover(t *testing.T) {
	pair := discoveryPair(1, "100")
	s, r, _, c := discoveryFixture([]store.MarketProject{discoveryProject(1, discoveryTokenA), discoveryProject(2, discoveryTokenB)}, map[string][]marketdata.Pair{discoveryTokenA: {pair}, discoveryTokenB: {pair}})
	if err := s.discoverPools(context.Background()); err != nil || len(r.writes) != 1 || !r.writes[0].FactoryVerified || !r.writes[0].SupportsEventParsing {
		t.Fatalf("initial verified pool failed: %v", err)
	}
	selectedA, selectedB := r.selected[1], r.selected[2]
	c.err = errors.New("RPC temporarily unavailable")
	if err := s.discoverPools(context.Background()); err == nil || len(r.writes) != 1 || len(r.links) != 2 || r.selected[1] != selectedA || r.selected[2] != selectedB {
		t.Fatalf("failed RPC downgraded existing facts/selection: err=%v writes=%d links=%d", err, len(r.writes), len(r.links))
	}
	c.err = nil
	if err := s.discoverPools(context.Background()); err != nil || len(r.writes) != 2 || !r.writes[1].FactoryVerified || !r.writes[1].SupportsEventParsing || len(r.links) != 4 {
		t.Fatalf("next task reused failed facts instead of recovering: %v", err)
	}
}

type discoveryQuoteProvider struct{ *quoteRefreshProvider }

func (p *discoveryQuoteProvider) DiscoverPools(context.Context, string, string) ([]marketdata.Pair, error) {
	return p.pairs, nil
}

func TestDiscoveryKnownQuotesContinueAfterVerifiedPoolRPCFailure(t *testing.T) {
	s, r, p := quoteRefreshFixture()
	s.chain = &discoveryTestChain{fakeChain: &fakeChain{}, err: errors.New("RPC temporarily unavailable")}
	s.market = &discoveryQuoteProvider{p}
	if err := s.RefreshMarketData(context.Background()); err == nil || p.quoteCalls != 1 || len(r.writes) != 2 || len(r.failed) != 0 || r.lastPool.PoolKey != "" {
		t.Fatalf("pool RPC failure blocked quotes or overwrote existing identity: %v writes=%d", err, len(r.writes))
	}
}
