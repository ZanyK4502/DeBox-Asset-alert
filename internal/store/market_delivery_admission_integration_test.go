package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationdelivery"
)

func TestPostgresMarketAdmissionDeferralRefundsOnlyFencedKnownUnsentAttempt(t *testing.T) {
	for _, kind := range []string{"realtime", "stage", "combination"} {
		t.Run(kind, func(t *testing.T) {
			database, pool, rule := marketDeliveryFixture(t, kind == "stage")
			ctx := context.Background()
			mode := "pending"
			if kind == "stage" {
				mode = "staged"
			}
			var other MarketRule
			if kind == "combination" {
				mode = "combined"
				params := CreateMarketRuleParams{DeBoxUserID: rule.DeBoxUserID, MarketProjectID: rule.MarketProjectID, RuleType: "market_large_sell", ThresholdValue: "1", ThresholdUnit: "token", RuleScope: "combination"}
				normalizeMarketRuleParams(&params)
				var err error
				other, err = createMarketRule(ctx, pool, params)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, `UPDATE market_rules SET rule_scope='combination' WHERE id=$1`, rule.ID); err != nil {
					t.Fatal(err)
				}
				var combinationID int64
				if err := pool.QueryRow(ctx, `INSERT INTO market_combination_rules(debox_user_id,cycle_type,cycle_minutes,notification_chat_id) VALUES($1,'follow',60,$1) RETURNING id`, rule.DeBoxUserID).Scan(&combinationID); err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, `INSERT INTO market_combination_members(market_combination_rule_id,source_type,market_rule_id) VALUES($1,'market',$2),($1,'market',$3)`, combinationID, rule.ID, other.ID); err != nil {
					t.Fatal(err)
				}
			}
			event := marketDeliveryFact(t, database, "admission-source", time.Now().UTC())
			marketDeliveryTrigger(t, database, rule, event, mode)
			if kind == "combination" {
				second := marketDeliveryFact(t, database, "admission-second-source", time.Now().UTC())
				marketDeliveryTrigger(t, database, other, second, mode)
			}
			claim := claimMarketDelivery(t, database, kind)
			frozen := prepareMarketDelivery(t, database, claim, rule.DeBoxUserID)
			raw, _ := json.Marshal(frozen)
			table, _ := marketDeliveryTable(kind)
			at := time.Now().UTC().Add(time.Minute)
			assertWaiting := func(attempts int) {
				t.Helper()
				var ok bool
				if err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT notification_status='pending' AND notification_attempts=$2 AND notification_send_started_at IS NULL AND notification_lease_expires_at IS NULL AND next_attempt_at>NOW()+INTERVAL '50 seconds' AND notification_payload=$3::jsonb FROM %s WHERE id=$1`, table), claim.ID, attempts, string(raw)).Scan(&ok); err != nil || !ok {
					t.Fatal("admission deferral lost attempts/frozen payload/wait", ok, err)
				}
			}
			if err := database.DeferMarketDeliveryAdmission(ctx, claim, at, false); err != nil {
				t.Fatal(err)
			}
			assertWaiting(0)
			if claims, err := database.ClaimMarketDeliveries(ctx, 200); err != nil || len(claims) != 0 {
				t.Fatal("deferred work was immediately reclaimed", claims, err)
			}
			if err := database.DeferMarketDeliveryAdmission(ctx, claim, at, false); !errors.Is(err, ErrMarketDeliveryLeaseLost) {
				t.Fatal("old claim refunded twice", err)
			}
			nextClaim := func() MarketDeliveryClaim {
				t.Helper()
				if _, err := pool.Exec(ctx, fmt.Sprintf(`UPDATE %s SET next_attempt_at=NOW() WHERE id=$1`, table), claim.ID); err != nil {
					t.Fatal(err)
				}
				return claimMarketDelivery(t, database, kind)
			}
			claim = nextClaim()
			if err := database.BeginMarketDeliverySend(ctx, claim); err != nil {
				t.Fatal(err)
			}
			if err := database.DeferMarketDeliveryAdmission(ctx, claim, at, false); !errors.Is(err, ErrMarketDeliveryLeaseLost) {
				t.Fatal("before-Begin branch modified a started attempt", err)
			}
			if err := database.DeferMarketDeliveryAdmission(ctx, claim, at, true); err != nil {
				t.Fatal(err)
			}
			assertWaiting(0)
			claim = nextClaim()
			if err := database.BeginMarketDeliverySend(ctx, claim); err != nil {
				t.Fatal(err)
			}
			if err := database.CompleteMarketDeliveryResult(ctx, claim, nil, &notificationdelivery.Failure{Kind: notificationdelivery.Retryable, Code: "real_HTTP_rejection"}); err != nil {
				t.Fatal(err)
			}
			claim = nextClaim()
			if err := database.BeginMarketDeliverySend(ctx, claim); err != nil {
				t.Fatal(err)
			}
			if err := database.DeferMarketDeliveryAdmission(ctx, claim, at, true); err != nil {
				t.Fatal(err)
			}
			assertWaiting(1)
			claim = nextClaim()
			if err := database.BeginMarketDeliverySend(ctx, claim); err != nil {
				t.Fatal(err)
			}
			if err := database.CompleteMarketDeliveryResult(ctx, claim, nil, &notificationdelivery.Failure{Kind: notificationdelivery.Unknown, Code: "Do_started_result_unknown"}); err != nil {
				t.Fatal(err)
			}
			if err := database.DeferMarketDeliveryAdmission(ctx, claim, at, true); !errors.Is(err, ErrMarketDeliveryLeaseLost) {
				t.Fatal("unknown send was refunded into an automatic retry", err)
			}
			var preserved bool
			if err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT delivery_outcome='unknown' AND notification_attempts=2 AND notification_payload=$2::jsonb FROM %s WHERE id=$1`, table), claim.ID, string(raw)).Scan(&preserved); err != nil || !preserved {
				t.Fatal("unknown evidence or real attempt was changed", preserved, err)
			}
		})
	}
}
