package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresHolderDemandRequiresActualEligibleRulesAndChoosesDemandingOwner(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	holderJobProject(t, db, 56, "bsc", tradeFixtureToken, "older-without-demand")
	if _, err := pool.Exec(ctx, `UPDATE market_rules SET enabled=0 WHERE debox_user_id='older-without-demand'`); err != nil {
		t.Fatal(err)
	}
	if job, err := db.ClaimMarketHolderRefreshJob(ctx, 15*time.Minute); err != nil || job != nil {
		t.Fatal("bare paid project caused sampling", job, err)
	}
	var controls, states int
	if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM market_holder_refresh_scheduler),(SELECT COUNT(*) FROM market_holder_refresh_states)`).Scan(&controls, &states); err != nil || controls != 0 || states != 0 {
		t.Fatal("idle claim wrote scheduler state", controls, states, err)
	}
	holderJobProject(t, db, 56, "bsc", tradeFixtureToken, "actual-holder-owner")
	job := claimHolderJob(t, db)
	if job.Project.DeBoxUserID != "actual-holder-owner" {
		t.Fatalf("metadata chosen from unrelated owner: %+v", job.Project)
	}
	if other, err := db.ClaimMarketHolderRefreshJob(ctx, 15*time.Minute); err != nil || other != nil {
		t.Fatal("same token got per-owner duplicate lease", other, err)
	}
	if err := db.FailMarketHolderRefreshJob(ctx, *job, "round_cancelled", time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{
		`UPDATE market_rules SET enabled=1,run_status='paused' WHERE debox_user_id='actual-holder-owner'`,
		`UPDATE market_rules SET run_status='active',rule_type='market_price_above' WHERE debox_user_id='actual-holder-owner'`,
		`UPDATE market_rules SET rule_type='market_holder_rank_entered',threshold_value=101 WHERE debox_user_id='actual-holder-owner'`,
		`UPDATE market_rules SET threshold_value=20 WHERE debox_user_id='actual-holder-owner'; UPDATE subscriptions SET plan_code='standard' WHERE debox_user_id='actual-holder-owner'`,
	} {
		if _, err := pool.Exec(ctx, mutation); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE market_holder_refresh_states SET next_attempt_at=NOW()-INTERVAL '1 second'`); err != nil {
			t.Fatal(err)
		}
		if job, err := db.ClaimMarketHolderRefreshJob(ctx, 15*time.Minute); err != nil || job != nil {
			t.Fatal("inactive/unsupported/unauthorized rule caused sampling", job, err)
		}
	}
}

