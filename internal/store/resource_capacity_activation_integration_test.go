package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func resourceActivationQuota() QuotaPolicy {
	p := interactionQuotaPolicy()
	p.MarketProjectLimit = 1
	p.AllowedMarketRuleTypes = []string{"market_price_above"}
	return p
}

func resourceActivationProject(owner string) CreateMarketProjectParams {
	return CreateMarketProjectParams{DeBoxUserID: owner, ChainKey: "bsc", ChainID: 56,
		TokenAddress: "0x4444444444444444444444444444444444444444", TokenName: "Local capacity fixture", TokenSymbol: "LOCAL", TokenDecimals: 18}
}

// Capture application state, including nonzero baselines, unknown delivery
// evidence, configuration windows and project freeze timestamps. A rejected
// operation must roll back its whole candidate, not only its enabled count.
func resourceActivationSnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner string) string {
	t.Helper()
	wallet := interactionQuotaSnapshot(t, ctx, pool, owner, nil, nil)
	var market json.RawMessage
	if err := pool.QueryRow(ctx, `SELECT jsonb_build_object(
 'projects',COALESCE((SELECT jsonb_agg(to_jsonb(p) ORDER BY p.id) FROM market_projects p WHERE p.debox_user_id=$1),'[]'::jsonb),
 'rules',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM market_rules r WHERE r.debox_user_id=$1),'[]'::jsonb),
 'targets',COALESCE((SELECT jsonb_agg(to_jsonb(s) ORDER BY s.market_rule_id,s.target_key) FROM market_rule_target_states s JOIN market_rules r ON r.id=s.market_rule_id WHERE r.debox_user_id=$1),'[]'::jsonb),
 'deployments',COALESCE((SELECT jsonb_agg(to_jsonb(d) ORDER BY d.id) FROM market_project_deployments d JOIN market_projects p ON p.id=d.market_project_id WHERE p.debox_user_id=$1),'[]'::jsonb))`, owner).Scan(&market); err != nil {
		t.Fatal(err)
	}
	return wallet + "\n" + string(market)
}

func TestPostgresResourceActivationFreeAndPermanentGrowthRejectsAtomically(t *testing.T) {
	for _, verified := range []bool{true, false} {
		t.Run(map[bool]string{true: "full", false: "unverified"}[verified], func(t *testing.T) {
			db, pool := openIntegrationStore(t)
			ctx := context.Background()
			p := resourceCapacityTestPolicy()
			p.Verified = verified
			p.AccountMonthlyCU = 410 // reservations 400 + one existing ordinary rule 10.
			resourceCapacityWatchFixture(t, pool, "existing-owner", "balance_change", "standalone")
			db.resourceCapacity = &p
			want := ErrResourceCapacityReached
			if !verified {
				want = ErrResourceCapacityUnavailable
			}
			for _, permanent := range []bool{false, true} {
				owner := map[bool]string{false: "new-free-owner", true: "new-permanent-owner"}[permanent]
				quota := QuotaPolicy{PlanCode: "free", WalletLimit: 1, RuleLimit: 1, AllowedRuleTypes: []string{"balance_change"}}
				if permanent {
					resourceCapacitySubscriptionFixture(t, pool, owner, "professional", 1)
					quota = resourceActivationQuota()
				}
				before := resourceActivationSnapshot(t, ctx, pool, owner)
				params := interactionQuotaRule(owner, 801, "balance_change")
				if !permanent {
					params.DeliveryMode = "realtime" // the existing free-plan eligibility contract.
				}
				if _, err := db.CreateWatchRuleWithinQuota(ctx, params, quota); !errors.Is(err, want) {
					t.Fatalf("new %s rule error=%v want=%v", owner, err, want)
				}
				if after := resourceActivationSnapshot(t, ctx, pool, owner); after != before {
					t.Fatalf("rejected %s rule left preferences/baseline/window residue", owner)
				}
				if permanent {
					if _, err := db.CreateCombinationRuleWithinQuota(ctx, interactionQuotaCombination(owner, 810, "balance_change", "balance_change"), quota); !errors.Is(err, want) {
						t.Fatalf("new permanent combination error=%v want=%v", err, want)
					}
					if _, err := db.CreateMarketProjectWithinQuota(ctx, resourceActivationProject(owner), quota); !errors.Is(err, want) {
						t.Fatalf("new permanent project error=%v want=%v", err, want)
					}
					if after := resourceActivationSnapshot(t, ctx, pool, owner); after != before {
						t.Fatal("rejected permanent combination/project left nested members or project residue")
					}
				}
			}
			if status, err := db.ReadResourceCapacityStatus(ctx); err != nil || status.ReservedMonthlyCU != 10 || status.Users != 1 {
				t.Fatalf("rejected growth changed original reservations: %+v/%v", status, err)
			}
		})
	}
}

