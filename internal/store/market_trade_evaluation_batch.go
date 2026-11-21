package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketwindow"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	MarketTradeWindowCandidateLimit = 100000
	marketTradeWindowReadTimeout    = 2 * time.Second
)

var ErrMarketTradeWindowUnavailable = errors.New("complete market trade window is unavailable")

// MarketTradeWindowCandidate contains no receipt/leg JSON. Its qualification
// uses one frozen selected-pool projection and the original threshold rounding.
type MarketTradeWindowCandidate struct {
	TradeReadyID int64     `db:"trade_ready_id"`
	ParentID     int64     `db:"parent_id"`
	OccurredAt   time.Time `db:"occurred_at"`
	Qualified    bool      `db:"qualified"`
	Unknown      bool      `db:"unknown"`
}

type MarketTradeEvaluationBatch struct {
	Current                    []MarketEvent
	Candidates                 []MarketTradeWindowCandidate
	NotificationEventDecisions map[int64]bool
	HistoryUnavailable         map[int64]string
}

// LoadMarketTradeEvaluationBatch freezes details, liquidity and compact window
// candidates in one read-only repeatable-read snapshot. Any budget/coverage
// failure returns no partial count and cannot advance a rule watermark.
func (s *Store) LoadMarketTradeEvaluationBatch(ctx context.Context, rule MarketRule, target MarketRuleTarget, afterReadyID int64, now time.Time) (result MarketTradeEvaluationBatch, err error) {
	return s.LoadMarketTradeEvaluationBatchAfter(ctx, rule, target, afterReadyID, now, nil)
}

func (s *Store) LoadMarketTradeEvaluationBatchAfter(ctx context.Context, rule MarketRule, target MarketRuleTarget, afterReadyID int64, now time.Time, sourceAfter *time.Time) (MarketTradeEvaluationBatch, error) {
	return s.loadMarketTradeEvaluationBatch(ctx, rule, target, afterReadyID, now, sourceAfter, false, nil)
}

// The readiness reader distinguishes reliable missing historical evidence from
// a failed or incomplete current read. Only the former may warm up a rule.
func (s *Store) LoadMarketTradeReadinessBatch(ctx context.Context, rule MarketRule, target MarketRuleTarget, afterReadyID int64, now time.Time, sourceAfter *time.Time, readiness MarketTradeReadiness) (MarketTradeEvaluationBatch, error) {
	return s.loadMarketTradeEvaluationBatch(ctx, rule, target, afterReadyID, now, sourceAfter, true, &readiness)
}

