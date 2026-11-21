package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func holderRefreshFixture(at time.Time) MarketHolderRefreshParams {
	p := MarketHolderRefreshParams{ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureToken, ObservedAt: at, SourceComplete: true, SourceItemCount: 100, CoveredTopN: 100, ExclusionHash: strings.Repeat("a", 64), Ranks: []MarketHolderRank{}, ExpectedBalances: map[string]string{}}
	for i := 0; i < 100; i++ {
		p.Ranks = append(p.Ranks, MarketHolderRank{HolderAddress: fmt.Sprintf("0x%040x", i+100), BalanceRaw: fmt.Sprint(10000 - i), Rank: int32(i + 1)})
	}
	fillHolderRefreshTracking(&p)
	return p
}

func fillHolderRefreshTracking(p *MarketHolderRefreshParams) {
	p.Holders = nil
	for _, r := range p.Ranks[:min(20, len(p.Ranks))] {
		rank := r.Rank
		p.Holders = append(p.Holders, UpsertMarketHolderParams{ChainKey: p.ChainKey, ChainID: p.ChainID, TokenAddress: p.TokenAddress, HolderAddress: r.HolderAddress, BalanceRaw: r.BalanceRaw, Balance: r.BalanceRaw, Rank: &rank, AddressKind: "wallet", Source: "nodit_holders"})
	}
}

func swapHolderRanks(p *MarketHolderRefreshParams, a, b int) {
	p.Ranks = append([]MarketHolderRank(nil), p.Ranks...)
	p.Ranks[a], p.Ranks[b] = p.Ranks[b], p.Ranks[a]
	for i := range p.Ranks {
		p.Ranks[i].Rank = int32(i + 1)
		p.Ranks[i].BalanceRaw = fmt.Sprint(10000 - i)
	}
	fillHolderRefreshTracking(p)
}

