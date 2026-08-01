package marketrules

import (
	"strings"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestSmallMarketPricesKeepMeaningfulPrecision(t *testing.T) {
	current := "0.02016"
	previous := "0.0189"
	text := MarketNotificationText(store.MarketNotificationDelivery{
		Kind: "realtime", NotificationLanguage: "en", Timezone: "UTC",
		Project: store.MarketProject{TokenSymbol: "BOX"},
		Rule: &store.MarketRule{
			RuleType: plans.MarketPriceAbove, ThresholdValue: "0.02", ThresholdUnit: "usd",
		},
		Event:         &store.MarketEvent{},
		Snapshot:      &store.MarketSnapshot{PriceUSD: &current},
		PreviousValue: &previous,
		CurrentValue:  &current,
	})
	for _, expected := range []string{"$0.02016", "≥ $0.02", "+$0.00016"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("small-price notification missing %q:\n%s", expected, text)
		}
	}
}
