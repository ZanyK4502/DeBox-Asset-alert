package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestPostgresPoolDecimalsRequireVerifiedMetadata(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	params := UpsertMarketPoolParams{
		ChainKey: "bsc", ChainID: 56, Protocol: "pancakeswap", ProtocolVersion: "v3",
		PoolKey: "precision-pool", Token0Address: "0x1111111111111111111111111111111111111111",
		Token1Address:  "0x2222222222222222222222222222222222222222",
		Token0Decimals: 0, Token1Decimals: 6, SupportsEventParsing: true,
	}
	if _, err := database.UpsertMarketPool(ctx, params); !errors.Is(err, ErrInvalidMarketPool) {
		t.Fatalf("unknown new pool error = %v", err)
	}
	params.Token0DecimalsVerified, params.Token1DecimalsVerified = true, true
	created, err := database.UpsertMarketPool(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	params.Token0Decimals, params.Token1Decimals = 18, 18
	params.Token0DecimalsVerified, params.Token1DecimalsVerified = false, false
	updated, err := database.UpsertMarketPool(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Token0Decimals != 0 || updated.Token1Decimals != 6 {
		t.Fatalf("known precision overwritten: %+v", updated)
	}
	var firstVerified, secondVerified bool
	if err := pool.QueryRow(ctx, `SELECT token0_decimals_verified, token1_decimals_verified FROM market_pools WHERE id=$1`, created.ID).Scan(&firstVerified, &secondVerified); err != nil {
		t.Fatal(err)
	}
	if !firstVerified || !secondVerified {
		t.Fatal("verified provenance was cleared")
	}
	params.Token0Address = "0x3333333333333333333333333333333333333333"
	if _, err := database.UpsertMarketPool(ctx, params); !errors.Is(err, ErrInvalidMarketPool) {
		t.Fatalf("unverified token replacement error = %v", err)
	}
	params.Token0Address = created.Token0Address
	params.Token0Decimals, params.Token0DecimalsVerified = 8, true
	updated, err = database.UpsertMarketPool(ctx, params)
	if err != nil || updated.Token0Decimals != 8 || updated.Token1Decimals != 6 {
		t.Fatalf("partial verified update = %+v, %v", updated, err)
	}
}

const fourMemeFixtureToken = "0x7777777777777777777777777777777777777777"

func fourMemeFixtureProject(t *testing.T, database *Store, user string) MarketProject {
	t.Helper()
	project, err := createMarketProject(context.Background(), database.db, CreateMarketProjectParams{
		DeBoxUserID: user, ChainKey: "bsc", ChainID: 56, TokenAddress: fourMemeFixtureToken,
		TokenName: "Fixture", TokenSymbol: "FIX", TokenDecimals: 18, FourMemeStatus: "unknown",
	})
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func fourMemeFixtureEvent(t *testing.T, database *Store, block int64, transaction, log int32, eventType string, occurred time.Time) MarketEvent {
	t.Helper()
	hash := fmt.Sprintf("0x%064x", block)
	tx := fmt.Sprintf("0x%064x", block*1000+int64(transaction)*100+int64(log))
	key := fmt.Sprintf("four-meme-%d-%d-%d", block, transaction, log)
	event, _, err := database.CreateMarketEvent(context.Background(), CreateMarketEventParams{
		ChainKey: "bsc", ChainID: 56, TokenAddress: fourMemeFixtureToken, EventType: eventType, EventKey: key,
		TransactionHash: &tx, BlockHash: &hash, BlockNumber: &block, TransactionIndex: &transaction, LogIndex: &log,
		Metadata: []byte(`{"adapter":"four_meme_v2"}`), OccurredAt: occurred,
		Source: "rpc_fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func TestPostgresFourMemeStateUsesStoredChainOrderAndReorgRecovery(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	project := fourMemeFixtureProject(t, database, "phase-active")
	archived := fourMemeFixtureProject(t, database, "phase-archived")
	if _, err := pool.Exec(ctx, `UPDATE market_projects SET status='archived',four_meme_status='migrated' WHERE id=$1`, archived.ID); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	stopped := fourMemeFixtureEvent(t, database, 102, 1, 12, "trading_stopped", base)
	oldBuy := fourMemeFixtureEvent(t, database, 100, 0, 1, "buy", base.Add(time.Minute))
	// The first callback is stale. Bootstrap must discover the newer event
	// already stored for this token, rather than trusting that callback.
	if _, err := database.UpdateMarketFourMemeFromEvent(ctx, oldBuy.ID); err != nil {
		t.Fatal(err)
	}
	assertState := func(want string, source int64) {
		t.Helper()
		var status string
		var sourceID int64
		if err := pool.QueryRow(ctx, `SELECT mp.four_meme_status,s.source_event_id FROM market_projects mp JOIN market_four_meme_states s ON s.chain_id=mp.chain_id AND s.token_address=mp.token_address WHERE mp.id=$1`, project.ID).Scan(&status, &sourceID); err != nil {
			t.Fatal(err)
		}
		if status != want || sourceID != source {
			t.Fatalf("state=%s source=%d, want %s/%d", status, sourceID, want, source)
		}
	}
	assertState("graduating", stopped.ID)
	migrated := fourMemeFixtureEvent(t, database, 102, 2, 20, "migrated", base.Add(-time.Minute))
	if _, err := database.UpdateMarketFourMemeFromEvent(ctx, migrated.ID); err != nil {
		t.Fatal(err)
	}
	assertState("migrated", migrated.ID)
	var before, after time.Time
	if err := pool.QueryRow(ctx, `SELECT updated_at FROM market_four_meme_states WHERE source_event_id=$1`, migrated.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateMarketFourMemeFromEvent(ctx, oldBuy.ID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT updated_at FROM market_four_meme_states WHERE source_event_id=$1`, migrated.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !before.Equal(after) {
		t.Fatal("duplicate/stale event rewrote the source checkpoint")
	}
	assertState("migrated", migrated.ID)
	// The same helper must obey the caller's transaction and rollback.
	future := fourMemeFixtureEvent(t, database, 103, 0, 0, "buy", base)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := updateMarketFourMemeFromEvent(ctx, tx, future.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	assertState("migrated", migrated.ID)
	if _, err := pool.Exec(ctx, `UPDATE market_events SET reorged=1,confirmed=0 WHERE id IN ($1,$2)`, migrated.ID, future.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ReconcileMarketFourMemeState(ctx, 56, fourMemeFixtureToken); err != nil {
		t.Fatal(err)
	}
	assertState("graduating", stopped.ID)
	if _, err := pool.Exec(ctx, `UPDATE market_events SET reorged=1,confirmed=0 WHERE id=$1`, stopped.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ReconcileMarketFourMemeState(ctx, 56, fourMemeFixtureToken); err != nil {
		t.Fatal(err)
	}
	assertState("bonding", oldBuy.ID)
	if _, err := pool.Exec(ctx, `UPDATE market_events SET reorged=1,confirmed=0 WHERE id=$1`, oldBuy.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ReconcileMarketFourMemeState(ctx, 56, fourMemeFixtureToken); err != nil {
		t.Fatal(err)
	}
	var status string
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT four_meme_status FROM market_projects WHERE id=$1`, project.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM market_four_meme_states`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if status != "unknown" || remaining != 0 {
		t.Fatalf("empty effective history = %s, checkpoints=%d", status, remaining)
	}
	if err := pool.QueryRow(ctx, `SELECT four_meme_status FROM market_projects WHERE id=$1`, archived.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "migrated" {
		t.Fatal("archived display was changed")
	}
}

func TestPostgresFourMemeConcurrentCallbacksCannotRegress(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	fourMemeFixtureProject(t, database, "phase-concurrent")
	events := []MarketEvent{
		fourMemeFixtureEvent(t, database, 100, 0, 0, "buy", time.Now().UTC()),
		fourMemeFixtureEvent(t, database, 101, 0, 0, "trading_stopped", time.Now().UTC()),
		fourMemeFixtureEvent(t, database, 102, 0, 0, "migrated", time.Now().UTC()),
	}
	var wg sync.WaitGroup
	failures := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := database.UpdateMarketFourMemeFromEvent(ctx, events[index%len(events)].ID)
			if err != nil {
				failures <- err
			}
		}(i)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var source int64
	var status string
	if err := pool.QueryRow(ctx, `SELECT source_event_id,status FROM market_four_meme_states WHERE chain_id=56 AND token_address=$1`, fourMemeFixtureToken).Scan(&source, &status); err != nil {
		t.Fatal(err)
	}
	if source != events[2].ID || status != "migrated" {
		t.Fatalf("concurrent state=%d/%s", source, status)
	}
}

func TestPostgresCleanupKeepsOnlyCurrentFourMemeSource(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-40 * 24 * time.Hour)
	previous := fourMemeFixtureEvent(t, database, 100, 0, 0, "buy", old)
	current := fourMemeFixtureEvent(t, database, 101, 0, 0, "migrated", old)
	if _, err := database.UpdateMarketFourMemeFromEvent(ctx, current.ID); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)
	if _, err := database.CleanupMarketCollectionData(ctx, cutoff, cutoff, cutoff); err != nil {
		t.Fatal(err)
	}
	var currentExists, previousExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM market_events WHERE id=$1),EXISTS(SELECT 1 FROM market_events WHERE id=$2)`, current.ID, previous.ID).Scan(&currentExists, &previousExists); err != nil {
		t.Fatal(err)
	}
	if !currentExists || previousExists {
		t.Fatalf("cleanup source=%v previous=%v", currentExists, previousExists)
	}
}
