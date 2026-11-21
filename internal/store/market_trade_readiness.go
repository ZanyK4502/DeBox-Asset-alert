package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var ErrMarketTradeReadinessCapacity = errors.New("market trade readiness data exceeds bounded capacity")

var ErrMarketTradeActivationRequired = errors.New("market trade scope requires reviewed activation or an actual recovery boundary")

// MarketTradeReadiness is evidence about this exact selected-pool scope. It is
// independent of the legacy event cursor, threshold parameters and cooldown.
type MarketTradeReadiness struct {
	RuleID                 int64      `db:"rule_id" json:"rule_id"`
	TargetKey              string     `db:"target_key" json:"target_key"`
	ScopeFingerprint       string     `db:"scope_fingerprint" json:"scope_fingerprint"`
	ScopeSourceFingerprint string     `db:"scope_source_fingerprint" json:"-"`
	ScopeRevision          int64      `db:"scope_revision" json:"scope_revision"`
	EligibilityAfter       *time.Time `db:"eligibility_after" json:"eligibility_after,omitempty"`
	ActivationOrigin       string     `db:"activation_origin" json:"activation_origin"`
	LatestEventAt          *time.Time `db:"latest_event_at" json:"latest_event_at,omitempty"`
	Status                 string     `db:"status" json:"status"`
	Reason                 string     `db:"reason" json:"reason,omitempty"`
	Version                int64      `db:"version" json:"-"`
	UpdatedAt              time.Time  `db:"updated_at" json:"updated_at"`
}

const marketTradeReadinessColumns = `rule_id,target_key,scope_fingerprint,scope_source_fingerprint,scope_revision,eligibility_after,activation_origin,latest_event_at,status,reason,version,updated_at`

type MarketTradeWarmupPeriod struct {
	ID          int64      `db:"id"`
	FromAt      time.Time  `db:"from_at"`
	ThroughAt   time.Time  `db:"through_at"`
	ReadyFromAt *time.Time `db:"ready_from_at"`
}
type MarketTradeReadinessDecision struct {
	OccurredAt   time.Time
	WarmupReason string
}

func tradeScopeFingerprint(target MarketRuleTarget) string {
	pools := uniquePositiveIDs(target.TradePoolIDs)
	sort.Slice(pools, func(i, j int) bool { return pools[i] < pools[j] })
	payload, _ := json.Marshal([]any{target.TargetKey, target.ChainID, strings.ToLower(target.TokenAddress), pools})
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])
}

func recordMarketProjectTradeScope(ctx context.Context, db DBTX, projectID int64) error {
	var signature string
	var selected json.RawMessage
	var total int64
	if err := db.QueryRow(ctx, `WITH bounded AS MATERIALIZED (
  SELECT market_pool_id,market_project_deployment_id,is_primary FROM market_project_pools
  WHERE market_project_id=$1 AND selected=1 ORDER BY market_pool_id LIMIT 60001
 ) SELECT COUNT(*) AS total,encode(sha256(convert_to(COALESCE(string_agg(
  market_pool_id::text||':'||COALESCE(market_project_deployment_id::text,'legacy')||':'||is_primary::text,
  ',' ORDER BY market_pool_id),''),'UTF8')),'hex'),
 COALESCE(jsonb_object_agg(market_pool_id::text,jsonb_build_array(market_project_deployment_id,is_primary)),'{}'::jsonb)
 FROM bounded`, projectID).Scan(&total, &signature, &selected); err != nil {
		return err
	}
	if total > 60000 {
		return ErrMarketTradeReadinessCapacity
	}
	_, err := db.Exec(ctx, `INSERT INTO market_project_trade_scope_changes(project_id,scope_fingerprint,selected_state,changed_at)
 VALUES($1,$2,$3,clock_timestamp()) ON CONFLICT(project_id) DO UPDATE SET scope_fingerprint=EXCLUDED.scope_fingerprint,
 selected_state=EXCLUDED.selected_state,
 pool_boundaries=COALESCE((SELECT jsonb_object_agg(entry.key,jsonb_build_object(
  'membership_epoch',CASE WHEN NOT (market_project_trade_scope_changes.selected_state ? entry.key)
   OR (market_project_trade_scope_changes.selected_state->entry.key->0) IS DISTINCT FROM (entry.value->0)
   THEN to_jsonb(market_project_trade_scope_changes.revision+1)
   ELSE COALESCE(market_project_trade_scope_changes.pool_boundaries->entry.key->'membership_epoch','0'::jsonb) END,
  'selected_at',CASE WHEN NOT (market_project_trade_scope_changes.selected_state ? entry.key)
   OR (market_project_trade_scope_changes.selected_state->entry.key->0) IS DISTINCT FROM (entry.value->0)
   THEN to_jsonb(GREATEST(EXCLUDED.changed_at,market_project_trade_scope_changes.changed_at+INTERVAL '1 microsecond'))
   ELSE market_project_trade_scope_changes.pool_boundaries->entry.key->'selected_at' END,
  'primary_epoch',CASE WHEN market_project_trade_scope_changes.selected_state->entry.key IS DISTINCT FROM entry.value
   THEN to_jsonb(market_project_trade_scope_changes.revision+1)
   ELSE COALESCE(market_project_trade_scope_changes.pool_boundaries->entry.key->'primary_epoch','0'::jsonb) END,
  'primary_at',CASE WHEN market_project_trade_scope_changes.selected_state->entry.key IS DISTINCT FROM entry.value
   THEN to_jsonb(GREATEST(EXCLUDED.changed_at,market_project_trade_scope_changes.changed_at+INTERVAL '1 microsecond'))
   ELSE market_project_trade_scope_changes.pool_boundaries->entry.key->'primary_at' END))
 FROM jsonb_each(EXCLUDED.selected_state) entry),'{}'::jsonb),
 changed_at=GREATEST(EXCLUDED.changed_at,market_project_trade_scope_changes.changed_at+INTERVAL '1 microsecond'),
 revision=market_project_trade_scope_changes.revision+1
 WHERE market_project_trade_scope_changes.scope_fingerprint<>EXCLUDED.scope_fingerprint`, projectID, signature, selected)
	return err
}

