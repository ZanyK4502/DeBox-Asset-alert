package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Synthetic costs deliberately stay separate from Config's measured floors.
// The five single-chain project units each include the five-pool envelope.
func resourceScopeFiveProjectPolicy() ResourceCapacityPolicy {
	p := resourceCapacityTestPolicy()
	p.AccountMonthlyCU = 10_000
	p.MarketProjectCostMaxPools = 5
	p.MarketProjectCostChainKeys = []string{"bsc"}
	p.Plans["professional"] = ResourcePlanCapacity{RuleLimit: 10, InteractionRuleLimit: 1,
		MarketProjectLimit: 5, MarketPoolLimit: 5, AllowedMarketChainKeys: []string{"bsc"}}
	return p
}

func TestPostgresResourceScopeFiveProjectSnapshotPreservesLegacyV1(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	p := resourceScopeFiveProjectPolicy()
	db.resourceCapacity = &p
	legacy := resourceCapacityOrderFixture(t, pool, "scope-old-order", "professional", "pending", false)
	resourceCapacityHoldFixture(t, pool, "scope-old-order", "professional", legacy, nil, 240)
	var legacyBefore string
	if err := pool.QueryRow(ctx, `SELECT plan_snapshot::text FROM resource_capacity_holds WHERE order_id=$1`, legacy).Scan(&legacyBefore); err != nil {
		t.Fatal(err)
	}
	order, err := db.CreateOrder(ctx, resourceAdmissionOrderParams("scope-new-order", "professional"))
	if err != nil {
		t.Fatal(err)
	}
	var monthly int64
	var raw json.RawMessage
	if err := pool.QueryRow(ctx, `SELECT monthly_cu,plan_snapshot FROM resource_capacity_holds WHERE order_id=$1`, order.ID).Scan(&monthly, &raw); err != nil {
		t.Fatal(err)
	}
	var snapshot resourcePlanSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	// 9 ordinary + 1 interaction + all 5 project promises. The pool count
	// describes what P covers; it must not multiply an already complete P again.
	if monthly != 640 || snapshot.MonthlyCU != 640 || snapshot.SchemaVersion != 3 || snapshot.MonthDays != 31 || snapshot.MarketDeploymentsPerProject != 1 ||
		snapshot.RuleLimit != 10 || snapshot.InteractionRuleLimit != 1 || snapshot.MarketProjectLimit != 5 || snapshot.MarketPoolLimit != 5 ||
		fmt.Sprint(snapshot.AllowedMarketChainKeys) != "[bsc]" || snapshot.MarketProjectCostMaxPools != 5 || fmt.Sprint(snapshot.MarketProjectCostChainKeys) != "[bsc]" ||
		snapshot.OrdinaryRuleMonthlyCU != 10 || snapshot.InteractionRuleMonthlyCU != 50 || snapshot.MarketProjectMonthlyCU != 100 {
		t.Fatalf("new full-plan scope/cost snapshot incorrect: monthly=%d snapshot=%+v", monthly, snapshot)
	}
	status, err := db.ReadResourceCapacityStatus(ctx)
	if err != nil || status.ReservedMonthlyCU != 1280 {
		t.Fatalf("old pending promise did not use greater current plan cost: %+v %v", status, err)
	}
	var legacyAfter string
	if err := pool.QueryRow(ctx, `SELECT plan_snapshot::text FROM resource_capacity_holds WHERE order_id=$1`, legacy).Scan(&legacyAfter); err != nil || legacyAfter != legacyBefore {
		t.Fatalf("v1 evidence was rewritten: before=%s after=%s err=%v", legacyBefore, legacyAfter, err)
	}
	// A completed payment associates its old hold without upgrading the
	// original JSON evidence or rechecking an exhausted/unverified new-sale budget.
	p.Verified, p.AccountMonthlyCU = false, 400
	p.MarketProjectCostMaxPools, p.MarketProjectCostChainKeys = 0, nil
	hash := "0x" + strings.Repeat("e", 64)
	if _, err := db.ClaimOrderTransaction(ctx, legacy, "scope-old-order", "0x1111111111111111111111111111111111111111", hash); err != nil {
		t.Fatal(err)
	}
	paid, err := db.FinalizePaidOrder(ctx, legacy, hash, 100, 3)
	if err != nil || paid.Subscription == nil || paid.Order.Status != "paid" {
		t.Fatalf("v1 paid promise blocked: %+v %v", paid, err)
	}
	if err := pool.QueryRow(ctx, `SELECT plan_snapshot::text FROM resource_capacity_holds WHERE order_id=$1`, legacy).Scan(&legacyAfter); err != nil || legacyAfter != legacyBefore {
		t.Fatalf("v1 paid JSON evidence changed: %s %v", legacyAfter, err)
	}
}

