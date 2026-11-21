package marketrules

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type holderRefreshTestRepository struct {
	Repository
	state    *store.MarketHolderRefreshState
	pools    []string
	old      []store.MarketHolder
	params   []store.MarketHolderRefreshParams
	attempts int
	apply    func(store.MarketHolderRefreshParams) (store.MarketHolderRefreshResult, error)
}

func (r *holderRefreshTestRepository) GetMarketHolderRefreshState(context.Context, int64, string) (*store.MarketHolderRefreshState, error) {
	return r.state, nil
}
func (r *holderRefreshTestRepository) RecordMarketHolderRefreshAttempt(context.Context, int64, string, string, time.Time) error {
	r.attempts++
	return nil
}
func (r *holderRefreshTestRepository) ListMarketHolderPoolAddresses(context.Context, int64, string) ([]string, error) {
	return r.pools, nil
}
func (r *holderRefreshTestRepository) ApplyMarketHolderRefresh(_ context.Context, p store.MarketHolderRefreshParams) (store.MarketHolderRefreshResult, error) {
	r.params = append(r.params, p)
	if r.apply != nil {
		return r.apply(p)
	}
	return store.MarketHolderRefreshResult{}, nil
}
func (r *holderRefreshTestRepository) ListMarketHolders(context.Context, int64, string, bool, int) ([]store.MarketHolder, error) {
	return r.old, nil
}
func (r *holderRefreshTestRepository) ListMarketSnapshots(context.Context, int64, string, int) ([]store.MarketSnapshot, error) {
	return nil, nil
}

type holderSampleTestProvider struct {
	pages   []chain.TokenHoldersPage
	errAt   int
	err     error
	options []chain.TokenHoldersOptions
	onFirst func()
}

func (p *holderSampleTestProvider) TokenHoldersByContract(_ context.Context, _, _, _ string, o chain.TokenHoldersOptions) (chain.TokenHoldersPage, error) {
	i := len(p.options)
	p.options = append(p.options, o)
	if len(p.options) == 1 && p.onFirst != nil {
		p.onFirst()
	}
	if p.errAt > 0 && len(p.options) == p.errAt {
		if p.err != nil {
			return chain.TokenHoldersPage{}, p.err
		}
		return chain.TokenHoldersPage{}, errors.New("fixture provider failed")
	}
	if i >= len(p.pages) {
		return chain.TokenHoldersPage{}, errors.New("unexpected page request")
	}
	return p.pages[i], nil
}

func TestHolderRefreshCapturesCASBeforeProviderSampling(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	oldAt, newerAt := now.Add(-2*time.Minute), now.Add(-time.Minute)
	count := int64(1)
	repository := &holderRefreshTestRepository{state: &store.MarketHolderRefreshState{LastSuccessAt: &oldAt}}
	provider := &holderSampleTestProvider{pages: []chain.TokenHoldersPage{{Count: &count, Items: []chain.TokenHolder{{OwnerAddress: fmt.Sprintf("0x%040x", 100), BalanceRaw: "100"}}}}}
	provider.onFirst = func() { repository.state = &store.MarketHolderRefreshState{LastSuccessAt: &newerAt} }
	repository.apply = func(p store.MarketHolderRefreshParams) (store.MarketHolderRefreshResult, error) {
		if p.ExpectedPreviousAt == nil || !p.ExpectedPreviousAt.Equal(oldAt) {
			t.Fatal("CAS was read after provider sampling")
		}
		return store.MarketHolderRefreshResult{}, store.ErrStaleMarketHolderRefresh
	}
	service := New(Dependencies{Repository: repository, Holders: provider}, Settings{})
	service.now = func() time.Time { return now }
	err := service.refreshProjectHolders(context.Background(), store.MarketProject{ChainID: 56, ChainKey: "bsc", TokenAddress: tradeFactsTestToken})
	if !errors.Is(err, store.ErrStaleMarketHolderRefresh) || !repository.state.LastSuccessAt.Equal(newerAt) {
		t.Fatalf("older in-flight sample was accepted: %v", err)
	}
}

