package store

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
)

var (
	ErrResourceCapacityUnavailable = errors.New("resource capacity has not been verified or has no net budget")
	ErrResourceCapacityReached     = errors.New("resource capacity is fully reserved")
	ErrResourceCapacityState       = errors.New("resource capacity state is invalid or incomplete")
)

const (
	resourceCapacityTimeout   = 2 * time.Second
	resourceCapacityMonthDays = 31 // Full cost envelope, not subscription duration or first-month proration.
)

// Costs are admission commitments, not measured provider bills or latency
// promises. Verification requires a complete monthly envelope covering the
// declared pool and chain scope. Unverified costs may remain placeholders or
// known floors and never authorize a new sale.
type ResourceCapacityPolicy struct {
	Verified                 bool
	AccountMonthlyCU         int64
	OtherMonthlyCU           int64
	BackgroundMonthlyCU      int64
	SafetyMonthlyCU          int64
	OrdinaryRuleMonthlyCU    int64
	InteractionRuleMonthlyCU int64
	// One chain/token collection unit, including all pools within the verified
	// pool envelope. A logical project can contain several such deployments.
	MarketProjectMonthlyCU     int64
	MarketProjectCostMaxPools  int
	MarketProjectCostChainKeys []string
	MarketChainMonthlyCU       map[string]plans.MarketChainCUCost
	Plans                      map[string]ResourcePlanCapacity
	// Zero preserves legacy Store policies. The app supplies both positive limits.
	StandardSalesSeatLimit     int
	ProfessionalSalesSeatLimit int
}

type ResourcePlanCapacity struct {
	RuleLimit              int      `json:"rule_limit"`
	InteractionRuleLimit   int      `json:"interaction_rule_limit"`
	MarketProjectLimit     int      `json:"market_project_limit"`
	MarketPoolLimit        int      `json:"market_pool_limit"`
	AllowedMarketChainKeys []string `json:"allowed_market_chain_keys"`
}

type ResourceCapacityStatus struct {
	Configured        bool  `json:"configured"`
	Verified          bool  `json:"verified"`
	NetMonthlyCU      int64 `json:"net_monthly_cu"`
	ReservedMonthlyCU int64 `json:"reserved_monthly_cu"`
	Users             int64 `json:"users"`
	UnknownPlan       bool  `json:"unknown_plan"`
	ownerMonthlyCU    int64
}

type resourcePlanSnapshot struct {
	SchemaVersion int    `json:"schema_version"`
	PlanCode      string `json:"plan_code"`
	MonthDays     int    `json:"month_days"`
	ResourcePlanCapacity
	MonthlyCU                   int64                              `json:"monthly_cu"`
	OrdinaryRuleMonthlyCU       int64                              `json:"ordinary_rule_monthly_cu"`
	InteractionRuleMonthlyCU    int64                              `json:"interaction_rule_monthly_cu"`
	MarketProjectMonthlyCU      int64                              `json:"market_project_monthly_cu"`
	MarketProjectCostMaxPools   int                                `json:"market_project_cost_max_pools"`
	MarketProjectCostChainKeys  []string                           `json:"market_project_cost_chain_keys"`
	MarketDeploymentsPerProject int                                `json:"market_deployments_per_project"`
	MarketChainMonthlyCU        map[string]plans.MarketChainCUCost `json:"market_chain_monthly_cu,omitempty"`
}

// An absent option preserves legacy Store adapters. Production explicitly
// supplies the option even when Verified=false, so new capacity fails closed.
func WithResourceCapacityPolicy(policy ResourceCapacityPolicy) OpenOption {
	return func(settings *openSettings) {
		copy := policy
		copy.MarketProjectCostChainKeys = cloneResourceCapacityChainKeys(policy.MarketProjectCostChainKeys)
		copy.MarketChainMonthlyCU = maps.Clone(policy.MarketChainMonthlyCU)
		copy.Plans = make(map[string]ResourcePlanCapacity, len(policy.Plans))
		for code, plan := range policy.Plans {
			plan.AllowedMarketChainKeys = cloneResourceCapacityChainKeys(plan.AllowedMarketChainKeys)
			copy.Plans[code] = plan
		}
		settings.resourceCapacity = &copy
	}
}

