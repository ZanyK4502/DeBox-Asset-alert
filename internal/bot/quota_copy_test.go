package bot

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/subscription"
)

func TestQuotaCopyUsesCatalogCapacitiesAndPreservesCapabilities(t *testing.T) {
	for _, limits := range []plans.QuotaLimits{plans.DefaultQuotaLimits(), {FreeRuleLimit: 1, StandardRuleLimit: 4, ProfessionalRuleLimit: 14, ProfessionalInteractionRuleLimit: 2, ProfessionalMarketProjectLimit: 3}} {
		catalog, err := plans.NewCatalogWithQuotaLimits("5", 30, "USDT", limits)
		if err != nil {
			t.Fatal(err)
		}
		service := New(Dependencies{Catalog: catalog})
		for _, language := range []string{"zh", "en"} {
			text := service.plansText(language)
			expected := []string{fmt.Sprintf("%d 条共享总规则", limits.StandardRuleLimit), fmt.Sprintf("%d 条共享总规则", limits.ProfessionalRuleLimit), fmt.Sprintf("指定地址交互最多 %d 条且计入总额", limits.ProfessionalInteractionRuleLimit), fmt.Sprintf("%d 个代币监控项目", limits.ProfessionalMarketProjectLimit), "5 个群通知", "大户", "Four.meme", "实时或阶段提醒", "组合规则", "永久套餐也使用对应套餐额度"}
			if language == "en" {
				expected = []string{fmt.Sprintf("%d total shared rules", limits.StandardRuleLimit), fmt.Sprintf("%d total shared rules", limits.ProfessionalRuleLimit), fmt.Sprintf("address interaction limit %d within the total", limits.ProfessionalInteractionRuleLimit), fmt.Sprintf("token project limit %d", limits.ProfessionalMarketProjectLimit), "alerts to 5 groups", "holders", "Four.meme", "real-time or stage alerts", "combination rules", "Permanent plans use the same limits"}
			}
			pro, _ := catalog.Get(plans.Professional)
			if language == "en" {
				expected = append(expected, "persistent market monitoring on BNB Chain (BSC), Ethereum, Base, Polygon, Arbitrum, Optimism", fmt.Sprintf("up to %d selected pools", pro.MarketPoolLimit))
			} else {
				expected = append(expected, "行情持续监控 BNB Chain (BSC), Ethereum, Base, Polygon, Arbitrum, Optimism", fmt.Sprintf("最多 %d 个已选交易池", pro.MarketPoolLimit))
			}
			for _, part := range expected {
				if !strings.Contains(text, part) {
					t.Fatalf("%s catalog copy missing %q: %s", language, part, text)
				}
			}
			for _, stale := range []string{"100 条总规则", "100 total rules"} {
				if strings.Contains(text, stale) || strings.Contains(featuresText(language), stale) {
					t.Fatalf("stale quota %q in %s copy", stale, language)
				}
			}
		}
	}
}

func TestMarketScopeCopyUsesCatalogAndKeepsWalletAndPaymentNetworks(t *testing.T) {
	limits := plans.DefaultQuotaLimits()
	limits.ProfessionalMarketPoolLimit = 4
	catalog, err := plans.NewCatalogWithMarketScope("5", 30, "USDT", limits, []string{"base"})
	if err != nil {
		t.Fatal(err)
	}
	service := New(Dependencies{Catalog: catalog})
	for _, language := range []string{"zh", "en"} {
		feature, err := service.callbackText(context.Background(), "alert:features", "copy-user", language)
		if err != nil {
			t.Fatal(err)
		}
		planText := service.plansText(language)
		entry, err := service.callbackText(context.Background(), "alert:market", "copy-user", language)
		if err != nil || !strings.Contains(entry, "Base") || strings.Contains(entry, "BNB Chain") {
			t.Fatalf("token entry ignored configured scope: %s %v", entry, err)
		}
		if language == "en" {
			for _, part := range []string{"Persistent token monitoring: Base", "up to 4 selected pools", "BNB Chain, Ethereum, Base, Polygon, Arbitrum, and Optimism"} {
				if !strings.Contains(feature, part) {
					t.Fatalf("missing feature %q: %s", part, feature)
				}
			}
			if !strings.Contains(planText, "persistent market monitoring on Base") || !strings.Contains(planText, "Pay with USDT on BNB Chain") {
				t.Fatal("market scope changed payment or failed to follow catalog")
			}
		} else {
			for _, part := range []string{"行情持续监控范围：Base", "最多 4 个已选交易池", "BNB Chain、Ethereum、Base、Polygon、Arbitrum、Optimism"} {
				if !strings.Contains(feature, part) {
					t.Fatalf("missing feature %q: %s", part, feature)
				}
			}
			if !strings.Contains(planText, "行情持续监控 Base") || !strings.Contains(planText, "使用 BNB Chain USDT 支付") {
				t.Fatal("market scope changed payment or failed to follow catalog")
			}
		}
	}
}

