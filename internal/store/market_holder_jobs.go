package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	MarketHolderRefreshMaxWorkers   = 2
	MarketHolderRefreshMaxHolders   = 2000
	MarketHolderRefreshPageSize     = 100
	MarketHolderRefreshMaxPages     = (MarketHolderRefreshMaxHolders + MarketHolderRefreshPageSize - 1) / MarketHolderRefreshPageSize
	MarketHolderRefreshMinutePages  = 60
	MarketHolderRefreshRollingPages = 120
	MarketHolderRefreshLease        = 90 * time.Second
	holderRefreshPageWindow         = 15 * time.Minute
	holderJobDBTimeout              = 2 * time.Second
	holderJobApplyTimeout           = 10 * time.Second
)

var ErrMarketHolderRefreshLeaseLost = errors.New("holder refresh lease is no longer owned")
var ErrMarketHolderRefreshBudget = errors.New("holder page budget is exhausted")
var ErrMarketHolderRefreshDemandLost = errors.New("holder refresh no longer has active eligible rule demand")

type MarketHolderRefreshBudgetError struct{ RetryAt time.Time }

func (e *MarketHolderRefreshBudgetError) Error() string { return ErrMarketHolderRefreshBudget.Error() }
func (e *MarketHolderRefreshBudgetError) Unwrap() error { return ErrMarketHolderRefreshBudget }

// The representative project supplies the existing token metadata only. The
// observation and lease belong to the shared chain/token, never to its owner.
type MarketHolderRefreshJob struct {
	Project             MarketProject
	LeaseToken          int64
	StartedAt           time.Time
	LeaseUntil          time.Time
	ExpectedPreviousAt  *time.Time
	ConsecutiveFailures int32
}

type holderSchedulerBudget struct {
	LastChainID      int64
	WindowAt         *time.Time
	PagesUsed        int
	RecentAdmissions []time.Time
	Active           int
	Reserved         int
	Now              time.Time
}