func cloneResourceCapacityChainKeys(keys []string) []string {
	if keys == nil {
		return nil
	}
	return append([]string{}, keys...)
}

// An empty scope is unknown, never an all-chain grant. Normalize aliases for
// comparison and new audit snapshots without modifying caller-owned slices.
func resourceCapacityChainKeys(keys []string) ([]string, error) {
	if len(keys) > 6 {
		return nil, fmt.Errorf("%w: resource chain scope exceeds supported chain capacity", ErrResourceCapacityState)
	}
	if keys == nil {
		return nil, nil
	}
	result := make([]string, 0, len(keys))
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("%w: resource chain scope contains an empty key", ErrResourceCapacityState)
		}
		profile, err := chain.ChainProfile(key, "")
		if err != nil || seen[profile.Key] {
			return nil, fmt.Errorf("%w: resource chain scope contains an unsupported or repeated chain", ErrResourceCapacityState)
		}
		seen[profile.Key] = true
		result = append(result, profile.Key)
	}
	return result, nil
}

func validateResourceCapacityPolicy(policy ResourceCapacityPolicy) error {
	if policy.StandardSalesSeatLimit < 0 || policy.ProfessionalSalesSeatLimit < 0 {
		return fmt.Errorf("%w: sales seat limits must not be negative", ErrResourceCapacityState)
	}
	for _, value := range []int64{policy.AccountMonthlyCU, policy.OtherMonthlyCU, policy.BackgroundMonthlyCU, policy.SafetyMonthlyCU, policy.OrdinaryRuleMonthlyCU, policy.InteractionRuleMonthlyCU, policy.MarketProjectMonthlyCU} {
		if value < 0 {
			return fmt.Errorf("%w: resource costs and reserves must not be negative", ErrResourceCapacityState)
		}
	}
	if policy.OrdinaryRuleMonthlyCU < 1 || policy.InteractionRuleMonthlyCU < policy.OrdinaryRuleMonthlyCU || len(policy.MarketChainMonthlyCU) == 0 && policy.MarketProjectMonthlyCU < 1 {
		return fmt.Errorf("%w: capacity accounting always requires positive costs and interaction cost at least ordinary cost", ErrResourceCapacityState)
	}
	if policy.MarketProjectCostMaxPools < 0 {
		return fmt.Errorf("%w: project cost pool coverage must not be negative", ErrResourceCapacityState)
	}
	if len(policy.MarketChainMonthlyCU) > 0 && (policy.AccountMonthlyCU == 0 || policy.MarketProjectCostMaxPools == 0) {
		return fmt.Errorf("%w: chain pricing requires a positive account budget and pool coverage", ErrResourceCapacityState)
	}
	for key, cost := range policy.MarketChainMonthlyCU {
		profile, err := chain.ChainProfile(key, "")
		if err != nil || profile.Key != key || cost.Deployment < 1 || cost.Pool < 1 {
			return fmt.Errorf("%w: invalid market CU price for chain %q", ErrResourceCapacityState, key)
		}
	}
	costChains, err := resourceCapacityChainKeys(policy.MarketProjectCostChainKeys)
	if err != nil {
		return err
	}
	coveredChains := make(map[string]bool, len(costChains))
	for _, key := range costChains {
		coveredChains[key] = true
	}
	for _, code := range []string{"free", "standard", "professional"} {
		plan, exists := policy.Plans[code]
		if !exists || plan.RuleLimit < 1 || plan.InteractionRuleLimit < 0 || plan.InteractionRuleLimit > plan.RuleLimit || plan.MarketProjectLimit < 0 || plan.MarketPoolLimit < 0 {
			return fmt.Errorf("%w: invalid %s plan capacity", ErrResourceCapacityState, code)
		}
		planChains, err := resourceCapacityChainKeys(plan.AllowedMarketChainKeys)
		if err != nil {
			return err
		}
		if policy.Verified && plan.MarketProjectLimit > 0 {
			if plan.MarketPoolLimit < 1 || len(planChains) == 0 || policy.MarketProjectCostMaxPools < plan.MarketPoolLimit {
				return fmt.Errorf("%w: verified project cost does not cover %s plan pool scope", ErrResourceCapacityState, code)
			}
			for _, key := range planChains {
				if len(policy.MarketChainMonthlyCU) > 0 {
					if _, ok := policy.MarketChainMonthlyCU[key]; !ok {
						return fmt.Errorf("%w: missing %s plan market price on chain %s", ErrResourceCapacityState, code, key)
					}
					continue
				}
				if !coveredChains[key] {
					return fmt.Errorf("%w: verified project cost does not cover %s plan chain scope", ErrResourceCapacityState, code)
				}
			}
		}
		if _, err := resourcePlanMonthlyCU(policy, plan); err != nil {
			return err
		}
	}
	return nil
}