func TestMarketScopeCopyWithoutCatalogDefaultsToSixChains(t *testing.T) {
	service := New(Dependencies{})
	for _, language := range []string{"zh", "en"} {
		for _, callback := range []string{"alert:features", "alert:market"} {
			text, err := service.callbackText(context.Background(), callback, "copy-user", language)
			if err != nil || !strings.Contains(text, "BNB Chain (BSC), Ethereum, Base, Polygon, Arbitrum, Optimism") {
				t.Fatalf("%s %s default scope=%q %v", language, callback, text, err)
			}
		}
	}
}

func TestQuotaCopyPermanentStatusShowsSharedAndInteractionUsage(t *testing.T) {
	catalog, err := plans.NewCatalog("5", 30, "USDT")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := catalog.Get(plans.Professional)
	if err != nil {
		t.Fatal(err)
	}
	service := New(Dependencies{Catalog: catalog, Subscriptions: fakeSubscriptions{value: subscription.Entitlement{Plan: plan, Permanent: true, RuleCount: 7, InteractionRuleCount: 1, MarketProjectCount: 1}}})
	for _, language := range []string{"zh", "en"} {
		text, err := service.subscriptionText(context.Background(), "fixture-user", language)
		if err != nil {
			t.Fatal(err)
		}
		for _, part := range []string{fmt.Sprintf("7 / %d", plan.RuleLimit), fmt.Sprintf("1 / %d", plan.InteractionRuleLimit), fmt.Sprintf("1 / %d", plan.MarketProjectLimit)} {
			if !strings.Contains(text, part) {
				t.Fatalf("permanent quota usage missing %s: %s", part, text)
			}
		}
		if strings.Contains(text, "100") || strings.Contains(text, "unlimited") || strings.Contains(text, "无限") {
			t.Fatalf("permanent plan bypassed normal capacity: %s", text)
		}
	}
}

func TestQuotaCopyProFiveProjectsFivePoolsUsesCatalogWithoutChangingOtherTerms(t *testing.T) {
	catalog, err := plans.NewCatalog("5", 30, "USDT")
	if err != nil {
		t.Fatal(err)
	}
	pro, err := catalog.Get(plans.Professional)
	if err != nil {
		t.Fatal(err)
	}
	standard, err := catalog.Get(plans.Standard)
	if err != nil {
		t.Fatal(err)
	}
	if pro.MarketProjectLimit != 5 || pro.MarketPoolLimit != 5 || pro.RuleLimit != 10 || pro.InteractionRuleLimit != 1 || pro.GroupLimit != 5 || standard.MarketProjectLimit != 1 || standard.MarketPoolLimit != 1 {
		t.Fatalf("catalog target or retained quotas changed: pro=%+v standard=%+v", pro, standard)
	}
	service := New(Dependencies{Catalog: catalog})
	for _, language := range []string{"zh", "en"} {
		planCopy := service.plansText(language)
		featureCopy := featuresText(language)
		terms := []string{"5 个代币监控项目", "最多 5 个已选交易池", "10 条共享总规则", "指定地址交互最多 1 条且计入总额", "永久套餐也使用对应套餐额度", "3 个区块确认"}
		if language == "en" {
			terms = []string{"token project limit 5", "up to 5 selected pools", "10 total shared rules", "address interaction limit 1 within the total", "Permanent plans use the same limits", "3 block confirmations"}
		}
		for _, term := range terms {
			if !strings.Contains(planCopy, term) {
				t.Fatalf("%s plan copy missing %q", language, term)
			}
		}
		poolTerm := "最多 5 个已选交易池"
		if language == "en" {
			poolTerm = "up to 5 selected pools"
		}
		if !strings.Contains(featureCopy, poolTerm) {
			t.Fatalf("feature fallback ignored catalog defaults: %s", featureCopy)
		}
	}
}
