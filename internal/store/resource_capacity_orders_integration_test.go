package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func resourceAdmissionOrderParams(owner, plan string) CreateOrderParams {
	return CreateOrderParams{DeBoxUserID: owner, PayerAddress: "0x2222222222222222222222222222222222222222", PlanCode: plan, BillingCycle: "monthly", SubscriptionDays: 30, ChainKey: "bsc", ChainID: 56, TokenSymbol: "USDT", TokenDecimals: 18, TotalAmount: "10", RecipientAddress: "0x3333333333333333333333333333333333333333"}
}
func resourceAdmissionBudget(net int64) ResourceCapacityPolicy {
	p := resourceCapacityTestPolicy()
	p.AccountMonthlyCU = p.OtherMonthlyCU + p.BackgroundMonthlyCU + p.SafetyMonthlyCU + net
	return p
}

func TestPostgresResourceOrdersConcurrentLastSlotAndReplacementRollback(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	p := resourceAdmissionBudget(130)
	db.resourceCapacity = &p
	type result struct {
		order Order
		err   error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, owner := range []string{"capacity-buyer-a", "capacity-buyer-b"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			<-start
			o, e := db.CreateOrder(ctx, resourceAdmissionOrderParams(owner, "standard"))
			results <- result{o, e}
		}(owner)
	}
	close(start)
	wg.Wait()
	close(results)
	var winner Order
	successes, rejections := 0, 0
	for r := range results {
		if r.err == nil {
			successes++
			winner = r.order
		} else if errors.Is(r.err, ErrResourceCapacityReached) {
			rejections++
		} else {
			t.Fatal(r.err)
		}
	}
	if successes != 1 || rejections != 1 {
		t.Fatalf("success/rejection=%d/%d", successes, rejections)
	}
	var orders, holds int64
	if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM orders),(SELECT COUNT(*) FROM resource_capacity_holds)`).Scan(&orders, &holds); err != nil || orders != 1 || holds != 1 {
		t.Fatalf("partial order/hold left %d/%d %v", orders, holds, err)
	}
	// Replacing a pending order is atomic with its candidate full-plan hold.
	if _, err := db.CreateOrder(ctx, resourceAdmissionOrderParams(winner.DeBoxUserID, "professional")); !errors.Is(err, ErrResourceCapacityReached) {
		t.Fatalf("upgrade replacement=%v", err)
	}
	old, err := db.GetOrder(ctx, winner.ID)
	if err != nil || old == nil || old.Status != "pending" {
		t.Fatalf("old pending lost: %+v %v", old, err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM resource_capacity_holds`).Scan(&holds); err != nil || holds != 1 {
		t.Fatal(holds, err)
	}
	// Even net-neutral new sales require an explicitly verified resource share.
	p.Verified = false
	if _, err := db.CreateOrder(ctx, resourceAdmissionOrderParams(winner.DeBoxUserID, "standard")); !errors.Is(err, ErrResourceCapacityUnavailable) {
		t.Fatalf("unverified replacement=%v", err)
	}
	old, err = db.GetOrder(ctx, winner.ID)
	if err != nil || old.Status != "pending" {
		t.Fatal(old, err)
	}
}

