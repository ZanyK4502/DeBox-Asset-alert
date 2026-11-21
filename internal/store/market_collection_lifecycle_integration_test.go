package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func collectionLifecycleProject(t *testing.T, db *Store, owner string) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	var asset, deployment, project, linked int64
	queries := []struct {
		sql  string
		args []any
		out  *int64
	}{
		{`INSERT INTO market_assets(identity_source,canonical_asset_id) VALUES('legacy',$1) RETURNING id`, []any{owner}, &asset},
	}
	for _, q := range queries {
		if err := db.db.QueryRow(ctx, q.sql, q.args...).Scan(q.out); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.db.QueryRow(ctx, `INSERT INTO market_asset_deployments(market_asset_id,chain_key,chain_id,token_address)
 VALUES($1,'bsc',56,$2) RETURNING id`, asset, fmt.Sprintf("0x%040x", asset)).Scan(&deployment); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow(ctx, `INSERT INTO market_projects(debox_user_id,market_asset_id,chain_key,chain_id,token_address)
 VALUES($1,$2,'bsc',56,$3) RETURNING id`, owner, asset, fmt.Sprintf("0x%040x", asset)).Scan(&project); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow(ctx, `INSERT INTO market_project_deployments(market_project_id,market_asset_id,market_asset_deployment_id)
 VALUES($1,$2,$3) RETURNING id`, project, asset, deployment).Scan(&linked); err != nil {
		t.Fatal(err)
	}
	return project, linked
}

