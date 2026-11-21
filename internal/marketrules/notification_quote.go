package marketrules

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/notificationfmt"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

// This only annotates newly formatted text. Stored notification snapshots and
// frozen transaction values are neither rewritten nor revalued here.
func appendMarketQuoteReference(text string, delivery store.MarketNotificationDelivery, now time.Time) string {
	unavailable, unknownTime := false, false
	var earliest *time.Time
	inspectQuote := func(quote marketquality.Quote) {
		if quote.AvailableAt(now) {
			return
		}
		unavailable = true
		fetched := quote.FetchedAt
		if fetched == nil || fetched.IsZero() || fetched.After(now) {
			unknownTime = true
			return
		}
		if earliest == nil || fetched.Before(*earliest) {
			value := *fetched
			earliest = &value
		}
	}
	inspect := func(snapshot *store.MarketSnapshot, event *store.MarketEvent) {
		if snapshot == nil || !snapshotHasMarketValues(*snapshot) {
			return
		}
		inspectQuote(snapshot.QuoteQuality)
		if event == nil || snapshot.LiquidityUSD == nil || strings.TrimSpace(*snapshot.LiquidityUSD) == "" {
			return
		}
		if _, present := tradeProjectionFromEvent(*event); !present {
			return
		}
		// Trade price and current selected-pool liquidity have independent
		// frozen provenance. A valid trade price cannot certify its denominator.
		var metadata struct {
			Projection struct {
				SelectedPoolIDs []int64 `json:"selected_pool_ids"`
				Snapshots       []struct {
					PoolID  int64               `json:"pool_id"`
					Quality marketquality.Quote `json:"quote_quality"`
				} `json:"liquidity_snapshots"`
			} `json:"trade_projection"`
		}
		if json.Unmarshal(event.Metadata, &metadata) != nil || len(metadata.Projection.SelectedPoolIDs) == 0 ||
			len(metadata.Projection.Snapshots) != len(metadata.Projection.SelectedPoolIDs) {
			inspectQuote(marketquality.Quote{Status: marketquality.StatusUnknown})
			return
		}
		seen := make(map[int64]bool, len(metadata.Projection.Snapshots))
		for _, entry := range metadata.Projection.Snapshots {
			if seen[entry.PoolID] {
				inspectQuote(marketquality.Quote{Status: marketquality.StatusUnknown})
			}
			seen[entry.PoolID] = true
			inspectQuote(entry.Quality)
		}
		for _, poolID := range metadata.Projection.SelectedPoolIDs {
			if !seen[poolID] {
				inspectQuote(marketquality.Quote{Status: marketquality.StatusUnknown})
			}
		}
	}
	inspect(delivery.Snapshot, delivery.Event)
	for _, event := range delivery.RecentEvents {
		inspect(event.Snapshot, &event.Event)
	}
	for _, event := range delivery.StageEvents {
		inspect(event.Snapshot, &event.Event)
	}
	for _, member := range delivery.CombinationMembers {
		for _, event := range member.MarketEvents {
			inspect(event.Snapshot, &event.Event)
		}
	}
	if !unavailable {
		return text
	}
	english := notificationLanguage(delivery.NotificationLanguage) == "en"
	note := "行情参考：附带行情包含旧值或有效性未知的数据，仅供参考；获取时间未知。"
	if english {
		note = "Quote reference: attached market values include older or unverified data; fetch time is unknown."
	}
	if earliest != nil {
		zone := strings.TrimSpace(delivery.Timezone)
		if zone == "" {
			zone = "Asia/Shanghai"
		}
		location, err := time.LoadLocation(zone)
		if err != nil {
			zone = "Asia/Shanghai"
			location, _ = time.LoadLocation(zone)
		}
		if location == nil {
			zone, location = "UTC", time.UTC
		}
		// Reuse the resolved location; validating it and then calling LocalTime
		// would load the same timezone data twice for this one reference line.
		fetched := earliest.In(location).Format("2006-01-02 15:04") + " (" + zone + ")"
		note = "行情参考：附带行情包含旧值或有效性未知的数据，仅供参考；最早可确认获取时间：" + fetched + "。"
		if english {
			note = "Quote reference: attached market values include older or unverified data; earliest confirmed fetch: " + fetched + "."
		}
		if unknownTime {
			if english {
				note += " Some fetch times are unknown."
			} else {
				note += "部分行情获取时间未知。"
			}
		}
	}
	return text + notificationfmt.LineBreak + escape(note)
}

func snapshotHasMarketValues(snapshot store.MarketSnapshot) bool {
	for _, value := range []*string{snapshot.PriceUSD, snapshot.LiquidityUSD, snapshot.FDVUSD, snapshot.MarketCapUSD,
		snapshot.Volume5mUSD, snapshot.Volume15mUSD, snapshot.Volume1hUSD, snapshot.Volume6hUSD, snapshot.Volume24hUSD} {
		if value != nil && strings.TrimSpace(*value) != "" {
			return true
		}
	}
	for _, value := range []*int64{snapshot.Buys5m, snapshot.Sells5m, snapshot.Buys1h, snapshot.Sells1h, snapshot.Buys24h, snapshot.Sells24h} {
		if value != nil {
			return true
		}
	}
	return false
}