func TestPostgresHolderRefreshCommitsWholeRankObservationAndKeepsBalanceScope(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	p := holderRefreshFixture(at)
	first, err := database.ApplyMarketHolderRefresh(ctx, p)
	if err != nil || !first.InitialBaseline || first.RankEventsCreated != 0 || first.BalanceEventsCreated != 0 || first.SnapshotsCreated != 20 {
		t.Fatalf("initial=%#v,%v", first, err)
	}
	var tracked, events int
	if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM market_holders),(SELECT COUNT(*) FROM market_events)`).Scan(&tracked, &events); err != nil || tracked != 20 || events != 0 {
		t.Fatalf("rank baseline widened tracker: holders=%d events=%d %v", tracked, events, err)
	}
	state, err := database.GetMarketHolderRefreshState(ctx, 56, tradeFixtureToken)
	if err != nil || state == nil || len(state.RankBaseline) != 100 || state.CoveredTopN != 100 || state.LastSuccessAt == nil || !state.LastSuccessAt.Equal(at) {
		t.Fatalf("coverage=%#v,%v", state, err)
	}
	next := p
	next.ObservedAt = at.Add(time.Minute)
	next.ExpectedPreviousAt = &at
	swapHolderRanks(&next, 9, 10)
	result, err := database.ApplyMarketHolderRefresh(ctx, next)
	if err != nil || result.InitialBaseline || result.RankEventsCreated != 2 {
		t.Fatalf("11↔10 not recorded: %#v,%v", result, err)
	}
	for _, boundary := range []int{20, 50} {
		previous := next.ObservedAt
		next.ExpectedPreviousAt = &previous
		next.ObservedAt = previous.Add(time.Minute)
		swapHolderRanks(&next, boundary-1, boundary)
		result, err = database.ApplyMarketHolderRefresh(ctx, next)
		if err != nil || result.RankEventsCreated != 2 {
			t.Fatalf("%d boundary=%#v,%v", boundary, result, err)
		}
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_holders`).Scan(&tracked); err != nil || tracked != 21 {
		t.Fatalf("only original Top20 entrants may become tracked: %d,%v", tracked, err)
	}
	if duplicate, err := database.ApplyMarketHolderRefresh(ctx, next); err != nil || duplicate.HoldersUpdated != 0 || duplicate.RankEventsCreated != 0 {
		t.Fatalf("duplicate observation rewrote baseline: %#v,%v", duplicate, err)
	}
	var falseSeen int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_holder_snapshots WHERE managed_observation AND NOT balance_observed AND balance_observed_at=captured_at`).Scan(&falseSeen); err != nil || falseSeen != 0 {
		t.Fatalf("unseen balance was made current: %d,%v", falseSeen, err)
	}
}

func TestPostgresHolderRefreshPersistsCompleteEmptyRanking(t *testing.T) {
	for _, count := range []int32{0, 1000} {
		t.Run(fmt.Sprintf("source_items_%d", count), func(t *testing.T) {
			database, pool := openIntegrationStore(t)
			ctx := context.Background()
			at := time.Now().UTC().Truncate(time.Microsecond)
			p := holderRefreshFixture(at)
			p.SourceItemCount, p.Ranks, p.Holders = count, nil, nil
			result, err := database.ApplyMarketHolderRefresh(ctx, p)
			if err != nil || !result.InitialBaseline || result.RankEventsCreated != 0 || result.HoldersUpdated != 0 {
				t.Fatalf("complete empty baseline=%#v,%v", result, err)
			}
			var kind, status string
			var rankCount, holderCount, eventCount int
			if err := pool.QueryRow(ctx, `SELECT jsonb_typeof(rank_baseline),jsonb_array_length(rank_baseline),status,
				(SELECT COUNT(*) FROM market_holders),(SELECT COUNT(*) FROM market_events)
				FROM market_holder_refresh_states WHERE chain_id=56 AND token_address=$1`, tradeFixtureToken).
				Scan(&kind, &rankCount, &status, &holderCount, &eventCount); err != nil || kind != "array" || rankCount != 0 || status != "complete" || holderCount != 0 || eventCount != 0 {
				t.Fatalf("empty rank persistence=%s/%d/%s/%d/%d,%v", kind, rankCount, status, holderCount, eventCount, err)
			}
			if again, err := database.ApplyMarketHolderRefresh(ctx, p); err != nil || again != (MarketHolderRefreshResult{}) {
				t.Fatalf("duplicate empty observation=%#v,%v", again, err)
			}
			bad := p
			bad.SourceComplete = false
			bad.ObservedAt = at.Add(time.Minute)
			bad.ExpectedPreviousAt = &at
			if _, err := database.ApplyMarketHolderRefresh(ctx, bad); !errors.Is(err, ErrInvalidMarketHolder) {
				t.Fatalf("incomplete empty sample accepted: %v", err)
			}
			state, err := database.GetMarketHolderRefreshState(ctx, 56, tradeFixtureToken)
			if err != nil || state == nil || state.LastSuccessAt == nil || !state.LastSuccessAt.Equal(at) || len(state.RankBaseline) != 0 {
				t.Fatalf("rejected sample changed successful empty baseline: %#v,%v", state, err)
			}
		})
	}
}

func TestPostgresHolderRefreshEmptyRankingRecordsExitsWithoutInventingBalances(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	first := holderRefreshFixture(at)
	if _, err := database.ApplyMarketHolderRefresh(ctx, first); err != nil {
		t.Fatal(err)
	}
	empty := first
	empty.ObservedAt = at.Add(time.Minute)
	empty.ExpectedPreviousAt = &at
	empty.Ranks, empty.Holders = []MarketHolderRank{}, nil
	result, err := database.ApplyMarketHolderRefresh(ctx, empty)
	if err != nil || result.InitialBaseline || result.RankEventsCreated != 100 || result.BalanceEventsCreated != 0 || result.HoldersUpdated != 0 || result.SnapshotsCreated != 20 {
		t.Fatalf("empty observation exits=%#v,%v", result, err)
	}
	var holders, unchangedBalances, clearedRanks, unobservedSnapshots int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*),COUNT(*) FILTER(WHERE balance_raw>0 AND last_seen_at=$1),
		COUNT(*) FILTER(WHERE rank IS NULL),
		(SELECT COUNT(*) FROM market_holder_snapshots WHERE captured_at=$2 AND NOT balance_observed AND balance_observed_at=$1)
		FROM market_holders`, at, empty.ObservedAt).
		Scan(&holders, &unchangedBalances, &clearedRanks, &unobservedSnapshots); err != nil || holders != 20 || unchangedBalances != 20 || clearedRanks != 20 || unobservedSnapshots != 20 {
		t.Fatalf("former Top20 balances/ranks=%d/%d/%d/%d,%v", holders, unchangedBalances, clearedRanks, unobservedSnapshots, err)
	}
	if again, err := database.ApplyMarketHolderRefresh(ctx, empty); err != nil || again != (MarketHolderRefreshResult{}) {
		t.Fatalf("empty exit replay duplicated events: %#v,%v", again, err)
	}
}

