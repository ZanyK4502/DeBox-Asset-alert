package marketcollector

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

// All six chains intentionally reuse a delivery ID, transaction hash, contract,
// pool address and block hash. Only the chain distinguishes their identities.
func TestPostgresSixChainWebhookAndScanShareOneReceiptPerChain(t *testing.T) {
	f := newScannerPGFixture(t, 1)
	ctx := context.Background()
	profiles := chain.SupportedChains()
	if len(profiles) != 6 {
		t.Fatalf("expected six supported chains, got %d", len(profiles))
	}
	poolAddress := f.chain.logs[0].Address
	var deployments []store.CreateMultiChainMarketDeploymentParams
	var services []*Service
	for _, profile := range profiles {
		services = append(services, New(Dependencies{Repository: f.db, Chain: f.chain}, Settings{
			Enabled: true, ChainKey: profile.Key, ChainID: profile.ChainID,
			ConfirmationDepth: 15, InitialLookback: 1, ScanBatchSize: 1,
			WebhookSigningKeys: map[string]string{profile.Key + ":transfer": "fixture-" + profile.Key},
		}))
		if profile.ChainID == f.project.ChainID {
			continue // newScannerPGFixture already linked its BSC pools.
		}
		pool, err := f.db.UpsertMarketPool(ctx, store.UpsertMarketPoolParams{
			ChainKey: profile.Key, ChainID: profile.ChainID, Protocol: "uniswap", ProtocolVersion: "v2",
			PoolKey: poolAddress, PoolAddress: &poolAddress, Token0Address: initializedQuote,
			Token1Address: initializedToken, Token0DecimalsVerified: true, Token1DecimalsVerified: true,
			SupportsEventParsing: true, ParserAdapter: marketparse.AdapterV2, VerificationStatus: "verified",
		})
		if err != nil {
			t.Fatal(err)
		}
		deployments = append(deployments, store.CreateMultiChainMarketDeploymentParams{
			ChainKey: profile.Key, ChainID: profile.ChainID, TokenAddress: initializedToken, TokenSymbol: "FIX",
			VerificationStatus: "verified", VerificationSource: "fixture",
			Pools: []store.CreateMultiChainMarketPoolParams{{MarketPoolID: pool.ID, Selected: true, IsPrimary: true}},
		})
	}
	_, err := f.db.CreateMultiChainMarketProjectWithinQuota(ctx, store.CreateMultiChainMarketProjectParams{
		DeBoxUserID: scannerFixtureOwner, CanonicalName: "Five local chains", Symbol: "FIX",
		IdentitySource: "local_fixture", CanonicalAssetID: "multi-chain-fixture", VerificationStatus: "verified",
		Deployments: deployments,
	}, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	// Block 100 becomes safe after the projects were enabled. The fake chain
	// supplies the preceding headers used to resolve the first activation.
	at := time.Now().UTC().Truncate(time.Second).Add(4 * time.Second)
	f.chain.blocks[100]["timestamp"] = fmt.Sprintf("0x%x", at.Unix())
	for height := uint64(0); height < 99; height++ {
		hash := fmt.Sprintf("0x%064x", height+20)
		if height == 98 {
			hash = testHashA // The existing fixture's block 99 references this hash.
		}
		f.chain.blocks[height] = map[string]any{
			"number": fmt.Sprintf("0x%x", height), "hash": hash,
			"parentHash": fmt.Sprintf("0x%064x", height+19),
			"timestamp":  fmt.Sprintf("0x%x", at.Unix()-240+int64(height)),
		}
	}
	cluster, err := NewCluster(services, "bsc")
	if err != nil {
		t.Fatal(err)
	}
	hash := f.chain.logs[0].TransactionHash
	body := []byte(fmt.Sprintf(`{"transactionHash":%q}`, hash))
	seen := make(map[int64]bool)
	for _, svc := range cluster.Services() {
		t.Run(svc.settings.ChainKey, func(t *testing.T) {
			key, chainID := svc.settings.ChainKey, svc.settings.ChainID
			headers := map[string][]string{
				"X-Signature":         {signWebhook(body, "fixture-"+key)},
				"X-Nodit-Delivery-ID": {"shared-local-delivery"},
			}
			first, err := cluster.AcceptWebhookForChain(ctx, key, "transfer", headers, body)
			if err != nil || !first.Created || seen[first.InboxID] {
				t.Fatalf("first webhook = %+v, error = %v", first, err)
			}
			seen[first.InboxID] = true
			if err := svc.ProcessInbox(ctx); err != nil {
				t.Fatal(err)
			}
			if pending := f.count(t, "SELECT COUNT(*) FROM market_transaction_receipts WHERE chain_id=$1 AND transaction_hash=$2 AND complete AND NOT confirmed", chainID, hash); pending != 1 {
				t.Fatalf("before scanner verification, complete unconfirmed receipts = %d", pending)
			}
			if err := svc.ScanLogs(ctx); err != nil {
				t.Fatal(err)
			}
			replay, err := cluster.AcceptWebhookForChain(ctx, key, "transfer", headers, body)
			if err != nil || !replay.Duplicate || replay.InboxID != first.InboxID {
				t.Fatalf("webhook replay = %+v, error = %v", replay, err)
			}
			if err := svc.ProcessInbox(ctx); err != nil {
				t.Fatal(err)
			}
			checks := []struct{ name, query string }{
				{"processed inbox", "SELECT COUNT(*) FROM webhook_inbox WHERE chain_id=$1 AND processing_status='processed'"},
				{"confirmed receipt", "SELECT COUNT(*) FROM market_transaction_receipts WHERE chain_id=$1 AND complete AND confirmed AND NOT reorged"},
				{"canonical trade", "SELECT COUNT(*) FROM market_events WHERE chain_id=$1 AND event_type='buy' AND confirmed=1 AND reorged=0"},
				{"published trade", "SELECT COUNT(*) FROM market_trade_ready_events WHERE chain_id=$1 AND valid"},
			}
			for _, check := range checks {
				if n := f.count(t, check.query, chainID); n != 1 {
					t.Errorf("%s rows = %d, want 1", check.name, n)
				}
			}
		})
	}
}
