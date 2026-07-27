package assetcatalog

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
)

type fakePrimary struct {
	candidates []Candidate
	candidate  *Candidate
	err        error
}

func (f fakePrimary) Search(
	context.Context,
	string,
	int,
) ([]Candidate, error) {
	return append([]Candidate(nil), f.candidates...), f.err
}

func (f fakePrimary) ResolveContract(
	context.Context,
	string,
	string,
) (*Candidate, error) {
	if f.candidate == nil {
		return nil, f.err
	}
	copy := *f.candidate
	return &copy, f.err
}

type fakePairSearcher struct {
	pairs []marketdata.Pair
	err   error
}

func (f fakePairSearcher) SearchPairs(
	context.Context,
	string,
) ([]marketdata.Pair, error) {
	return append([]marketdata.Pair(nil), f.pairs...), f.err
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestCatalogUsesSingleChainDexScreenerFallback(t *testing.T) {
	catalog, err := NewCatalog(
		fakePrimary{err: ErrUnavailable},
		fakePairSearcher{pairs: []marketdata.Pair{
			{
				ChainID: "bsc",
				BaseToken: marketdata.Token{
					Address: testAddress(1), Name: "Project Coin", Symbol: "PRJ",
				},
				QuoteToken: marketdata.Token{
					Address: testAddress(2), Name: "Wrapped BNB", Symbol: "WBNB",
				},
				Liquidity: marketdata.Liquidity{USD: "25000"},
			},
			{
				ChainID: "ethereum",
				BaseToken: marketdata.Token{
					Address: testAddress(3), Name: "Project Coin", Symbol: "PRJ",
				},
				QuoteToken: marketdata.Token{
					Address: testAddress(4), Name: "Tether", Symbol: "USDT",
				},
				Liquidity: marketdata.Liquidity{USD: "10000"},
			},
		}},
		nil,
	)
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	result, err := catalog.Search(context.Background(), "PRJ", 10)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if !result.Degraded || result.Source != SourceDexScreener ||
		len(result.Candidates) != 2 {
		t.Fatalf("fallback result = %#v", result)
	}
	for _, candidate := range result.Candidates {
		if candidate.IdentityStatus != IdentitySingleChain ||
			len(candidate.Deployments) != 1 {
			t.Fatalf("fallback candidate can be merged: %#v", candidate)
		}
	}
	if result.Candidates[0].Deployments[0].ChainKey != "bsc" {
		t.Fatalf("liquidity order = %#v", result.Candidates)
	}
}

func TestCatalogDoesNotHideTwoProviderFailures(t *testing.T) {
	catalog, err := NewCatalog(
		fakePrimary{err: ErrUnavailable},
		fakePairSearcher{err: errors.New("rate limited")},
		nil,
	)
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	if _, err := catalog.Search(
		context.Background(), "coin", 10,
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Search() error = %v", err)
	}
}

func TestLogoProxyValidatesSourceTypeAndCaches(t *testing.T) {
	var calls atomic.Int64
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	}
	catalog, err := NewCatalog(
		fakePrimary{},
		nil,
		doerFunc(func(request *http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/octet-stream"},
				},
				Body: io.NopCloser(bytes.NewReader(png)),
			}, nil
		}),
	)
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	source := "https://coin-images.coingecko.com/coins/project.png"
	first, err := catalog.Logo(context.Background(), source)
	if err != nil {
		t.Fatalf("Logo() error = %v", err)
	}
	second, err := catalog.Logo(context.Background(), source)
	if err != nil {
		t.Fatalf("cached Logo() error = %v", err)
	}
	if first.ContentType != "image/png" ||
		first.ETag == "" ||
		!bytes.Equal(first.Body, second.Body) ||
		calls.Load() != 1 {
		t.Fatalf(
			"logos/calls = %#v/%#v/%d", first, second, calls.Load(),
		)
	}
	if _, err := catalog.Logo(
		context.Background(),
		"https://127.0.0.1/private.png",
	); !errors.Is(err, ErrInvalidLogo) {
		t.Fatalf("private logo error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("invalid source reached upstream")
	}
}

func TestCatalogReturnsLocalLogoURLOnly(t *testing.T) {
	catalog, err := NewCatalog(
		fakePrimary{candidates: []Candidate{{
			CanonicalAssetID: "project",
			Name:             "Project",
			Symbol:           "PRJ",
			IdentitySource:   SourceCoinGecko,
			remoteLogoURL:    "https://coin-images.coingecko.com/project.png",
		}}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	result, err := catalog.Search(context.Background(), "project", 1)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Candidates) != 1 ||
		result.Candidates[0].LogoURL !=
			"/api/market/assets/logo?source=https%3A%2F%2Fcoin-images.coingecko.com%2Fproject.png" {
		t.Fatalf("candidate = %#v", result.Candidates)
	}
}
