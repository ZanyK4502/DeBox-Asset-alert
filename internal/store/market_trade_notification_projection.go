package store

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
)

// frozenMarketTradeEvent restores the trigger-time selected-pool amounts. A
// recognized projection must never fall back to today's parent totals or quote.
func frozenMarketTradeEvent(event MarketEvent, details json.RawMessage) (MarketEvent, *MarketSnapshot, bool, error) {
	var envelope struct {
		Projection json.RawMessage `json:"trade_projection"`
	}
	if json.Unmarshal(details, &envelope) != nil || len(envelope.Projection) == 0 {
		return event, nil, false, nil
	}
	var projection struct {
		SchemaVersion     int               `json:"schema_version"`
		ReadyID           int64             `json:"ready_id"`
		PoolIDs           []int64           `json:"pool_ids"`
		TokenAmountRaw    *string           `json:"token_amount_raw"`
		QuoteAmountRaw    *string           `json:"quote_amount_raw"`
		TokenAmount       *string           `json:"token_amount"`
		QuoteAmount       *string           `json:"quote_amount"`
		USDValue          *string           `json:"usd_value"`
		PriceUSD          *string           `json:"price_usd"`
		QuoteAddress      string            `json:"quote_address"`
		LiquidityUSD      *string           `json:"liquidity_usd"`
		SourceLegIDs      []int64           `json:"source_leg_ids"`
		ValuationMetadata []json.RawMessage `json:"valuation_metadata"`
	}
	if err := json.Unmarshal(envelope.Projection, &projection); err != nil {
		return event, nil, true, fmt.Errorf("decode frozen market trade projection: %w", err)
	}
	if projection.SchemaVersion != 1 || projection.ReadyID < 1 || len(projection.PoolIDs) == 0 {
		return event, nil, true, fmt.Errorf("invalid frozen market trade projection")
	}
	event.TokenAmountRaw, event.QuoteAmountRaw = projection.TokenAmountRaw, projection.QuoteAmountRaw
	event.TokenAmount, event.QuoteAmount = projection.TokenAmount, projection.QuoteAmount
	event.USDValue, event.PriceUSD = projection.USDValue, projection.PriceUSD
	event.TradeReadyID = projection.ReadyID
	event.TradePoolIDs = append([]int64(nil), projection.PoolIDs...)
	event.MarketPoolID = nil
	if len(projection.PoolIDs) == 1 {
		poolID := projection.PoolIDs[0]
		event.MarketPoolID = &poolID
	}
	var metadata map[string]json.RawMessage
	if json.Unmarshal(details, &metadata) != nil {
		return event, nil, true, fmt.Errorf("invalid frozen market trade metadata")
	}
	quoteAddress, _ := json.Marshal(projection.QuoteAddress)
	metadata["quote_address"] = quoteAddress
	event.Metadata, _ = json.Marshal(metadata)
	quality, valuedAt := frozenTradeValuationQuality(projection.SourceLegIDs, projection.ValuationMetadata, event.OccurredAt)
	return event, &MarketSnapshot{
		ChainKey: event.ChainKey, ChainID: event.ChainID, TokenAddress: event.TokenAddress,
		PriceUSD: projection.PriceUSD, LiquidityUSD: projection.LiquidityUSD,
		QuoteQuality: quality, CapturedAt: valuedAt, SourceTimestamp: quality.SourceTimestamp,
	}, true, nil
}

// Frozen values keep their original amounts. This only restores the compact
// evidence used by the notification formatter; no current quote is fetched and
// historical validity is checked at each leg's own valuation time, not now.
func frozenTradeValuationQuality(legIDs []int64, metadata []json.RawMessage, occurredAt time.Time) (marketquality.Quote, time.Time) {
	if len(legIDs) == 0 || len(legIDs) != len(metadata) || occurredAt.IsZero() {
		return marketquality.Quote{}, time.Time{}
	}
	occurredAt = occurredAt.UTC().Truncate(time.Microsecond)
	quality := marketquality.Quote{Status: marketquality.StatusLive}
	var valuedAt time.Time
	for _, raw := range metadata {
		var evidence struct {
			SchemaVersion int                 `json:"valuation_schema_version"`
			Complete      bool                `json:"valuation_complete"`
			At            time.Time           `json:"valuation_at"`
			Quote         marketquality.Quote `json:"valuation_quote_quality"`
		}
		if json.Unmarshal(raw, &evidence) != nil || evidence.SchemaVersion != 1 || !evidence.Complete ||
			!evidence.At.UTC().Truncate(time.Microsecond).Equal(occurredAt) || !evidence.Quote.AvailableAt(occurredAt) {
			return marketquality.Quote{}, time.Time{}
		}
		valuedAt = occurredAt
		if quality.FetchedAt == nil || evidence.Quote.FetchedAt.Before(*quality.FetchedAt) {
			value := *evidence.Quote.FetchedAt
			quality.FetchedAt = &value
		}
		if source := evidence.Quote.SourceTimestamp; source != nil && !source.IsZero() && (quality.SourceTimestamp == nil || source.Before(*quality.SourceTimestamp)) {
			value := *source
			quality.SourceTimestamp = &value
		}
	}
	return quality, valuedAt
}