func resourcePlanMonthlyCU(policy ResourceCapacityPolicy, plan ResourcePlanCapacity) (int64, error) {
	deployments, err := resourcePlanDeploymentsPerProject(plan)
	if err != nil {
		return 0, err
	}
	ordinary := new(big.Int).Mul(big.NewInt(int64(plan.RuleLimit-plan.InteractionRuleLimit)), big.NewInt(policy.OrdinaryRuleMonthlyCU))
	interactions := new(big.Int).Mul(big.NewInt(int64(plan.InteractionRuleLimit)), big.NewInt(policy.InteractionRuleMonthlyCU))
	project := resourceMarketProjectMonthlyCU(policy, plan, deployments)
	projects := new(big.Int).Mul(big.NewInt(int64(plan.MarketProjectLimit)), project)
	total := new(big.Int).Add(ordinary, interactions)
	total.Add(total, projects)
	if !total.IsInt64() || total.Sign() <= 0 {
		return 0, fmt.Errorf("%w: plan monthly CU overflow or zero", ErrResourceCapacityState)
	}
	return total.Int64(), nil
}

func resourceMarketProjectMonthlyCU(policy ResourceCapacityPolicy, plan ResourcePlanCapacity, deployments int) *big.Int {
	if deployments == 0 {
		return new(big.Int)
	}
	if len(policy.MarketChainMonthlyCU) == 0 {
		return new(big.Int).Mul(big.NewInt(int64(deployments)), big.NewInt(policy.MarketProjectMonthlyCU))
	}
	keys, _ := resourceCapacityChainKeys(plan.AllowedMarketChainKeys) // validated by resourcePlanDeploymentsPerProject
	prices := make([]plans.MarketChainCUCost, 0, len(keys))
	for _, key := range keys {
		if price, ok := policy.MarketChainMonthlyCU[key]; ok {
			prices = append(prices, price)
		}
	}
	if len(prices) == 0 || len(prices) != len(keys) || plan.MarketPoolLimit > policy.MarketProjectCostMaxPools || plan.MarketPoolLimit == 0 {
		// Only an unverified legacy policy may reach this path. Reserve at the
		// account ceiling rather than pricing an uncovered project at zero.
		return new(big.Int).Mul(big.NewInt(int64(deployments)), big.NewInt(policy.AccountMonthlyCU))
	}
	slices.SortFunc(prices, func(a, b plans.MarketChainCUCost) int { return cmp.Compare(b.Deployment, a.Deployment) })
	upper := new(big.Int)
	for i, price := range prices {
		// Active deployments can retain zero pools after deselection. For each
		// possible most expensive pool chain, fund all pool slots there and the
		// most expensive remaining deployment slots on distinct chains.
		candidate := new(big.Int).Mul(big.NewInt(int64(plan.MarketPoolLimit)), big.NewInt(price.Pool))
		candidate.Add(candidate, big.NewInt(price.Deployment))
		remaining := deployments - 1
		for j, other := range prices {
			if remaining == 0 {
				break
			}
			if j != i {
				candidate.Add(candidate, big.NewInt(other.Deployment))
				remaining--
			}
		}
		if candidate.Cmp(upper) > 0 {
			upper = candidate
		}
	}
	return upper
}

