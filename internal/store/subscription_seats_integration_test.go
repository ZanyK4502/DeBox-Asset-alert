package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestPostgresSubscriptionSeatLimitsHoldEachTierUnderConcurrentOrders(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	policy := resourceAdmissionBudget(10_000)
	policy.StandardSalesSeatLimit = 3
	policy.ProfessionalSalesSeatLimit = 2
	db.resourceCapacity = &policy

	if _, err := pool.Exec(ctx, `
		INSERT INTO subscriptions(debox_user_id,plan_code,status,starts_at,expires_at,is_permanent)
		VALUES ('permanent-seat','professional','active',NOW()-INTERVAL '2 days',NOW()-INTERVAL '1 day',1)
	`); err != nil {
		t.Fatal(err)
	}

	// The permanent professional holder consumes one seat. Concurrent
	// Standard purchases must still be able to use their separate inventory.
	type outcome struct {
		plan string
		err  error
	}
	start := make(chan struct{})
	results := make(chan outcome, 7)
	for i, plan := range []string{"standard", "professional", "standard", "professional", "standard", "professional", "standard"} {
		go func() {
			<-start
			_, err := db.CreateOrder(ctx, resourceAdmissionOrderParams(fmt.Sprintf("seat-buyer-%d", i), plan))
			results <- outcome{plan: plan, err: err}
		}()
	}
	close(start)
	sold := make(map[string]int)
	for range 7 {
		result := <-results
		switch {
		case result.err == nil:
			sold[result.plan]++
		case errors.Is(result.err, ErrSubscriptionSeatsReached):
		default:
			t.Fatal(result.err)
		}
	}
	if sold["standard"] != 3 || sold["professional"] != 1 {
		t.Fatalf("wrong tier mix with one permanent Professional: %v", sold)
	}
	var orders, holds int
	if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM orders), (SELECT COUNT(*) FROM resource_capacity_holds)`).Scan(&orders, &holds); err != nil || orders != 4 || holds != 4 {
		t.Fatalf("rejected purchase left an order or hold: %d/%d, %v", orders, holds, err)
	}
}

func TestPostgresSubscriptionSeatReleaseAndRenewalPreserveInFlightOrders(t *testing.T) {
	policy := resourceAdmissionBudget(10_000)
	policy.StandardSalesSeatLimit = 1
	policy.ProfessionalSalesSeatLimit = 1
	t.Run("monthly_cu", func(t *testing.T) {
		testSubscriptionSeatReleaseAndRenewal(t, policy)
	})
	t.Run("seats_only", func(t *testing.T) {
		testSubscriptionSeatReleaseAndRenewal(t, ResourceCapacityPolicy{
			SeatOnly: true, StandardSalesSeatLimit: 1, ProfessionalSalesSeatLimit: 1,
		})
	})
}

func testSubscriptionSeatReleaseAndRenewal(t *testing.T, policy ResourceCapacityPolicy) {
	t.Helper()
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	db.resourceCapacity = &policy

	place := func(owner, plan string) Order {
		t.Helper()
		order, err := db.CreateOrder(ctx, resourceAdmissionOrderParams(owner, plan))
		if err != nil {
			t.Fatal(err)
		}
		return order
	}
	claim := func(order Order, number int) string {
		t.Helper()
		hash := fmt.Sprintf("0x%064x", number)
		if _, err := db.ClaimOrderTransaction(ctx, order.ID, order.DeBoxUserID, order.PayerAddress, hash); err != nil {
			t.Fatal(err)
		}
		return hash
	}
	assertBlocked := func(owner, plan string) {
		t.Helper()
		if _, err := db.CreateOrder(ctx, resourceAdmissionOrderParams(owner, plan)); !errors.Is(err, ErrSubscriptionSeatsReached) {
			t.Fatalf("%s order for %s: %v", plan, owner, err)
		}
	}
	forceOrderPastExpiry := func(order Order) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE orders SET expires_at=clock_timestamp()-INTERVAL '1 second' WHERE id=$1`, order.ID); err != nil {
			t.Fatal(err)
		}
	}

	first := place("seat-renewing-owner", "standard")
	hash := claim(first, 1)
	paid, err := db.FinalizePaidOrder(ctx, first.ID, hash, 100, 3)
	if err != nil || paid.Subscription == nil {
		t.Fatalf("finalize: %+v, %v", paid, err)
	}
	renewal := place(first.DeBoxUserID, "standard")
	renewal = place(renewal.DeBoxUserID, renewal.PlanCode)
	renewalHash := claim(renewal, 2)
	forceOrderPastExpiry(renewal)
	assertBlocked("seat-second-buyer", "standard")
	if n, err := db.ExpirePendingOrders(ctx); err != nil || n != 0 {
		t.Fatalf("confirming order released: %d, %v", n, err)
	}
	renewed, err := db.FinalizePaidOrder(ctx, renewal.ID, renewalHash, 101, 3)
	if err != nil || renewed.Subscription == nil || renewed.Subscription.ID != paid.Subscription.ID || !renewed.Subscription.ExpiresAt.After(paid.Subscription.ExpiresAt) {
		t.Fatalf("renewal at full capacity: %+v, %v", renewed, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE subscriptions
		SET starts_at=clock_timestamp()-INTERVAL '30 days',expires_at=clock_timestamp()-INTERVAL '1 second'
		WHERE id=$1`, renewed.Subscription.ID); err != nil {
		t.Fatal(err)
	}

	successor := place("seat-second-buyer", "standard")
	professional := place("seat-professional-buyer", "professional")
	if _, err := db.CreateOrder(ctx, resourceAdmissionOrderParams(successor.DeBoxUserID, "professional")); !errors.Is(err, ErrSubscriptionSeatsReached) {
		t.Fatalf("full Professional accepted a pending tier replacement: %v", err)
	}
	old, err := db.GetOrder(ctx, successor.ID)
	if err != nil || old == nil || old.Status != "pending" {
		t.Fatalf("failed replacement lost original seat: %+v, %v", old, err)
	}
	forceOrderPastExpiry(professional)
	assertBlocked("seat-new-professional", "professional")
	if n, err := db.ExpirePendingOrders(ctx); err != nil || n != 1 {
		t.Fatalf("pending order expiry: %d, %v", n, err)
	}
	inFlight := place("seat-new-professional", "professional")
	stale, err := db.UpdateOrderVerification(ctx, professional.ID, UpdateOrderVerificationParams{Status: "confirming"})
	if err != nil || stale.Status != "expired" {
		t.Fatalf("expired order reclaimed a released seat: %+v, %v", stale, err)
	}
	inFlightHash := claim(inFlight, 3)
	forceOrderPastExpiry(inFlight)
	assertBlocked("seat-later-professional", "professional")
	settled, err := db.FinalizePaidOrder(ctx, inFlight.ID, inFlightHash, 102, 3)
	if err != nil || settled.Order.Status != "paid" || settled.Subscription == nil {
		t.Fatalf("confirming past original order expiry failed to settle: %+v, %v", settled, err)
	}
}

func TestPostgresSeatOnlySalesAdmitThreeStandardAndTwoProfessional(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	policy := ResourceCapacityPolicy{SeatOnly: true, StandardSalesSeatLimit: 3, ProfessionalSalesSeatLimit: 2}
	db.resourceCapacity = &policy
	other := &Store{db: pool, resourceCapacity: &policy}
	if _, err := db.CreateOrder(ctx, resourceAdmissionOrderParams("unknown-plan-buyer", "free")); !errors.Is(err, ErrOrderInvalid) {
		t.Fatalf("unknown sales tier bypassed seat admission: %v", err)
	}

	type outcome struct {
		plan string
		err  error
	}
	tiers := []string{"standard", "professional", "standard", "professional", "standard", "professional", "standard"}
	start := make(chan struct{})
	results := make(chan outcome, len(tiers))
	stores := []*Store{db, other}
	for i, plan := range tiers {
		go func() {
			<-start
			_, err := stores[i%len(stores)].CreateOrder(ctx, resourceAdmissionOrderParams(fmt.Sprintf("seat-only-buyer-%d", i), plan))
			results <- outcome{plan: plan, err: err}
		}()
	}
	close(start)
	sold, refused := map[string]int{}, map[string]int{}
	for range tiers {
		result := <-results
		switch {
		case result.err == nil:
			sold[result.plan]++
		case errors.Is(result.err, ErrSubscriptionSeatsReached):
			refused[result.plan]++
		default:
			t.Fatal(result.err)
		}
	}
	if sold["standard"] != 3 || sold["professional"] != 2 || refused["standard"] != 1 || refused["professional"] != 1 {
		t.Fatalf("wrong mixed sale limits: sold=%v refused=%v", sold, refused)
	}
	var pending, holds int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM orders WHERE status='pending'), (SELECT count(*) FROM resource_capacity_holds)`).Scan(&pending, &holds); err != nil || pending != 5 || holds != 0 {
		t.Fatalf("expected five pending orders and no CU holds: %d/%d, %v", pending, holds, err)
	}
}

