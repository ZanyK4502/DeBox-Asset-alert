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

func TestPostgresHolderRefreshRejectsSampleOverlappedByAnotherSuccessfulObservation(t *testing.T) {
	database, pool := openTradeLaneIntegrationStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	oldAt, newerAt := now.Add(-2*time.Minute), now.Add(-time.Minute)
	address := fmt.Sprintf("0x%040x", 100)
	rank := int32(1)
	makeParams := func(at time.Time, raw string, expected *time.Time) store.MarketHolderRefreshParams {
		return store.MarketHolderRefreshParams{
			ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFactsTestToken, ObservedAt: at, ExpectedPreviousAt: expected, SourceComplete: true, SourceItemCount: 1, CoveredTopN: 100, ExclusionHash: strings.Repeat("a", 64),
			Ranks: []store.MarketHolderRank{{HolderAddress: address, BalanceRaw: raw, Rank: 1}}, Holders: []store.UpsertMarketHolderParams{{ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFactsTestToken, HolderAddress: address, BalanceRaw: raw, Balance: raw, Rank: &rank, AddressKind: "wallet", Source: "nodit_holders"}}}
	}
	if _, err := database.ApplyMarketHolderRefresh(ctx, makeParams(oldAt, "100", nil)); err != nil {
		t.Fatal(err)
	}
	count := int64(1)
	provider := &holderSampleTestProvider{pages: []chain.TokenHoldersPage{{Count: &count, Items: []chain.TokenHolder{{OwnerAddress: address, BalanceRaw: "100"}}}}}
	provider.onFirst = func() {
		if _, err := database.ApplyMarketHolderRefresh(ctx, makeParams(newerAt, "200", &oldAt)); err != nil {
			t.Fatal(err)
		}
	}
	service := New(Dependencies{Repository: database, Holders: provider}, Settings{})
	service.now = func() time.Time { return now }
	err := service.refreshProjectHolders(ctx, store.MarketProject{ID: 1, DeBoxUserID: "isolated-holder-owner", ChainID: 56, ChainKey: "bsc", TokenAddress: tradeFactsTestToken, TokenDecimals: 0})
	if !errors.Is(err, store.ErrStaleMarketHolderRefresh) {
		t.Fatalf("in-flight older sample accepted: %v", err)
	}
	state, err := database.GetMarketHolderRefreshState(ctx, 56, tradeFactsTestToken)
	if err != nil || state.LastSuccessAt == nil || !state.LastSuccessAt.Equal(newerAt) || state.RankBaseline[0].BalanceRaw != "200" {
		t.Fatalf("failed CAS changed newer baseline: %#v,%v", state, err)
	}
	var raw string
	var snapshots int
	if err := pool.QueryRow(ctx, `SELECT balance_raw::text,(SELECT COUNT(*) FROM market_holder_snapshots) FROM market_holders WHERE holder_address=$1`, address).Scan(&raw, &snapshots); err != nil || raw != "200" || snapshots != 2 {
		t.Fatalf("stale batch partially wrote balance/history: %s,%d,%v", raw, snapshots, err)
	}
}