func TestPostgresResourceActivationPaidPromiseAllowsCoveredGrowthAndStopping(t *testing.T) {
	for _, verified := range []bool{true, false} {
		t.Run(map[bool]string{true: "over_budget", false: "unverified"}[verified], func(t *testing.T) {
			db, pool := openIntegrationStore(t)
			ctx := context.Background()
			const owner = "already-paid-owner"
			sub := resourceCapacitySubscriptionFixture(t, pool, owner, "professional", 0)
			order := resourceCapacityOrderFixture(t, pool, owner, "professional", "paid", false)
			resourceCapacityHoldFixture(t, pool, owner, "professional", order, &sub, 240)
			resourceCapacityWatchFixture(t, pool, "other-active-owner", "balance_change", "standalone")
			p := resourceCapacityTestPolicy()
			p.Verified, p.AccountMonthlyCU = verified, 640 // Net 240, existing commitments total 250.
			db.resourceCapacity = &p
			quota := resourceActivationQuota()
			rule := interactionQuotaCreateRule(t, ctx, db, owner, 820, "balance_change", quota)
			combo, err := db.CreateCombinationRuleWithinQuota(ctx, interactionQuotaCombination(owner, 830, "balance_change", "balance_change"), quota)
			if err != nil {
				t.Fatalf("already promised combination was blocked: %v", err)
			}
			project, err := db.CreateMarketProjectWithinQuota(ctx, resourceActivationProject(owner), quota)
			if err != nil {
				t.Fatalf("already promised market project was blocked: %v", err)
			}
			if status, err := db.ReadResourceCapacityStatus(ctx); err != nil || status.NetMonthlyCU != 240 || status.ReservedMonthlyCU != 250 {
				t.Fatalf("covered growth consumed a second plan reservation: %+v/%v", status, err)
			}
			if paused, err := db.PauseUserWatchRules(ctx, owner, nil); err != nil || paused != 3 {
				t.Fatalf("stop blocked under closed admission: %d/%v", paused, err)
			}
			if deleted, err := db.DeleteWatchRule(ctx, rule.ID, owner); err != nil || !deleted {
				t.Fatalf("standalone deletion blocked: %t/%v", deleted, err)
			}
			if deleted, err := db.DeleteCombinationRule(ctx, combo.ID, owner); err != nil || !deleted {
				t.Fatalf("combination deletion blocked: %t/%v", deleted, err)
			}
			if _, err := db.ArchiveMarketProject(ctx, project.ID, owner); err != nil {
				t.Fatalf("project stopping blocked: %v", err)
			}
			if err := db.DeleteArchivedMarketProject(ctx, project.ID, owner); err != nil {
				t.Fatalf("project deletion blocked: %v", err)
			}
		})
	}
}