// New projects require at least one selected pool for each deployment, while
// the pool limit applies across the whole logical project. Their maximum
// collection units are therefore bounded by both the allowed chains and pools.
// Missing scope remains compatible with unverified policies, but is priced at
// the supported-chain upper bound rather than becoming a zero-cost promise.
// Verified policies reject missing chain/pool scope before admission. Existing
// configurations, including legacy deployments without a selected pool, retain
// their actual chain/token accounting in resourceCapacityAggregateSQL.
func resourcePlanDeploymentsPerProject(plan ResourcePlanCapacity) (int, error) {
	if plan.MarketProjectLimit < 0 || plan.MarketPoolLimit < 0 {
		return 0, fmt.Errorf("%w: negative market capacity", ErrResourceCapacityState)
	}
	if plan.MarketProjectLimit == 0 {
		return 0, nil
	}
	keys, err := resourceCapacityChainKeys(plan.AllowedMarketChainKeys)
	if err != nil {
		return 0, err
	}
	deployments := len(keys)
	if deployments == 0 {
		deployments = len(chain.SupportedChains())
	}
	if plan.MarketPoolLimit > 0 && plan.MarketPoolLimit < deployments {
		deployments = plan.MarketPoolLimit
	}
	return deployments, nil
}

func resourceNetMonthlyCU(policy ResourceCapacityPolicy) int64 {
	net := big.NewInt(policy.AccountMonthlyCU)
	for _, reserve := range []int64{policy.OtherMonthlyCU, policy.BackgroundMonthlyCU, policy.SafetyMonthlyCU} {
		net.Sub(net, big.NewInt(reserve))
	}
	if net.Sign() <= 0 || !net.IsInt64() {
		return 0
	}
	return net.Int64()
}

type resourceCapacityLockKey struct{}
type resourceCapacityAdmission struct {
	policy          *ResourceCapacityPolicy
	owner           string
	beforeMonthlyCU int64
}

// All capacity-increasing entry points take this lock BEFORE user/order row
// locks, and retain it through their existing transaction commit. The returned
// context bounds the whole admission, not just each individual SQL statement.
func (s *Store) beginResourceCapacityCheck(ctx context.Context, tx DBTX, owners ...string) (context.Context, context.CancelFunc, error) {
	if s.resourceCapacity == nil {
		return ctx, func() {}, nil
	}
	if len(owners) > 1 || len(owners) == 1 && strings.TrimSpace(owners[0]) != owners[0] {
		return ctx, func() {}, fmt.Errorf("%w: capacity admission accepts at most one exact owner", ErrResourceCapacityState)
	}
	owner := ""
	if len(owners) == 1 {
		owner = owners[0]
	}
	bounded, cancel := context.WithTimeout(ctx, resourceCapacityTimeout)
	if err := validateResourceCapacityPolicy(*s.resourceCapacity); err != nil {
		cancel()
		return ctx, func() {}, err
	}
	if _, err := tx.Exec(bounded, `SET LOCAL statement_timeout='2s'; SET LOCAL lock_timeout='2s'`); err != nil {
		cancel()
		return ctx, func() {}, fmt.Errorf("%w: configure capacity transaction budget: %w", ErrResourceCapacityState, err)
	}
	if _, err := tx.Exec(bounded, `SELECT pg_advisory_xact_lock(1684370038,1)`); err != nil {
		cancel()
		return ctx, func() {}, fmt.Errorf("%w: lock resource capacity: %w", ErrResourceCapacityState, err)
	}
	before, err := s.resourceCapacityStatus(bounded, tx, owner)
	if err != nil {
		cancel()
		return ctx, func() {}, err
	}
	admission := resourceCapacityAdmission{policy: s.resourceCapacity, owner: owner, beforeMonthlyCU: before.ownerMonthlyCU}
	return context.WithValue(bounded, resourceCapacityLockKey{}, admission), cancel, nil
}