func TestHolderRefreshUsesCompleteSharedSampleAndKeepsTop20BalanceContract(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	previous := now.Add(-15 * time.Minute)
	a, b, poolAddress := fmt.Sprintf("0x%040x", 100), fmt.Sprintf("0x%040x", 101), fmt.Sprintf("0x%040x", 102)
	rank := int32(1)
	total := int64(4)
	supply := "2000"
	repository := &holderRefreshTestRepository{pools: []string{poolAddress}, state: &store.MarketHolderRefreshState{LastSuccessAt: &previous}, old: []store.MarketHolder{{HolderAddress: a, BalanceRaw: "80", Balance: "80", Rank: &rank}}}
	provider := &holderSampleTestProvider{pages: []chain.TokenHoldersPage{
		{Count: &total, Cursor: "second", Items: []chain.TokenHolder{{OwnerAddress: b, BalanceRaw: "90"}, {OwnerAddress: poolAddress, BalanceRaw: "9999"}}},
		{Count: &total, Items: []chain.TokenHolder{{OwnerAddress: a, BalanceRaw: "100"}, {OwnerAddress: fmt.Sprintf("0x%040x", 103), BalanceRaw: "0"}}},
	}}
	service := New(Dependencies{Repository: repository, Holders: provider}, Settings{})
	service.now = func() time.Time { return now }
	project := store.MarketProject{ID: 1, ChainKey: "base", ChainID: 8453, TokenAddress: tradeFactsTestToken, TokenDecimals: 0, TotalSupplyRaw: &supply}
	if err := service.refreshProjectHolders(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if len(provider.options) != 2 || provider.options[1].Cursor != "second" || len(repository.params) != 1 {
		t.Fatalf("sample was repeated/per-rule or truncated: calls=%d apply=%d", len(provider.options), len(repository.params))
	}
	p := repository.params[0]
	if len(p.Ranks) != 2 || p.Ranks[0].HolderAddress != a || p.Ranks[1].HolderAddress != b || p.CoveredTopN != 100 || p.SourceItemCount != 4 || !p.SourceComplete || len(p.Holders) != 2 || p.Holders[0].SupplyPercent == nil || *p.Holders[0].SupplyPercent != "5" {
		t.Fatalf("global exclusions/order/target supply incorrect: %#v", p)
	}
	if len(p.BalanceEvents) != 1 || p.BalanceEvents[0].TokenAmountRaw == nil || *p.BalanceEvents[0].TokenAmountRaw != "20" || p.BalanceEvents[0].USDValue != nil || p.ExpectedBalances[a] != "80" {
		t.Fatalf("legacy tracked holder facts changed: %#v", p.BalanceEvents)
	}
}

func TestHolderRefreshIncompleteOrOverBudgetSourceDoesNotApplyAnyBaseline(t *testing.T) {
	a, b := fmt.Sprintf("0x%040x", 100), fmt.Sprintf("0x%040x", 101)
	two, three, bigCount := int64(2), int64(3), int64(store.MarketHolderRefreshMaxHolders+1)
	first := chain.TokenHoldersPage{Count: &two, Cursor: "next", Items: []chain.TokenHolder{{OwnerAddress: a, BalanceRaw: "100"}}}
	for _, test := range []struct {
		name  string
		pages []chain.TokenHoldersPage
		errAt int
	}{
		{"second page failed", []chain.TokenHoldersPage{first}, 2},
		{"missing count", []chain.TokenHoldersPage{{Items: []chain.TokenHolder{{OwnerAddress: a, BalanceRaw: "100"}}}}, 0},
		{"inconsistent count", []chain.TokenHoldersPage{first, {Count: &three, Items: []chain.TokenHolder{{OwnerAddress: b, BalanceRaw: "90"}}}}, 0},
		{"duplicate across pages", []chain.TokenHoldersPage{first, {Count: &two, Items: []chain.TokenHolder{{OwnerAddress: a, BalanceRaw: "100"}}}}, 0},
		{"cursor loop", []chain.TokenHoldersPage{{Count: &three, Cursor: "same", Items: first.Items}, {Count: &three, Cursor: "same", Items: []chain.TokenHolder{{OwnerAddress: b, BalanceRaw: "90"}}}}, 0},
		{"missing next cursor", []chain.TokenHoldersPage{{Count: &two, Items: first.Items}}, 0},
		{"capacity", []chain.TokenHoldersPage{{Count: &bigCount, Cursor: "more", Items: first.Items}}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &holderRefreshTestRepository{}
			provider := &holderSampleTestProvider{pages: test.pages, errAt: test.errAt}
			service := New(Dependencies{Repository: repository, Holders: provider}, Settings{})
			err := service.refreshProjectHolders(context.Background(), store.MarketProject{ChainID: 56, ChainKey: "bsc", TokenAddress: tradeFactsTestToken})
			if err == nil || !strings.Contains(err.Error(), "source_coverage_unknown") || len(repository.params) != 0 || repository.attempts != 1 {
				t.Fatalf("partial source advanced baseline: err=%v apply=%d", err, len(repository.params))
			}
		})
	}
}

func TestCompleteHolderSamplingHonorsPageAndSweepBudgets(t *testing.T) {
	count := int64(store.MarketHolderRefreshMaxHolders)
	pages := []chain.TokenHoldersPage{}
	for i := 0; i < store.MarketHolderRefreshMaxPages; i++ {
		pages = append(pages, chain.TokenHoldersPage{Count: &count, Cursor: fmt.Sprint(i + 1), Items: []chain.TokenHolder{{OwnerAddress: fmt.Sprintf("0x%040x", i+100), BalanceRaw: "1"}}})
	}
	provider := &holderSampleTestProvider{pages: pages}
	service := New(Dependencies{Holders: provider}, Settings{})
	budget := 60
	if _, err := service.completeHolderSample(context.Background(), store.MarketProject{}, &budget); err == nil || len(provider.options) != store.MarketHolderRefreshMaxPages || budget != 60-store.MarketHolderRefreshMaxPages {
		t.Fatalf("page budget ignored: requests=%d budget=%d err=%v", len(provider.options), budget, err)
	}
	provider.options = nil
	budget = 1
	if _, err := service.completeHolderSample(context.Background(), store.MarketProject{}, &budget); err == nil || len(provider.options) != 1 || budget != 0 {
		t.Fatalf("sweep budget ignored: requests=%d budget=%d err=%v", len(provider.options), budget, err)
	}
}