func TestPostgresHolderRefreshFailureRollsBackHoldersEventsSnapshotsAndBaseline(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	p := holderRefreshFixture(at)
	if _, err := database.ApplyMarketHolderRefresh(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE market_events ADD CONSTRAINT reject_rank_fixture CHECK(event_type<>'holder_rank_changed')`); err != nil {
		t.Fatal(err)
	}
	next := p
	next.ObservedAt = at.Add(time.Minute)
	next.ExpectedPreviousAt = &at
	swapHolderRanks(&next, 19, 20)
	if _, err := database.ApplyMarketHolderRefresh(ctx, next); err == nil {
		t.Fatal("mid-observation failure was ignored")
	}
	state, err := database.GetMarketHolderRefreshState(ctx, 56, tradeFixtureToken)
	if err != nil || state == nil || state.LastSuccessAt == nil || !state.LastSuccessAt.Equal(at) {
		t.Fatalf("failed batch advanced coverage: %#v,%v", state, err)
	}
	var holders, snapshots, events int
	if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM market_holders),(SELECT COUNT(*) FROM market_holder_snapshots),(SELECT COUNT(*) FROM market_events)`).Scan(&holders, &snapshots, &events); err != nil || holders != 20 || snapshots != 20 || events != 0 {
		t.Fatalf("partial batch persisted: %d/%d/%d %v", holders, snapshots, events, err)
	}
	var rank int32
	if err := pool.QueryRow(ctx, `SELECT rank FROM market_holders WHERE holder_address=$1`, p.Ranks[19].HolderAddress).Scan(&rank); err != nil || rank != 20 {
		t.Fatalf("failed batch cleared previous rank: %d,%v", rank, err)
	}
	bad := p
	bad.SourceComplete = false
	if _, err := database.ApplyMarketHolderRefresh(ctx, bad); !errors.Is(err, ErrInvalidMarketHolder) {
		t.Fatalf("incomplete sample accepted: %v", err)
	}
}