func TestPostgresResourceScopeMultiChainPromisePreservesLegacyV2(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	p := resourceScopeFiveProjectPolicy()
	p.MarketProjectCostChainKeys = []string{"bsc", "ethereum", "base", "polygon", "arbitrum", "optimism"}
	pro := p.Plans["professional"]
	pro.AllowedMarketChainKeys = append([]string(nil), p.MarketProjectCostChainKeys...)
	p.Plans["professional"] = pro
	db.resourceCapacity = &p
	legacy := resourceCapacityOrderFixture(t, pool, "multichain-v2-order", "professional", "pending", false)
	resourceCapacityHoldFixture(t, pool, "multichain-v2-order", "professional", legacy, nil, 640)
	var legacyBefore string
	if err := pool.QueryRow(ctx, `UPDATE resource_capacity_holds
	 SET plan_snapshot=plan_snapshot||'{"schema_version":2,"market_project_limit":5,"market_pool_limit":5,"allowed_market_chain_keys":["bsc"]}'::jsonb
	 WHERE order_id=$1 RETURNING plan_snapshot::text`, legacy).Scan(&legacyBefore); err != nil {
		t.Fatal(err)
	}
	order, err := db.CreateOrder(ctx, resourceAdmissionOrderParams("multichain-v3-order", "professional"))
	if err != nil {
		t.Fatal(err)
	}
	var raw json.RawMessage
	var monthly int64
	if err := pool.QueryRow(ctx, `SELECT monthly_cu,plan_snapshot FROM resource_capacity_holds WHERE order_id=$1`, order.ID).Scan(&monthly, &raw); err != nil {
		t.Fatal(err)
	}
	var snapshot resourcePlanSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	// Five logical projects can each deploy on five chains with one selected
	// pool per chain: 25 collection units, plus the unchanged wallet promise.
	if monthly != 2640 || snapshot.MonthlyCU != 2640 || snapshot.SchemaVersion != 3 || snapshot.MarketDeploymentsPerProject != 5 || len(snapshot.AllowedMarketChainKeys) != 6 {
		t.Fatalf("six-chain promise undercounted: monthly=%d snapshot=%+v", monthly, snapshot)
	}
	status, err := db.ReadResourceCapacityStatus(ctx)
	if err != nil || status.ReservedMonthlyCU != 5280 {
		t.Fatalf("current catalog did not cover old pending promise: %+v %v", status, err)
	}
	p.Verified, p.AccountMonthlyCU = false, 400
	hash := "0x" + strings.Repeat("a", 64)
	if _, err := db.ClaimOrderTransaction(ctx, legacy, "multichain-v2-order", "0x1111111111111111111111111111111111111111", hash); err != nil {
		t.Fatal(err)
	}
	if paid, err := db.FinalizePaidOrder(ctx, legacy, hash, 100, 3); err != nil || paid.Subscription == nil || paid.Order.Status != "paid" {
		t.Fatalf("new capacity model blocked an existing paid promise: %+v %v", paid, err)
	}
	var legacyAfter string
	if err := pool.QueryRow(ctx, `SELECT plan_snapshot::text FROM resource_capacity_holds WHERE order_id=$1`, legacy).Scan(&legacyAfter); err != nil || legacyBefore != legacyAfter {
		t.Fatalf("v2 payment evidence changed: before=%s after=%s err=%v", legacyBefore, legacyAfter, err)
	}
}