func laterTradeTime(a, b *time.Time) *time.Time {
	if a == nil {
		return b
	}
	if b != nil && b.After(*a) {
		return b
	}
	return a
}
func sameTradeTime(a, b *time.Time) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && a.Equal(*b))
}

// Verify the resolved scope again under the project lifecycle lock, so a pool
// edit between the read phase and commit cannot bind stale pool evidence.
func (s *Store) verifyMarketTradeScope(ctx context.Context, rule MarketRule, project MarketProject, target MarketRuleTarget) error {
	targets, err := s.ListMarketRuleTargets(ctx, rule, project)
	if err != nil {
		return err
	}
	pools := []int64{}
	base := strings.TrimSuffix(target.TargetKey, "/trades:v2")
	for _, candidate := range targets {
		if strings.Split(candidate.TargetKey, "/pool:")[0] == base && candidate.MarketPoolID != nil {
			if candidate.ChainID != target.ChainID || !strings.EqualFold(candidate.TokenAddress, target.TokenAddress) {
				return ErrMarketRuleEvaluationStale
			}
			pools = append(pools, *candidate.MarketPoolID)
		}
	}
	current := target
	current.TradePoolIDs = pools
	if len(pools) == 0 || tradeScopeFingerprint(current) != tradeScopeFingerprint(target) {
		return ErrMarketRuleEvaluationStale
	}
	return nil
}

