package marketview

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type quoteViewRepository struct {
	precisionCreationRepository
	snapshot             *store.MarketSnapshot
	snapshotErr          error
	eventReads           int
	sampleReads          int
	sampleFrom, sampleTo time.Time
	sampleLimit          int
	sampleReference      store.MarketSnapshot
	samples              []store.MarketRecommendationSample
	sampleErr            error
	pools                []store.MarketPoolView
	holderStateReads     int
	holderStates         []store.MarketHolderRefreshState
	rules                []store.MarketRule
}

func (r *quoteViewRepository) LatestMarketSnapshot(context.Context, int64, string, *int64) (*store.MarketSnapshot, error) {
	return r.snapshot, r.snapshotErr
}
func (r *quoteViewRepository) ListMarketEvents(context.Context, int64, string, int64, int) ([]store.MarketEvent, error) {
	r.eventReads++
	return nil, nil
}

func (r *quoteViewRepository) ListMarketRecommendationSamples(_ context.Context, _ int64, _ string, reference store.MarketSnapshot, from, to time.Time, limit int) ([]store.MarketRecommendationSample, error) {
	r.sampleReads++
	r.sampleReference, r.sampleFrom, r.sampleTo, r.sampleLimit = reference, from, to, limit
	return r.samples, r.sampleErr
}

func TestRecommendationsSampleBudgetFailureRejectsInsteadOfUsingFormulaFallback(t *testing.T) {
	at := time.Now().UTC()
	price, liquidity := "2", "100"
	repository := &quoteViewRepository{snapshot: &store.MarketSnapshot{PriceUSD: &price, LiquidityUSD: &liquidity, QuoteQuality: marketquality.Quote{Status: marketquality.StatusLive, FetchedAt: &at}}, sampleErr: store.ErrMarketRecommendationUnavailable}
	result, err := New(Dependencies{Repository: repository}).RecommendationsWithQuote(context.Background(), "fixture", 1)
	if !errors.Is(err, ErrQuoteUnavailable) || !errors.Is(err, store.ErrMarketRecommendationUnavailable) || len(result.Recommendations) != 0 || repository.sampleReads != 1 {
		t.Fatalf("budget error replaced with thresholds=%+v %v reads%d", result, err, repository.sampleReads)
	}
}

func (r *quoteViewRepository) ListMarketProjectPoolViews(context.Context, int64, string) ([]store.MarketPoolView, error) {
	return r.pools, nil
}

func (r *quoteViewRepository) ListMarketHolderRefreshStatesForProject(context.Context, int64, string) ([]store.MarketHolderRefreshState, error) {
	r.holderStateReads++
	return r.holderStates, nil
}

func (r *quoteViewRepository) ListMarketRules(context.Context, string, *int64) ([]store.MarketRule, error) {
	return r.rules, nil
}

func TestProjectAnnotatesLegacyParametersAndReadsHolderCoverageOnce(t *testing.T) {
	window := int32(17)
	at := time.Now().UTC().Add(-time.Minute)
	repository := &quoteViewRepository{
		rules:        []store.MarketRule{{ID: 7, RuleType: plans.MarketVolumeAbove, WindowMinutes: &window, ThresholdValue: "42", Enabled: 1, RunStatus: "active"}},
		holderStates: []store.MarketHolderRefreshState{{ChainID: 56, ChainKey: "bsc", TokenAddress: testToken, LastSuccessAt: &at, CoveredTopN: 100, Status: "source_coverage_unknown"}},
	}
	repository.project = store.MarketProject{ID: 1, ChainID: 56, TokenAddress: testToken, Status: "active"}
	result, err := New(Dependencies{Repository: repository}).Project(context.Background(), "fixture", 1)
	if err != nil || repository.holderStateReads != 1 || len(result.HolderRefreshStates) != 1 || !result.HolderRefreshStates[0].LastSuccessAt.Equal(at) || result.HolderRefreshStates[0].Status != "source_coverage_unknown" {
		t.Fatalf("holder coverage=%+v reads=%d err=%v", result.HolderRefreshStates, repository.holderStateReads, err)
	}
	if len(result.Rules) != 1 || len(result.Rules[0].RuntimeIssues) != 1 || *result.Rules[0].WindowMinutes != 17 || result.Rules[0].ThresholdValue != "42" || result.Rules[0].Enabled != 1 || result.Rules[0].RunStatus != "active" {
		t.Fatalf("legacy parameter/status rewrite=%+v", result.Rules)
	}
}

