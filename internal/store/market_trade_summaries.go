package store

import (
	"context"
	"fmt"
	"strconv"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
)

// persistMarketTradeSummaries runs in the receipt's existing atomic write. It
// never repairs/revalues an old receipt, and stores no duplicate leg metadata.
func persistMarketTradeSummaries(ctx context.Context, db DBTX, receiptID int64) error {
	if _, err := db.Exec(ctx, `
		INSERT INTO market_trade_parent_summaries(receipt_id,parent_event_id,summary_version,pool_ids)
		SELECT receipt_id,parent_event_id,1,array_agg(DISTINCT pool_id ORDER BY pool_id)
		FROM market_trade_legs WHERE receipt_id=$1 GROUP BY receipt_id,parent_event_id
	`, receiptID); err != nil {
		return fmt.Errorf("save trade summary pool coverage: %w", err)
	}
	maximumAge := strconv.FormatInt(int64(marketquality.MaxAge.Seconds()), 10)
	proof := marketValuationAvailableSQL("f", maximumAge)
	if _, err := db.Exec(ctx, `
		WITH leg_facts AS (
			SELECT l.*,e.occurred_at FROM market_trade_legs l
			JOIN market_events e ON e.id=l.parent_event_id WHERE l.receipt_id=$1
		)
		INSERT INTO market_trade_pool_summaries(
			receipt_id,parent_event_id,pool_id,summary_version,leg_count,
			token_amount,usd_value,usd_valuation_complete,valuation_at
		)
		SELECT f.receipt_id,f.parent_event_id,f.pool_id,1,COUNT(*)::integer,
			CASE WHEN COUNT(f.token_amount)=COUNT(*) THEN SUM(f.token_amount) END,
			CASE WHEN COUNT(f.usd_value)=COUNT(*) THEN SUM(f.usd_value) END,
			BOOL_AND(COALESCE((`+proof+` AND f.token_amount IS NOT NULL AND f.price_usd>0),FALSE)),
			f.occurred_at
		FROM leg_facts f GROUP BY f.receipt_id,f.parent_event_id,f.pool_id,f.occurred_at
	`, receiptID); err != nil {
		return fmt.Errorf("save compact trade pool sums: %w", err)
	}
	return nil
}
