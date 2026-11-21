package marketrules

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func completeHolderPages(count int) []chain.TokenHoldersPage {
	pages := make([]chain.TokenHoldersPage, 0, (count+store.MarketHolderRefreshPageSize-1)/store.MarketHolderRefreshPageSize)
	for offset := 0; offset < count; offset += store.MarketHolderRefreshPageSize {
		total := int64(count)
		page := chain.TokenHoldersPage{Count: &total}
		end := min(offset+store.MarketHolderRefreshPageSize, count)
		for index := offset; index < end; index++ {
			page.Items = append(page.Items, chain.TokenHolder{OwnerAddress: completeHolderAddress(index), BalanceRaw: fmt.Sprint(index + 1)})
		}
		if end < count {
			page.Cursor = fmt.Sprintf("holder-page-%d", len(pages)+1)
		}
		pages = append(pages, page)
	}
	return pages
}

func completeHolderAddress(index int) string { return fmt.Sprintf("0x%040x", index+10000) }

func TestHolderRefreshCompleteEnumerationSupports1001And2000(t *testing.T) {
	for _, count := range []int{1001, 2000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			previous := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
			firstRank := int32(1)
			// The largest balance is excluded. The next-largest balance occurs
			// on the last page and must win after the full sample is collected.
			winner := completeHolderAddress(count - 2)
			repo := &holderRefreshTestRepository{
				state: &store.MarketHolderRefreshState{LastSuccessAt: &previous},
				pools: []string{completeHolderAddress(count - 1)},
				old:   []store.MarketHolder{{HolderAddress: winner, BalanceRaw: fmt.Sprint(count - 2), Balance: fmt.Sprint(count - 2), Rank: &firstRank}},
			}
			provider := &holderSampleTestProvider{pages: completeHolderPages(count)}
			service := New(Dependencies{Repository: repo, Holders: provider}, Settings{})
			service.now = func() time.Time { return previous.Add(15 * time.Minute) }
			if err := service.refreshProjectHolders(context.Background(), store.MarketProject{ChainID: 56, ChainKey: "bsc", TokenAddress: tradeFactsTestToken}); err != nil {
				t.Fatal(err)
			}
			if len(provider.options) != len(provider.pages) || len(repo.params) != 1 || repo.attempts != 1 {
				t.Fatalf("incomplete/shared sample: calls=%d pages=%d applies=%d attempts=%d", len(provider.options), len(provider.pages), len(repo.params), repo.attempts)
			}
			for index, options := range provider.options {
				wantCursor := ""
				if index > 0 {
					wantCursor = provider.pages[index-1].Cursor
				}
				if options.Cursor != wantCursor || options.RPP != store.MarketHolderRefreshPageSize || !options.WithCount {
					t.Fatalf("page %d options=%+v", index+1, options)
				}
			}
			p := repo.params[0]
			if !p.SourceComplete || p.SourceItemCount != int32(count) || p.CoveredTopN != 100 || len(p.Ranks) != 100 || len(p.Holders) != 20 || p.ExpectedPreviousAt == nil || !p.ExpectedPreviousAt.Equal(previous) {
				t.Fatalf("coverage/old tracking contract changed: %+v", p)
			}
			for index, rank := range p.Ranks {
				if rank.HolderAddress != completeHolderAddress(count-2-index) || rank.Rank != int32(index+1) || rank.BalanceRaw != fmt.Sprint(count-1-index) {
					t.Fatalf("global sampled rank %d incorrect: %+v", index+1, rank)
				}
			}
			if len(p.BalanceEvents) != 1 || p.BalanceEvents[0].TokenAmountRaw == nil || *p.BalanceEvents[0].TokenAmountRaw != "1" || p.ExpectedBalances[winner] != fmt.Sprint(count-2) {
				t.Fatalf("net balance change contract changed: %+v", p.BalanceEvents)
			}
		})
	}
}

func TestHolderSchedulerCompletesTwentyPageSharedSampleOnce(t *testing.T) {
	provider := &holderSampleTestProvider{pages: completeHolderPages(store.MarketHolderRefreshMaxHolders)}
	runner, repo := holderSchedulerFixture(t, provider)
	result, err := runner.RunOnce(context.Background())
	if err != nil || result.Claimed != 1 || result.Refreshed != 1 || result.PagesAdmitted != int64(store.MarketHolderRefreshMaxPages) || repo.pages != store.MarketHolderRefreshMaxPages || repo.completed != 1 || repo.failed != 0 || len(repo.params) != 1 {
		t.Fatalf("complete sample not applied once: result=%+v pages=%d complete=%d failed=%d err=%v", result, repo.pages, repo.completed, repo.failed, err)
	}
	p := repo.params[0]
	if p.SourceItemCount != int32(store.MarketHolderRefreshMaxHolders) || !p.SourceComplete || len(p.Ranks) != 100 || len(p.Holders) != 20 || p.Ranks[0].HolderAddress != completeHolderAddress(store.MarketHolderRefreshMaxHolders-1) {
		t.Fatalf("scheduler did not use the complete shared sample: %+v", p)
	}
}