func TestRecommendationsRejectUnusableQuoteBeforeReadingSamples(t *testing.T) {
	now, old, future := time.Now().UTC(), time.Now().UTC().Add(-24*time.Hour), time.Now().UTC().Add(time.Hour)
	for _, quality := range []marketquality.Quote{
		{}, {Status: marketquality.StatusLive},
		{Status: marketquality.StatusStaleFallback, FetchedAt: &now},
		{Status: marketquality.StatusLive, FetchedAt: &old},
		{Status: marketquality.StatusCached, FetchedAt: &future},
	} {
		repository := &quoteViewRepository{snapshot: &store.MarketSnapshot{QuoteQuality: quality}}
		repository.project = store.MarketProject{ID: 1, ChainID: 56, TokenAddress: testToken}
		service := New(Dependencies{Repository: repository})
		result, err := service.RecommendationsWithQuote(context.Background(), "fixture", 1)
		if !errors.Is(err, ErrQuoteUnavailable) || len(result.Recommendations) != 0 || repository.eventReads != 0 || repository.sampleReads != 0 {
			t.Fatalf("quality=%+v result=%+v err=%v sampleReads=%d", quality, result, err, repository.eventReads)
		}
	}
}

func TestProjectReadKeepsOldQuoteAndAmountsWithoutInventingTime(t *testing.T) {
	old, captured := time.Now().UTC().Add(-time.Hour), time.Now().UTC()
	price := "2"
	repository := &quoteViewRepository{snapshot: &store.MarketSnapshot{PriceUSD: &price, CapturedAt: captured, QuoteQuality: marketquality.Quote{Status: marketquality.StatusStaleFallback, FetchedAt: &old}}}
	repository.project = store.MarketProject{ID: 1, ChainID: 56, ChainKey: "bsc", TokenAddress: testToken, Status: "active"}
	service := New(Dependencies{Repository: repository})
	result, err := service.Project(context.Background(), "fixture", 1)
	if err != nil || result.LatestSnapshot == nil || *result.LatestSnapshot.PriceUSD != price || !result.LatestSnapshot.QuoteQuality.ObservationTime().Equal(old) || !result.LatestSnapshot.CapturedAt.Equal(captured) {
		t.Fatalf("old display quote=%+v,%v", result.LatestSnapshot, err)
	}
	if result.QuoteMaxAgeSeconds != int64(marketquality.MaxAge/time.Second) {
		t.Fatal("view invented a different quote age policy")
	}
}

func TestPreviewRecommendationsRejectUnknownFallbackAndExpiredPairs(t *testing.T) {
	old, now := time.Now().UTC().Add(-time.Hour), time.Now().UTC()
	for _, quality := range []marketquality.Quote{{}, {Status: marketquality.StatusStaleFallback, FetchedAt: &now}, {Status: marketquality.StatusCached, FetchedAt: &old}} {
		p := pair(testPairA, "pancakeswap", []string{"v3"}, "100000")
		p.PriceUSD, p.QuoteQuality = "2", quality
		service := New(Dependencies{Entitlements: fakeEntitlements{plan: plans.Plan{MarketQuery: true}}, Market: fakeMarket{pairs: []marketdata.Pair{p}}})
		result, err := service.PreviewRecommendationsWithQuote(context.Background(), "fixture", RecommendationPreviewInput{Deployments: []RecommendationPreviewDeployment{{ChainKey: "bsc", TokenAddress: testToken, PoolAddresses: []string{testPairA}}}})
		if !errors.Is(err, ErrQuoteUnavailable) || len(result.Recommendations) != 0 {
			t.Fatalf("quality=%+v result=%+v,%v", quality, result, err)
		}
	}
}