func collectionLifecycleObserve(t *testing.T, db *Store) MarketChainCollectionState {
	t.Helper()
	s, err := db.ObserveMarketChainCollection(context.Background(), "bsc", 56)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func collectionLifecycleClosedIdle(t *testing.T, db *Store) MarketChainCollectionState {
	t.Helper()
	zero := collectionLifecycleObserve(t, db)
	if zero.Phase != "idle" || zero.PendingIdle == nil || zero.PendingIdle.ThroughAt != nil {
		t.Fatal("missing committed zero proof", zero)
	}
	collectionLifecycleProject(t, db, "lifecycle-active")
	s := collectionLifecycleObserve(t, db)
	if s.Phase != "active" || s.ActiveTargetCount != 1 || s.PendingIdle == nil || s.PendingIdle.ThroughAt == nil {
		t.Fatal("activation did not close proof", s)
	}
	return s
}

func TestPostgresCollectionLifecycleTracksCommittedDemandAndRollback(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	first := collectionLifecycleObserve(t, db)
	second := collectionLifecycleObserve(t, db)
	if first.Revision != second.Revision || first.PendingIdle.ID != second.PendingIdle.ID || !first.PendingIdle.FromAt.Equal(second.PendingIdle.FromAt) {
		t.Fatal("idle polling wrote another transition")
	}
	project, linked := collectionLifecycleProject(t, db, "lifecycle-owner")
	active := collectionLifecycleObserve(t, db)
	if active.ActiveTargetCount != 1 || active.PendingIdle.ThroughAt == nil || active.PendingIdle.ThroughAt.Before(active.PendingIdle.FromAt) {
		t.Fatal("project deployment not tracked", active)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE market_projects SET status='archived' WHERE id=$1`, project); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	after := collectionLifecycleObserve(t, db)
	if after.Revision != active.Revision || after.ActiveTargetCount != 1 {
		t.Fatal("rolled-back archive escaped its transaction", after)
	}
	if _, err = pool.Exec(ctx, `UPDATE market_project_deployments SET status='paused' WHERE id=$1`, linked); err != nil {
		t.Fatal(err)
	}
	var phase string
	if err = pool.QueryRow(ctx, `SELECT phase FROM market_chain_collection_states WHERE chain_id=56`).Scan(&phase); err != nil || phase != "unobserved" {
		t.Fatal("uncommitted-time zero was treated as idle", phase, err)
	}
	zero := collectionLifecycleObserve(t, db)
	var intervals int
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_collection_idle_intervals WHERE chain_id=56`).Scan(&intervals); err != nil || intervals != 2 {
		t.Fatal("zero observation did not create exactly one proof", intervals, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE market_project_deployments SET status='active' WHERE id=$1`, linked); err != nil {
		t.Fatal(err)
	}
	if resumed := collectionLifecycleObserve(t, db); resumed.ActiveTargetCount != 1 || resumed.Revision <= zero.Revision {
		t.Fatal("deployment restore not tracked", resumed)
	}
}

func TestPostgresCollectionIdleAdvanceRequiresWholeProofAndIsAtomic(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	s := collectionLifecycleClosedIdle(t, db)
	boundary, err := db.ConfigureNotificationRecoveryBoundary(ctx, time.Now().Add(-24*time.Hour), 0, "retain original recovery boundary")
	if err != nil {
		t.Fatal(err)
	}
	hash := "0x" + strings.Repeat("a", 64)
	if _, err := db.AdvanceMarketChainCursor(ctx, AdvanceMarketChainCursorParams{ChainKey: "bsc", ChainID: 56, CursorKey: "market_logs", NextBlockNumber: 100, SafeBlockNumber: 300, LastBlockHash: &hash, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	p := AdvanceMarketChainCursorOverIdleParams{ChainKey: "bsc", ChainID: 56, CursorKey: "market_logs", StateRevision: s.Revision, IntervalID: s.PendingIdle.ID,
		ExpectedNextBlock: 100, NextBlockNumber: 200, SafeBlockNumber: 300, LastBlockHash: hash, LastBlockParentHash: "0x" + strings.Repeat("c", 64),
		FirstSkippedBlockAt: s.PendingIdle.FromAt.Add(time.Microsecond), LastSkippedBlockAt: s.PendingIdle.ThroughAt.Add(-time.Microsecond)}
	bad := p
	bad.FirstSkippedBlockAt = s.PendingIdle.FromAt.Add(-time.Microsecond)
	if _, err := db.AdvanceMarketChainCursorOverIdle(ctx, bad); !errors.Is(err, ErrMarketCollectionProofRequired) {
		t.Fatal("unknown/active prefix was skipped", err)
	}
	bad = p
	bad.LastSkippedBlockAt = *s.PendingIdle.ThroughAt
	if _, err := db.AdvanceMarketChainCursorOverIdle(ctx, bad); !errors.Is(err, ErrMarketCollectionProofRequired) {
		t.Fatal("active suffix was skipped", err)
	}
	bad = p
	bad.LastBlockHash = "invalid"
	if _, err := db.AdvanceMarketChainCursorOverIdle(ctx, bad); err == nil {
		t.Fatal("invalid cursor anchor accepted")
	}
	var gaps int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_collection_gaps`).Scan(&gaps); err != nil || gaps != 0 {
		t.Fatal("failed cursor write left a gap", gaps, err)
	}
	var anchors int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_scanned_blocks`).Scan(&anchors); err != nil || anchors != 0 {
		t.Fatal("failed cursor start left an anchor", anchors, err)
	}
	advanced, err := db.AdvanceMarketChainCursorOverIdle(ctx, p)
	if err != nil || advanced.NextBlockNumber != 200 {
		t.Fatal("valid idle proof was not applied", advanced, err)
	}
	var storedHash, storedParent string
	var storedAt time.Time
	if err = pool.QueryRow(ctx, `SELECT block_hash,parent_hash,block_timestamp FROM market_scanned_blocks WHERE chain_id=56 AND block_number=199 AND canonical=1`).Scan(&storedHash, &storedParent, &storedAt); err != nil || storedHash != p.LastBlockHash || storedParent != p.LastBlockParentHash || !storedAt.Equal(p.LastSkippedBlockAt.UTC().Truncate(time.Microsecond)) {
		t.Fatal("committed cursor lacks full predecessor anchor", storedHash, storedParent, storedAt, err)
	}
	if _, err = db.AdvanceMarketChainCursorOverIdle(ctx, p); !errors.Is(err, ErrMarketCollectionStateChanged) {
		t.Fatal("stale proof reused", err)
	}
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_collection_gaps WHERE from_block=100 AND through_block=199`).Scan(&gaps); err != nil || gaps != 1 {
		t.Fatal("idle gap missing", gaps, err)
	}
	var recoveryChanges int
	if err = pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM notification_recovery_gaps)+(SELECT COUNT(*) FROM daily_summary_dispatches)`).Scan(&recoveryChanges); err != nil || recoveryChanges != 0 {
		t.Fatal("collection proof changed unrelated recovery/report state", recoveryChanges, err)
	}
	retained, err := db.GetNotificationRecoveryBoundary(ctx)
	if err != nil || retained == nil || *retained != boundary {
		t.Fatal("collection idle advance altered T0", retained, err)
	}
}

func TestPostgresCollectionActivationInitializationAndZeroSkip(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	s := collectionLifecycleClosedIdle(t, db)
	p := AdvanceMarketChainCursorOverIdleParams{ChainKey: "bsc", ChainID: 56, CursorKey: "market_logs", StateRevision: s.Revision, IntervalID: s.PendingIdle.ID,
		ExpectedNextBlock: 0, NextBlockNumber: 100, SafeBlockNumber: 300, LastBlockHash: "0x" + strings.Repeat("b", 64), LastBlockParentHash: "0x" + strings.Repeat("c", 64), LastSkippedBlockAt: s.PendingIdle.FromAt.Add(-time.Hour)}
	if _, err := db.AdvanceMarketChainCursorOverIdle(ctx, p); err != nil {
		t.Fatal("atomic activation start failed", err)
	}
	anchors, err := db.ListCanonicalMarketScannedBlocks(ctx, 56, "market_logs", 99, 1)
	if err != nil || len(anchors) != 1 || anchors[0].ParentHash != p.LastBlockParentHash || anchors[0].BlockNumber != 99 {
		t.Fatal("initialized cursor lacks a durable reorg anchor", anchors, err)
	}
	var gaps int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_collection_gaps`).Scan(&gaps); err != nil || gaps != 0 {
		t.Fatal("nil cursor claimed unknown history was idle", gaps, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE market_projects SET status='archived'`); err != nil {
		t.Fatal(err)
	}
	collectionLifecycleObserve(t, db)
	if _, err := pool.Exec(ctx, `UPDATE market_projects SET status='active'`); err != nil {
		t.Fatal(err)
	}
	s = collectionLifecycleObserve(t, db)
	before, err := db.GetMarketChainCursor(ctx, 56, "market_logs")
	if err != nil {
		t.Fatal(err)
	}
	p.StateRevision, p.IntervalID, p.ExpectedNextBlock, p.NextBlockNumber = s.Revision, s.PendingIdle.ID, 100, 100
	p.FirstSkippedBlockAt = s.PendingIdle.FromAt.Add(time.Microsecond)
	if _, err = db.AdvanceMarketChainCursorOverIdle(ctx, p); err != nil {
		t.Fatal("zero skip failed", err)
	}
	after, err := db.GetMarketChainCursor(ctx, 56, "market_logs")
	if err != nil {
		t.Fatal(err)
	}
	if before.NextBlockNumber != after.NextBlockNumber || !before.LastScannedAt.Equal(*after.LastScannedAt) {
		t.Fatal("zero skip rewrote cursor")
	}
}

func TestPostgresCollectionStateLockCoordinatesLifecycleWithoutScannerDeadlock(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	collectionLifecycleObserve(t, db)
	project, _ := collectionLifecycleProject(t, db, "concurrent-lifecycle")
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT chain_id FROM market_chain_collection_states WHERE chain_id=56 FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	writer, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Release()
	writerPID := writer.Conn().PgConn().PID()
	done := make(chan error, 1)
	go func() {
		_, e := writer.Exec(ctx, `UPDATE market_projects SET status='archived' WHERE id=$1`, project)
		done <- e
	}()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
waiting:
	for {
		var blocked bool
		if err = pool.QueryRow(ctx, `SELECT COALESCE((SELECT wait_event_type='Lock' FROM pg_stat_activity WHERE pid=$1),false)`, writerPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break waiting
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("writer did not reach the held lifecycle state row")
		}
	}
	// The lifecycle writer may hold its project row while waiting for state.
	// The scanner side must only read demand (MVCC), not lock project rows.
	var demand int64
	if err = tx.QueryRow(ctx, `SELECT debox_market_collection_target_count(56)`).Scan(&demand); err != nil || demand != 1 {
		t.Fatal("state holder tried to lock a lifecycle project row", demand, err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("lifecycle/state lock deadlock")
	}
	if s := collectionLifecycleObserve(t, db); s.ActiveTargetCount != 0 || s.Phase != "idle" {
		t.Fatal("committed zero was not observed", s)
	}
}

func TestPostgresCollectionIdleProofDoesNotUseTransactionStartOrUncommittedDemand(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var transactionStart time.Time
	if err = tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&transactionStart); err != nil {
		t.Fatal(err)
	}
	idle := collectionLifecycleObserve(t, db)
	collectionLifecycleProject(t, newWithDB(tx), "started-before-idle")
	during := collectionLifecycleObserve(t, db)
	if during.ActiveTargetCount != 0 || during.PendingIdle.ThroughAt != nil {
		t.Fatal("uncommitted activation changed committed idle proof", during)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	after := collectionLifecycleObserve(t, db)
	if after.PendingIdle.ThroughAt == nil || !idle.PendingIdle.FromAt.After(transactionStart) || after.PendingIdle.ThroughAt.Before(idle.PendingIdle.FromAt) {
		t.Fatal("transaction start was used as activation proof end", transactionStart, idle, after)
	}
}

func TestPostgresCollectionHistoryAndProvenIdleArePartitionedAtomically(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	state := collectionLifecycleClosedIdle(t, db)
	hash := func(n int64) string { return fmt.Sprintf("0x%064x", n+1) }
	oldHash := hash(99)
	if _, err := db.AdvanceMarketChainCursor(ctx, AdvanceMarketChainCursorParams{ChainKey: "bsc", ChainID: 56, CursorKey: "market_logs", NextBlockNumber: 100, SafeBlockNumber: 500, LastBlockHash: &oldHash, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	firstAt := state.PendingIdle.FromAt.Add(-time.Hour)
	retainedEnd := firstAt.Add(30 * time.Minute)
	firstIdle := state.PendingIdle.FromAt
	p := AdvanceMarketChainCursorOverIdleParams{ChainKey: "bsc", ChainID: 56, CursorKey: "market_logs", StateRevision: state.Revision, IntervalID: state.PendingIdle.ID, ExpectedNextBlock: 100,
		NextBlockNumber: 200, SafeBlockNumber: 500, FirstSkippedBlockAt: firstAt, LastSkippedBlockAt: state.PendingIdle.ThroughAt.Add(-time.Microsecond), LastBlockHash: hash(199), LastBlockParentHash: hash(198),
		HistoryPrefix: &MarketCollectionHistoryPrefix{ThroughBlock: 129, ThroughAt: retainedEnd, PreviousHash: hash(99), ThroughHash: hash(129), FirstIdleAt: &firstIdle}}
	invalid := p
	invalid.LastBlockHash = "malformed"
	if _, err := db.AdvanceMarketChainCursorOverIdle(ctx, invalid); err == nil {
		t.Fatal("malformed anchor accepted")
	}
	var count int64
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM market_collection_history_ranges)+(SELECT count(*) FROM market_collection_gaps)`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial activation survived rollback", count, err)
	}
	if _, err := db.AdvanceMarketChainCursorOverIdle(ctx, p); err != nil {
		t.Fatal(err)
	}
	var workID, reviewNext, gapStart, gapEnd int64
	if err := pool.QueryRow(ctx, `SELECT h.id,h.next_review_block,g.from_block,g.through_block
 FROM market_collection_history_ranges h JOIN market_collection_gaps g USING(chain_id,idle_interval_id,cursor_key)
 WHERE h.from_block=100 AND h.through_block=129 AND h.predecessor_hash=$1 AND h.through_hash=$2`, hash(99), hash(129)).Scan(&workID, &reviewNext, &gapStart, &gapEnd); err != nil || reviewNext != 100 || gapStart != 130 || gapEnd != 199 {
		t.Fatal("interval split", reviewNext, gapStart, gapEnd, err)
	}
	var live, coverage, notices int64
	if err := pool.QueryRow(ctx, `SELECT (SELECT next_block_number FROM market_chain_cursors WHERE chain_id=56),(SELECT count(*) FROM market_scan_coverage),
 (SELECT count(*) FROM market_rule_events)+(SELECT count(*) FROM notification_recovery_gaps)+(SELECT count(*) FROM market_trade_ready_events)`).Scan(&live, &coverage, &notices); err != nil || live != 200 || coverage != 0 || notices != 0 {
		t.Fatal("invented coverage, notification or evidence", live, coverage, notices, err)
	}
	review := func(from, through, scope int64, reason string) (int64, error) {
		t.Helper()
		var next int64
		err := pool.QueryRow(ctx, `SELECT debox_review_collection_history($1,$2,$3,$4,$5)`, workID, from, through, scope, reason).Scan(&next)
		return next, err
	}
	if _, err := review(100, 129, 1, "historical filter attested by owner"); err == nil {
		t.Fatal("history closed without verified coverage")
	}
	const token = "0x1111111111111111111111111111111111111111"
	previousAt := firstAt.Add(-time.Second)
	for _, header := range []struct {
		number int64
		at     time.Time
	}{{99, previousAt}, {129, retainedEnd}} {
		if _, err := db.UpsertMarketScannedBlock(ctx, UpsertMarketScannedBlockParams{ChainID: 56, ChainKey: "bsc", CursorKey: "historical_replay", BlockNumber: header.number, BlockHash: hash(header.number), ParentHash: hash(header.number - 1), BlockTimestamp: &header.at}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.RecordMarketScanCoverage(ctx, RecordMarketScanCoverageParams{ChainID: 56, CursorKey: "historical_replay", ParserVersion: 1, ScopeTokenAddresses: []string{token}, FromBlock: 100, ThroughBlock: 129, AfterAt: previousAt, ThroughAt: retainedEnd, AfterHash: hash(99), ThroughHash: hash(129)}); err != nil {
		t.Fatal(err)
	}
	var scope int64
	if err := pool.QueryRow(ctx, `SELECT scope_id FROM market_scan_coverage WHERE cursor_key='historical_replay'`).Scan(&scope); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct {
		from, through, scope int64
		evidence             string
	}{{101, 129, scope, "historical filter"}, {100, 130, scope, "historical filter"}, {100, 129, scope, "  "}, {100, 129, scope + 1, "historical filter"}} {
		if _, err := review(bad.from, bad.through, bad.scope, bad.evidence); err == nil {
			t.Fatal("unsupported history closure", bad)
		}
	}
	for range 2 {
		if next, err := review(100, 129, scope, "reviewed original target record"); err != nil || next != 130 {
			t.Fatal("valid/retried review", next, err)
		}
	}
	if _, err := review(100, 129, scope, "conflicting evidence"); err == nil {
		t.Fatal("conflicting retry accepted")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM market_collection_history_reviews WHERE range_id=$1`, workID).Scan(&count); err != nil || count != 1 {
		t.Fatal("review was not idempotent", count, err)
	}
	cursor, err := db.GetMarketChainCursor(ctx, 56, "market_logs")
	if err != nil || cursor.NextBlockNumber != 200 {
		t.Fatal("history review changed live progress", cursor, err)
	}
}

func TestPostgresCollectionMultipleActivationsBeforeFirstCursorReserveOldPeriod(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	first := collectionLifecycleClosedIdle(t, db)
	if _, err := pool.Exec(ctx, `UPDATE market_projects SET status='archived' WHERE status='active'`); err != nil {
		t.Fatal(err)
	}
	idle := collectionLifecycleObserve(t, db)
	if idle.PendingIdle.ID == first.PendingIdle.ID || idle.PendingIdle.ThroughAt != nil {
		t.Fatal("latest zero observation not visible", idle)
	}
	if _, err := pool.Exec(ctx, `UPDATE market_projects SET status='active' WHERE status='archived'`); err != nil {
		t.Fatal(err)
	}
	resumed := collectionLifecycleObserve(t, db)
	if resumed.PendingIdle.ID != idle.PendingIdle.ID || resumed.PendingIdle.EarlierActivationAt == nil || !resumed.PendingIdle.EarlierActivationAt.Equal(*first.PendingIdle.ThroughAt) {
		t.Fatal("missing earlier activation responsibility", resumed.PendingIdle)
	}
	hash := func(n int64) string { return fmt.Sprintf("0x%064x", n+1) }
	firstIdle := resumed.PendingIdle.FromAt
	p := AdvanceMarketChainCursorOverIdleParams{ChainKey: "bsc", ChainID: 56, CursorKey: "market_logs", StateRevision: resumed.Revision, IntervalID: resumed.PendingIdle.ID, ExpectedNextBlock: 0,
		HistoryStartBlock: 50, NextBlockNumber: 100, SafeBlockNumber: 200, FirstSkippedBlockAt: first.PendingIdle.ThroughAt.Add(-time.Second), LastSkippedBlockAt: firstIdle,
		LastBlockHash: hash(99), LastBlockParentHash: hash(98), HistoryPrefix: &MarketCollectionHistoryPrefix{PreviousHash: hash(49), ThroughHash: hash(75),
			ThroughBlock: 75, ThroughAt: firstIdle.Add(-time.Microsecond), FirstIdleAt: &firstIdle}}
	if _, err := db.AdvanceMarketChainCursorOverIdle(ctx, p); err != nil {
		t.Fatal(err)
	}
	var from, through int64
	if err := pool.QueryRow(ctx, `SELECT from_block,through_block FROM market_collection_history_ranges WHERE idle_interval_id=$1`, resumed.PendingIdle.ID).Scan(&from, &through); err != nil || from != 50 || through != 75 {
		t.Fatal("earlier active period lost", from, through, err)
	}
	if s := collectionLifecycleObserve(t, db); s.PendingIdle != nil || s.AcknowledgedIdleIntervalID != resumed.PendingIdle.ID {
		t.Fatal("older activation will replay", s)
	}
}