func TestPostgresResourceScopeCoverageRejectsUnreviewedExpansion(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	for _, test := range []struct {
		name   string
		mutate func(*ResourceCapacityPolicy)
	}{
		{"old_two_pool_cost", func(p *ResourceCapacityPolicy) { p.MarketProjectCostMaxPools = 2 }},
		{"missing_pool_cost_scope", func(p *ResourceCapacityPolicy) { p.MarketProjectCostMaxPools = 0 }},
		{"missing_cost_chain_scope", func(p *ResourceCapacityPolicy) { p.MarketProjectCostChainKeys = nil }},
		{"cost_chain_does_not_cover_bsc", func(p *ResourceCapacityPolicy) { p.MarketProjectCostChainKeys = []string{"base"} }},
		{"new_plan_chain_outside_cost_scope", func(p *ResourceCapacityPolicy) {
			plan := p.Plans["professional"]
			plan.AllowedMarketChainKeys = []string{"bsc", "base"}
			p.Plans["professional"] = plan
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := resourceScopeFiveProjectPolicy()
			test.mutate(&p)
			db.resourceCapacity = &p
			if _, err := db.CreateOrder(ctx, resourceAdmissionOrderParams("scope-rejected-"+test.name, "professional")); !errors.Is(err, ErrResourceCapacityState) {
				t.Fatalf("unreviewed scope was admitted: %v", err)
			}
			var orders, holds int64
			if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM orders),(SELECT COUNT(*) FROM resource_capacity_holds)`).Scan(&orders, &holds); err != nil || orders != 0 || holds != 0 {
				t.Fatalf("scope refusal left partial sale %d/%d %v", orders, holds, err)
			}
		})
	}
}

func TestPostgresResourceScopeUnknownModelHonorsConfirmingAndFullMonthAllocation(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	p := resourceScopeFiveProjectPolicy()
	db.resourceCapacity = &p
	params := resourceAdmissionOrderParams("scope-confirming", "professional")
	order, err := db.CreateOrder(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	hash := "0x" + strings.Repeat("f", 64)
	if _, err := db.ClaimOrderTransaction(ctx, order.ID, params.DeBoxUserID, params.PayerAddress, hash); err != nil {
		t.Fatal(err)
	}
	var snapshotBefore string
	if err := pool.QueryRow(ctx, `UPDATE orders SET expires_at=NOW()-INTERVAL '1 minute' WHERE id=$1 RETURNING (SELECT plan_snapshot::text FROM resource_capacity_holds WHERE order_id=$1)`, order.ID).Scan(&snapshotBefore); err != nil {
		t.Fatal(err)
	}
	// The full monthly allocation is independent of the first month or today's
	// remaining provider balance. Background consumes part of the 240M envelope.
	p.AccountMonthlyCU, p.OtherMonthlyCU, p.SafetyMonthlyCU, p.BackgroundMonthlyCU = 450_000_000, 150_000_000, 60_000_000, 10_000_000
	p.Verified, p.MarketProjectCostMaxPools, p.MarketProjectCostChainKeys = false, 0, nil
	status, err := db.ReadResourceCapacityStatus(ctx)
	if err != nil || status.Verified || status.NetMonthlyCU != 230_000_000 || status.ReservedMonthlyCU != 640 {
		t.Fatalf("full allocation or confirming promise wrong: %+v %v", status, err)
	}
	if expired, err := db.ExpirePendingOrders(ctx); err != nil || expired != 0 {
		t.Fatalf("confirming promise expired: %d %v", expired, err)
	}
	if _, err := db.CreateOrder(ctx, resourceAdmissionOrderParams("scope-unknown-new-sale", "professional")); !errors.Is(err, ErrResourceCapacityUnavailable) {
		t.Fatalf("unknown scope opened a sale: %v", err)
	}
	paid, err := db.FinalizePaidOrder(ctx, order.ID, hash, 101, 3)
	if err != nil || paid.Subscription == nil || paid.Order.Status != "paid" || paid.Order.SubscriptionDays != 30 || paid.Order.TotalAmount != order.TotalAmount {
		t.Fatalf("unknown scope changed old payment/term/amount: %+v %v", paid, err)
	}
	var snapshotAfter string
	if err := pool.QueryRow(ctx, `SELECT plan_snapshot::text FROM resource_capacity_holds WHERE order_id=$1`, order.ID).Scan(&snapshotAfter); err != nil || snapshotAfter != snapshotBefore {
		t.Fatalf("confirmation rewrote original snapshot: %s %v", snapshotAfter, err)
	}
	status, err = db.ReadResourceCapacityStatus(ctx)
	if err != nil || status.ReservedMonthlyCU != 640 || status.NetMonthlyCU != 230_000_000 {
		t.Fatalf("paid transition released or duplicated the promise: %+v %v", status, err)
	}
}
