package marketprotocol

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
)

const (
	testToken = "0x1111111111111111111111111111111111111111"
	testQuote = "0x2222222222222222222222222222222222222222"
	testPool  = "0x3333333333333333333333333333333333333333"
)

type fakePoolReader struct {
	token0  string
	token1  string
	factory string
	err     error
}

func (f fakePoolReader) PoolTokens(
	context.Context,
	string,
	string,
	string,
) (string, string, error) {
	if f.err != nil {
		return "", "", f.err
	}
	return f.token0, f.token1, nil
}

func (f fakePoolReader) PoolFactory(
	context.Context,
	string,
	string,
	string,
) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.factory, nil
}

func TestTrustedDeploymentsCoverEverySupportedChain(t *testing.T) {
	t.Parallel()
	for _, profile := range chain.SupportedChains() {
		profile := profile
		t.Run(profile.Key, func(t *testing.T) {
			t.Parallel()
			values := Deployments(profile.Key)
			if len(values) == 0 {
				t.Fatalf("%s has no trusted deployments", profile.Key)
			}
			seen := make(map[string]struct{}, len(values))
			for _, value := range values {
				if value.ChainKey != profile.Key ||
					value.ChainID != profile.ChainID {
					t.Fatalf("deployment on wrong chain: %#v", value)
				}
				key := value.Factory + "|" + value.Adapter
				if _, duplicate := seen[key]; duplicate {
					t.Fatalf("duplicate deployment: %s", key)
				}
				seen[key] = struct{}{}
				if _, err := chain.ValidateAddress(value.Factory); err != nil {
					t.Fatalf("invalid factory %q: %v", value.Factory, err)
				}
				lookup, ok := Lookup(profile.Key, value.Factory)
				if !ok || lookup != value {
					t.Fatalf("Lookup() = %#v/%v, want %#v", lookup, ok, value)
				}
			}
		})
	}
}

func TestVerifyPairUsesOnchainFactoryAndTokenComposition(t *testing.T) {
	t.Parallel()
	for _, profile := range chain.SupportedChains() {
		deployment := Deployments(profile.Key)[0]
		classification := VerifyPair(
			context.Background(),
			fakePoolReader{
				token0: testToken, token1: testQuote,
				factory: deployment.Factory,
			},
			profile.Key,
			testToken,
			marketdata.Pair{
				ChainID: profile.Key, PairAddress: testPool,
				DexID: "untrusted-provider-label",
			},
		)
		if !classification.Supported ||
			!classification.FactoryVerified ||
			classification.MonitoringLevel != MonitoringFull ||
			classification.Protocol != deployment.Protocol ||
			classification.ParserAdapter != deployment.Adapter ||
			len(classification.SupportedFeature) != len(FullMonitoringFeatures) {
			t.Fatalf("%s classification = %#v", profile.Key, classification)
		}
	}
}

func TestVerifyPairFailsClosed(t *testing.T) {
	t.Parallel()
	trustedFactory := Deployments("bsc")[0].Factory
	tests := []struct {
		name      string
		chainKey  string
		pairChain string
		reader    fakePoolReader
		reason    string
	}{
		{
			name: "cross chain", chainKey: "bsc", pairChain: "ethereum",
			reader: fakePoolReader{
				token0: testToken, token1: testQuote, factory: trustedFactory,
			},
			reason: "跨链",
		},
		{
			name: "project token missing", chainKey: "bsc", pairChain: "bsc",
			reader: fakePoolReader{
				token0:  testQuote,
				token1:  "0x4444444444444444444444444444444444444444",
				factory: trustedFactory,
			},
			reason: "不包含",
		},
		{
			name: "untrusted factory", chainKey: "bsc", pairChain: "bsc",
			reader: fakePoolReader{
				token0: testToken, token1: testQuote,
				factory: "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
			reason: "可信协议名单",
		},
		{
			name: "rpc failure", chainKey: "bsc", pairChain: "bsc",
			reader: fakePoolReader{err: errors.New("rpc unavailable")},
			reason: "无法读取",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			classification := VerifyPair(
				context.Background(),
				test.reader,
				test.chainKey,
				testToken,
				marketdata.Pair{
					ChainID:     test.pairChain,
					PairAddress: testPool,
				},
			)
			if classification.Supported ||
				classification.FactoryVerified ||
				classification.MonitoringLevel != MonitoringQuoteOnly ||
				!strings.Contains(classification.UnsupportedReason, test.reason) {
				t.Fatalf("classification = %#v", classification)
			}
		})
	}
}