func TestPostgresHolderDemandRevocationStopsNewPagesAndPreservesBaseline(t *testing.T) {
	for _, kind := range []string{"rule_disabled", "rule_paused", "project_paused", "deployment_paused", "subscription_expired", "scope_removed", "combination_paused"} {
		t.Run(kind, func(t *testing.T) {
			db, pool := openIntegrationStore(t)
			ctx := context.Background()
			owner := "holder-cancel-" + kind
			holderJobProject(t, db, 56, "bsc", tradeFixtureToken, owner)
			if kind == "combination_paused" {
				var parent, rule1, rule2 int64
				if err := pool.QueryRow(ctx, `UPDATE market_rules SET rule_scope='combination' WHERE debox_user_id=$1 RETURNING id`, owner).Scan(&rule1); err != nil {
					t.Fatal(err)
				}
				if err := pool.QueryRow(ctx, `INSERT INTO market_rules(debox_user_id,market_project_id,rule_type,threshold_value,threshold_unit,notification_chat_id,rule_scope)
 SELECT debox_user_id,market_project_id,'market_holder_decrease',1,'percent',debox_user_id,'combination' FROM market_rules WHERE id=$1 RETURNING id`, rule1).Scan(&rule2); err != nil {
					t.Fatal(err)
				}
				if err := pool.QueryRow(ctx, `INSERT INTO market_combination_rules(debox_user_id,cycle_type,cycle_minutes,notification_chat_id) VALUES($1,'fixed',60,$1) RETURNING id`, owner).Scan(&parent); err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, `INSERT INTO market_combination_members(market_combination_rule_id,source_type,market_rule_id,required_trigger_count) VALUES($1,'market',$2,1),($1,'market',$3,1)`, parent, rule1, rule2); err != nil {
					t.Fatal(err)
				}
			}
			at := time.Now().UTC().Truncate(time.Microsecond).Add(-30 * time.Minute)
			baseline := holderRefreshFixture(at)
			if _, err := db.ApplyMarketHolderRefresh(ctx, baseline); err != nil {
				t.Fatal(err)
			}
			job := claimHolderJob(t, db)
			if err := db.ReserveMarketHolderRefreshPage(ctx, *job); err != nil {
				t.Fatal(err)
			}
			mutation := map[string]string{
				"rule_disabled":        `UPDATE market_rules SET enabled=0 WHERE debox_user_id=$1`,
				"rule_paused":          `UPDATE market_rules SET run_status='paused' WHERE debox_user_id=$1`,
				"project_paused":       `UPDATE market_projects SET status='paused' WHERE debox_user_id=$1`,
				"deployment_paused":    `UPDATE market_project_deployments SET status='paused' WHERE market_project_id IN(SELECT id FROM market_projects WHERE debox_user_id=$1)`,
				"subscription_expired": `UPDATE subscriptions SET is_permanent=0,expires_at=NOW()-INTERVAL '1 second' WHERE debox_user_id=$1`,
				"scope_removed":        `UPDATE market_rules SET deployment_scope='selected' WHERE debox_user_id=$1`,
				"combination_paused":   `UPDATE market_combination_rules SET run_status='paused' WHERE debox_user_id=$1`,
			}[kind]
			if _, err := pool.Exec(ctx, mutation, owner); err != nil {
				t.Fatal(err)
			}
			if err := db.ReserveMarketHolderRefreshPage(ctx, *job); !errors.Is(err, ErrMarketHolderRefreshDemandLost) {
				t.Fatalf("page after withdrawal=%v", err)
			}
			p := baseline
			p.ExpectedPreviousAt = job.ExpectedPreviousAt
			p.ObservedAt = time.Now().UTC().Truncate(time.Microsecond)
			p.SourceItemCount = 2000
			if _, err := db.CompleteMarketHolderRefreshJob(ctx, *job, p, 15*time.Minute); !errors.Is(err, ErrMarketHolderRefreshDemandLost) {
				t.Fatalf("commit after withdrawal=%v", err)
			}
			state, err := db.GetMarketHolderRefreshState(ctx, 56, tradeFixtureToken)
			if err != nil || state.LastSuccessAt == nil || !state.LastSuccessAt.Equal(at) || state.SourceItemCount != 100 {
				t.Fatalf("withdrawal advanced successful baseline: %+v %v", state, err)
			}
			var used, history int
			if err := pool.QueryRow(ctx, `SELECT refresh_pages_used,(SELECT COUNT(*) FROM market_holder_snapshots) FROM market_holder_refresh_states`).Scan(&used, &history); err != nil || used != 1 || history != 20 {
				t.Fatal("withdrawal consumed a page or changed history", used, history, err)
			}
			if err := db.FailMarketHolderRefreshJob(ctx, *job, "round_cancelled", time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if next, err := db.ClaimMarketHolderRefreshJob(ctx, 15*time.Minute); err != nil || next != nil {
				t.Fatal("withdrawn demand reclaimed", next, err)
			}
		})
	}
}