func TestPostgresResourceOrdersPaidPromiseSurvivesClosureAndRenewalIsIdempotent(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	p := resourceAdmissionBudget(130)
	db.resourceCapacity = &p
	params := resourceAdmissionOrderParams("paid-capacity-owner", "standard")
	order, err := db.CreateOrder(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	hash := "0x" + strings.Repeat("a", 64)
	if _, err = db.ClaimOrderTransaction(ctx, order.ID, params.DeBoxUserID, params.PayerAddress, hash); err != nil {
		t.Fatal(err)
	}
	p.Verified = false
	p.AccountMonthlyCU = 0
	paid, err := db.FinalizePaidOrder(ctx, order.ID, hash, 100, 3)
	if err != nil || paid.Subscription == nil || paid.Order.Status != "paid" {
		t.Fatalf("paid blocked %+v %v", paid, err)
	}
	var linked int64
	if err = pool.QueryRow(ctx, `SELECT subscription_id FROM resource_capacity_holds WHERE order_id=$1`, order.ID).Scan(&linked); err != nil || linked != paid.Subscription.ID {
		t.Fatal(linked, err)
	}
	again, err := db.FinalizePaidOrder(ctx, order.ID, hash, 100, 3)
	if err != nil || again.Subscription == nil || !again.Subscription.ExpiresAt.Equal(paid.Subscription.ExpiresAt) {
		t.Fatalf("idempotence %+v %v", again, err)
	}
	// Renewals reserve the user's maximum promise, not two complete allocations.
	p = resourceAdmissionBudget(130)
	renewal, err := db.CreateOrder(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	status, err := db.ReadResourceCapacityStatus(ctx)
	if err != nil || status.ReservedMonthlyCU != 130 {
		t.Fatalf("renewal double-count %+v %v", status, err)
	}
	hash2 := "0x" + strings.Repeat("b", 64)
	if _, err = db.ClaimOrderTransaction(ctx, renewal.ID, params.DeBoxUserID, params.PayerAddress, hash2); err != nil {
		t.Fatal(err)
	}
	p.Verified = false
	renewed, err := db.FinalizePaidOrder(ctx, renewal.ID, hash2, 101, 3)
	if err != nil || renewed.Subscription == nil {
		t.Fatal(err)
	}
	if got := renewed.Subscription.ExpiresAt.Sub(paid.Subscription.ExpiresAt); got != 30*24*time.Hour {
		t.Fatalf("renewal term changed: %v", got)
	}
	if _, err = db.FinalizePaidOrder(ctx, renewal.ID, hash2, 101, 3); err != nil {
		t.Fatal(err)
	}
	status, err = db.ReadResourceCapacityStatus(ctx)
	if err != nil || status.ReservedMonthlyCU != 130 {
		t.Fatal(status, err)
	}
}

func TestPostgresResourceOrdersOnlyTerminalDatabaseStateReleasesPromise(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	p := resourceAdmissionBudget(130)
	db.resourceCapacity = &p
	params := resourceAdmissionOrderParams("expiry-capacity-owner", "standard")
	order, err := db.CreateOrder(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE orders SET expires_at=NOW()-INTERVAL '1 minute' WHERE id=$1`, order.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateOrder(ctx, resourceAdmissionOrderParams("new-buyer", "standard")); !errors.Is(err, ErrResourceCapacityReached) {
		t.Fatalf("time alone released promise: %v", err)
	}
	if n, err := db.ExpirePendingOrders(ctx); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	order, err = db.CreateOrder(ctx, resourceAdmissionOrderParams("new-buyer", "standard"))
	if err != nil {
		t.Fatal(err)
	}
	hash := "0x" + strings.Repeat("c", 64)
	if _, err = db.ClaimOrderTransaction(ctx, order.ID, "new-buyer", params.PayerAddress, hash); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE orders SET expires_at=NOW()-INTERVAL '1 minute' WHERE id=$1`, order.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := db.ExpirePendingOrders(ctx); err != nil || n != 0 {
		t.Fatalf("confirming expired %d %v", n, err)
	}
	if _, err = db.CreateOrder(ctx, resourceAdmissionOrderParams("third-buyer", "standard")); !errors.Is(err, ErrResourceCapacityReached) {
		t.Fatalf("confirming hold released: %v", err)
	}
	if _, err = db.UpdateOrderVerification(ctx, order.ID, UpdateOrderVerificationParams{Status: "failed", Error: "explicit chain rejection"}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateOrder(ctx, resourceAdmissionOrderParams("third-buyer", "standard")); err != nil {
		t.Fatal(err)
	}
	stale, err := db.UpdateOrderVerification(ctx, order.ID, UpdateOrderVerificationParams{Status: "confirming"})
	if err != nil || stale.Status != "failed" {
		t.Fatalf("stale verifier resurrected freed promise: %+v %v", stale, err)
	}
	status, err := db.ReadResourceCapacityStatus(ctx)
	if err != nil || status.ReservedMonthlyCU != 130 {
		t.Fatal(status, err)
	}
}

func TestPostgresResourceOrdersLegacyConfirmationWorksWithoutNewHold(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	params := resourceAdmissionOrderParams("legacy-capacity-owner", "standard")
	legacy, err := db.CreateOrder(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	p := resourceAdmissionBudget(0)
	p.Verified = false
	db.resourceCapacity = &p
	hash := "0x" + strings.Repeat("d", 64)
	if _, err = db.ClaimOrderTransaction(ctx, legacy.ID, params.DeBoxUserID, params.PayerAddress, hash); err != nil {
		t.Fatal(err)
	}
	paid, err := db.FinalizePaidOrder(ctx, legacy.ID, hash, 100, 3)
	if err != nil || paid.Subscription == nil {
		t.Fatalf("legacy paid promise blocked: %+v %v", paid, err)
	}
	var holds int64
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM resource_capacity_holds`).Scan(&holds); err != nil || holds != 0 {
		t.Fatal(holds, err)
	}
}