func TestPostgresHolderRefreshUnchangedRanksDoNotAppendSnapshotsAndAttemptDoesNotAdvance(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	p := holderRefreshFixture(at)
	if _, err := database.ApplyMarketHolderRefresh(ctx, p); err != nil {
		t.Fatal(err)
	}
	next := p
	next.ExpectedPreviousAt = &at
	next.ObservedAt = at.Add(time.Minute)
	result, err := database.ApplyMarketHolderRefresh(ctx, next)
	if err != nil || result.SnapshotsCreated != 0 || result.RankEventsCreated != 0 {
		t.Fatalf("unchanged sample appended history: %#v,%v", result, err)
	}
	if err := database.RecordMarketHolderRefreshAttempt(ctx, 56, "bsc", tradeFixtureToken, at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	state, err := database.GetMarketHolderRefreshState(ctx, 56, tradeFixtureToken)
	if err != nil || state.LastSuccessAt == nil || !state.LastSuccessAt.Equal(next.ObservedAt) || state.Status != "source_coverage_unknown" || len(state.RankBaseline) != 100 {
		t.Fatalf("attempt changed baseline: %#v,%v", state, err)
	}
	var total int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_holder_snapshots`).Scan(&total); err != nil || total != 20 {
		t.Fatalf("unchanged sample grew history: %d,%v", total, err)
	}
}

func TestPostgresManagedHolderHistoryRetentionProtectsLegacyLatestFrozenAndReferences(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	address := "0x1111111111111111111111111111111111111111"
	ids := make([]int64, 5)
	for i := range ids {
		if err := pool.QueryRow(ctx, `INSERT INTO market_holder_snapshots(chain_key,chain_id,token_address,holder_address,balance_raw,balance,source,captured_at,managed_observation) VALUES('bsc',56,$1,$2,1,1,'test',$3,TRUE) RETURNING id`, tradeFixtureToken, address, now.Add(-time.Duration(50-i)*24*time.Hour)).Scan(&ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO market_holder_snapshots(chain_key,chain_id,token_address,holder_address,balance_raw,balance,source,captured_at) VALUES('bsc',56,$1,$2,1,1,'legacy',$3)`, tradeFixtureToken, address, now.Add(-60*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO market_projects(debox_user_id,chain_key,chain_id,token_address,frozen_at) VALUES('holder-freeze','bsc',56,$1,$2)`, tradeFixtureToken, now.Add(-49*24*time.Hour+time.Hour)); err != nil {
		t.Fatal(err)
	}
	event, _, err := database.CreateMarketEvent(ctx, CreateMarketEventParams{ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureToken, EventType: "holder_increase", EventKey: "holder-ref-test", Source: "test", Confirmed: true, OccurredAt: now, Metadata: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO market_holder_snapshot_refs(snapshot_id,market_event_id) VALUES($1,$2)`, ids[0], event.ID); err != nil {
		t.Fatal(err)
	}
	removed, err := database.CleanupManagedMarketHolderHistory(ctx, now.Add(-30*24*time.Hour))
	if err != nil || removed != 1 {
		t.Fatalf("retention=%d,%v", removed, err)
	}
	for _, id := range []int64{ids[0], ids[1], ids[3], ids[4]} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM market_holder_snapshots WHERE id=$1)`, id).Scan(&exists); err != nil || !exists {
			t.Fatalf("protected history removed: id%d %v", id, err)
		}
	}
	if _, err := database.CleanupManagedMarketHolderHistory(ctx, now.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	} // EOF wraps the bounded cursor.
	if _, err := pool.Exec(ctx, `UPDATE market_projects SET frozen_at=NULL WHERE debox_user_id='holder-freeze'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM market_holder_snapshot_refs WHERE snapshot_id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	removed, err = database.CleanupManagedMarketHolderHistory(ctx, now.Add(-30*24*time.Hour))
	if err != nil || removed != 2 {
		t.Fatalf("wrapped cursor did not revisit unpinned facts: %d,%v", removed, err)
	}
	var legacy int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_holder_snapshots WHERE NOT managed_observation`).Scan(&legacy); err != nil || legacy != 1 {
		t.Fatalf("legacy history altered: %d,%v", legacy, err)
	}
}

func TestPostgresHolderObservationCASAllowsOnlyOneConcurrentBaselineCommit(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	first := holderRefreshFixture(at)
	if _, err := database.ApplyMarketHolderRefresh(ctx, first); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			p := first
			p.ExpectedPreviousAt = &at
			p.ObservedAt = at.Add(time.Duration(index+1) * time.Minute)
			swapHolderRanks(&p, 9+index*10, 10+index*10)
			_, err := database.ApplyMarketHolderRefresh(ctx, p)
			results <- err
		}(i)
	}
	wait.Wait()
	close(results)
	succeeded, rejected := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrStaleMarketHolderRefresh) {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("concurrent baselines both applied: success=%d stale=%d", succeeded, rejected)
	}
	var events int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_events WHERE event_type='holder_rank_changed'`).Scan(&events); err != nil || events != 2 {
		t.Fatalf("concurrent facts were duplicated: %d,%v", events, err)
	}
}