func (s *Store) checkResourceCapacity(ctx context.Context, tx DBTX) error {
	if s.resourceCapacity == nil {
		return nil
	}
	admission, ok := ctx.Value(resourceCapacityLockKey{}).(resourceCapacityAdmission)
	if !ok || admission.policy != s.resourceCapacity || admission.owner == "" {
		return fmt.Errorf("%w: global capacity lock and candidate owner are required", ErrResourceCapacityState)
	}
	status, err := s.resourceCapacityStatus(ctx, tx, admission.owner)
	if err != nil {
		return err
	}
	// Only this owner's promises may cover their candidate. An unrelated user's
	// concurrent stop/delete cannot fund growth while sales are unverified.
	// New orders use a stricter path below even for owner-neutral renewals.
	if status.ownerMonthlyCU <= admission.beforeMonthlyCU {
		return nil
	}
	return requireResourceCapacityStatus(status)
}

func requireResourceCapacityStatus(status ResourceCapacityStatus) error {
	if !status.Verified || status.NetMonthlyCU == 0 {
		return ErrResourceCapacityUnavailable
	}
	if status.UnknownPlan {
		return fmt.Errorf("%w: an existing order, subscription or active market scope lacks a CU price", ErrResourceCapacityState)
	}
	if status.ReservedMonthlyCU > status.NetMonthlyCU {
		return ErrResourceCapacityReached
	}
	return nil
}

// ReadResourceCapacityStatus is only a bounded diagnostic snapshot. It grants
// no capacity; increasing operations must use the locked transactional check.
func (s *Store) ReadResourceCapacityStatus(ctx context.Context) (ResourceCapacityStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, resourceCapacityTimeout)
	defer cancel()
	return s.resourceCapacityStatus(ctx, s.db, "")
}

func (s *Store) resourceCapacityStatus(ctx context.Context, db DBTX, owner string) (ResourceCapacityStatus, error) {
	if s.resourceCapacity == nil {
		return ResourceCapacityStatus{}, nil
	}
	policy := *s.resourceCapacity
	status := ResourceCapacityStatus{Configured: true, Verified: policy.Verified, NetMonthlyCU: resourceNetMonthlyCU(policy)}
	if err := validateResourceCapacityPolicy(policy); err != nil {
		return status, err
	}
	planCosts := make([]resourcePlanSnapshot, 0, 3)
	for _, code := range []string{"free", "standard", "professional"} {
		plan := policy.Plans[code]
		cu, err := resourcePlanMonthlyCU(policy, plan)
		if err != nil {
			return status, err
		}
		planCosts = append(planCosts, resourcePlanSnapshot{PlanCode: code, MonthlyCU: cu})
	}
	body, err := json.Marshal(planCosts)
	if err != nil {
		return status, err
	}
	var prices json.RawMessage
	unknownMarketCU := policy.MarketProjectMonthlyCU
	if len(policy.MarketChainMonthlyCU) > 0 {
		byChainID := make(map[int64]plans.MarketChainCUCost, len(policy.MarketChainMonthlyCU))
		for key, cost := range policy.MarketChainMonthlyCU {
			profile, _ := chain.ChainProfile(key, "") // validated above
			byChainID[profile.ChainID] = cost
		}
		prices, err = json.Marshal(byChainID)
		if err != nil {
			return status, err
		}
		unknownMarketCU = policy.AccountMonthlyCU
	}
	var rawCU, rawOwnerCU string
	err = db.QueryRow(ctx, resourceCapacityAggregateSQL, body, policy.OrdinaryRuleMonthlyCU, policy.InteractionRuleMonthlyCU, unknownMarketCU, owner, prices, policy.MarketProjectCostMaxPools).Scan(&rawCU, &status.Users, &status.UnknownPlan, &rawOwnerCU)
	if err != nil {
		return status, fmt.Errorf("%w: aggregate resource capacity: %w", ErrResourceCapacityState, err)
	}
	used, ok := new(big.Int).SetString(rawCU, 10)
	if !ok || used.Sign() < 0 || !used.IsInt64() {
		return status, fmt.Errorf("%w: aggregate CU is invalid or too large", ErrResourceCapacityState)
	}
	status.ReservedMonthlyCU = used.Int64()
	ownerUsed, ok := new(big.Int).SetString(rawOwnerCU, 10)
	if !ok || ownerUsed.Sign() < 0 || !ownerUsed.IsInt64() {
		return status, fmt.Errorf("%w: owner aggregate CU is invalid or too large", ErrResourceCapacityState)
	}
	status.ownerMonthlyCU = ownerUsed.Int64()
	return status, nil
}

