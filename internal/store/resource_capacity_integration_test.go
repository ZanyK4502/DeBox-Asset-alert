package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func resourceCapacityOrderFixture(t *testing.T, pool *pgxpool.Pool, owner, plan, status string, expired bool) int64 {
	t.Helper()
	expiry := time.Now().Add(time.Hour)
	if expired {
		expiry = time.Now().Add(-time.Hour)
	}
	var id int64
	err := pool.QueryRow(context.Background(), `INSERT INTO orders(debox_user_id,payer_address,plan_code,token_symbol,total_amount,recipient_address,expires_at,status)
 VALUES($1,'0x1111111111111111111111111111111111111111',$2,'USDT',5,'0x2222222222222222222222222222222222222222',$3,$4) RETURNING id`, owner, plan, expiry, status).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func resourceCapacitySubscriptionFixture(t *testing.T, pool *pgxpool.Pool, owner, plan string, permanent int) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(), `INSERT INTO subscriptions(debox_user_id,plan_code,status,starts_at,expires_at,is_permanent)
 VALUES($1,$2,'active',NOW(),NOW()+INTERVAL '30 days',$3) RETURNING id`, owner, plan, permanent).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func resourceCapacityHoldFixture(t *testing.T, pool *pgxpool.Pool, owner, plan string, orderID int64, subID *int64, cu int64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `INSERT INTO resource_capacity_holds(order_id,subscription_id,debox_user_id,plan_code,monthly_cu,plan_snapshot,linked_at)
 VALUES($1,$2,$3,$4,$5,jsonb_build_object('schema_version',1,'plan_code',$4::text,'monthly_cu',$5::bigint),CASE WHEN $2::bigint IS NOT NULL THEN NOW() END)`, orderID, subID, owner, plan, cu)
	if err != nil {
		t.Fatal(err)
	}
}

func resourceCapacityWatchFixture(t *testing.T, pool *pgxpool.Pool, owner, kind, scope string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `INSERT INTO watch_rules(debox_user_id,wallet_address,rule_type,notification_chat_id,notification_chat_type,rule_scope)
 VALUES($1,'0x3333333333333333333333333333333333333333',$2,$1,'private',$3)`, owner, kind, scope)
	if err != nil {
		t.Fatal(err)
	}
}

func TestPostgresResourceCapacityAggregatesPromisesAndActualByUserMax(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	p := resourceCapacityTestPolicy()
	p.AccountMonthlyCU = 10000
	db.resourceCapacity = &p
	if status, err := db.ReadResourceCapacityStatus(ctx); err != nil || status.ReservedMonthlyCU != 0 || status.Users != 0 {
		t.Fatal(status, err)
	}
	ordinary := resourceCapacitySubscriptionFixture(t, pool, "ordinary", "professional", 0)
	for _, cu := range []int64{120, 260} {
		paid := resourceCapacityOrderFixture(t, pool, "ordinary", "professional", "paid", false)
		resourceCapacityHoldFixture(t, pool, "ordinary", "professional", paid, &ordinary, cu)
	}
	resourceCapacityWatchFixture(t, pool, "ordinary", "address_interaction", "combination")
	resourceCapacityWatchFixture(t, pool, "ordinary", "balance_change", "standalone")
	if _, err := pool.Exec(ctx, `INSERT INTO market_projects(debox_user_id,chain_id,chain_key,token_address) VALUES('ordinary',56,'bsc','0x4444444444444444444444444444444444444444')`); err != nil {
		t.Fatal(err)
	}
	// No-hold pending is still a full promise even after its timestamp passes.
	resourceCapacityOrderFixture(t, pool, "pending", "standard", "pending", true)
	resourceCapacityOrderFixture(t, pool, "confirming", "professional", "confirming", true)
	resourceCapacityWatchFixture(t, pool, "free", "balance_change", "standalone")
	if _, err := pool.Exec(ctx, `INSERT INTO market_projects(debox_user_id,chain_id,chain_key,token_address) VALUES('free',56,'bsc','0x4444444444444444444444444444444444444444')`); err != nil {
		t.Fatal(err)
	}
	resourceCapacitySubscriptionFixture(t, pool, "permanent", "professional", 1)
	resourceCapacityWatchFixture(t, pool, "permanent", "address_interaction", "standalone")
	resourceCapacitySubscriptionFixture(t, pool, "legacy-paid", "standard", 0)
	status, err := db.ReadResourceCapacityStatus(ctx)
	// ordinary260 + pending130 + confirming240 + free110 + permanent50 + legacy130.
	if err != nil || status.ReservedMonthlyCU != 920 || status.Users != 6 || status.UnknownPlan {
		t.Fatalf("aggregate=%+v %v", status, err)
	}
	if _, err = db.ExpirePendingOrders(ctx); err != nil {
		t.Fatal(err)
	}
	status, err = db.ReadResourceCapacityStatus(ctx)
	if err != nil || status.ReservedMonthlyCU != 790 {
		t.Fatalf("only DB-expired pending should release, confirming stays: %+v %v", status, err)
	}
}

