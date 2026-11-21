package marketview

import (
	"context"
	"errors"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func (f fakeChain) TokenDecimals(context.Context, string, string, string) (int32, error) {
	return f.metadata.Decimals, f.err
}

type precisionViewChain struct {
	fakeChain
	calls        int
	decimals     int32
	decimalsErr  error
	errorAddress string
}

func (f *precisionViewChain) TokenDecimals(_ context.Context, address, _, _ string) (int32, error) {
	f.calls++
	if f.errorAddress != "" && f.errorAddress != address {
		return f.decimals, nil
	}
	return f.decimals, f.decimalsErr
}

type precisionCreationRepository struct {
	precisionPoolRepository
	project       store.MarketProject
	statusChanges int
}

func (r *precisionCreationRepository) GetMarketProject(context.Context, int64, string) (*store.MarketProject, error) {
	return &r.project, nil
}
func (r *precisionCreationRepository) SetMarketProjectStatus(context.Context, int64, string, string, string) (store.MarketProject, error) {
	r.statusChanges++
	return r.project, nil
}
func (*precisionCreationRepository) ListMarketProjectPoolViews(context.Context, int64, string) ([]store.MarketPoolView, error) {
	return nil, nil
}
func (*precisionCreationRepository) ListMarketRules(context.Context, string, *int64) ([]store.MarketRule, error) {
	return nil, nil
}
func (*precisionCreationRepository) ListMarketHolders(context.Context, int64, string, bool, int) ([]store.MarketHolder, error) {
	return nil, nil
}
func (*precisionCreationRepository) ListMarketAddressLabels(context.Context, int64, string) ([]store.MarketAddressLabel, error) {
	return nil, nil
}
func (*precisionCreationRepository) ListMarketProviderHealth(context.Context) ([]store.MarketProviderHealth, error) {
	return nil, nil
}
func (*precisionCreationRepository) LatestMarketSnapshot(context.Context, int64, string, *int64) (*store.MarketSnapshot, error) {
	return nil, nil
}

type precisionCreationEntitlements struct {
	fakeEntitlements
	repository *precisionCreationRepository
	linked     int
}

func (e *precisionCreationEntitlements) CreateMarketProject(_ context.Context, p store.CreateMarketProjectParams) (store.MarketProject, error) {
	e.repository.project = store.MarketProject{ID: 1, DeBoxUserID: p.DeBoxUserID, ChainID: p.ChainID, ChainKey: p.ChainKey, TokenAddress: p.TokenAddress, TokenDecimals: p.TokenDecimals, Status: "active"}
	return e.repository.project, nil
}
func (e *precisionCreationEntitlements) LinkMarketProjectPool(_ context.Context, p store.LinkMarketProjectPoolParams) (store.MarketProjectPool, error) {
	if p.Selected {
		e.linked++
	}
	return store.MarketProjectPool{}, nil
}

func TestCreateProjectSkipsUnknownUnselectedQuoteWithoutArchivingSelectedPool(t *testing.T) {
	unknownQuote := "0x5555555555555555555555555555555555555555"
	client := &precisionViewChain{fakeChain: fakeChain{metadata: chain.TokenMetadata{Address: testToken, Symbol: "PROJECT", Decimals: 0, TotalSupplyRaw: "1000"}, factories: map[string]string{testPairB: "0x9999999999999999999999999999999999999999"}}, decimals: 6, decimalsErr: errors.New("unknown quote"), errorAddress: unknownQuote}
	repository := &precisionCreationRepository{}
	entitlements := &precisionCreationEntitlements{fakeEntitlements: fakeEntitlements{plan: plans.Plan{Code: plans.Professional, MarketQuery: true, MarketProjectLimit: 1, MarketPoolLimit: 2, AllowedMarketChainKeys: []string{"bsc"}}}, repository: repository}
	service := New(Dependencies{Chain: client, Repository: repository, Entitlements: entitlements, Market: fakeMarket{pairs: []marketdata.Pair{
		{ChainID: "bsc", PairAddress: testPairA, BaseToken: marketdata.Token{Address: testToken}, QuoteToken: marketdata.Token{Address: testQuote}},
		{ChainID: "bsc", PairAddress: testPairB, BaseToken: marketdata.Token{Address: testToken}, QuoteToken: marketdata.Token{Address: unknownQuote}},
	}}})
	detail, err := service.CreateProject(context.Background(), "fixture", CreateProjectInput{ChainKey: "bsc", TokenAddress: testToken, PoolAddresses: []string{testPairA}})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Project.Status != "active" || entitlements.linked != 1 || repository.calls != 1 || repository.statusChanges != 0 {
		t.Fatalf("selected creation was affected: detail=%+v links=%d writes=%d archived=%d", detail.Project, entitlements.linked, repository.calls, repository.statusChanges)
	}
}

type precisionPoolRepository struct {
	Repository
	calls  int
	params store.UpsertMarketPoolParams
}

func (r *precisionPoolRepository) UpsertMarketPool(_ context.Context, p store.UpsertMarketPoolParams) (store.MarketPool, error) {
	r.calls++
	r.params = p
	return store.MarketPool{ID: 1}, nil
}

func TestPersistPoolResolvesBothSidesWithoutOverwritingProjectZero(t *testing.T) {
	client := &precisionViewChain{fakeChain: fakeChain{token0: testQuote, token1: testToken}, decimals: 6}
	repo := &precisionPoolRepository{}
	service := New(Dependencies{Chain: client, Repository: repo})
	token := chain.TokenMetadata{Address: testToken, Symbol: "PROJECT", Decimals: 0}
	preview := PoolPreview{ChainKey: "bsc", ChainID: 56, Supported: true, Pair: marketdata.Pair{PairAddress: testPairA, BaseToken: marketdata.Token{Address: testToken, Symbol: "PROJECT"}, QuoteToken: marketdata.Token{Address: testQuote, Symbol: "QUOTE"}}}
	known := make(map[string]int32)
	for i := 0; i < 2; i++ {
		if _, err := service.persistPoolWithDecimals(context.Background(), token, preview, known); err != nil {
			t.Fatal(err)
		}
	}
	if client.calls != 1 || repo.params.Token0Decimals != 6 || repo.params.Token1Decimals != 0 || repo.params.Token0Symbol != "QUOTE" || !repo.params.Token0DecimalsVerified || !repo.params.Token1DecimalsVerified {
		t.Fatalf("metadata=%+v, calls=%d", repo.params, client.calls)
	}
}

func TestPersistQuoteOnlyPoolDoesNotInventUnknownPrecision(t *testing.T) {
	client := &precisionViewChain{decimalsErr: errors.New("unavailable")}
	repo := &precisionPoolRepository{}
	service := New(Dependencies{Chain: client, Repository: repo})
	_, err := service.persistPool(context.Background(), chain.TokenMetadata{Address: testToken, Decimals: 18}, PoolPreview{ChainKey: "bsc", ChainID: 56, Pair: marketdata.Pair{PairAddress: testPairA, BaseToken: marketdata.Token{Address: testToken}, QuoteToken: marketdata.Token{Address: testQuote}}})
	if !errors.Is(err, errPoolTokenDecimalsUnavailable) || repo.calls != 0 {
		t.Fatalf("unknown precision error=%v, writes=%d", err, repo.calls)
	}
}