func TestPostgresHolderRefreshDueIsSharedPerChainAndUsesDeploymentSupply(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	var asset int64
	if err := pool.QueryRow(ctx, `INSERT INTO market_assets(identity_source,canonical_asset_id) VALUES('holder-test','supply') RETURNING id`).Scan(&asset); err != nil {
		t.Fatal(err)
	}
	var bsc, base int64
	for _, entry := range []struct {
		id            *int64
		key           string
		chain         int64
		token, supply string
	}{{&bsc, "bsc", 56, tradeFixtureToken, "1000"}, {&base, "base", 8453, tradeFixtureQuote, "2000"}} {
		if err := pool.QueryRow(ctx, `INSERT INTO market_asset_deployments(market_asset_id,chain_key,chain_id,token_address,total_supply_raw) VALUES($1,$2,$3,$4,$5) RETURNING id`, asset, entry.key, entry.chain, entry.token, entry.supply).Scan(entry.id); err != nil {
			t.Fatal(err)
		}
	}
	var firstProject int64
	for _, owner := range []string{"holder-owner-a", "holder-owner-b"} {
		var project int64
		if err := pool.QueryRow(ctx, `INSERT INTO market_projects(debox_user_id,market_asset_id,chain_key,chain_id,token_address,total_supply_raw) VALUES($1,$2,'bsc',56,$3,1000) RETURNING id`, owner, asset, tradeFixtureToken).Scan(&project); err != nil {
			t.Fatal(err)
		}
		if firstProject == 0 {
			firstProject = project
		}
		holderJobDemand(t, database, project, owner)
		for _, deployment := range []int64{bsc, base} {
			if _, err := pool.Exec(ctx, `INSERT INTO market_project_deployments(market_project_id,market_asset_id,market_asset_deployment_id) VALUES($1,$2,$3)`, project, asset, deployment); err != nil {
				t.Fatal(err)
			}
		}
	}
	due, err := database.ListMarketProjectsDueHolderRefresh(ctx, 8453, now.Add(-15*time.Minute), 10)
	if err != nil || len(due) != 1 || due[0].TotalSupplyRaw == nil || *due[0].TotalSupplyRaw != "2000" || due[0].TokenAddress != tradeFixtureQuote {
		t.Fatalf("shared/multichain holder scope: %#v,%v", due, err)
	}
	if err := database.RecordMarketHolderRefreshAttempt(ctx, 8453, "base", tradeFixtureQuote, now); err != nil {
		t.Fatal(err)
	}
	due, err = database.ListMarketProjectsDueHolderRefresh(ctx, 8453, now.Add(-15*time.Minute), 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("failed attempt was retried without fair backoff: %#v,%v", due, err)
	}
	states, err := database.ListMarketHolderRefreshStatesForProject(ctx, firstProject, "holder-owner-a")
	if err != nil || len(states) != 1 || states[0].LastSuccessAt != nil || states[0].CoveredTopN != 0 {
		t.Fatalf("attempt invented coverage: %#v,%v", states, err)
	}
	states, err = database.ListMarketHolderRefreshStatesForProject(ctx, firstProject, "holder-owner-b")
	if err != nil || len(states) != 0 {
		t.Fatalf("holder coverage crossed project ownership: %#v,%v", states, err)
	}
}

