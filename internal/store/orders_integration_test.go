package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestPostgresOrderClaimsRequireOriginalOwnerAndPayer(t *testing.T) {
	database, _ := openIntegrationStore(t)
	ctx := context.Background()
	payer := "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd"
	otherPayer := "0x1111111111111111111111111111111111111111"
	token := "0x55d398326f99059ff775485246999027b3197955"

	for index, status := range []string{"pending", "confirming", "paid"} {
		t.Run(status, func(t *testing.T) {
			owner := "order-owner-" + status
			hash := "0x" + strings.Repeat(string(rune('a'+index)), 64)
			order, err := database.CreateOrder(ctx, CreateOrderParams{
				DeBoxUserID:      owner,
				PayerAddress:     payer,
				PlanCode:         "standard",
				BillingCycle:     "monthly",
				SubscriptionDays: 30,
				ChainKey:         "bsc",
				ChainID:          56,
				TokenAddress:     &token,
				TokenSymbol:      "USDT",
				TokenDecimals:    18,
				TotalAmount:      "10",
				RecipientAddress: otherPayer,
			})
			if err != nil {
				t.Fatalf("CreateOrder(): %v", err)
			}
			if status != "pending" {
				if _, err := database.ClaimOrderTransaction(ctx, order.ID, owner, payer, hash); err != nil {
					t.Fatalf("initial ClaimOrderTransaction(): %v", err)
				}
			}
			if status == "paid" {
				if _, err := database.FinalizePaidOrder(ctx, order.ID, hash, 100, 3); err != nil {
					t.Fatalf("FinalizePaidOrder(): %v", err)
				}
			}
			before, err := database.GetOrder(ctx, order.ID)
			if err != nil || before == nil || before.Status != status {
				t.Fatalf("GetOrder() before claims = %#v, %v", before, err)
			}
			type claimAttempt struct {
				name  string
				owner string
				payer string
				hash  string
			}
			attempts := []claimAttempt{
				{"different owner", "another-user", payer, hash},
				{"different payer", owner, otherPayer, hash},
				{"different owner and payer", "another-user", otherPayer, hash},
			}
			if status != "pending" {
				attempts = append(attempts, claimAttempt{
					"different hash", owner, payer, "0x" + strings.Repeat("f", 64),
				})
			}
			for _, attempt := range attempts {
				t.Run(attempt.name, func(t *testing.T) {
					claimed, err := database.ClaimOrderTransaction(
						ctx, order.ID, attempt.owner, attempt.payer, attempt.hash,
					)
					if !errors.Is(err, ErrOrderInvalid) {
						t.Fatalf("ClaimOrderTransaction() error = %v, want ErrOrderInvalid", err)
					}
					if !reflect.DeepEqual(claimed, Order{}) {
						t.Fatalf("ClaimOrderTransaction() disclosed an order: %#v", claimed)
					}
					after, err := database.GetOrder(ctx, order.ID)
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatalf("rejected claim changed order: before=%#v, after=%#v, err=%v", before, after, err)
					}
				})
			}

			casePayer := "0x" + strings.ToUpper(payer[2:])
			caseHash := "0x" + strings.ToUpper(hash[2:])
			for range 2 {
				claimed, err := database.ClaimOrderTransaction(ctx, order.ID, owner, casePayer, caseHash)
				if err != nil {
					t.Fatalf("owner ClaimOrderTransaction(): %v", err)
				}
				wantStatus := "confirming"
				if status == "paid" {
					wantStatus = "paid"
				}
				if claimed.ID != order.ID || claimed.DeBoxUserID != owner ||
					claimed.Status != wantStatus || claimed.TxHash == nil ||
					!strings.EqualFold(*claimed.TxHash, hash) {
					t.Fatalf("unexpected owner claim: %#v", claimed)
				}
			}
			if status == "paid" {
				after, err := database.GetOrder(ctx, order.ID)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("idempotent paid claim changed order: before=%#v, after=%#v, err=%v", before, after, err)
				}
			}
		})
	}
}