func TestPostgresHolderDemandSelectedDeploymentAndRemainingOwnerAreShared(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	const owner = "holder-deployment-scope"
	resourceCapacitySubscriptionFixture(t, pool, owner, "professional", 1)
	bscToken := tradeFixtureToken
	baseToken := tradeFixtureQuote
	bscPool := createWizardIntegrationPool(t, db, "bsc", 56, bscToken, fmt.Sprintf("0x%040x", 4100), fmt.Sprintf("0x%040x", 4101))
	basePool := createWizardIntegrationPool(t, db, "base", 8453, baseToken, fmt.Sprintf("0x%040x", 4102), fmt.Sprintf("0x%040x", 4103))
	at := time.Now().UTC()
	project, err := db.CreateMultiChainMarketProjectWithinQuota(ctx, CreateMultiChainMarketProjectParams{DeBoxUserID: owner, CanonicalName: "Holder scope", Symbol: "HS", IdentitySource: "coingecko", CanonicalAssetID: "holder-scope", VerificationStatus: "verified", Deployments: []CreateMultiChainMarketDeploymentParams{
		wizardIntegrationDeployment("bsc", 56, bscToken, bscPool.ID, at), wizardIntegrationDeployment("base", 8453, baseToken, basePool.ID, at),
	}}, marketScopeLegacyQuota("professional"))
	if err != nil {
		t.Fatal(err)
	}
	holderJobDemand(t, db, project.ID, owner)
	if _, err = pool.Exec(ctx, `UPDATE market_rules SET deployment_scope='selected' WHERE debox_user_id=$1`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO market_rule_deployments(market_rule_id,market_project_deployment_id)
 SELECT mr.id,mpd.id FROM market_rules mr JOIN market_project_deployments mpd ON mpd.market_project_id=mr.market_project_id JOIN market_asset_deployments mad ON mad.id=mpd.market_asset_deployment_id WHERE mr.debox_user_id=$1 AND mad.chain_id=56`, owner); err != nil {
		t.Fatal(err)
	}
	job := claimHolderJob(t, db)
	if job.Project.ChainID != 56 {
		t.Fatalf("selected BSC scope leased another deployment: %+v", job.Project)
	}
	if other, err := db.ClaimMarketHolderRefreshJob(ctx, 15*time.Minute); err != nil || other != nil {
		t.Fatal("unselected Base deployment leased", other, err)
	}
	// A second owner's same token demand keeps this shared job alive when
	// the representative's rule is stopped; no owner-local baseline is created.
	resourceCapacitySubscriptionFixture(t, pool, "remaining-holder-owner", "professional", 1)
	remaining, err := db.CreateMarketProjectWithinQuota(ctx, CreateMarketProjectParams{DeBoxUserID: "remaining-holder-owner", ChainKey: "bsc", ChainID: 56, TokenAddress: tradeFixtureToken, TokenDecimals: 18}, marketScopeLegacyQuota("professional"))
	if err != nil {
		t.Fatal(err)
	}
	holderJobDemand(t, db, remaining.ID, "remaining-holder-owner")
	if _, err = pool.Exec(ctx, `UPDATE market_rules SET enabled=0 WHERE debox_user_id=$1`, owner); err != nil {
		t.Fatal(err)
	}
	if err = db.ReserveMarketHolderRefreshPage(ctx, *job); err != nil {
		t.Fatalf("remaining owner's shared demand ignored: %v", err)
	}
}

func TestPostgresHolderDemandCapacityMigrationAndComplete2000PreserveRankContract(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	holderJobProject(t, db, 56, "bsc", tradeFixtureToken, "holder-2000")
	job := claimHolderJob(t, db)
	for page := 0; page < MarketHolderRefreshMaxPages; page++ {
		if err := db.ReserveMarketHolderRefreshPage(ctx, *job); err != nil {
			t.Fatalf("page %d: %v", page+1, err)
		}
	}
	if err := db.ReserveMarketHolderRefreshPage(ctx, *job); !errors.Is(err, ErrMarketHolderRefreshLeaseLost) {
		t.Fatalf("21st page accepted: %v", err)
	}
	p := holderRefreshFixture(time.Now().UTC().Truncate(time.Microsecond))
	p.SourceItemCount = 2000
	result, err := db.CompleteMarketHolderRefreshJob(ctx, *job, p, 15*time.Minute)
	if err != nil || !result.InitialBaseline || result.HoldersUpdated != 20 {
		t.Fatal(result, err)
	}
	state, err := db.GetMarketHolderRefreshState(ctx, 56, tradeFixtureToken)
	if err != nil || state.SourceItemCount != 2000 || state.CoveredTopN != 100 || len(state.RankBaseline) != 100 {
		t.Fatal(state, err)
	}
	var reserved, used, tracked int
	var next time.Time
	if err := pool.QueryRow(ctx, `SELECT refresh_pages_reserved,refresh_pages_used,next_due_at,(SELECT COUNT(*) FROM market_holders) FROM market_holder_refresh_states`).Scan(&reserved, &used, &next, &tracked); err != nil || reserved != 20 || used != 20 || tracked != 20 || !next.Equal(job.StartedAt.Add(15*time.Minute)) {
		t.Fatal(reserved, used, next, tracked, err)
	}
	bad := p
	bad.SourceItemCount = 2001
	previous := p.ObservedAt
	bad.ExpectedPreviousAt = &previous
	bad.ObservedAt = previous.Add(time.Second)
	if _, err := db.ApplyMarketHolderRefresh(ctx, bad); !errors.Is(err, ErrInvalidMarketHolder) {
		t.Fatal("2001 source accepted", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE market_holder_refresh_states SET source_item_count=2001`); err == nil {
		t.Fatal("migration lost upper source safety bound")
	}
	if _, err := pool.Exec(ctx, `UPDATE market_holder_refresh_states SET refresh_pages_reserved=21`); err == nil {
		t.Fatal("migration lost page reservation safety bound")
	}
}