// One fixed-count aggregate query uses database state only. There is no
// per-user loop and no polling cache to drift from orders or enabled rules.
// Actual costs are summed first, then MAX-ed with each user's sale promises.
const resourceCapacityAggregateSQL = `WITH
 plan_costs AS(SELECT plan_code,monthly_cu::numeric FROM jsonb_to_recordset($1::jsonb) AS p(plan_code text,monthly_cu bigint)),
 observed_at AS MATERIALIZED(SELECT clock_timestamp() AS at),
 actual_rules AS(SELECT debox_user_id,SUM(CASE WHEN rule_type='address_interaction' THEN $3::numeric ELSE $2::numeric END) AS cu
  FROM watch_rules WHERE enabled=1 AND run_status='active' GROUP BY debox_user_id),
 project_deployments AS(
  SELECT mp.debox_user_id,mad.chain_id,LOWER(mad.token_address) AS token_address,mp.id AS project_id,mpd.id AS deployment_id
  FROM market_projects mp JOIN market_project_deployments mpd ON mpd.market_project_id=mp.id AND mpd.status='active'
  JOIN market_asset_deployments mad ON mad.id=mpd.market_asset_deployment_id WHERE mp.status='active'
  UNION ALL
  SELECT mp.debox_user_id,mp.chain_id,LOWER(mp.token_address),mp.id,NULL FROM market_projects mp WHERE mp.status='active'
   AND NOT EXISTS(SELECT 1 FROM market_project_deployments mpd WHERE mpd.market_project_id=mp.id)),
 project_units AS(
  SELECT d.debox_user_id,d.chain_id,d.token_address,COUNT(DISTINCT p.id) AS pools
  FROM project_deployments d
  LEFT JOIN market_project_pools mpp ON $6::jsonb IS NOT NULL AND mpp.market_project_id=d.project_id AND mpp.selected=1
   AND (mpp.market_project_deployment_id=d.deployment_id OR mpp.market_project_deployment_id IS NULL)
  LEFT JOIN market_pools p ON p.id=mpp.market_pool_id AND p.chain_id=d.chain_id
   AND (mpp.market_project_deployment_id IS NOT NULL OR LOWER(p.token0_address)=d.token_address OR LOWER(p.token1_address)=d.token_address)
  GROUP BY d.debox_user_id,d.chain_id,d.token_address),
 priced_projects AS(
  SELECT debox_user_id,pools,$6::jsonb->(chain_id::text) AS price FROM project_units),
 actual_projects AS(
  SELECT debox_user_id,SUM(CASE WHEN price IS NULL OR pools>$7::bigint THEN $4::numeric
   ELSE (price->>'deployment')::numeric+pools*(price->>'pool')::numeric END) AS cu,
   BOOL_OR($6::jsonb IS NOT NULL AND (price IS NULL OR pools>$7::bigint)) AS unknown_plan
  FROM priced_projects GROUP BY debox_user_id),
 actual AS(SELECT debox_user_id,SUM(cu) AS cu,BOOL_OR(unknown_plan) AS unknown_plan FROM(
  SELECT debox_user_id,cu,FALSE AS unknown_plan FROM actual_rules UNION ALL SELECT debox_user_id,cu,unknown_plan FROM actual_projects) a GROUP BY debox_user_id),
 ordinary AS(SELECT s.debox_user_id,MAX(GREATEST(COALESCE(h.monthly_cu::numeric,0),COALESCE(p.monthly_cu,0))) AS cu,
  BOOL_OR(h.id IS NULL AND p.monthly_cu IS NULL) AS unknown_plan
  FROM subscriptions s LEFT JOIN resource_capacity_holds h ON h.subscription_id=s.id AND h.debox_user_id=s.debox_user_id AND h.plan_code=s.plan_code
  LEFT JOIN plan_costs p ON p.plan_code=s.plan_code CROSS JOIN observed_at t
  WHERE s.status='active' AND s.is_permanent=0 AND s.plan_code<>'free' AND s.expires_at>t.at GROUP BY s.debox_user_id),
 pending AS(SELECT o.debox_user_id,MAX(GREATEST(COALESCE(h.monthly_cu::numeric,0),COALESCE(p.monthly_cu,0))) AS cu,
  BOOL_OR(h.id IS NULL AND p.monthly_cu IS NULL) AS unknown_plan
  FROM orders o LEFT JOIN resource_capacity_holds h ON h.order_id=o.id AND h.debox_user_id=o.debox_user_id AND h.plan_code=o.plan_code
  LEFT JOIN plan_costs p ON p.plan_code=o.plan_code WHERE o.status IN('pending','confirming') GROUP BY o.debox_user_id),
 per_user AS(SELECT debox_user_id,MAX(cu) AS cu,BOOL_OR(unknown_plan) AS unknown_plan FROM(
  SELECT * FROM actual UNION ALL SELECT * FROM ordinary UNION ALL SELECT * FROM pending) c GROUP BY debox_user_id)
 SELECT COALESCE(SUM(cu),0)::text,COUNT(*)::bigint,COALESCE(BOOL_OR(unknown_plan),FALSE),
 COALESCE(MAX(cu) FILTER(WHERE $5::text<>'' AND debox_user_id=$5),0)::text FROM per_user`