func TestPostgresResourceCapacityPaidLinkHonorsOldPromiseAndFuturePlanCosts(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	p := resourceCapacityTestPolicy()
	db.resourceCapacity = &p
	id := resourceCapacityOrderFixture(t, pool, "paid-owner", "professional", "pending", false)
	if _, err := withTxValue(ctx, pool, func(tx DBTX) (bool, error) {
		admission, cancel, err := db.beginResourceCapacityCheck(ctx, tx)
		if err != nil {
			return false, err
		}
		defer cancel()
		return true, db.reserveResourceCapacityOrder(admission, tx, Order{ID: id, DeBoxUserID: "paid-owner", PlanCode: "professional", Status: "pending"})
	}); err != nil {
		t.Fatal(err)
	}
	sub := resourceCapacitySubscriptionFixture(t, pool, "paid-owner", "professional", 0)
	if _, err := pool.Exec(ctx, `UPDATE orders SET status='paid' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	p.Verified = false
	p.AccountMonthlyCU = 0
	if _, err := withTxValue(ctx, pool, func(tx DBTX) (bool, error) { return true, db.linkResourceCapacityOrder(ctx, tx, id, sub) }); err != nil {
		t.Fatalf("already paid link was capacity gated: %v", err)
	}
	second := resourceCapacityOrderFixture(t, pool, "paid-owner", "professional", "paid", false)
	resourceCapacityHoldFixture(t, pool, "paid-owner", "professional", second, &sub, 300)
	pending := resourceCapacityOrderFixture(t, pool, "pending-owner", "standard", "pending", false)
	resourceCapacityHoldFixture(t, pool, "pending-owner", "standard", pending, nil, 100)
	status, err := db.ReadResourceCapacityStatus(ctx)
	if err != nil || status.ReservedMonthlyCU != 430 {
		t.Fatalf("renew holds were summed: %+v %v", status, err)
	}
	// Current catalog increases apply to the whole ordinary paid plan. Keep the
	// larger current promise while still retaining an older higher hold.
	p.OrdinaryRuleMonthlyCU = 20
	p.InteractionRuleMonthlyCU = 60
	p.MarketProjectMonthlyCU = 200
	status, err = db.ReadResourceCapacityStatus(ctx)
	if err != nil || status.ReservedMonthlyCU != 700 {
		t.Fatalf("current full plan upgrade ignored: %+v %v", status, err)
	}
	// A preexisting no-hold paid order is allowed through the association path.
	legacy := resourceCapacityOrderFixture(t, pool, "legacy-owner", "standard", "paid", false)
	legacySub := resourceCapacitySubscriptionFixture(t, pool, "legacy-owner", "standard", 0)
	if _, err := withTxValue(ctx, pool, func(tx DBTX) (bool, error) { return true, db.linkResourceCapacityOrder(ctx, tx, legacy, legacySub) }); err != nil {
		t.Fatalf("legacy paid order was blocked: %v", err)
	}
}

func TestPostgresResourceCapacityOwnerGrowthCannotBorrowConcurrentRelease(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	p := resourceCapacityTestPolicy()
	p.Verified = false
	db.resourceCapacity = &p
	resourceCapacityWatchFixture(t, pool, "releasing-owner", "address_interaction", "standalone")
	_, err := withTxValue(ctx, pool, func(tx DBTX) (bool, error) {
		admission, cancel, err := db.beginResourceCapacityCheck(ctx, tx, "candidate-owner")
		if err != nil {
			return false, err
		}
		defer cancel()
		// Stops/deletes do not take the capacity lock. Commit another user's
		// release while this candidate still owns the global admission lock.
		if _, err = pool.Exec(ctx, `UPDATE watch_rules SET enabled=0 WHERE debox_user_id='releasing-owner'`); err != nil {
			return false, err
		}
		if _, err = tx.Exec(admission, `INSERT INTO watch_rules(debox_user_id,wallet_address,rule_type,notification_chat_id,notification_chat_type)
 VALUES('candidate-owner','0x3333333333333333333333333333333333333333','balance_change','candidate-owner','private')`); err != nil {
			return false, err
		}
		return true, db.checkResourceCapacity(admission, tx)
	})
	if !errors.Is(err, ErrResourceCapacityUnavailable) {
		t.Fatalf("other owner release admitted unverified growth: %v", err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM watch_rules WHERE debox_user_id='candidate-owner'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected growth committed: count=%d %v", count, err)
	}
	// The same owner's already-paid whole-plan promise still covers actual
	// configuration when financial verification is off and net budget is0.
	resourceCapacitySubscriptionFixture(t, pool, "covered-owner", "professional", 0)
	p.AccountMonthlyCU = 0
	if _, err = withTxValue(ctx, pool, func(tx DBTX) (bool, error) {
		admission, cancel, err := db.beginResourceCapacityCheck(ctx, tx, "covered-owner")
		if err != nil {
			return false, err
		}
		defer cancel()
		if _, err = tx.Exec(admission, `INSERT INTO watch_rules(debox_user_id,wallet_address,rule_type,notification_chat_id,notification_chat_type)
 VALUES('covered-owner','0x3333333333333333333333333333333333333333','balance_change','covered-owner','private')`); err != nil {
			return false, err
		}
		return true, db.checkResourceCapacity(admission, tx)
	}); err != nil {
		t.Fatalf("own paid promise failed to cover neutral configuration: %v", err)
	}
}
