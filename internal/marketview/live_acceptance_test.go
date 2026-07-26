package marketview

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
)

const liveCakeAddress = "0x0e09fabb73bd3ade0a17ecc321fd13a19e81ce82"

// TestLiveBNBMarketAcceptance is opt-in because it calls public external
// services. It is read-only: no transaction, webhook, rule, or notification is
// created.
func TestLiveBNBMarketAcceptance(t *testing.T) {
	if os.Getenv("RUN_LIVE_MARKET_ACCEPTANCE") != "1" {
		t.Skip("set RUN_LIVE_MARKET_ACCEPTANCE=1 for read-only live validation")
	}
	noditKey := strings.TrimSpace(os.Getenv("NODIT_API_KEY"))
	fourMemeAddress := strings.TrimSpace(os.Getenv("LIVE_FOUR_MEME_TOKEN"))
	if noditKey == "" || fourMemeAddress == "" {
		t.Fatal("NODIT_API_KEY and LIVE_FOUR_MEME_TOKEN are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	chainClient, err := chain.NewClient(noditKey, strings.TrimSpace(os.Getenv("NODIT_BASE_URL")))
	if err != nil {
		t.Fatalf("create Nodit client: %v", err)
	}
	dexClient, err := marketdata.NewDexScreenerClient("")
	if err != nil {
		t.Fatalf("create DexScreener client: %v", err)
	}

	service := New(Dependencies{
		Entitlements: fakeEntitlements{plan: plans.Plan{MarketQuery: true}},
		Chain:        chainClient,
		Market:       dexClient,
	})
	cakeQuery, err := service.QueryToken(ctx, "live-acceptance", TokenQueryInput{
		ChainKey: "bsc", TokenAddress: liveCakeAddress,
	})
	if err != nil {
		t.Fatalf("query CAKE through market service: %v", err)
	}
	cake := cakeQuery.Token
	if !strings.EqualFold(cake.Symbol, "CAKE") || cake.Decimals <= 0 ||
		cake.TotalSupplyRaw == "" {
		t.Fatalf("unexpected CAKE metadata: %#v", cake)
	}
	fourMemeQuery, err := service.QueryToken(
		ctx,
		"live-acceptance",
		TokenQueryInput{ChainKey: "bsc", TokenAddress: fourMemeAddress},
	)
	if err != nil {
		t.Fatalf("query Four.meme token through market service: %v", err)
	}
	fourMeme := fourMemeQuery.Token
	if strings.TrimSpace(fourMeme.Symbol) == "" || fourMeme.Decimals <= 0 ||
		fourMeme.TotalSupplyRaw == "" {
		t.Fatalf("unexpected Four.meme metadata: %#v", fourMeme)
	}

	for label, address := range map[string]string{
		"CAKE":      liveCakeAddress,
		"Four.meme": fourMemeAddress,
	} {
		holders, err := chainClient.TokenHoldersByContract(
			ctx,
			address,
			"bsc",
			"bsc",
			chain.TokenHoldersOptions{RPP: 3, WithCount: true},
		)
		if err != nil {
			t.Fatalf("read %s holders from Nodit: %v", label, err)
		}
		if len(holders.Items) == 0 {
			t.Fatalf("%s holder response is empty", label)
		}
	}

	var hasV2, hasV3 bool
	for _, pool := range cakeQuery.Pools {
		if !pool.Supported {
			continue
		}
		hasV2 = hasV2 || pool.ParserAdapter == marketparse.AdapterV2
		hasV3 = hasV3 || pool.ParserAdapter == marketparse.AdapterV3
	}
	if !hasV2 || !hasV3 {
		t.Fatalf("CAKE supported pools missing: v2=%v v3=%v", hasV2, hasV3)
	}
}
