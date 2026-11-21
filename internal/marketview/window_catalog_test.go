package marketview

import (
	"context"
	"reflect"
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketwindow"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
)

func TestCatalogPublishesSharedWindowAndRankCapabilities(t *testing.T) {
	s := New(Dependencies{Entitlements: fakeEntitlements{plan: plans.Plan{}}})
	result, err := s.Catalog(context.Background(), "fixture")
	if err != nil || len(result.Rules) != 23 {
		t.Fatalf("catalog=%+v,%v", result, err)
	}
	for _, definition := range result.Rules {
		want := marketwindow.ForRule(definition.Code)
		if !reflect.DeepEqual(definition.Window, want) || definition.DefaultWindow != want.DefaultMinutes {
			t.Fatalf("window drift=%+v", definition)
		}
		if want.Kind == marketwindow.KindCustomSnapshot && definition.MaxBaselineLagSeconds != 30 {
			t.Fatalf("baseline limit missing=%+v", definition)
		}
		if definition.Code == plans.MarketHolderRankEntered || definition.Code == plans.MarketHolderRankExited {
			if definition.MaxHolderRank != 100 {
				t.Fatalf("rank limit missing=%+v", definition)
			}
		}
		if len(definition.Window.FixedMinutes) > 0 {
			definition.Window.FixedMinutes[0] = 999
		}
	}
	again, err := s.Catalog(context.Background(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range again.Rules {
		if !reflect.DeepEqual(definition.Window, marketwindow.ForRule(definition.Code)) {
			t.Fatal("caller mutated shared catalog policy")
		}
	}
}
