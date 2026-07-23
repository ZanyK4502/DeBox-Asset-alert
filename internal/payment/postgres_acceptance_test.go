package payment

import (
	"context"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/testdb"
)

func TestAcceptanceVerifiedUSDTTransferActivatesSubscription(t *testing.T) {
	database := testdb.Open(t)
	catalog, err := plans.NewCatalog("10", 30, "USDT")
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	blockchain := successfulBlockchain(t, 102, "10", testRecipient)
	service := New(database, blockchain, catalog, liveSettings())

	prepared, err := service.Prepare(
		context.Background(),
		"acceptance-payment-user",
		testPayer,
		plans.Standard,
	)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if prepared.Order.Status != "pending" || prepared.Transaction.To != BSCUSDTAddress {
		t.Fatalf("unexpected prepared order: %#v", prepared)
	}

	verified, err := service.Verify(
		context.Background(),
		prepared.Order.ID,
		testHash,
		"acceptance-payment-user",
		testPayer,
	)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verified.PaymentStatus != "paid" ||
		verified.Order.Status != "paid" ||
		verified.Confirmations != RequiredConfirmations ||
		verified.Subscription == nil ||
		verified.Subscription.PlanCode != plans.Standard {
		t.Fatalf("unexpected verification: %#v", verified)
	}

	active, err := database.GetActiveSubscription(
		context.Background(),
		"acceptance-payment-user",
	)
	if err != nil || active == nil || active.PlanCode != plans.Standard {
		t.Fatalf("GetActiveSubscription() = %#v, %v", active, err)
	}
}
