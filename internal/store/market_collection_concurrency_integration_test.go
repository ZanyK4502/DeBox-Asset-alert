package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPostgresCollectionActivationBeforeFirstIdlePoll(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	project, _ := collectionLifecycleProject(t, db, "initial-activation")
	state := collectionLifecycleObserve(t, db)
	marker := state.PendingIdle
	if state.Phase != "active" || marker == nil || marker.ThroughAt == nil || !marker.FromAt.Equal(*marker.ThroughAt) {
		t.Fatalf("first activation needs a timestamp without any certified idle duration: %+v", state)
	}

	// Both statements commit together. The scanner never saw an inactive
	// project, so this must not manufacture a second activation or gap.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for _, status := range []string{"paused", "active"} {
		if _, err := tx.Exec(ctx, "UPDATE market_projects SET status=$2 WHERE id=$1", project, status); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	after := collectionLifecycleObserve(t, db)
	if after.Revision != state.Revision || after.ActiveTargetCount != 1 || after.PendingIdle == nil || after.PendingIdle.ID != marker.ID {
		t.Fatalf("an invisible pause changed the activation boundary: %+v", after)
	}
}

func TestPostgresCollectionConcurrentReverseOrderActivations(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	type pair struct{ bsc, eth int64 }
	var links [2]pair
	for i := range links {
		project, bsc := collectionLifecycleProject(t, db, fmt.Sprintf("two-chain-owner-%d", i))
		links[i].bsc = bsc
		if _, err := pool.Exec(ctx, "UPDATE market_project_deployments SET status='paused' WHERE id=$1", bsc); err != nil {
			t.Fatal(err)
		}
		err := pool.QueryRow(ctx, `
			WITH deployment AS (
				INSERT INTO market_asset_deployments(market_asset_id,chain_key,chain_id,token_address)
				SELECT market_asset_id,'ethereum',1,token_address FROM market_projects WHERE id=$1
				RETURNING id,market_asset_id
			)
			INSERT INTO market_project_deployments(market_project_id,market_asset_id,market_asset_deployment_id,status)
			SELECT $1,market_asset_id,id,'paused' FROM deployment RETURNING id`, project).Scan(&links[i].eth)
		if err != nil {
			t.Fatal(err)
		}
	}
	chains := []struct {
		key string
		id  int64
	}{{"ethereum", 1}, {"bsc", 56}}
	for _, chain := range chains {
		state, err := db.ObserveMarketChainCollection(ctx, chain.key, chain.id)
		if err != nil || state.Phase != "idle" {
			t.Fatal("fixture has nonzero demand", chain.key, state, err)
		}
	}
	var txs [2]pgx.Tx
	for i := range txs {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		txs[i] = tx
		defer tx.Rollback(context.Background())
	}
	// Each writer touches its own rows and both chains. The first statements
	// deliberately acquire opposing chains before either transaction commits.
	for _, op := range []struct {
		owner int
		id    int64
	}{
		{0, links[0].bsc}, {1, links[1].eth}, {0, links[0].eth}, {1, links[1].bsc},
	} {
		if _, err := txs[op.owner].Exec(ctx, "UPDATE market_project_deployments SET status='active' WHERE id=$1", op.id); err != nil {
			t.Fatal("opposing chain order blocked a project edit", err)
		}
	}
	committed := make(chan error, len(txs))
	for _, tx := range txs {
		go func(tx pgx.Tx) { committed <- tx.Commit(ctx) }(tx)
	}
	for range txs {
		if err := <-committed; err != nil {
			t.Error("concurrent commit", err)
		}
	}
	for _, chain := range chains {
		state, err := db.ObserveMarketChainCollection(ctx, chain.key, chain.id)
		if err != nil || state.ActiveTargetCount != 2 || state.Phase != "active" {
			t.Fatalf("%s lost a committed activation: %+v, %v", chain.key, state, err)
		}
	}
}