// EnsureMarketTradeReadiness activates only new lifecycle-marked rules or
// scopes reviewed at an actual recovery boundary. It never reads MAX(ready_id).
func (s *Store) EnsureMarketTradeReadiness(ctx context.Context, rule MarketRule, project MarketProject, target MarketRuleTarget, sourceAfter *time.Time) (resolved MarketRuleTarget, result MarketTradeReadiness, err error) {
	err = s.WithMarketRuleEvaluationWrite(ctx, rule.ID, func(writer *Store) error {
		actual, e := writer.GetMarketRuleNotificationSourceAfter(ctx, rule.ID)
		if e != nil {
			return e
		}
		if !sameTradeTime(actual, sourceAfter) {
			return ErrMarketRuleEvaluationStale
		}
		if e = writer.verifyMarketTradeScope(ctx, rule, project, target); e != nil {
			return e
		}
		var creation *time.Time
		if e = writer.db.QueryRow(ctx, `SELECT trade_activation_after FROM market_rules WHERE id=$1`, rule.ID).Scan(&creation); e != nil {
			return e
		}
		var scopeChanged *time.Time
		if e = writer.db.QueryRow(ctx, `SELECT (SELECT changed_at FROM market_project_trade_scope_changes WHERE project_id=$1)`, project.ID).Scan(&scopeChanged); e != nil {
			return e
		}
		boundary := laterTradeTime(creation, sourceAfter)
		sourceFingerprint, selectedChanged, e := readMarketTradeScopeSource(ctx, writer.db, project.ID, rule.PoolScope, target)
		if e != nil {
			return e
		}
		boundary = laterTradeTime(boundary, selectedChanged)
		if target.MarketProjectDeploymentID != nil {
			var created time.Time
			if e = writer.db.QueryRow(ctx, `SELECT created_at FROM market_project_deployments WHERE id=$1 AND market_project_id=$2`, *target.MarketProjectDeploymentID, project.ID).Scan(&created); e != nil {
				return e
			}
			boundary = laterTradeTime(boundary, &created)
		}
		var exists bool
		resolved, exists, e = writer.LoadMarketRuleTargetState(ctx, rule.ID, target)
		if e != nil {
			return e
		}
		if exists {
			var lane struct {
				Fence *int64 `json:"activation_fence"`
				Ready int64  `json:"last_trade_ready_id"`
			}
			if json.Unmarshal(resolved.State, &lane) != nil || lane.Fence == nil || *lane.Fence < 0 || lane.Ready < *lane.Fence {
				return ErrMarketTradeActivationRequired
			}
		}
		result, e = collectOne[MarketTradeReadiness](ctx, writer.db, `SELECT `+marketTradeReadinessColumns+` FROM market_trade_rule_readiness WHERE rule_id=$1 AND target_key=$2 FOR UPDATE`, rule.ID, target.TargetKey)
		hasReadiness := e == nil
		if e != nil && !isNoRows(e) {
			return e
		}
		fingerprint := tradeScopeFingerprint(target)
		if !exists {
			if creation == nil && sourceAfter == nil {
				return ErrMarketTradeActivationRequired
			}
			state := json.RawMessage(`{"activation_fence":0,"last_trade_ready_id":0}`)
			if _, e = writer.initializeTradeRuleState(ctx, rule.ID, target, state); e != nil {
				return e
			}
			resolved, _, e = writer.LoadMarketRuleTargetState(ctx, rule.ID, target)
			if e != nil {
				return e
			}
		}
		// A legacy valid lane carries no proof of its historical selected pools.
		// Keep its watermark; only the actual reviewed cutover can establish scope.
		if exists && !hasReadiness {
			if sourceAfter == nil {
				return ErrMarketTradeActivationRequired
			}
			boundary = laterTradeTime(sourceAfter, selectedChanged)
		}
		if !hasReadiness {
			origin := "lifecycle"
			if exists || creation == nil {
				origin = "recovery_boundary"
			}
			result, e = collectOne[MarketTradeReadiness](ctx, writer.db, `INSERT INTO market_trade_rule_readiness(rule_id,target_key,scope_fingerprint,eligibility_after,activation_origin,scope_source_fingerprint)
    VALUES($1,$2,$3,$4,$5,$6) RETURNING `+marketTradeReadinessColumns, rule.ID, target.TargetKey, fingerprint, boundary, origin, sourceFingerprint)
			return e
		}
		changed := result.ScopeFingerprint != fingerprint
		sourceChanged := result.ScopeSourceFingerprint != sourceFingerprint
		newBoundary := result.EligibilityAfter
		if changed {
			if scopeChanged == nil {
				return ErrMarketTradeActivationRequired
			}
			newBoundary = laterTradeTime(newBoundary, scopeChanged)
		}
		if sourceChanged {
			if selectedChanged == nil && !changed {
				return ErrMarketTradeActivationRequired
			}
			newBoundary = laterTradeTime(newBoundary, selectedChanged)
		}
		// Restore's real notification fence is a lifecycle boundary, not a v2 ID.
		newBoundary = laterTradeTime(newBoundary, sourceAfter)
		if changed || sourceChanged || !sameTradeTime(newBoundary, result.EligibilityAfter) {
			result, e = collectOne[MarketTradeReadiness](ctx, writer.db, `UPDATE market_trade_rule_readiness SET scope_fingerprint=$3,scope_source_fingerprint=$5,
    scope_revision=scope_revision+1,eligibility_after=$4,latest_event_at=NULL,status='pending',reason='scope_or_lifecycle_changed',version=version+1,updated_at=NOW()
    WHERE rule_id=$1 AND target_key=$2 RETURNING `+marketTradeReadinessColumns, rule.ID, target.TargetKey, fingerprint, newBoundary, sourceFingerprint)
			if e != nil {
				return e
			}
		}
		return nil
	})
	return
}

