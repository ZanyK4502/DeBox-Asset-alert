package marketrules

import (
	"context"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketwindow"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

type snapshotBaselineRepository interface {
	MarketSnapshotBaselineForTarget(context.Context, store.MarketRuleTarget, time.Time) (*store.MarketSnapshot, error)
}

func (service *Service) loadRuleSnapshots(ctx context.Context, rule store.MarketRule, target store.MarketRuleTarget, load func(int) ([]store.MarketSnapshot, error)) ([]store.MarketSnapshot, error) {
	capability := marketwindow.ForRule(rule.RuleType)
	if rule.RuleType == plans.MarketVolumeSpike {
		// Preserve the existing average formula (up to 12 available historical
		// bucket values in the original bounded read). Other rules need one row.
		return load(1000)
	}
	snapshots, err := load(1)
	if err == nil && target.MarketPoolID != nil {
		snapshots = eligibleSnapshots(EvaluationInput{Rule: store.MarketRule{MarketPoolID: target.MarketPoolID}, Snapshots: snapshots})
	}
	if err != nil || len(snapshots) == 0 || capability.Kind != marketwindow.KindCustomSnapshot {
		return snapshots, err
	}
	minutes, supported := marketwindow.Effective(rule.RuleType, rule.WindowMinutes)
	if !supported {
		return snapshots, marketwindow.ErrUnsupported
	}
	observed := snapshots[0].QuoteQuality.ObservationTime()
	if observed == nil {
		return snapshots, nil
	}
	repository, ok := service.repository.(snapshotBaselineRepository)
	if !ok {
		// Compatibility for repositories without the indexed lookup; the pure
		// evaluator still rejects a missing/misaligned baseline.
		return load(1000)
	}
	baseline, err := repository.MarketSnapshotBaselineForTarget(ctx, target, observed.Add(-time.Duration(minutes)*time.Minute))
	if err != nil {
		return nil, err
	}
	if baseline != nil && baseline.ID != snapshots[0].ID {
		snapshots = append(snapshots, *baseline)
	}
	return snapshots, nil
}