func readHolderSchedulerBudget(ctx context.Context, db DBTX, lock bool) (holderSchedulerBudget, error) {
	var b holderSchedulerBudget
	if lock {
		if _, err := db.Exec(ctx, `INSERT INTO market_holder_refresh_scheduler(id,window_started_at)
 VALUES(1,date_trunc('minute',clock_timestamp())) ON CONFLICT(id) DO NOTHING`); err != nil {
			return b, err
		}
		// Take the time after locking so a waiting transaction cannot age out
		// an admission early by recording its pre-lock timestamp.
		if err := db.QueryRow(ctx, `WITH locked AS MATERIALIZED (
 SELECT last_chain_id,window_started_at,pages_used,recent_page_admissions
 FROM market_holder_refresh_scheduler WHERE id=1 FOR UPDATE
 ) SELECT last_chain_id,window_started_at,pages_used,recent_page_admissions,clock_timestamp() FROM locked`).Scan(&b.LastChainID, &b.WindowAt, &b.PagesUsed, &b.RecentAdmissions, &b.Now); err != nil {
			return b, err
		}
	} else if err := db.QueryRow(ctx, `SELECT COALESCE(s.last_chain_id,0),s.window_started_at,COALESCE(s.pages_used,0),
 COALESCE(s.recent_page_admissions,'{}'::timestamptz[]),clock_timestamp()
 FROM (SELECT 1 AS id) singleton LEFT JOIN market_holder_refresh_scheduler s USING(id)`).Scan(&b.LastChainID, &b.WindowAt, &b.PagesUsed, &b.RecentAdmissions, &b.Now); err != nil {
		return b, err
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*),COALESCE(SUM(refresh_pages_reserved-refresh_pages_used),0)
 FROM market_holder_refresh_states WHERE refresh_lease_until>$1`, b.Now).Scan(&b.Active, &b.Reserved); err != nil {
		return b, err
	}
	b.Now = b.Now.UTC()
	if b.WindowAt == nil || b.Now.Truncate(time.Minute).After(*b.WindowAt) {
		b.PagesUsed = 0
	}
	cutoff := b.Now.Add(-holderRefreshPageWindow)
	b.RecentAdmissions = slices.DeleteFunc(b.RecentAdmissions, func(admittedAt time.Time) bool {
		return !admittedAt.After(cutoff)
	})
	return b, nil
}

func holderBudgetCanClaim(b holderSchedulerBudget) bool {
	promised := b.Reserved + MarketHolderRefreshMaxPages
	return b.Active < MarketHolderRefreshMaxWorkers &&
		b.PagesUsed+promised <= MarketHolderRefreshMinutePages &&
		len(b.RecentAdmissions)+promised <= MarketHolderRefreshRollingPages
}

func (b holderSchedulerBudget) minuteWindow() time.Time {
	window := b.Now.Truncate(time.Minute)
	if b.WindowAt != nil && b.WindowAt.After(window) {
		return *b.WindowAt
	}
	return window
}

func (b holderSchedulerBudget) pageRetryAt() time.Time {
	var retryAt time.Time
	if b.PagesUsed >= MarketHolderRefreshMinutePages {
		retryAt = b.minuteWindow().Add(time.Minute)
	}
	if len(b.RecentAdmissions) >= MarketHolderRefreshRollingPages {
		oldest := b.RecentAdmissions[0]
		for _, admittedAt := range b.RecentAdmissions[1:] {
			if admittedAt.Before(oldest) {
				oldest = admittedAt
			}
		}
		if availableAt := oldest.Add(holderRefreshPageWindow); availableAt.After(retryAt) {
			retryAt = availableAt
		}
	}
	return retryAt
}

// This predicate is shared by deployed and pre-deployment legacy projects.
// A scheduling backoff does not disable a rule; only actual lifecycle, scope,
// runtime parameters and current Professional entitlement determine demand.
func holderRuleDemandSQL(legacy bool) string {
	scope := `(mr.deployment_scope='all' OR EXISTS(SELECT 1 FROM market_rule_deployments mrd WHERE mrd.market_rule_id=mr.id AND mrd.market_project_deployment_id=mpd.id))`
	if legacy {
		scope = `TRUE`
	}
	return `EXISTS(SELECT 1 FROM market_rules mr WHERE mr.market_project_id=mp.id AND mr.debox_user_id=mp.debox_user_id
 AND mr.enabled=1 AND mr.run_status='active'
 AND mr.rule_type IN(` + holderRuleTypesSQL + `)
 AND ` + marketRuleSupportedParametersSQL("mr") + ` AND ` + scope + `
 AND(mr.rule_scope='standalone' OR(mr.rule_scope='combination' AND EXISTS(
 SELECT 1 FROM market_combination_members mcm JOIN market_combination_rules parent ON parent.id=mcm.market_combination_rule_id
 WHERE mcm.market_rule_id=mr.id AND mcm.source_type='market' AND parent.debox_user_id=mr.debox_user_id AND parent.enabled=1 AND parent.run_status='active')))
 ) AND EXISTS(SELECT 1 FROM subscriptions sub WHERE sub.debox_user_id=mp.debox_user_id
 AND sub.plan_code='professional' AND sub.status='active' AND(sub.is_permanent=1 OR sub.expires_at>statement_timestamp()))`
}

// Only a project which actually requests holder observations may supply the
// representative metadata. Legacy projects are used only before any deployment
// record exists, so removed deployments cannot resurrect their legacy token.
func holderDemandSourceSQL() string {
	return `SELECT ` + marketCollectionProjectColumns + ` FROM market_projects mp
 JOIN market_project_deployments mpd ON mpd.market_project_id=mp.id AND mpd.status='active'
 JOIN market_asset_deployments mad ON mad.id=mpd.market_asset_deployment_id
 WHERE mp.status='active' AND ` + holderRuleDemandSQL(false) + `
 UNION ALL SELECT mp.id,mp.debox_user_id,mp.market_asset_id,NULL::bigint AS market_asset_deployment_id,NULL::bigint AS market_project_deployment_id,
 mp.chain_key,mp.chain_id,mp.token_address,mp.token_name,mp.token_symbol,mp.token_decimals,mp.total_supply_raw::text,
 mp.status,mp.pause_reason,mp.four_meme_status,mp.main_pool_id,mp.metadata,mp.last_discovered_at,mp.created_at,mp.updated_at
 FROM market_projects mp WHERE mp.status='active'
 AND NOT EXISTS(SELECT 1 FROM market_project_deployments d WHERE d.market_project_id=mp.id)
 AND ` + holderRuleDemandSQL(true)
}

// Each of the six chains contributes at most one due candidate. Rotation is
// persisted only for a real claim; within a chain the least recently attempted
// token wins. Multiple demanding owners still share the chain/token lease.
func nextHolderRefreshProject(ctx context.Context, db DBTX, lastChainID int64, now time.Time, interval time.Duration, scopes ...[]int64) (*MarketProject, error) {
	query := `WITH chains(chain_id) AS (VALUES (1::bigint),(10),(56),(137),(8453),(42161)),
 demands AS NOT MATERIALIZED (` + holderDemandSourceSQL() + `), candidates AS (
 SELECT chosen.* FROM chains c CROSS JOIN LATERAL (
  SELECT demand.* FROM demands demand
  LEFT JOIN market_holder_refresh_states state ON state.chain_id=demand.chain_id AND state.token_address=demand.token_address
  WHERE demand.chain_id=c.chain_id
   AND (state.refresh_lease_until IS NULL OR state.refresh_lease_until<=$2)
   AND (state.next_attempt_at IS NULL OR state.next_attempt_at<=$2)
   AND COALESCE(state.next_due_at,state.last_success_at+$3::double precision*INTERVAL '1 second','-infinity')<=$2
  ORDER BY state.last_attempt_at NULLS FIRST,demand.created_at,demand.id,demand.market_project_deployment_id LIMIT 1
 ) chosen)
 SELECT * FROM candidates ORDER BY CASE WHEN chain_id>$1 THEN 0 ELSE 1 END,chain_id LIMIT 1`
	args := []any{lastChainID, now.UTC(), interval.Seconds()}
	if len(scopes) > 0 && scopes[0] != nil {
		query = strings.Replace(query, "WITH chains(chain_id) AS (VALUES (1::bigint),(10),(56),(137),(8453),(42161))", "WITH chains(chain_id) AS (SELECT UNNEST($4::bigint[]))", 1)
		args = append(args, scopes[0])
	}
	return collectOptional[MarketProject](ctx, db, query, args...)
}

func (s *Store) holderRefreshChainAllowed(chainID int64) bool {
	ids := s.marketMonitoringChainIDs()
	if ids == nil {
		return true
	}
	for _, id := range ids {
		if id == chainID {
			return true
		}
	}
	return false
}

func (s *Store) checkHolderRefreshDemand(ctx context.Context, db DBTX, project MarketProject) error {
	if !s.holderRefreshChainAllowed(project.ChainID) {
		return ErrMarketHolderRefreshDemandLost
	}
	var active, metadataMatch bool
	err := db.QueryRow(ctx, `WITH demand AS NOT MATERIALIZED (`+holderDemandSourceSQL()+`)
 SELECT EXISTS(SELECT 1 FROM demand WHERE chain_id=$1 AND token_address=$2),
 EXISTS(SELECT 1 FROM demand WHERE chain_id=$1 AND token_address=$2 AND token_decimals=$3 AND total_supply_raw::numeric IS NOT DISTINCT FROM $4::numeric)`, project.ChainID, project.TokenAddress, project.TokenDecimals, project.TotalSupplyRaw).Scan(&active, &metadataMatch)
	if err != nil {
		return err
	}
	if !active {
		return ErrMarketHolderRefreshDemandLost
	}
	if !metadataMatch {
		return ErrStaleMarketHolderRefresh
	}
	return nil
}

// Idle polls stay read-only, including an uninitialized scheduler. A real
// candidate is rechecked while holding the single short scheduling row lock.
func (s *Store) ClaimMarketHolderRefreshJob(ctx context.Context, interval time.Duration) (*MarketHolderRefreshJob, error) {
	if interval <= 0 {
		return nil, ErrInvalidMarketHolder
	}
	ctx, cancel := context.WithTimeout(ctx, holderJobDBTimeout)
	defer cancel()
	b, err := readHolderSchedulerBudget(ctx, s.db, false)
	if err != nil || !holderBudgetCanClaim(b) {
		return nil, err
	}
	project, err := nextHolderRefreshProject(ctx, s.db, b.LastChainID, b.Now, interval, s.marketMonitoringChainIDs())
	if err != nil || project == nil {
		return nil, err
	}
	return withTxValue(ctx, s.db, func(tx DBTX) (*MarketHolderRefreshJob, error) {
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='2s'; SET LOCAL lock_timeout='2s'`); err != nil {
			return nil, err
		}
		b, err := readHolderSchedulerBudget(ctx, tx, true)
		if err != nil || !holderBudgetCanClaim(b) {
			return nil, err
		}
		project, err := nextHolderRefreshProject(ctx, tx, b.LastChainID, b.Now, interval, s.marketMonitoringChainIDs())
		if err != nil || project == nil {
			return nil, err
		}
		job := &MarketHolderRefreshJob{Project: *project}
		err = tx.QueryRow(ctx, `INSERT INTO market_holder_refresh_states(chain_id,chain_key,token_address,
 last_attempt_at,status,next_due_at,next_attempt_at,refresh_lease_token,refresh_lease_until,refresh_started_at,refresh_pages_reserved,refresh_pages_used)
 VALUES($1,$2,$3,$4::timestamptz,'source_coverage_unknown',$4::timestamptz,$4::timestamptz,1,$4::timestamptz+INTERVAL '90 seconds',$4::timestamptz,$6,0)
 ON CONFLICT(chain_id,token_address) DO UPDATE SET
 next_due_at=COALESCE(market_holder_refresh_states.next_due_at,market_holder_refresh_states.last_success_at+$5::double precision*INTERVAL '1 second',$4::timestamptz),
 last_attempt_at=$4::timestamptz,status='source_coverage_unknown',refresh_lease_token=market_holder_refresh_states.refresh_lease_token+1,
 refresh_lease_until=$4::timestamptz+INTERVAL '90 seconds',refresh_started_at=$4::timestamptz,refresh_pages_reserved=$6,refresh_pages_used=0
 WHERE (market_holder_refresh_states.refresh_lease_until IS NULL OR market_holder_refresh_states.refresh_lease_until<=$4::timestamptz)
 AND (market_holder_refresh_states.next_attempt_at IS NULL OR market_holder_refresh_states.next_attempt_at<=$4::timestamptz)
 AND COALESCE(market_holder_refresh_states.next_due_at,market_holder_refresh_states.last_success_at+$5::double precision*INTERVAL '1 second','-infinity')<=$4::timestamptz
 RETURNING refresh_lease_token,refresh_started_at,refresh_lease_until,last_success_at,consecutive_failures`,
			project.ChainID, project.ChainKey, project.TokenAddress, b.Now, interval.Seconds(), MarketHolderRefreshMaxPages).Scan(&job.LeaseToken, &job.StartedAt, &job.LeaseUntil, &job.ExpectedPreviousAt, &job.ConsecutiveFailures)
		if isNoRows(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, `UPDATE market_holder_refresh_scheduler SET last_chain_id=$1,window_started_at=$2,pages_used=$3 WHERE id=1`, project.ChainID, b.minuteWindow(), b.PagesUsed)
		return job, err
	})
}