func (s *Store) ListMarketTradeWarmupPeriods(ctx context.Context, readiness MarketTradeReadiness, from, through time.Time) ([]MarketTradeWarmupPeriod, error) {
	rows, err := collectMany[MarketTradeWarmupPeriod](ctx, s.db, `SELECT id,from_at,through_at,ready_from_at FROM market_trade_warmup_periods
 WHERE rule_id=$1 AND target_key=$2 AND scope_revision=$3 AND from_at<=$5 AND (ready_from_at IS NULL OR ready_from_at>$4)
 ORDER BY from_at,id LIMIT 1001`, readiness.RuleID, readiness.TargetKey, readiness.ScopeRevision, from, through)
	if len(rows) > 1000 {
		return nil, fmt.Errorf("%w: warmup_period_budget", ErrMarketTradeWindowUnavailable)
	}
	return rows, err
}

// CommitMarketTradeReadiness is called inside the existing evaluation write
// transaction before events/outbox/cursor writes. Failure rolls everything back.
func (s *Store) CommitMarketTradeReadiness(ctx context.Context, rule MarketRule, project MarketProject, target MarketRuleTarget, expected MarketTradeReadiness, expectedState json.RawMessage, decisions []MarketTradeReadinessDecision, status, reason string) error {
	if err := s.verifyMarketTradeScope(ctx, rule, project, target); err != nil {
		return err
	}
	sourceFingerprint, _, err := readMarketTradeScopeSource(ctx, s.db, project.ID, rule.PoolScope, target)
	if err != nil {
		return err
	}
	if sourceFingerprint != expected.ScopeSourceFingerprint {
		return ErrMarketRuleEvaluationStale
	}
	var unchanged bool
	if err := s.db.QueryRow(ctx, `SELECT state=$3::jsonb FROM market_rule_target_states WHERE market_rule_id=$1 AND target_key=$2 FOR UPDATE`, rule.ID, target.TargetKey, expectedState).Scan(&unchanged); err != nil {
		return err
	}
	if !unchanged {
		return ErrMarketRuleEvaluationStale
	}
	latest := expected.LatestEventAt
	for _, decision := range decisions {
		at := decision.OccurredAt
		latest = laterTradeTime(latest, &at)
	}
	tag, err := s.db.Exec(ctx, `UPDATE market_trade_rule_readiness SET status=$5,reason=$6,latest_event_at=$7,version=version+1,updated_at=NOW()
 WHERE rule_id=$1 AND target_key=$2 AND scope_fingerprint=$3 AND version=$4 AND scope_source_fingerprint=$8`, rule.ID, target.TargetKey, expected.ScopeFingerprint, expected.Version, status, reason, latest, expected.ScopeSourceFingerprint)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrMarketRuleEvaluationStale
	}
	// Process event time separately from publication time. A completed period is
	// immutable; future late publication inside it remains ineligible.
	ordered := append([]MarketTradeReadinessDecision(nil), decisions...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].OccurredAt.Before(ordered[j].OccurredAt) })
	for _, decision := range ordered {
		if decision.WarmupReason != "" {
			tag, err = s.db.Exec(ctx, `UPDATE market_trade_warmup_periods SET through_at=GREATEST(through_at,$4),updated_at=NOW()
    WHERE rule_id=$1 AND target_key=$2 AND scope_revision=$3 AND ready_from_at IS NULL AND from_at<=$4`, rule.ID, target.TargetKey, expected.ScopeRevision, decision.OccurredAt)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				_, err = s.db.Exec(ctx, `INSERT INTO market_trade_warmup_periods(rule_id,target_key,scope_revision,from_at,through_at,reason,ready_from_at)
     VALUES($1,$2,$3,$4,$4,$5,(SELECT MIN(at) FROM (
      SELECT ready_from_at AS at FROM market_trade_warmup_periods WHERE rule_id=$1 AND target_key=$2 AND scope_revision=$3 AND ready_from_at>$4
      UNION ALL SELECT $6::timestamptz WHERE $6::timestamptz>$4) known))`, rule.ID, target.TargetKey, expected.ScopeRevision, decision.OccurredAt, decision.WarmupReason, knownTradeReadyAfter(expected, ordered, decision.OccurredAt))
				if err != nil {
					return err
				}
			}
		} else {
			if _, err = s.db.Exec(ctx, `UPDATE market_trade_warmup_periods SET ready_from_at=$4,updated_at=NOW()
    WHERE rule_id=$1 AND target_key=$2 AND scope_revision=$3 AND ready_from_at IS NULL AND through_at<$4`, rule.ID, target.TargetKey, expected.ScopeRevision, decision.OccurredAt); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) MarkMarketTradeReadinessBlocked(ctx context.Context, expected MarketTradeReadiness, reason string) error {
	_, err := s.db.Exec(ctx, `UPDATE market_trade_rule_readiness SET status='blocked',reason=$4,version=version+1,updated_at=NOW()
 WHERE rule_id=$1 AND target_key=$2 AND version=$3`, expected.RuleID, expected.TargetKey, expected.Version, reason)
	return err
}