func (s *Store) loadMarketTradeEvaluationBatch(ctx context.Context, rule MarketRule, target MarketRuleTarget, afterReadyID int64, now time.Time, sourceAfter *time.Time, allowWarmup bool, readiness *MarketTradeReadiness) (result MarketTradeEvaluationBatch, err error) {
	if rule.RuleType != plans.MarketConsecutiveLargeBuy && rule.RuleType != plans.MarketConsecutiveLargeSell {
		return result, ErrInvalidMarketRule
	}
	minutes, valid := marketwindow.Effective(rule.RuleType, rule.WindowMinutes)
	if !valid || afterReadyID < 0 || now.IsZero() {
		return result, ErrInvalidMarketRule
	}
	readCtx, cancel := context.WithTimeout(ctx, marketTradeWindowReadTimeout)
	defer cancel()
	tx, err := s.db.Begin(readCtx)
	if err != nil {
		return result, marketTradeReadError(ctx, err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cleanupCancel()
		_ = tx.Rollback(cleanupCtx)
	}()
	if _, err = tx.Exec(readCtx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY"); err != nil {
		return result, marketTradeReadError(ctx, err)
	}
	if _, err = tx.Exec(readCtx, "SET LOCAL statement_timeout = '2s'"); err != nil {
		return result, marketTradeReadError(ctx, err)
	}
	reader := newWithDB(tx)
	result.Current, err = reader.ListMarketTradeEventsForTarget(readCtx, target, afterReadyID, true, MarketRuleCurrentEventLimit)
	if err != nil {
		return MarketTradeEvaluationBatch{}, marketTradeReadError(ctx, err)
	}
	if len(result.Current) == 0 {
		if err = tx.Commit(readCtx); err != nil {
			return MarketTradeEvaluationBatch{}, marketTradeReadError(ctx, err)
		}
		return result, nil
	}
	if rule.ID > 0 {
		parentIDs := make([]int64, 0, len(result.Current))
		for _, event := range result.Current {
			parentIDs = append(parentIDs, event.ID)
		}
		result.NotificationEventDecisions, err = reader.ListMarketRuleNotificationEventDecisions(readCtx, rule.ID, parentIDs)
		if err != nil {
			return MarketTradeEvaluationBatch{}, marketTradeReadError(ctx, err)
		}
	}
	result.HistoryUnavailable = make(map[int64]string)
	if readiness != nil && len(result.Current) > 0 {
		from, through := result.Current[0].OccurredAt, result.Current[0].OccurredAt
		for _, event := range result.Current {
			if event.OccurredAt.Before(from) {
				from = event.OccurredAt
			}
			if event.OccurredAt.After(through) {
				through = event.OccurredAt
			}
		}
		periods, e := reader.ListMarketTradeWarmupPeriods(readCtx, *readiness, from, through)
		if e != nil {
			return MarketTradeEvaluationBatch{}, e
		}
		for _, event := range result.Current {
			for _, period := range periods {
				if !event.OccurredAt.Before(period.FromAt) && ((period.ReadyFromAt != nil && event.OccurredAt.Before(*period.ReadyFromAt)) || (period.ReadyFromAt == nil && !event.OccurredAt.After(period.ThroughAt))) {
					result.HistoryUnavailable[event.ID] = "recorded_warmup"
					break
				}
			}
		}
	}
	window := time.Duration(minutes) * time.Minute
	windows := make([]MarketTradeScanWindow, 0, len(result.Current))
	windowParents := make([]int64, 0, len(result.Current))
	maxReady := afterReadyID
	firstFresh := -1
	direction := "buy"
	if rule.RuleType == plans.MarketConsecutiveLargeSell {
		direction = "sell"
	}
	for index, event := range result.Current {
		if event.BlockNumber == nil || *event.BlockNumber < 1 || event.TradeReadyID <= afterReadyID || event.OccurredAt.IsZero() {
			return MarketTradeEvaluationBatch{}, fmt.Errorf("%w: invalid_current_scope", ErrMarketTradeWindowUnavailable)
		}
		if event.EventType != direction {
			continue
		}
		if sourceAfter != nil && event.OccurredAt.Before(*sourceAfter) {
			continue
		}
		if result.NotificationEventDecisions[event.ID] || result.HistoryUnavailable[event.ID] == "recorded_warmup" {
			continue
		}
		if rule.RuleScope != "combination" && rule.DeliveryMode == "stage" && rule.CycleMinutes > 0 && !event.OccurredAt.Add(time.Duration(rule.CycleMinutes)*time.Minute).After(now) {
			continue
		}
		if firstFresh < 0 {
			firstFresh = index
		}
		windows = append(windows, MarketTradeScanWindow{From: event.OccurredAt.Add(-window), Through: event.OccurredAt, ThroughBlock: *event.BlockNumber})
		windowParents = append(windowParents, event.ID)
		if event.TradeReadyID > maxReady {
			maxReady = event.TradeReadyID
		}
	}
	if len(windows) == 0 {
		if err = tx.Commit(readCtx); err != nil {
			return MarketTradeEvaluationBatch{}, marketTradeReadError(ctx, err)
		}
		return result, nil
	}
	coverage, err := marketTradeWindowScanCoverageStates(readCtx, tx, target, windows)
	if err != nil {
		return MarketTradeEvaluationBatch{}, marketTradeReadError(ctx, err)
	}
	for index, reason := range coverage {
		if reason != "" {
			if !allowWarmup {
				return MarketTradeEvaluationBatch{}, fmt.Errorf("%w: %s", ErrMarketTradeWindowUnavailable, reason)
			}
			result.HistoryUnavailable[windowParents[index]] = reason
		}
	}
	liquidity, liquidityKnown := marketTradeBatchLiquidity(result.Current[firstFresh], target, now)
	result.Candidates, err = reader.listCompactMarketTradeWindow(readCtx, rule, target, maxReady, windows, liquidity, liquidityKnown, MarketTradeWindowCandidateLimit)
	if err != nil {
		return MarketTradeEvaluationBatch{}, marketTradeReadError(ctx, err)
	}
	if err = tx.Commit(readCtx); err != nil {
		return MarketTradeEvaluationBatch{}, marketTradeReadError(ctx, err)
	}
	return result, nil
}

func marketTradeReadError(parent context.Context, err error) error {
	if parent.Err() != nil {
		return err
	}
	var queryError *pgconn.PgError
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &queryError) && queryError.Code == "57014") {
		return fmt.Errorf("%w: database_budget: %v", ErrMarketTradeWindowUnavailable, err)
	}
	return err
}

