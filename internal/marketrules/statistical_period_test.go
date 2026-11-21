package marketrules

import (
	"testing"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

func TestSnapshotStatisticalPeriodsNeverMapToAnotherBucket(t *testing.T) {
	five, hour, day := int64(5), int64(60), int64(1440)
	snapshot := store.MarketSnapshot{Volume5mUSD: stringPointer("5"), Volume15mUSD: stringPointer("15"), Volume1hUSD: stringPointer("60"), Volume6hUSD: stringPointer("360"), Volume24hUSD: stringPointer("1440"),
		Buys1h: &hour, Sells1h: &hour, Buys24h: &day, Sells24h: &day}
	for _, minutes := range []int32{1, 10, 15, 30, 120, 720} {
		if value := snapshotVolume(snapshot, &minutes); value != nil {
			t.Fatalf("unsupported %dm silently used volume %s", minutes, *value)
		}
	}
	for _, minutes := range []int32{5, 15, 30, 60, 1440} {
		_, _, ok := snapshotTrades(snapshot, &minutes)
		if want := minutes == 60 || minutes == 1440; ok != want {
			t.Fatalf("trade bucket %dm availability=%v want=%v", minutes, ok, want)
		}
	}
	snapshot.Buys5m, snapshot.Sells5m = &five, &five
	minutes := int32(5)
	buys, sells, ok := snapshotTrades(snapshot, &minutes)
	if !ok || buys != 5 || sells != 5 {
		t.Fatalf("exact 5m bucket was not read: %d %d %v", buys, sells, ok)
	}
}