// Call after inserting the candidate order, before committing its existing
// transaction. A failed check rolls back both this hold and pending replacement.
func (s *Store) reserveResourceCapacityOrder(ctx context.Context, tx DBTX, order Order) error {
	if s.resourceCapacity == nil {
		return nil
	}
	admission, ok := ctx.Value(resourceCapacityLockKey{}).(resourceCapacityAdmission)
	if !ok || admission.policy != s.resourceCapacity {
		return fmt.Errorf("%w: global capacity lock is required", ErrResourceCapacityState)
	}
	if admission.owner != "" && admission.owner != order.DeBoxUserID {
		return fmt.Errorf("%w: order differs from capacity admission owner", ErrResourceCapacityState)
	}
	// A sale promises a complete plan, even when replacing an equal pending
	// order or renewing an already reserved plan. It always needs verification.
	if !s.resourceCapacity.Verified || resourceNetMonthlyCU(*s.resourceCapacity) == 0 {
		return ErrResourceCapacityUnavailable
	}
	code := strings.TrimSpace(order.PlanCode)
	plan, exists := s.resourceCapacity.Plans[code]
	if order.ID < 1 || order.DeBoxUserID == "" || (code != "standard" && code != "professional") || !exists || order.Status != "pending" && order.Status != "confirming" {
		return ErrResourceCapacityState
	}
	cu, err := resourcePlanMonthlyCU(*s.resourceCapacity, plan)
	if err != nil {
		return err
	}
	plan.AllowedMarketChainKeys, err = resourceCapacityChainKeys(plan.AllowedMarketChainKeys)
	if err != nil {
		return err
	}
	costChains, err := resourceCapacityChainKeys(s.resourceCapacity.MarketProjectCostChainKeys)
	if err != nil {
		return err
	}
	deployments, err := resourcePlanDeploymentsPerProject(plan)
	if err != nil {
		return err
	}
	snapshot := resourcePlanSnapshot{SchemaVersion: 3, PlanCode: code, MonthDays: resourceCapacityMonthDays, ResourcePlanCapacity: plan, MonthlyCU: cu,
		OrdinaryRuleMonthlyCU: s.resourceCapacity.OrdinaryRuleMonthlyCU, InteractionRuleMonthlyCU: s.resourceCapacity.InteractionRuleMonthlyCU, MarketProjectMonthlyCU: s.resourceCapacity.MarketProjectMonthlyCU,
		MarketProjectCostMaxPools: s.resourceCapacity.MarketProjectCostMaxPools, MarketProjectCostChainKeys: costChains, MarketDeploymentsPerProject: deployments}
	if len(s.resourceCapacity.MarketChainMonthlyCU) > 0 {
		snapshot.SchemaVersion = 4
		snapshot.MarketChainMonthlyCU = s.resourceCapacity.MarketChainMonthlyCU
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO resource_capacity_holds(order_id,debox_user_id,plan_code,monthly_cu,plan_snapshot)
 VALUES($1,$2,$3,$4,$5::jsonb)`, order.ID, order.DeBoxUserID, code, cu, body); err != nil {
		return fmt.Errorf("%w: reserve order resource capacity: %w", ErrResourceCapacityState, err)
	}
	status, err := s.resourceCapacityStatus(ctx, tx, order.DeBoxUserID)
	if err != nil {
		return err
	}
	return requireResourceCapacityStatus(status)
}

// Existing paid promises are honored even when the current policy is unknown
// or full. No hold on a legacy order is a supported no-op, not a new admission.
func (s *Store) linkResourceCapacityOrder(ctx context.Context, tx DBTX, orderID, subscriptionID int64) error {
	if s.resourceCapacity == nil {
		return nil
	}
	if orderID < 1 || subscriptionID < 1 {
		return ErrResourceCapacityState
	}
	ctx, cancel := context.WithTimeout(ctx, resourceCapacityTimeout)
	defer cancel()
	tag, err := tx.Exec(ctx, `UPDATE resource_capacity_holds h SET subscription_id=$2,linked_at=COALESCE(h.linked_at,clock_timestamp())
 FROM orders o,subscriptions s WHERE h.order_id=$1 AND o.id=h.order_id AND s.id=$2
 AND o.status='paid' AND s.is_permanent=0 AND h.debox_user_id=o.debox_user_id AND h.debox_user_id=s.debox_user_id
 AND h.plan_code=o.plan_code AND h.plan_code=s.plan_code AND(h.subscription_id IS NULL OR h.subscription_id=$2)`, orderID, subscriptionID)
	if err != nil {
		return fmt.Errorf("%w: link paid resource capacity: %w", ErrResourceCapacityState, err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM resource_capacity_holds WHERE order_id=$1)`, orderID).Scan(&exists); err != nil {
		return fmt.Errorf("%w: read legacy paid resource hold: %w", ErrResourceCapacityState, err)
	}
	if exists {
		return fmt.Errorf("%w: paid order hold does not match subscription", ErrResourceCapacityState)
	}
	return nil
}