type holderDemandHookDB struct {
	DBTX
	hook func()
}

func (d holderDemandHookDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := d.DBTX.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return holderDemandHookTx{Tx: tx, hook: d.hook}, nil
}

type holderDemandHookTx struct {
	pgx.Tx
	hook func()
}

func (tx holderDemandHookTx) Begin(ctx context.Context) (pgx.Tx, error) {
	nested, err := tx.Tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return holderDemandHookTx{Tx: nested, hook: tx.hook}, nil
}
func (tx holderDemandHookTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tag, err := tx.Tx.Exec(ctx, sql, args...)
	if err == nil && strings.Contains(sql, "INSERT INTO market_holder_refresh_states") && strings.Contains(sql, "rank_baseline") {
		tx.hook()
	}
	return tag, err
}

func TestPostgresHolderDemandFinalRecheckRollsBackApplyAfterCommittedStop(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	const owner = "holder-stop-during-apply"
	holderJobProject(t, db, 56, "bsc", tradeFixtureToken, owner)
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-30 * time.Minute)
	baseline := holderRefreshFixture(at)
	if _, err := db.ApplyMarketHolderRefresh(ctx, baseline); err != nil {
		t.Fatal(err)
	}
	job := claimHolderJob(t, db)
	if err := db.ReserveMarketHolderRefreshPage(ctx, *job); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	var cancelErr error
	db.db = holderDemandHookDB{DBTX: pool, hook: func() {
		once.Do(func() {
			_, cancelErr = pool.Exec(ctx, `UPDATE market_rules SET enabled=0 WHERE debox_user_id=$1`, owner)
		})
	}}
	p := holderRefreshFixture(time.Now().UTC().Truncate(time.Microsecond))
	p.ExpectedPreviousAt = job.ExpectedPreviousAt
	p.SourceItemCount = 2000
	swapHolderRanks(&p, 9, 10)
	if _, err := db.CompleteMarketHolderRefreshJob(ctx, *job, p, 15*time.Minute); !errors.Is(err, ErrMarketHolderRefreshDemandLost) {
		t.Fatalf("final committed stop not checked: %v", err)
	}
	if cancelErr != nil {
		t.Fatal(cancelErr)
	}
	state, err := db.GetMarketHolderRefreshState(ctx, 56, tradeFixtureToken)
	if err != nil || state.LastSuccessAt == nil || !state.LastSuccessAt.Equal(at) || state.SourceItemCount != 100 {
		t.Fatal("partial Apply escaped rollback", state, err)
	}
	var events, history, disabled int
	if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM market_events),(SELECT COUNT(*) FROM market_holder_snapshots),(SELECT COUNT(*) FROM market_rules WHERE debox_user_id=$1 AND enabled=0)`, owner).Scan(&events, &history, &disabled); err != nil || events != 0 || history != 20 || disabled != 1 {
		t.Fatal("stop or observation atomicity failed", events, history, disabled, err)
	}
}