func TestPostgresSeatOnlySettlesOldHoldAndPreservesPaidMonitoring(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	legacy := resourceAdmissionBudget(10_000)
	db.resourceCapacity = &legacy
	const owner = "seat-only-existing-order"
	order, err := db.CreateOrder(ctx, resourceAdmissionOrderParams(owner, "professional"))
	if err != nil {
		t.Fatal(err)
	}

	db.resourceCapacity = &ResourceCapacityPolicy{SeatOnly: true, StandardSalesSeatLimit: 3, ProfessionalSalesSeatLimit: 2}
	hash := fmt.Sprintf("0x%064x", 130)
	if _, err := db.ClaimOrderTransaction(ctx, order.ID, owner, order.PayerAddress, hash); err != nil {
		t.Fatal(err)
	}
	paid, err := db.FinalizePaidOrder(ctx, order.ID, hash, 120, 3)
	if err != nil || paid.Subscription == nil || paid.Order.Status != "paid" {
		t.Fatalf("existing order did not settle: %+v, %v", paid, err)
	}
	var linked int64
	if err := pool.QueryRow(ctx, `SELECT subscription_id FROM resource_capacity_holds WHERE order_id=$1`, order.ID).Scan(&linked); err != nil || linked != paid.Subscription.ID {
		t.Fatalf("old hold did not follow the paid subscription: %d, %v", linked, err)
	}

	quota := resourceActivationQuota()
	interactionQuotaCreateRule(t, ctx, db, owner, 820, "balance_change", quota)
	if _, err := db.CreateMarketProjectWithinQuota(ctx, resourceActivationProject(owner), quota); err != nil {
		t.Fatalf("paid monitoring required a monthly budget: %v", err)
	}
	second := resourceActivationProject(owner)
	second.TokenAddress = "0x5555555555555555555555555555555555555555"
	if _, err := db.CreateMarketProjectWithinQuota(ctx, second, quota); !errors.Is(err, ErrMarketProjectLimitReached) {
		t.Fatalf("monthly budget removal bypassed the plan project cap: %v", err)
	}
}