func TestPostgresResourceActivationReconcileSkipsUnavailableCandidatesWithoutChangingState(t *testing.T) {
	for _, verified := range []bool{true, false} {
		t.Run(map[bool]string{true: "full", false: "unverified"}[verified], func(t *testing.T) {
			db, pool := openIntegrationStore(t)
			ctx := context.Background()
			const owner = "paused-permanent-owner"
			resourceCapacitySubscriptionFixture(t, pool, owner, "professional", 1)
			quota := resourceActivationQuota()
			rule := interactionQuotaCreateRule(t, ctx, db, owner, 840, "balance_change", quota)
			combo, err := db.CreateCombinationRuleWithinQuota(ctx, interactionQuotaCombination(owner, 850, "balance_change", "balance_change"), quota)
			if err != nil {
				t.Fatal(err)
			}
			project, err := db.CreateMarketProjectWithinQuota(ctx, resourceActivationProject(owner), quota)
			if err != nil {
				t.Fatal(err)
			}
			marketRule, err := db.CreateMarketRuleWithinQuota(ctx, CreateMarketRuleParams{DeBoxUserID: owner,
				MarketProjectID: project.ID, RuleType: "market_price_above", ThresholdValue: "1", ThresholdUnit: "usd",
				NotificationChatID: owner, State: json.RawMessage(`{"last_event_id":9007199254740993,"condition_active":true}`)}, quota)
			if err != nil {
				t.Fatal(err)
			}
			interactionQuotaPauseRule(t, ctx, pool, rule)
			interactionQuotaPauseCombination(t, ctx, pool, combo)
			if _, err := pool.Exec(ctx, `UPDATE market_projects SET status='paused',pause_reason='subscription_expired',frozen_at='2026-01-02T03:04:05Z' WHERE id=$1`, project.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE market_rules SET run_status='paused',pause_reason='subscription_expired',last_triggered_at='2026-01-02T03:04:05Z',aggregation_anchor_at='2026-01-02T02:00:00Z' WHERE id=$1`, marketRule.ID); err != nil {
				t.Fatal(err)
			}
			resourceCapacityWatchFixture(t, pool, "occupying-owner", "balance_change", "standalone")
			p := resourceCapacityTestPolicy()
			p.Verified, p.AccountMonthlyCU = verified, 410
			db.resourceCapacity = &p
			before := resourceActivationSnapshot(t, ctx, pool, owner)
			result, err := db.ReconcileUserEntitlements(ctx, owner, quota)
			if err != nil || result != (EntitlementReconcileResult{}) {
				t.Fatalf("normal admission rejection must skip candidates, not fail entitlement: %+v/%v", result, err)
			}
			if after := resourceActivationSnapshot(t, ctx, pool, owner); after != before {
				t.Fatal("capacity skip changed paused baseline, cooldown, window, delivery evidence or project freeze state")
			}
		})
	}
}

func TestPostgresResourceActivationPermanentIdentityBindingRemainsAvailable(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	const wallet = "0x0000000000000000000000000000000000000860"
	if _, err := pool.Exec(ctx, `INSERT INTO permanent_plan_allowlist(wallet_address,plan_code) VALUES($1,'professional')`, wallet); err != nil {
		t.Fatal(err)
	}
	p := resourceCapacityTestPolicy()
	p.Verified = false
	p.AccountMonthlyCU = 400 // no sellable capacity and no verified allocation.
	db.resourceCapacity = &p
	bound, err := db.BindPermanentPlan(ctx, "permanent-login-owner", wallet)
	if err != nil || bound.Subscription == nil || bound.Subscription.IsPermanent != 1 || bound.Subscription.PlanCode != "professional" {
		t.Fatalf("identity binding was capacity gated: %+v/%v", bound, err)
	}
	if _, err := db.CreateWatchRuleWithinQuota(ctx, interactionQuotaRule("permanent-login-owner", 861, "balance_change"), resourceActivationQuota()); !errors.Is(err, ErrResourceCapacityUnavailable) {
		t.Fatalf("identity binding accidentally granted unverified monitoring capacity: %v", err)
	}
}