// A claim reserves enough capacity for its bounded twenty-page round.
// Consuming a logical page updates both windows under the scheduling row lock.
// Physical HTTP retries are separately rate-limited by the Nodit client.
func (s *Store) ReserveMarketHolderRefreshPage(ctx context.Context, job MarketHolderRefreshJob) error {
	if !validHolderRefreshJob(job) {
		return ErrMarketHolderRefreshLeaseLost
	}
	ctx, cancel := context.WithTimeout(ctx, holderJobDBTimeout)
	defer cancel()
	_, err := withTxValue(ctx, s.db, func(tx DBTX) (struct{}, error) {
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='2s'; SET LOCAL lock_timeout='2s'`); err != nil {
			return struct{}{}, err
		}
		b, err := readHolderSchedulerBudget(ctx, tx, true)
		if err != nil {
			return struct{}{}, err
		}
		if err := s.checkHolderRefreshDemand(ctx, tx, job.Project); err != nil {
			return struct{}{}, err
		}
		if retryAt := b.pageRetryAt(); !retryAt.IsZero() {
			return struct{}{}, &MarketHolderRefreshBudgetError{RetryAt: retryAt}
		}
		tag, err := tx.Exec(ctx, `UPDATE market_holder_refresh_states SET refresh_pages_used=refresh_pages_used+1
 WHERE chain_id=$1 AND token_address=$2 AND refresh_lease_token=$3 AND refresh_lease_until>clock_timestamp()
 AND refresh_pages_used<refresh_pages_reserved AND refresh_pages_used<$4`, job.Project.ChainID, job.Project.TokenAddress, job.LeaseToken, MarketHolderRefreshMaxPages)
		if err != nil {
			return struct{}{}, err
		}
		if tag.RowsAffected() != 1 {
			return struct{}{}, ErrMarketHolderRefreshLeaseLost
		}
		admissions := append(b.RecentAdmissions, b.Now)
		_, err = tx.Exec(ctx, `UPDATE market_holder_refresh_scheduler SET window_started_at=$1,pages_used=$2,recent_page_admissions=$3 WHERE id=1`,
			b.minuteWindow(), b.PagesUsed+1, admissions)
		return struct{}{}, err
	})
	return err
}

func validHolderRefreshJob(job MarketHolderRefreshJob) bool {
	token, err := normalizeMarketAddress(job.Project.TokenAddress)
	return err == nil && token == job.Project.TokenAddress && job.Project.ChainID > 0 && job.LeaseToken > 0 && !job.StartedAt.IsZero()
}

// Lease validation, the pre-HTTP baseline CAS, legacy complete Apply, and the
// scheduling result commit together. No HTTP is executed inside this method.
func (s *Store) CompleteMarketHolderRefreshJob(ctx context.Context, job MarketHolderRefreshJob, params MarketHolderRefreshParams, interval time.Duration) (MarketHolderRefreshResult, error) {
	if !validHolderRefreshJob(job) || interval <= 0 || params.ChainID != job.Project.ChainID || params.TokenAddress != job.Project.TokenAddress ||
		(params.ExpectedPreviousAt == nil) != (job.ExpectedPreviousAt == nil) || params.ExpectedPreviousAt != nil && !params.ExpectedPreviousAt.Equal(*job.ExpectedPreviousAt) {
		return MarketHolderRefreshResult{}, ErrMarketHolderRefreshLeaseLost
	}
	ctx, cancel := context.WithTimeout(ctx, holderJobApplyTimeout)
	defer cancel()
	return withTxValue(ctx, s.db, func(tx DBTX) (MarketHolderRefreshResult, error) {
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='10s'; SET LOCAL lock_timeout='2s'`); err != nil {
			return MarketHolderRefreshResult{}, err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, fmt.Sprintf("debox-holder-ranks:%d:%s", job.Project.ChainID, job.Project.TokenAddress)); err != nil {
			return MarketHolderRefreshResult{}, err
		}
		var token int64
		if err := tx.QueryRow(ctx, `SELECT refresh_lease_token FROM market_holder_refresh_states
 WHERE chain_id=$1 AND token_address=$2 AND refresh_lease_token=$3 AND refresh_lease_until>clock_timestamp()
 AND refresh_pages_used>=1 FOR UPDATE`, job.Project.ChainID, job.Project.TokenAddress, job.LeaseToken).Scan(&token); err != nil {
			if isNoRows(err) {
				return MarketHolderRefreshResult{}, ErrMarketHolderRefreshLeaseLost
			}
			return MarketHolderRefreshResult{}, err
		}
		if err := s.checkHolderRefreshDemand(ctx, tx, job.Project); err != nil {
			return MarketHolderRefreshResult{}, err
		}
		result, err := newWithDB(tx).ApplyMarketHolderRefresh(ctx, params)
		if err != nil {
			return result, err
		}
		// Recheck after Apply inside the same transaction. A committed stop
		// observed here rolls back all rank, balance, history and schedule writes.
		if err := s.checkHolderRefreshDemand(ctx, tx, job.Project); err != nil {
			return MarketHolderRefreshResult{}, err
		}
		tag, err := tx.Exec(ctx, `UPDATE market_holder_refresh_states SET next_due_at=$4,next_attempt_at=$4,
 refresh_lease_until=NULL,consecutive_failures=0,last_failure_code=''
 WHERE chain_id=$1 AND token_address=$2 AND refresh_lease_token=$3 AND refresh_lease_until>clock_timestamp()`,
			job.Project.ChainID, job.Project.TokenAddress, job.LeaseToken, job.StartedAt.Add(interval))
		if err == nil && tag.RowsAffected() != 1 {
			err = ErrMarketHolderRefreshLeaseLost
		}
		return result, err
	})
}