func TestRecommendationSnapshotRetainsObservationInsteadOfCaptureTime(t *testing.T) {
	old := time.Now().UTC().Add(-time.Minute)
	p := pair(testPairA, "pancakeswap", nil, "100")
	p.QuoteQuality = marketquality.Quote{Status: marketquality.StatusCached, FetchedAt: &old}
	profile, err := chain.ChainProfile("bsc", "")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := recommendationSnapshotFromPair(p, testToken, profile)
	if !snapshot.QuoteQuality.FetchedAt.Equal(old) || !snapshot.CapturedAt.After(old) || snapshot.SourceTimestamp != nil {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	*snapshot.QuoteQuality.FetchedAt = time.Time{}
	if p.QuoteQuality.FetchedAt.IsZero() {
		t.Fatal("snapshot shares mutable provider evidence")
	}
}

func TestRecommendationsKeepRepositoryFailureDistinct(t *testing.T) {
	failure := errors.New("fixture read failure")
	repository := &quoteViewRepository{snapshotErr: failure}
	service := New(Dependencies{Repository: repository})
	_, err := service.RecommendationsWithQuote(context.Background(), "fixture", 1)
	if !errors.Is(err, failure) || errors.Is(err, ErrQuoteUnavailable) {
		t.Fatalf("repository error changed=%v", err)
	}
}

func TestRecommendationsWithQuoteReturnExactUsableEvidence(t *testing.T) {
	fetched := time.Now().UTC().Add(-30 * time.Second)
	price, liquidity := "2", "100000"
	repository := &quoteViewRepository{snapshot: &store.MarketSnapshot{
		PriceUSD: &price, LiquidityUSD: &liquidity,
		QuoteQuality: marketquality.Quote{Status: marketquality.StatusCached, FetchedAt: &fetched},
	}}
	service := New(Dependencies{Repository: repository})
	result, err := service.RecommendationsWithQuote(context.Background(), "fixture", 1)
	if err != nil || len(result.Recommendations) != 63 || result.QuoteQuality.Status != marketquality.StatusCached || result.QuoteQuality.FetchedAt == nil || !result.QuoteQuality.FetchedAt.Equal(fetched) || result.QuoteQuality.SourceTimestamp != nil || result.QuoteMaxAgeSeconds != int64(marketquality.MaxAge/time.Second) || !result.GeneratedAt.After(fetched) || repository.eventReads != 0 || repository.sampleReads != 1 {
		t.Fatalf("result=%+v err=%v reads=%d", result, err, repository.eventReads)
	}
	if repository.sampleTo.Sub(repository.sampleFrom) != 24*time.Hour || repository.sampleLimit != 100 || repository.sampleReference.ChainID != repository.snapshot.ChainID || repository.sampleReference.TokenAddress != repository.snapshot.TokenAddress {
		t.Fatalf("recommendation scope contract=%+v", repository)
	}
	*result.QuoteQuality.FetchedAt = time.Time{}
	if repository.snapshot.QuoteQuality.FetchedAt.IsZero() {
		t.Fatal("result changed persisted quote evidence")
	}
}

func TestPreviewRecommendationsChooseUsableSelectedPoolAndAllowRealZeroLiquidity(t *testing.T) {
	fetched := time.Now().UTC().Add(-10 * time.Second)
	stale := pair(testPairA, "pancakeswap", nil, "1000000")
	stale.PriceUSD = "3"
	stale.QuoteQuality = marketquality.Quote{Status: marketquality.StatusStaleFallback, FetchedAt: &fetched}
	usable := pair(testPairB, "pancakeswap", nil, "0")
	usable.PriceUSD = "2"
	usable.QuoteQuality = marketquality.Quote{Status: marketquality.StatusCached, FetchedAt: &fetched}
	service := New(Dependencies{Entitlements: fakeEntitlements{plan: plans.Plan{MarketQuery: true}}, Market: fakeMarket{pairs: []marketdata.Pair{stale, usable}}})
	result, err := service.PreviewRecommendationsWithQuote(context.Background(), "fixture", RecommendationPreviewInput{Deployments: []RecommendationPreviewDeployment{{ChainKey: "bsc", TokenAddress: testToken, PoolAddresses: []string{testPairA, testPairB}}}})
	if err != nil || len(result.Recommendations) != 63 || !result.QuoteQuality.FetchedAt.Equal(fetched) {
		t.Fatalf("usable zero-liquidity selected pool=%+v,%v", result, err)
	}
	for _, r := range result.Recommendations {
		if r.RuleType == plans.MarketPriceAbove && r.Sensitivity == "sensitive" && r.Threshold != "2.04" {
			t.Fatalf("wrong source or changed formula=%+v", r)
		}
	}
}

func TestPreviewRecommendationsRejectMissingOrNegativeRequiredAmounts(t *testing.T) {
	fetched := time.Now().UTC()
	for _, amounts := range [][2]string{{"", "10"}, {"0", "10"}, {"-1", "10"}, {"2", ""}, {"2", "-1"}} {
		p := pair(testPairA, "pancakeswap", nil, amounts[1])
		p.PriceUSD = marketdata.Decimal(amounts[0])
		p.QuoteQuality = marketquality.Quote{Status: marketquality.StatusLive, FetchedAt: &fetched}
		service := New(Dependencies{Entitlements: fakeEntitlements{plan: plans.Plan{MarketQuery: true}}, Market: fakeMarket{pairs: []marketdata.Pair{p}}})
		_, err := service.PreviewRecommendationsWithQuote(context.Background(), "fixture", RecommendationPreviewInput{Deployments: []RecommendationPreviewDeployment{{ChainKey: "bsc", TokenAddress: testToken, PoolAddresses: []string{testPairA}}}})
		if !errors.Is(err, ErrQuoteUnavailable) {
			t.Fatalf("amounts=%v err=%v", amounts, err)
		}
	}
}

func TestRecommendationSnapshotDistinguishesMissingCountsFromRealZero(t *testing.T) {
	profile, _ := chain.ChainProfile("bsc", "")
	p := pair(testPairA, "pancakeswap", nil, "0")
	p.Transactions = map[string]marketdata.TransactionCounts{"m5": {}}
	snapshot := recommendationSnapshotFromPair(p, testToken, profile)
	if snapshot.Buys5m == nil || snapshot.Sells5m == nil || *snapshot.Buys5m != 0 || *snapshot.Sells5m != 0 || snapshot.Buys1h != nil || snapshot.Sells1h != nil || snapshot.Buys24h != nil || snapshot.Sells24h != nil {
		t.Fatalf("count presence was lost=%+v", snapshot)
	}
}

func TestProjectPoolLiquidityNeedsFieldEvidenceAndKeepsFrozenValues(t *testing.T) {
	for _, test := range []struct {
		name, metadata, stored, want string
		frozen                       bool
	}{
		{"missing", `{}`, "0", "", false},
		{"null", `{"liquidity":{"usd":null}}`, "0", "", false},
		{"real zero", `{"liquidity":{"usd":"0"}}`, "0", "0", false},
		{"real amount", `{"liquidity":{"usd":"123"}}`, "123", "123", false},
		{"frozen missing ignores current", `{"liquidity":{"usd":"999"}}`, "", "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &quoteViewRepository{pools: []store.MarketPoolView{{MarketPool: store.MarketPool{LiquidityUSD: test.stored, Metadata: []byte(test.metadata)}}}}
			repository.project = store.MarketProject{ID: 1, ChainID: 56, TokenAddress: testToken}
			if test.frozen {
				frozen := time.Now().UTC()
				repository.project.FrozenAt = &frozen
			}
			result, err := New(Dependencies{Repository: repository}).Project(context.Background(), "fixture", 1)
			if err != nil || len(result.Pools) != 1 || result.Pools[0].LiquidityUSD != test.want {
				t.Fatalf("pools=%+v,%v", result.Pools, err)
			}
		})
	}
}