func TestPostgresHolderCleanupScansOnlyOneThousandAndAdvancesPastProtectedPrefix(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `INSERT INTO market_holder_snapshots(chain_key,chain_id,token_address,holder_address,balance_raw,balance,source,captured_at,managed_observation)
	 SELECT 'bsc',56,$1,'0x'||lpad(to_hex(i),40,'0'),1,1,'test',$2,TRUE FROM generate_series(1,1000) i`, tradeFixtureToken, now.Add(-60*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := pool.Exec(ctx, `INSERT INTO market_holder_snapshots(chain_key,chain_id,token_address,holder_address,balance_raw,balance,source,captured_at,managed_observation) VALUES('bsc',56,$1,$2,1,1,'test',$3,TRUE)`, tradeFixtureToken, fmt.Sprintf("0x%040x", 100000), now.Add(-time.Duration(59-i)*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := database.CleanupManagedMarketHolderHistory(ctx, now.Add(-30*24*time.Hour))
	if err != nil || removed != 0 {
		t.Fatalf("first call scanned past protected budget: %d,%v", removed, err)
	}
	removed, err = database.CleanupManagedMarketHolderHistory(ctx, now.Add(-30*24*time.Hour))
	if err != nil || removed != 1 {
		t.Fatalf("cursor stuck on protected prefix: %d,%v", removed, err)
	}
}

func TestPostgresFrozenHolderCoverageNeverUsesNewerAttemptHealth(t *testing.T) {
	database, pool := openIntegrationStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	params := holderRefreshFixture(at)
	if _, err := database.ApplyMarketHolderRefresh(ctx, params); err != nil {
		t.Fatal(err)
	}
	var project int64
	if err := pool.QueryRow(ctx, `INSERT INTO market_projects(debox_user_id,chain_key,chain_id,token_address,frozen_at) VALUES('frozen-coverage','bsc',56,$1,$2) RETURNING id`, tradeFixtureToken, at.Add(time.Minute)).Scan(&project); err != nil {
		t.Fatal(err)
	}
	states, err := database.ListMarketHolderRefreshStatesForProject(ctx, project, "frozen-coverage")
	if err != nil || len(states) != 1 {
		t.Fatalf("original as-of coverage missing: %#v,%v", states, err)
	}
	if err := database.RecordMarketHolderRefreshAttempt(ctx, 56, "bsc", tradeFixtureToken, at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	states, err = database.ListMarketHolderRefreshStatesForProject(ctx, project, "frozen-coverage")
	if err != nil || len(states) != 0 {
		t.Fatalf("post-freeze health washed into historical detail: %#v,%v", states, err)
	}
}

func TestPostgresHolderOlderSuccessDoesNotWashNewerFailedAttemptHealth(t *testing.T) {
	database, _ := openIntegrationStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	initial := holderRefreshFixture(at)
	if _, err := database.ApplyMarketHolderRefresh(ctx, initial); err != nil {
		t.Fatal(err)
	}
	laterFailure := at.Add(4 * time.Minute)
	if err := database.RecordMarketHolderRefreshAttempt(ctx, 56, "bsc", tradeFixtureToken, laterFailure); err != nil {
		t.Fatal(err)
	}
	olderSuccess := initial
	olderSuccess.ObservedAt = at.Add(3 * time.Minute)
	olderSuccess.ExpectedPreviousAt = &at
	if _, err := database.ApplyMarketHolderRefresh(ctx, olderSuccess); err != nil {
		t.Fatal(err)
	}
	state, err := database.GetMarketHolderRefreshState(ctx, 56, tradeFixtureToken)
	if err != nil || state.LastSuccessAt == nil || !state.LastSuccessAt.Equal(olderSuccess.ObservedAt) || !state.LastAttemptAt.Equal(laterFailure) || state.Status != "source_coverage_unknown" {
		t.Fatalf("older success washed newer failure health or lost valid baseline: %#v,%v", state, err)
	}
}