func validHolderFailureCode(code string) bool {
	switch code {
	case "source_error", "source_incomplete", "source_capacity", "round_timeout", "round_cancelled", "provider_budget", "database_error", "baseline_stale":
		return true
	}
	return false
}

// A failed read never advances next_due_at or any successful rank/balance
// baseline. Failure metadata is typed and contains no provider body or owner.
func (s *Store) FailMarketHolderRefreshJob(ctx context.Context, job MarketHolderRefreshJob, code string, retryAt time.Time) error {
	if !validHolderRefreshJob(job) || !validHolderFailureCode(code) || retryAt.IsZero() {
		return ErrInvalidMarketHolder
	}
	ctx, cancel := context.WithTimeout(ctx, holderJobDBTimeout)
	defer cancel()
	_, err := withTxValue(ctx, s.db, func(tx DBTX) (struct{}, error) {
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='2s'; SET LOCAL lock_timeout='2s'`); err != nil {
			return struct{}{}, err
		}
		tag, err := tx.Exec(ctx, `UPDATE market_holder_refresh_states SET refresh_lease_until=NULL,
 next_attempt_at=GREATEST($4,clock_timestamp()+INTERVAL '1 second'),status='source_coverage_unknown',last_failure_code=$5,
 consecutive_failures=CASE WHEN $5 IN('provider_budget','baseline_stale','round_cancelled') THEN consecutive_failures ELSE LEAST(30,consecutive_failures+1) END
 WHERE chain_id=$1 AND token_address=$2 AND refresh_lease_token=$3 AND refresh_lease_until>clock_timestamp()`,
			job.Project.ChainID, job.Project.TokenAddress, job.LeaseToken, retryAt.UTC(), code)
		if err == nil && tag.RowsAffected() != 1 {
			err = ErrMarketHolderRefreshLeaseLost
		}
		return struct{}{}, err
	})
	return err
}
