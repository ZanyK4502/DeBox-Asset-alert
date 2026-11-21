package store

import (
	"context"
	"time"
)

type DailySummaryReportContent struct {
	Statistics       SummaryStatistics           `json:"statistics"`
	AddressEvents    []SummaryEvent              `json:"address_events"`
	MarketEvents     []MarketSummaryEvent        `json:"market_events"`
	MarketSummaries  []MarketProjectChainSummary `json:"market_project_chain_summaries"`
	RecoveryCoverage *SummaryRecoveryCoverage    `json:"recovery_coverage,omitempty"`
}

// The original queries and formulas are unchanged. Their four views now share
// one consistent, bounded read instead of seeing different concurrent ingests.
func (s *Store) LoadDailySummaryReportContent(ctx context.Context, owner string, from, to time.Time, recentLimit int) (DailySummaryReportContent, error) {
	return summaryDispatchTx(ctx, s.db, true, func(ctx context.Context, db DBTX) (result DailySummaryReportContent, err error) {
		reader := newWithDB(db)
		if result.Statistics, err = reader.DailySummaryStatistics(ctx, owner, from, to); err != nil {
			return result, err
		}
		if result.AddressEvents, err = reader.ListSummaryRecentEvents(ctx, owner, from, to, recentLimit); err != nil {
			return result, err
		}
		if result.MarketEvents, err = reader.ListSummaryRecentMarketEvents(ctx, owner, from, to, recentLimit); err != nil {
			return result, err
		}
		if result.MarketSummaries, err = reader.ListDailyMarketProjectChainSummaries(ctx, owner, from, to); err != nil {
			return result, err
		}
		coverage, err := reader.LoadDailySummaryRecoveryCoverage(ctx, owner, from, to)
		if err != nil {
			return result, err
		}
		result.RecoveryCoverage = &coverage
		if result.AddressEvents == nil {
			result.AddressEvents = []SummaryEvent{}
		}
		if result.MarketEvents == nil {
			result.MarketEvents = []MarketSummaryEvent{}
		}
		if result.MarketSummaries == nil {
			result.MarketSummaries = []MarketProjectChainSummary{}
		}
		return result, nil
	})
}
