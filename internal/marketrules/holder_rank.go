package marketrules

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketwindow"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

// A rank observation proves membership only inside its complete sampled range.
// nil rank means outside that covered range, never zero or an exact unseen rank.
func holderRankCrossing(rule store.MarketRule, event store.MarketEvent) (bool, *string) {
	n, valid := marketwindow.HolderRank(rule.ThresholdValue)
	if !valid || event.EventType != "holder_rank_changed" || event.Source != "nodit_holders_sample" {
		return false, nil
	}
	var evidence struct {
		Version     int       `json:"holder_rank_schema_version"`
		OldRank     *int32    `json:"old_rank"`
		NewRank     *int32    `json:"new_rank"`
		OldCoverage int32     `json:"old_covered_top_n"`
		NewCoverage int32     `json:"new_covered_top_n"`
		OldAt       time.Time `json:"old_observation_at"`
		NewAt       time.Time `json:"new_observation_at"`
	}
	if json.Unmarshal(event.Metadata, &evidence) != nil || evidence.Version != 1 || evidence.OldCoverage < n || evidence.NewCoverage < n ||
		evidence.OldCoverage > marketwindow.MaxHolderRank || evidence.NewCoverage > marketwindow.MaxHolderRank ||
		evidence.OldAt.IsZero() || !evidence.NewAt.After(evidence.OldAt) ||
		!evidence.NewAt.UTC().Truncate(time.Microsecond).Equal(event.OccurredAt.UTC().Truncate(time.Microsecond)) {
		return false, nil
	}
	if evidence.OldRank != nil && (*evidence.OldRank < 1 || *evidence.OldRank > evidence.OldCoverage) ||
		evidence.NewRank != nil && (*evidence.NewRank < 1 || *evidence.NewRank > evidence.NewCoverage) {
		return false, nil
	}
	wasIn := evidence.OldRank != nil && *evidence.OldRank <= n
	isIn := evidence.NewRank != nil && *evidence.NewRank <= n
	if rule.RuleType == plans.MarketHolderRankEntered && !wasIn && isIn {
		value := strconv.FormatInt(int64(*evidence.NewRank), 10)
		return true, &value
	}
	if rule.RuleType == plans.MarketHolderRankExited && wasIn && !isIn {
		value := strconv.FormatInt(int64(*evidence.OldRank), 10)
		return true, &value
	}
	return false, nil
}