func TestProjectPoolSnapshotEvidenceOverridesLegacyPoolMetadata(t *testing.T) {
	snapshotID := int64(42)
	fetched := time.Now().UTC().Add(-time.Minute)
	for _, test := range []struct{ name, metadata, amount string }{
		{"snapshot amount with missing legacy field", `{}`, "123"},
		{"snapshot zero with null legacy field", `{"liquidity":{"usd":null}}`, "0"},
		{"snapshot missing never falls back to current pool", `{"liquidity":{"usd":"999"},"quote_quality":{"status":"live"}}`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &quoteViewRepository{pools: []store.MarketPoolView{{MarketPool: store.MarketPool{
				LiquidityUSD: test.amount, Metadata: []byte(test.metadata), QuoteSnapshotID: &snapshotID,
				QuoteQuality: marketquality.Quote{Status: marketquality.StatusStaleFallback, FetchedAt: &fetched},
			}}}}
			repository.project = store.MarketProject{ID: 1, ChainID: 56, TokenAddress: testToken, Status: "active"}
			result, err := New(Dependencies{Repository: repository}).Project(context.Background(), "fixture", 1)
			if err != nil || len(result.Pools) != 1 || result.Pools[0].LiquidityUSD != test.amount || result.Pools[0].QuoteSnapshotID == nil || *result.Pools[0].QuoteSnapshotID != snapshotID || result.Pools[0].QuoteQuality.Status != marketquality.StatusStaleFallback || !result.Pools[0].QuoteQuality.FetchedAt.Equal(fetched) {
				t.Fatalf("snapshot evidence=%+v,%v", result.Pools, err)
			}
		})
	}
}
