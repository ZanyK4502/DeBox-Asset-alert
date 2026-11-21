package marketrules

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestNewNotificationMarksUnusableAttachedQuoteWithoutChangingValues(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	price := "5"
	snapshot := &store.MarketSnapshot{PriceUSD: &price, CapturedAt: now, QuoteQuality: liveQuoteFixture(now)}
	delivery := store.MarketNotificationDelivery{Kind: "realtime", NotificationLanguage: "zh", Timezone: "Asia/Shanghai", Snapshot: snapshot,
		Project: store.MarketProject{TokenSymbol: "TEST"}, Rule: &store.MarketRule{RuleType: plans.MarketLargeBuy, ThresholdValue: "10", ThresholdUnit: "token"},
		Event: &store.MarketEvent{EventType: "buy", TokenAmount: stringPointer("12"), OccurredAt: now}}
	fresh := marketNotificationTextAt(delivery, now)
	if strings.Contains(fresh, "行情参考") {
		t.Fatalf("fresh attached quote was labeled stale: %s", fresh)
	}
	snapshot.QuoteQuality.Status = marketquality.StatusStaleFallback
	before, _ := json.Marshal(delivery)
	text := marketNotificationTextAt(delivery, now)
	after, _ := json.Marshal(delivery)
	if strings.Count(text, "行情参考") != 1 || !strings.Contains(text, "2026-11-14 20:00 (Asia/Shanghai)") || strings.Contains(text, "\n") ||
		!bytes.Equal(before, after) || *snapshot.PriceUSD != "5" || !strings.Contains(text, "12 TEST") {
		t.Fatalf("quote annotation changed the notification facts or omitted provenance: %s", text)
	}
}

func TestNewNotificationUnknownQuoteTimeAndEmptySnapshots(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	text := appendMarketQuoteReference("original", store.MarketNotificationDelivery{Snapshot: &store.MarketSnapshot{PriceUSD: stringPointer("5")}}, now)
	if !strings.Contains(text, "获取时间未知") || !strings.HasPrefix(text, "original<br/>") {
		t.Fatalf("unknown quote pretended to be current: %s", text)
	}
	if text := appendMarketQuoteReference("original", store.MarketNotificationDelivery{Snapshot: &store.MarketSnapshot{}}, now); text != "original" {
		t.Fatalf("empty snapshot generated noise: %s", text)
	}
	future := now.Add(time.Hour)
	text = appendMarketQuoteReference("original", store.MarketNotificationDelivery{Snapshot: &store.MarketSnapshot{PriceUSD: stringPointer("5"),
		QuoteQuality: marketquality.Quote{Status: marketquality.StatusLive, FetchedAt: &future}}}, now)
	if !strings.Contains(text, "获取时间未知") || strings.Contains(text, "最早可确认") {
		t.Fatalf("future timestamp was described as a completed fetch: %s", text)
	}
}

func TestNewStageAndCombinationQuoteNoteIsSingleAndIncludesNestedSources(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	old := now.Add(-time.Hour)
	known := &store.MarketSnapshot{LiquidityUSD: stringPointer("0"), QuoteQuality: marketquality.Quote{Status: marketquality.StatusStaleFallback, FetchedAt: &old}}
	unknown := &store.MarketSnapshot{PriceUSD: stringPointer("5")}
	for _, kind := range []string{"stage", "combination"} {
		delivery := store.MarketNotificationDelivery{Kind: kind, NotificationLanguage: "en", Timezone: "UTC",
			StageEvents: []store.MarketNotificationEvent{{Snapshot: known}}, RecentEvents: []store.MarketNotificationEvent{{Snapshot: known}},
			CombinationMembers: []store.MarketCombinationProgress{{MarketEvents: []store.MarketNotificationEvent{{Snapshot: unknown}}}}}
		text := appendMarketQuoteReference("original", delivery, now)
		if strings.Count(text, "Quote reference:") != 1 || !strings.Contains(text, "2026-11-14 11:00 (UTC)") || !strings.Contains(text, "Some fetch times are unknown") {
			t.Fatalf("nested quote status was omitted or duplicated for %s: %s", kind, text)
		}
	}
}

func TestFrozenTradeFreshPriceDoesNotCertifyOldLiquidityReference(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	old := now.Add(-time.Hour)
	event := tradeFactsTestEvent(3, 101, now)
	var metadata map[string]any
	_ = json.Unmarshal(event.Metadata, &metadata)
	projection := metadata["trade_projection"].(map[string]any)
	entries := projection["liquidity_snapshots"].([]any)
	entries[1].(map[string]any)["quote_quality"] = marketquality.Quote{Status: marketquality.StatusStaleFallback, FetchedAt: &old}
	event.Metadata, _ = json.Marshal(metadata)
	snapshot := &store.MarketSnapshot{PriceUSD: stringPointer("1000"), LiquidityUSD: stringPointer("2000000"), QuoteQuality: liveQuoteFixture(now)}
	delivery := store.MarketNotificationDelivery{Snapshot: snapshot, Event: &event, Timezone: "UTC"}
	text := appendMarketQuoteReference("original", delivery, now)
	if strings.Count(text, "行情参考") != 1 || !strings.Contains(text, "2026-11-14 11:00 (UTC)") {
		t.Fatalf("fresh trade price hid stale denominator provenance: %s", text)
	}
	if *snapshot.PriceUSD != "1000" || *snapshot.LiquidityUSD != "2000000" {
		t.Fatal("reference annotation recomputed frozen values")
	}
	snapshot.LiquidityUSD = nil
	if text := appendMarketQuoteReference("original", delivery, now); text != "original" {
		t.Fatalf("absent attached liquidity required an unrelated quote: %s", text)
	}
}