func marketTradeBatchLiquidity(event MarketEvent, target MarketRuleTarget, now time.Time) (*string, bool) {
	var envelope struct {
		Projection struct {
			Liquidity       *string `json:"liquidity_usd"`
			Complete        bool    `json:"liquidity_complete"`
			SelectedPoolIDs []int64 `json:"selected_pool_ids"`
			Snapshots       []struct {
				PoolID  int64               `json:"pool_id"`
				Quality marketquality.Quote `json:"quote_quality"`
			} `json:"liquidity_snapshots"`
		} `json:"trade_projection"`
	}
	if json.Unmarshal(event.Metadata, &envelope) != nil {
		return nil, false
	}
	p := envelope.Projection
	pools := uniquePositiveIDs(target.TradePoolIDs)
	if !p.Complete || p.Liquidity == nil || len(pools) == 0 || len(p.SelectedPoolIDs) != len(pools) || len(p.Snapshots) != len(pools) {
		return p.Liquidity, false
	}
	amount, ok := new(big.Rat).SetString(*p.Liquidity)
	if !ok || amount.Sign() <= 0 {
		return p.Liquidity, false
	}
	selected := make(map[int64]bool, len(pools))
	for _, id := range pools {
		selected[id] = true
	}
	seen := make(map[int64]bool, len(pools))
	for _, id := range p.SelectedPoolIDs {
		if !selected[id] || seen[id] {
			return p.Liquidity, false
		}
		seen[id] = true
	}
	seen = make(map[int64]bool, len(pools))
	for _, snapshot := range p.Snapshots {
		if !selected[snapshot.PoolID] || seen[snapshot.PoolID] || !snapshot.Quality.AvailableAt(now) {
			return p.Liquidity, false
		}
		seen[snapshot.PoolID] = true
	}
	return p.Liquidity, true
}

// Merging the actual time union avoids widening a disordered ready batch into
// an unrelated month of history. Publication order is still checked separately.
func mergedMarketTradeTimes(windows []MarketTradeScanWindow) ([]time.Time, []time.Time) {
	ordered := append([]MarketTradeScanWindow(nil), windows...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].From.Before(ordered[j].From) })
	var froms, throughs []time.Time
	for _, window := range ordered {
		last := len(froms) - 1
		if last < 0 || window.From.After(throughs[last]) {
			froms = append(froms, window.From)
			throughs = append(throughs, window.Through)
			continue
		}
		if window.Through.After(throughs[last]) {
			throughs[last] = window.Through
		}
	}
	return froms, throughs
}