func TestHolderSchedulerLastPageFailuresNeverCompleteBaseline(t *testing.T) {
	for _, test := range []struct {
		name     string
		mutate   func([]chain.TokenHoldersPage)
		provider error
		reserve  error
		wantCode string
		wantHTTP int
	}{
		{name: "missing twentieth page", mutate: func(p []chain.TokenHoldersPage) { p[19].Items = nil }, wantCode: "source_incomplete", wantHTTP: 20},
		{name: "missing final count", mutate: func(p []chain.TokenHoldersPage) { p[19].Count = nil }, wantCode: "source_incomplete", wantHTTP: 20},
		{name: "final count drift", mutate: func(p []chain.TokenHoldersPage) { count := int64(1999); p[19].Count = &count }, wantCode: "source_incomplete", wantHTTP: 20},
		{name: "final duplicate address", mutate: func(p []chain.TokenHoldersPage) { p[19].Items[99].OwnerAddress = p[0].Items[0].OwnerAddress }, wantCode: "source_incomplete", wantHTTP: 20},
		{name: "final repeated cursor", mutate: func(p []chain.TokenHoldersPage) { p[19].Items = p[19].Items[:99]; p[19].Cursor = p[18].Cursor }, wantCode: "source_incomplete", wantHTTP: 20},
		{name: "final malformed raw", mutate: func(p []chain.TokenHoldersPage) { p[19].Items[99].BalanceRaw = "+1" }, wantCode: "source_incomplete", wantHTTP: 20},
		{name: "final uint256 overflow", mutate: func(p []chain.TokenHoldersPage) {
			p[19].Items[99].BalanceRaw = new(big.Int).Lsh(big.NewInt(1), 256).String()
		}, wantCode: "source_incomplete", wantHTTP: 20},
		{name: "final provider error", provider: errors.New("fixture HTTP failure"), wantCode: "source_error", wantHTTP: 20},
		{name: "final provider timeout", provider: context.DeadlineExceeded, wantCode: "round_timeout", wantHTTP: 20},
		{name: "final budget revoked", reserve: &store.MarketHolderRefreshBudgetError{}, wantCode: "provider_budget", wantHTTP: 19},
		{name: "final demand removed", reserve: store.ErrMarketHolderRefreshDemandLost, wantCode: "round_cancelled", wantHTTP: 19},
	} {
		t.Run(test.name, func(t *testing.T) {
			pages := completeHolderPages(store.MarketHolderRefreshMaxHolders)
			if test.mutate != nil {
				test.mutate(pages)
			}
			provider := &holderSampleTestProvider{pages: pages, err: test.provider}
			if test.provider != nil {
				provider.errAt = store.MarketHolderRefreshMaxPages
			}
			runner, repo := holderSchedulerFixture(t, provider)
			previous := *repo.jobs[0].ExpectedPreviousAt
			repo.state = &store.MarketHolderRefreshState{LastSuccessAt: &previous}
			if test.reserve != nil {
				repo.reserveErrAt, repo.reserveErr = store.MarketHolderRefreshMaxPages, test.reserve
			}
			result, err := runner.RunOnce(context.Background())
			if err == nil || result.Refreshed != 0 || result.FailureCode != test.wantCode || repo.failed != 1 || repo.completed != 0 || len(repo.params) != 0 || len(provider.options) != test.wantHTTP || !repo.state.LastSuccessAt.Equal(previous) {
				t.Fatalf("partial sample committed: result=%+v calls=%d complete=%d failed=%d previous=%v err=%v", result, len(provider.options), repo.completed, repo.failed, repo.state.LastSuccessAt, err)
			}
		})
	}
}

type holderLateResponseProvider struct {
	*holderSampleTestProvider
	cancel context.CancelFunc
}

func (p holderLateResponseProvider) TokenHoldersByContract(ctx context.Context, token, key, fallback string, options chain.TokenHoldersOptions) (chain.TokenHoldersPage, error) {
	page, err := p.holderSampleTestProvider.TokenHoldersByContract(ctx, token, key, fallback, options)
	if len(p.options) == store.MarketHolderRefreshMaxPages {
		p.cancel()
	}
	return page, err
}

func TestHolderSchedulerIgnoresCompleteFinalResponseAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := holderLateResponseProvider{holderSampleTestProvider: &holderSampleTestProvider{pages: completeHolderPages(store.MarketHolderRefreshMaxHolders)}, cancel: cancel}
	runner, repo := holderSchedulerFixture(t, provider)
	result, err := runner.RunOnce(ctx)
	if !errors.Is(err, context.Canceled) || result.FailureCode != "round_cancelled" || len(provider.options) != 20 || repo.failed != 1 || repo.completed != 0 || len(repo.params) != 0 {
		t.Fatalf("cancelled final page was applied: result=%+v complete=%d failure=%d err=%v", result, repo.completed, repo.failed, err)
	}
}
