package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func holderTargetQuota() QuotaPolicy {
	return QuotaPolicy{
		PlanCode: "professional", RuleLimit: 10, MarketProjectLimit: 5,
		AllowedMarketRuleTypes: []string{"market_holder_increase", "market_holder_decrease", "market_holder_rank_exited"},
		MultiPoolMonitoring:    true, CombinationRules: true,
	}
}

func holderTargetProject(t *testing.T, db *Store, owner, chain string, chainID int64, token int) MarketProject {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ActivateSubscription(ctx, owner, "professional", 30); err != nil {
		t.Fatal(err)
	}
	project, err := db.CreateMarketProjectWithinQuota(ctx, CreateMarketProjectParams{
		DeBoxUserID: owner, ChainKey: chain, ChainID: chainID, TokenAddress: fmt.Sprintf("0x%040x", token),
	}, holderTargetQuota())
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func holderTargetRule(project MarketProject) CreateMarketRuleParams {
	return CreateMarketRuleParams{
		DeBoxUserID: project.DeBoxUserID, MarketProjectID: project.ID,
		RuleType: "market_holder_rank_exited", ThresholdValue: "10", ThresholdUnit: "count",
	}
}

func fillHolderTargetSlots(t *testing.T, db *Store, count int) []MarketRule {
	t.Helper()
	rules := make([]MarketRule, count)
	for i := range count {
		project := holderTargetProject(t, db, fmt.Sprintf("holder-target-%d", i), "bsc", 56, 400+i)
		rule, err := db.CreateMarketRuleWithinQuota(context.Background(), holderTargetRule(project), holderTargetQuota())
		if err != nil {
			t.Fatal(err)
		}
		rules[i] = rule
	}
	return rules
}

func TestPostgresHolderTargetAdmissionSerializesDistinctTargetsAndReleasesLastOwner(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	rules := fillHolderTargetSlots(t, db, MarketHolderTargetLimit-1)
	otherChain := holderTargetProject(t, db, "holder-target-same-token-different-chain", "base", 8453, 400)
	newToken := holderTargetProject(t, db, "holder-target-new-token", "bsc", 56, 500)
	type attempt struct {
		project MarketProject
		err     error
	}
	attempts := make(chan attempt, 2)
	start := make(chan struct{})
	for _, project := range []MarketProject{otherChain, newToken} {
		go func() {
			<-start
			_, err := db.CreateMarketRuleWithinQuota(ctx, holderTargetRule(project), holderTargetQuota())
			attempts <- attempt{project, err}
		}()
	}
	close(start)
	var rejected MarketProject
	var successes int
	for range 2 {
		result := <-attempts
		switch {
		case result.err == nil:
			successes++
		case errors.Is(result.err, ErrMarketHolderTargetsReached):
			rejected = result.project
		default:
			t.Fatal(result.err)
		}
	}
	if successes != 1 || rejected.ID == 0 {
		t.Fatalf("concurrent admissions succeeded %d times", successes)
	}
	assertProjectedCount := func(want int64) {
		t.Helper()
		if count, err := db.countMarketHolderTargets(ctx, db.db); err != nil || count != want {
			t.Fatalf("active holder targets=%d, want %d, error=%v", count, want, err)
		}
	}
	assertProjectedCount(MarketHolderTargetLimit)
	shared := holderTargetProject(t, db, "holder-target-shared-owner", "bsc", 56, 400)
	sharedRule, err := db.CreateMarketRuleWithinQuota(ctx, holderTargetRule(shared), holderTargetQuota())
	if err != nil {
		t.Fatalf("sharing an existing target at capacity failed: %v", err)
	}
	first := rules[0]
	if deleted, err := db.DeleteMarketRule(ctx, first.ID, first.DeBoxUserID); err != nil || !deleted {
		t.Fatalf("delete first shared rule: %t, %v", deleted, err)
	}
	assertProjectedCount(MarketHolderTargetLimit)
	if _, err := db.CreateMarketRuleWithinQuota(ctx, holderTargetRule(rejected), holderTargetQuota()); !errors.Is(err, ErrMarketHolderTargetsReached) {
		t.Fatalf("remaining shared owner lost its slot: %v", err)
	}
	if deleted, err := db.DeleteMarketRule(ctx, sharedRule.ID, sharedRule.DeBoxUserID); err != nil || !deleted {
		t.Fatalf("delete last shared rule: %t, %v", deleted, err)
	}
	assertProjectedCount(MarketHolderTargetLimit - 1)
	if _, err := db.CreateMarketRuleWithinQuota(ctx, holderTargetRule(rejected), holderTargetQuota()); err != nil {
		t.Fatalf("released slot not available: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE subscriptions SET expires_at=NOW()-INTERVAL '1 second' WHERE debox_user_id=$1`, rules[1].DeBoxUserID); err != nil {
		t.Fatal(err)
	}
	assertProjectedCount(MarketHolderTargetLimit - 1)
}

func TestPostgresHolderTargetAdmissionCoversRuleProjectAndCombinationRestore(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	rules := fillHolderTargetSlots(t, db, MarketHolderTargetLimit)
	project := holderTargetProject(t, db, "holder-target-restores", "bsc", 56, 550)
	params := holderTargetRule(project)
	params.RuleScope = "combination"
	first, err := db.CreateMarketRuleWithinQuota(ctx, params, holderTargetQuota())
	if err != nil {
		t.Fatal(err)
	}
	params.RuleType, params.ThresholdUnit = "market_holder_increase", "percent"
	second, err := db.CreateMarketRuleWithinQuota(ctx, params, holderTargetQuota())
	if err != nil {
		t.Fatal(err)
	}
	combinationParams := CreateMarketCombinationParams{
		DeBoxUserID: project.DeBoxUserID, Members: []CreateMarketCombinationMemberParams{
			{SourceType: "market", MarketRuleID: &first.ID}, {SourceType: "market", MarketRuleID: &second.ID},
		},
	}
	if _, err := db.CreateMarketCombinationWithinQuota(ctx, combinationParams, holderTargetQuota()); !errors.Is(err, ErrMarketHolderTargetsReached) {
		t.Fatalf("active combination introduced another target: %v", err)
	}
	var combinationCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_combination_rules WHERE debox_user_id=$1`, project.DeBoxUserID).Scan(&combinationCount); err != nil || combinationCount != 0 {
		t.Fatalf("blocked combination left data: %d, %v", combinationCount, err)
	}
	if _, err := db.SetMarketProjectStatus(ctx, rules[0].MarketProjectID, rules[0].DeBoxUserID, "paused", "user_paused"); err != nil {
		t.Fatal(err)
	}
	combination, err := db.CreateMarketCombinationWithinQuota(ctx, combinationParams, holderTargetQuota())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RestoreMarketProjectWithinQuota(ctx, rules[0].MarketProjectID, rules[0].DeBoxUserID, holderTargetQuota()); !errors.Is(err, ErrMarketHolderTargetsReached) {
		t.Fatalf("project restore bypassed holder limit: %v", err)
	}
	if _, err := db.SetMarketProjectStatus(ctx, rules[0].MarketProjectID, rules[0].DeBoxUserID, "active", ""); !errors.Is(err, ErrMarketHolderTargetsReached) {
		t.Fatalf("project activation bypassed holder limit: %v", err)
	}
	if _, err := db.ArchiveMarketCombinationRule(ctx, combination.ID, project.DeBoxUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RestoreMarketProjectWithinQuota(ctx, rules[0].MarketProjectID, rules[0].DeBoxUserID, holderTargetQuota()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RestoreMarketCombinationWithinQuota(ctx, combination.ID, project.DeBoxUserID, holderTargetQuota()); !errors.Is(err, ErrMarketHolderTargetsReached) {
		t.Fatalf("combination restore bypassed holder limit: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE market_rules SET rule_scope='standalone',run_status='paused',pause_reason='subscription_expired' WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RestoreMarketRuleWithinQuota(ctx, first.ID, project.DeBoxUserID, holderTargetQuota()); !errors.Is(err, ErrMarketHolderTargetsReached) {
		t.Fatalf("rule restore bypassed holder limit: %v", err)
	}
	restored, err := db.ReconcileUserEntitlements(ctx, project.DeBoxUserID, holderTargetQuota())
	if err != nil || restored.MarketRulesRestored != 0 {
		t.Fatalf("full capacity automatic restoration failed instead of preserving paused rule: %+v, %v", restored, err)
	}
	rule, err := db.GetMarketRule(ctx, first.ID, project.DeBoxUserID)
	if err != nil || rule == nil || rule.RunStatus != "paused" || rule.PauseReason != "subscription_expired" {
		t.Fatalf("blocked restores changed saved rule: %+v, %v", rule, err)
	}
}

func TestPostgresHolderTargetAdmissionCountsOnlySelectedDeployments(t *testing.T) {
	db, _ := openIntegrationStore(t)
	ctx := context.Background()
	fillHolderTargetSlots(t, db, MarketHolderTargetLimit-1)
	const owner = "holder-target-selected-deployment"
	if _, err := db.ActivateSubscription(ctx, owner, "professional", 30); err != nil {
		t.Fatal(err)
	}
	bscToken, baseToken := fmt.Sprintf("0x%040x", 600), fmt.Sprintf("0x%040x", 601)
	bscPool := createWizardIntegrationPool(t, db, "bsc", 56, bscToken, fmt.Sprintf("0x%040x", 610), fmt.Sprintf("0x%040x", 611))
	basePool := createWizardIntegrationPool(t, db, "base", 8453, baseToken, fmt.Sprintf("0x%040x", 612), fmt.Sprintf("0x%040x", 613))
	at := time.Now().UTC()
	project, err := db.CreateMultiChainMarketProjectWithinQuota(ctx, CreateMultiChainMarketProjectParams{
		DeBoxUserID: owner, CanonicalName: "Selected holder target", Symbol: "SHT",
		IdentitySource: "coingecko", CanonicalAssetID: "selected-holder-target", VerificationStatus: "verified",
		Deployments: []CreateMultiChainMarketDeploymentParams{
			wizardIntegrationDeployment("bsc", 56, bscToken, bscPool.ID, at),
			wizardIntegrationDeployment("base", 8453, baseToken, basePool.ID, at),
		},
	}, holderTargetQuota())
	if err != nil {
		t.Fatal(err)
	}
	params := holderTargetRule(project)
	if _, err := db.CreateMarketRuleWithinQuota(ctx, params, holderTargetQuota()); !errors.Is(err, ErrMarketHolderTargetsReached) {
		t.Fatalf("all-deployment rule grew past six: %v", err)
	}
	deployments, err := db.ListMarketProjectDeploymentViews(ctx, project.ID, owner)
	if err != nil || len(deployments) != 2 {
		t.Fatalf("project deployments=%+v err=%v", deployments, err)
	}
	params.DeploymentScope = "selected"
	params.MarketProjectDeploymentIDs = []int64{deployments[0].ID}
	if _, err := db.CreateMarketRuleWithinQuota(ctx, params, holderTargetQuota()); err != nil {
		t.Fatalf("one selected deployment should fit: %v", err)
	}
	params.MarketProjectDeploymentIDs = []int64{deployments[1].ID}
	if _, err := db.CreateMarketRuleWithinQuota(ctx, params, holderTargetQuota()); !errors.Is(err, ErrMarketHolderTargetsReached) {
		t.Fatalf("second selected deployment grew past six: %v", err)
	}
}

func TestPostgresHolderTargetAdmissionAllowsPaymentWithPausedHolderRestoration(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	rules := fillHolderTargetSlots(t, db, MarketHolderTargetLimit)
	project := holderTargetProject(t, db, "holder-target-paid-reactivation", "bsc", 56, 650)
	params := holderTargetRule(project)
	normalizeMarketRuleParams(&params)
	// Simulate an old config written before the target limit. The lapsed
	// owner's subscription is ineligible even before the cleanup task runs.
	legacy, err := createMarketRule(ctx, db.db, params)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE subscriptions SET expires_at=NOW()-INTERVAL '1 second' WHERE debox_user_id=$1`, project.DeBoxUserID); err != nil {
		t.Fatal(err)
	}
	orderParams := resourceAdmissionOrderParams(project.DeBoxUserID, "professional")
	order, err := db.CreateOrder(ctx, orderParams)
	if err != nil {
		t.Fatal(err)
	}
	txHash := fmt.Sprintf("0x%064x", 651)
	if _, err := db.ClaimOrderTransaction(ctx, order.ID, orderParams.DeBoxUserID, orderParams.PayerAddress, txHash); err != nil {
		t.Fatal(err)
	}
	finalized, err := db.FinalizePaidOrder(ctx, order.ID, txHash, 100, 3)
	if err != nil || finalized.Order.Status != "paid" || finalized.Subscription == nil || finalized.Subscription.Status != "active" {
		t.Fatalf("holder cap blocked paid checkout: %+v, %v", finalized, err)
	}
	restored, err := db.ReconcileUserEntitlements(ctx, project.DeBoxUserID, holderTargetQuota())
	if err != nil || restored.ProjectsRestored != 1 || restored.MarketRulesRestored != 0 {
		t.Fatalf("paid holder target above quota was not deferred: %+v, %v", restored, err)
	}
	rule, err := db.GetMarketRule(ctx, legacy.ID, project.DeBoxUserID)
	if err != nil || rule == nil || rule.RunStatus != "paused" {
		t.Fatalf("paid checkout changed old rule or enabled seventh target: %+v, %v", rule, err)
	}
	first := rules[0]
	if deleted, err := db.DeleteMarketRule(ctx, first.ID, first.DeBoxUserID); err != nil || !deleted {
		t.Fatalf("release previously active slot: %t, %v", deleted, err)
	}
	restored, err = db.ReconcileUserEntitlements(ctx, project.DeBoxUserID, holderTargetQuota())
	if err != nil || restored.MarketRulesRestored != 1 {
		t.Fatalf("deferred paid rule failed to restore after release: %+v, %v", restored, err)
	}
}

func TestPostgresHolderTargetAdmissionRenewalAcrossExpiryPreservesTerm(t *testing.T) {
	db, pool := openIntegrationStore(t)
	ctx := context.Background()
	fillHolderTargetSlots(t, db, MarketHolderTargetLimit-1)
	renewing := holderTargetProject(t, db, "holder-target-renewing-at-expiry", "bsc", 56, 660)
	replacement := holderTargetProject(t, db, "holder-target-expiry-replacement", "bsc", 56, 661)
	original, err := db.CreateMarketRuleWithinQuota(ctx, holderTargetRule(renewing), holderTargetQuota())
	if err != nil {
		t.Fatal(err)
	}
	var expiry time.Time
	if err := pool.QueryRow(ctx, `UPDATE subscriptions SET expires_at=clock_timestamp()+INTERVAL '1 second'
 WHERE debox_user_id=$1 RETURNING expires_at`, renewing.DeBoxUserID).Scan(&expiry); err != nil {
		t.Fatal(err)
	}
	renewal, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer renewal.Rollback(ctx)
	var started time.Time
	if err := renewal.QueryRow(ctx, `SELECT now()`).Scan(&started); err != nil || !started.Before(expiry) {
		t.Fatalf("test did not start a renewal before expiry: %v, %v", started, err)
	}
	if _, err := renewal.Exec(ctx, `SELECT pg_sleep(GREATEST(0, EXTRACT(epoch FROM $1::timestamptz-clock_timestamp()))+0.01)`, expiry); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateMarketRuleWithinQuota(ctx, holderTargetRule(replacement), holderTargetQuota()); err != nil {
		t.Fatalf("expired holder slot should become available before older renewal resumes: %v", err)
	}
	value, err := db.activateSubscription(ctx, renewal, renewing.DeBoxUserID, "professional", 30, true)
	if err != nil || !value.ExpiresAt.Equal(expiry.AddDate(0, 0, 30)) {
		t.Fatalf("boundary holder race changed original renewal term: %+v, %v", value, err)
	}
	if err := renewal.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	rule, err := db.GetMarketRule(ctx, original.ID, renewing.DeBoxUserID)
	if err != nil || rule == nil || rule.RunStatus != "paused" || rule.PauseReason != "subscription_expired" {
		t.Fatalf("boundary renewal reactivated its lost holder slot: %+v, %v", rule, err)
	}
	if count, err := db.countMarketHolderTargets(ctx, pool); err != nil || count != MarketHolderTargetLimit {
		t.Fatalf("renewal admitted extra target: %d %v", count, err)
	}
}
