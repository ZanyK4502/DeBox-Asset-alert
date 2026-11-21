package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/testdb"
)

// Use the real catalog and service policy rather than a hand-written legacy
// quota, then combine wallet, combination-member and market slots in Store.
func TestPostgresBasic10RealCatalogSharedQuota(t *testing.T) {
	db := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	catalog, err := plans.NewCatalog("10", 30, "USDT")
	if err != nil {
		t.Fatal(err)
	}
	service := New(db, catalog)
	for _, code := range []string{plans.Free, plans.Standard, plans.Professional} {
		t.Run(code, func(t *testing.T) {
			owner := "basic10-real-catalog-" + code
			if code != plans.Free {
				if _, err := db.ActivateSubscription(ctx, owner, code, 30); err != nil {
					t.Fatal(err)
				}
			}
			plan, err := service.ActivePlan(ctx, owner)
			if err != nil {
				t.Fatal(err)
			}
			wantLimit := map[string]int{plans.Free: 1, plans.Standard: 3, plans.Professional: 10}[code]
			if plan.RuleLimit != wantLimit {
				t.Fatalf("catalog limit=%d want=%d", plan.RuleLimit, wantLimit)
			}
			previous := "9007199254740993"
			ruleParams := func(index int, kind string) store.CreateWatchRuleParams {
				p := store.CreateWatchRuleParams{DeBoxUserID: owner, ChainKey: "bsc", ChainID: 56,
					WalletAddress: "0x1111111111111111111111111111111111111111", RuleType: kind,
					Threshold: fmt.Sprint(index), LastValue: &previous,
					NotificationChatID: owner, NotificationChatType: "private"}
				if kind == plans.AddressInteraction {
					target := "0x2222222222222222222222222222222222222222"
					p.TargetAddress = &target
				}
				return p
			}
			walletSlots := wantLimit
			if code != plans.Free {
				project, err := service.CreateMarketProject(ctx, store.CreateMarketProjectParams{DeBoxUserID: owner,
					ChainKey: "bsc", ChainID: 56, TokenAddress: "0x3333333333333333333333333333333333333333", TokenDecimals: 18})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := service.CreateMarketRule(ctx, store.CreateMarketRuleParams{DeBoxUserID: owner, MarketProjectID: project.ID,
					RuleType: plans.MarketPriceAbove, ThresholdValue: "1", ThresholdUnit: "usd", NotificationChatID: owner}); err != nil {
					t.Fatal(err)
				}
				walletSlots--
				// Project capacities come from the real catalog: Standard stays1,
				// Professional admits1..5 without expanding the shared rule limit.
				projects := []store.MarketProject{project}
				for index := 1; index < plan.MarketProjectLimit; index++ {
					added, err := service.CreateMarketProject(ctx, store.CreateMarketProjectParams{DeBoxUserID: owner,
						ChainKey: "bsc", ChainID: 56, TokenAddress: fmt.Sprintf("0x%040x", 7000+index)})
					if err != nil {
						t.Fatalf("project%d: %v", index+1, err)
					}
					projects = append(projects, added)
				}
				if _, err := service.CreateMarketProject(ctx, store.CreateMarketProjectParams{DeBoxUserID: owner,
					ChainKey: "bsc", ChainID: 56, TokenAddress: fmt.Sprintf("0x%040x", 7100)}); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%d 个持续监控代币", plan.MarketProjectLimit)) {
					t.Fatalf("project beyond catalog limit error=%v", err)
				}
				if code == plans.Professional {
					last := projects[len(projects)-1]
					archived, err := db.ArchiveMarketProject(ctx, last.ID, owner)
					if err != nil {
						t.Fatal(err)
					}
					replacement, err := service.CreateMarketProject(ctx, store.CreateMarketProjectParams{DeBoxUserID: owner, ChainKey: "bsc", ChainID: 56, TokenAddress: fmt.Sprintf("0x%040x", 7200)})
					if err != nil {
						t.Fatal("archived project did not release active slot", err)
					}
					if _, err = service.RestoreMarketProject(ctx, owner, last.ID); err == nil || !strings.Contains(err.Error(), "5 个持续监控代币") {
						t.Fatal("restore exceeded five projects", err)
					}
					unchanged, err := db.GetMarketProject(ctx, last.ID, owner)
					if err != nil || unchanged == nil {
						t.Fatal(err)
					}
					beforeJSON, _ := json.Marshal(archived)
					afterJSON, _ := json.Marshal(unchanged)
					if string(beforeJSON) != string(afterJSON) {
						t.Fatal("refused restore changed archived project")
					}
					if _, err = db.ArchiveMarketProject(ctx, replacement.ID, owner); err != nil {
						t.Fatal(err)
					}
					if _, err = service.RestoreMarketProject(ctx, owner, last.ID); err != nil {
						t.Fatal("fifth project did not restore after slot released", err)
					}
					listed, err := db.ListMarketProjects(ctx, owner, true)
					if err != nil {
						t.Fatal(err)
					}
					active := 0
					for _, p := range listed {
						if p.Status == "active" {
							active++
						}
					}
					if active != 5 || len(listed) != 6 {
						t.Fatalf("active/history project counts=%d/%d", active, len(listed))
					}
				}
				if _, err := service.CreateMarketProject(ctx, store.CreateMarketProjectParams{DeBoxUserID: owner,
					ChainKey: "base", ChainID: 8453, TokenAddress: "0x5555555555555555555555555555555555555555"}); !errors.Is(err, store.ErrMarketChainNotAllowed) {
					t.Fatalf("unsupported monitoring chain error=%v", err)
				}
			}
			var preserveIDs []int64
			if code == plans.Professional {
				combo, err := service.CreateCombinationRule(ctx, store.CreateCombinationRuleParams{DeBoxUserID: owner,
					CycleType: "fixed", CycleMinutes: 60, NotificationChatID: owner, NotificationChatType: "private",
					Members: []store.CreateCombinationMemberParams{
						{Rule: ruleParams(101, plans.AddressInteraction), RequiredTriggerCount: 2},
						{Rule: ruleParams(102, plans.BalanceChange), RequiredTriggerCount: 3},
					}})
				if err != nil {
					t.Fatal(err)
				}
				for _, member := range combo.Members {
					preserveIDs = append(preserveIDs, member.WatchRuleID)
				}
				// The existing combination contract uses one parent slot plus
				// two member slots; interaction occupies one of those members.
				walletSlots -= 3
				if _, err := service.CreateWatchRule(ctx, ruleParams(103, plans.AddressInteraction)); err == nil || !strings.Contains(err.Error(), "1 条指定地址交互") {
					t.Fatalf("combination member bypassed interaction cap: %v", err)
				}
			}
			for index := 0; index < walletSlots; index++ {
				rule, err := service.CreateWatchRule(ctx, ruleParams(index, plans.BalanceChange))
				if err != nil {
					t.Fatal(err)
				}
				preserveIDs = append(preserveIDs, rule.ID)
			}
			before, err := service.Entitlement(ctx, owner)
			if err != nil || before.RuleCount != int64(wantLimit) || before.InteractionRuleCount != map[string]int64{plans.Free: 0, plans.Standard: 0, plans.Professional: 1}[code] {
				t.Fatalf("mixed entitlement=%+v error=%v", before, err)
			}
			beforeRules, err := db.ListUserWatchRules(ctx, owner)
			if err != nil {
				t.Fatal(err)
			}
			beforeJSON, _ := json.Marshal(beforeRules)
			if _, err := service.CreateWatchRule(ctx, ruleParams(999, plans.BalanceChange)); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%d 条规则", wantLimit)) {
				t.Fatalf("shared cap was bypassed: %v", err)
			}
			afterRules, err := db.ListUserWatchRules(ctx, owner)
			if err != nil {
				t.Fatal(err)
			}
			afterJSON, _ := json.Marshal(afterRules)
			if string(beforeJSON) != string(afterJSON) {
				t.Fatal("rejected growth changed existing wallet rules")
			}
			for _, id := range preserveIDs {
				rule, err := db.GetWatchRule(ctx, id, owner)
				if err != nil || rule == nil || rule.LastValue == nil || *rule.LastValue != previous {
					t.Fatalf("existing/member baseline changed: id=%d rule=%+v err=%v", id, rule, err)
				}
			}
		})
	}
}