func (s *Store) ListMarketProjectTradeReadiness(ctx context.Context, projectID int64, owner string) ([]MarketTradeReadiness, error) {
	rows, err := collectMany[MarketTradeReadiness](ctx, s.db, `SELECT r.rule_id,r.target_key,r.scope_fingerprint,r.scope_source_fingerprint,r.scope_revision,r.eligibility_after,
 r.activation_origin,r.latest_event_at,r.status,r.reason,r.version,r.updated_at FROM market_trade_rule_readiness r JOIN market_rules rule ON rule.id=r.rule_id
 WHERE rule.market_project_id=$1 AND rule.debox_user_id=$2 ORDER BY r.rule_id,r.target_key LIMIT 1001`, projectID, owner)
	if len(rows) > 1000 {
		return nil, ErrMarketTradeReadinessCapacity
	}
	return rows, err
}

func tradePoolIDStrings(ids []int64) []string {
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		result = append(result, fmt.Sprint(id))
	}
	return result
}

// A late earlier warmup fact cannot reopen a period across an already proven
// later ready event. The facts stay intact; only its own notification is skipped.
func knownTradeReadyAfter(expected MarketTradeReadiness, decisions []MarketTradeReadinessDecision, at time.Time) *time.Time {
	// A later observed event does not prove an unobserved intervening interval.
	// For late history gaps, audit only the witnessed database-precision point.
	if expected.LatestEventAt != nil && !at.After(*expected.LatestEventAt) {
		pointEnd := at.UTC().Truncate(time.Microsecond).Add(time.Microsecond)
		return &pointEnd
	}
	var known *time.Time
	if expected.Status == "ready" && expected.LatestEventAt != nil && expected.LatestEventAt.After(at) {
		known = expected.LatestEventAt
	}
	for _, decision := range decisions {
		if decision.WarmupReason == "" && decision.OccurredAt.After(at) && (known == nil || decision.OccurredAt.Before(*known)) {
			value := decision.OccurredAt
			known = &value
		}
	}
	return known
}

// Per-pool epochs distinguish a genuine selection period from the same final
// set of IDs. Primary edits affect only rules whose scope is actually primary.
// Every value is bounded by the existing target pool limit; no lifetime history
// or timestamp comparison is used as the configuration concurrency fence.
func readMarketTradeScopeSource(ctx context.Context, db DBTX, projectID int64, poolScope string, target MarketRuleTarget) (string, *time.Time, error) {
	pools := uniquePositiveIDs(target.TradePoolIDs)
	if len(pools) == 0 || len(pools) > 10000 {
		return "", nil, ErrMarketTradeReadinessCapacity
	}
	sort.Slice(pools, func(i, j int) bool { return pools[i] < pools[j] })
	type entry struct {
		PoolID          int64      `db:"pool_id"`
		MembershipEpoch int64      `db:"membership_epoch"`
		PrimaryEpoch    int64      `db:"primary_epoch"`
		SelectedAt      *time.Time `db:"selected_at"`
		PrimaryAt       *time.Time `db:"primary_at"`
	}
	rows, err := collectMany[entry](ctx, db, `SELECT ids.pool_id,
  COALESCE((c.pool_boundaries->ids.pool_id::text->>'membership_epoch')::bigint,0) AS membership_epoch,
  COALESCE((c.pool_boundaries->ids.pool_id::text->>'primary_epoch')::bigint,0) AS primary_epoch,
  (c.pool_boundaries->ids.pool_id::text->>'selected_at')::timestamptz AS selected_at,
  (c.pool_boundaries->ids.pool_id::text->>'primary_at')::timestamptz AS primary_at
 FROM unnest($2::bigint[]) ids(pool_id) LEFT JOIN market_project_trade_scope_changes c ON c.project_id=$1
 ORDER BY ids.pool_id`, projectID, pools)
	if err != nil {
		return "", nil, err
	}
	if len(rows) != len(pools) {
		return "", nil, ErrMarketTradeActivationRequired
	}
	var changedAt *time.Time
	evidence := make([][3]int64, 0, len(rows))
	for _, row := range rows {
		value := [3]int64{row.PoolID, row.MembershipEpoch, 0}
		changedAt = laterTradeTime(changedAt, row.SelectedAt)
		if poolScope == "primary" {
			value[2] = row.PrimaryEpoch
			changedAt = laterTradeTime(changedAt, row.PrimaryAt)
		}
		evidence = append(evidence, value)
	}
	payload, _ := json.Marshal([]any{tradeScopeFingerprint(target), evidence})
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:]), changedAt, nil
}
