package store

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"
)

var ErrStaleMarketHolderRefresh = errors.New("holder observation baseline changed or is newer")

const holderRefreshStateColumns = `chain_id,chain_key,token_address,last_success_at,last_attempt_at,
	covered_top_n,source_item_count,rank_baseline,exclusion_hash,status`

type MarketHolderRefreshParams struct {
	ChainKey           string
	ChainID            int64
	TokenAddress       string
	ObservedAt         time.Time
	ExpectedPreviousAt *time.Time
	SourceComplete     bool
	SourceItemCount    int32
	CoveredTopN        int32
	ExclusionHash      string
	Ranks              []MarketHolderRank
	// Only the original Top20 tracker is updated. Rank-only observations do not
	// make the other Top100 wallets eligible for balance/transfer notifications.
	Holders          []UpsertMarketHolderParams
	BalanceEvents    []CreateMarketEventParams
	ExpectedBalances map[string]string
}

type MarketHolderRefreshResult struct {
	InitialBaseline      bool
	HoldersUpdated       int64
	RankEventsCreated    int64
	BalanceEventsCreated int64
	SnapshotsCreated     int64
}

func (s *Store) GetMarketHolderRefreshState(ctx context.Context, chainID int64, token string) (*MarketHolderRefreshState, error) {
	token, err := normalizeMarketAddress(token)
	if err != nil || chainID <= 0 {
		return nil, ErrInvalidMarketHolder
	}
	value, err := collectOptional[MarketHolderRefreshState](ctx, s.db, `SELECT `+holderRefreshStateColumns+` FROM market_holder_refresh_states WHERE chain_id=$1 AND token_address=$2`, chainID, token)
	if err != nil {
		return nil, fmt.Errorf("read holder refresh coverage: %w", err)
	}
	return value, nil
}

// Attempt health never changes the last successful rank/balance baseline.
func (s *Store) RecordMarketHolderRefreshAttempt(ctx context.Context, chainID int64, chainKey, token string, at time.Time) error {
	token, err := normalizeMarketAddress(token)
	if err != nil || chainID <= 0 || at.IsZero() || strings.TrimSpace(chainKey) == "" {
		return ErrInvalidMarketHolder
	}
	_, err = s.db.Exec(ctx, `INSERT INTO market_holder_refresh_states(chain_id,chain_key,token_address,last_attempt_at,status)
	 VALUES($1,$2,$3,$4,'source_coverage_unknown') ON CONFLICT(chain_id,token_address) DO UPDATE
	 SET last_attempt_at=GREATEST(market_holder_refresh_states.last_attempt_at,EXCLUDED.last_attempt_at),
	 status=CASE WHEN EXCLUDED.last_attempt_at>=market_holder_refresh_states.last_attempt_at THEN 'source_coverage_unknown' ELSE market_holder_refresh_states.status END`, chainID, chainKey, token, at.UTC().Truncate(time.Microsecond))
	return err
}

func (s *Store) ListMarketHolderPoolAddresses(ctx context.Context, chainID int64, token string) ([]string, error) {
	token, err := normalizeMarketAddress(token)
	if err != nil || chainID <= 0 {
		return nil, ErrInvalidMarketHolder
	}
	rows, err := s.db.Query(ctx, `SELECT DISTINCT LOWER(pool_address) FROM market_pools
	 WHERE chain_id=$1 AND pool_address IS NOT NULL AND (token0_address=$2 OR token1_address=$2)
	 ORDER BY LOWER(pool_address) LIMIT 10001`, chainID, token)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	addresses := []string{}
	for rows.Next() {
		var address string
		if err := rows.Scan(&address); err != nil {
			return nil, err
		}
		addresses = append(addresses, address)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(addresses) > 10000 {
		return nil, fmt.Errorf("holder pool exclusion capacity exceeds 10000")
	}
	return addresses, nil
}

func (s *Store) ListMarketHolderRefreshStatesForProject(ctx context.Context, projectID int64, owner string) ([]MarketHolderRefreshState, error) {
	values, err := collectMany[MarketHolderRefreshState](ctx, s.db, `SELECT state.chain_id,state.chain_key,state.token_address,state.last_success_at,state.last_attempt_at,
	 state.covered_top_n,state.source_item_count,state.rank_baseline,state.exclusion_hash,state.status
	 FROM market_holder_refresh_states state JOIN market_projects mp ON mp.id=$1 AND mp.debox_user_id=$2
	 WHERE (mp.frozen_at IS NULL OR state.last_success_at<=mp.frozen_at AND state.last_attempt_at<=mp.frozen_at) AND ((mp.chain_id=state.chain_id AND mp.token_address=state.token_address) OR EXISTS(
	 SELECT 1 FROM market_project_deployments d JOIN market_asset_deployments a ON a.id=d.market_asset_deployment_id
	 WHERE d.market_project_id=mp.id AND d.status<>'removed' AND a.chain_id=state.chain_id AND a.token_address=state.token_address))
	 ORDER BY state.chain_id,state.token_address`, projectID, strings.TrimSpace(owner))
	return values, err
}

func (s *Store) ApplyMarketHolderRefresh(ctx context.Context, params MarketHolderRefreshParams) (MarketHolderRefreshResult, error) {
	if err := validateHolderRefresh(&params); err != nil {
		return MarketHolderRefreshResult{}, err
	}
	baseline, _ := json.Marshal(params.Ranks)
	return withTxValue(ctx, s.db, func(tx DBTX) (MarketHolderRefreshResult, error) {
		var result MarketHolderRefreshResult
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, fmt.Sprintf("debox-holder-ranks:%d:%s", params.ChainID, params.TokenAddress)); err != nil {
			return result, err
		}
		old, err := collectOptional[MarketHolderRefreshState](ctx, tx, `SELECT `+holderRefreshStateColumns+` FROM market_holder_refresh_states WHERE chain_id=$1 AND token_address=$2 FOR UPDATE`, params.ChainID, params.TokenAddress)
		if err != nil {
			return result, err
		}
		if old != nil && old.LastSuccessAt != nil && old.LastSuccessAt.Equal(params.ObservedAt) {
			previous, _ := json.Marshal(old.RankBaseline)
			if bytes.Equal(previous, baseline) && old.ExclusionHash == params.ExclusionHash {
				return result, nil
			}
			return result, ErrStaleMarketHolderRefresh
		}
		var previousAt *time.Time
		if old != nil {
			previousAt = old.LastSuccessAt
		}
		if (previousAt == nil) != (params.ExpectedPreviousAt == nil) || previousAt != nil && (!previousAt.Equal(*params.ExpectedPreviousAt) || !params.ObservedAt.After(*previousAt)) {
			return result, ErrStaleMarketHolderRefresh
		}
		result.InitialBaseline = previousAt == nil || old.ExclusionHash != params.ExclusionHash
		addresses := make([]string, len(params.Holders))
		for i, h := range params.Holders {
			addresses[i] = h.HolderAddress
		}
		before, err := collectMany[MarketHolder](ctx, tx, `SELECT `+marketHolderColumns+` FROM market_holders WHERE chain_id=$1 AND token_address=$2 AND (rank IS NOT NULL OR holder_address=ANY($3::text[])) ORDER BY holder_address LIMIT 1001 FOR UPDATE`, params.ChainID, params.TokenAddress, addresses)
		if err != nil {
			return result, err
		}
		if len(before) > 1000 {
			return result, fmt.Errorf("legacy holder baseline capacity exceeds 1000")
		}
		oldHolders := make(map[string]MarketHolder, len(before))
		for _, h := range before {
			oldHolders[h.HolderAddress] = h
		}
		if !result.InitialBaseline {
			for address, expected := range params.ExpectedBalances {
				if h, ok := oldHolders[address]; !ok || h.BalanceRaw != expected {
					return result, ErrStaleMarketHolderRefresh
				}
			}
		}
		snapshots := map[string]int64{}
		for _, h := range params.Holders {
			previous, existed := oldHolders[h.HolderAddress]
			updated, err := upsertMarketHolder(ctx, tx, h, params.ObservedAt)
			if err != nil {
				return result, err
			}
			result.HoldersUpdated++
			if !existed || previous.BalanceRaw != updated.BalanceRaw || !sameRank(previous.Rank, updated.Rank) || !sameString(previous.SupplyPercent, updated.SupplyPercent) {
				id, created, err := recordManagedHolderSnapshot(ctx, tx, updated, params.ObservedAt, true)
				if err != nil {
					return result, err
				}
				snapshots[h.HolderAddress] = id
				if created {
					result.SnapshotsCreated++
				}
			}
		}
		current := map[string]bool{}
		for _, address := range addresses {
			current[address] = true
		}
		for _, h := range before {
			if h.Rank == nil || current[h.HolderAddress] {
				continue
			}
			if _, err := tx.Exec(ctx, `UPDATE market_holders SET rank=NULL,updated_at=NOW() WHERE id=$1`, h.ID); err != nil {
				return result, err
			}
			h.Rank = nil
			id, created, err := recordManagedHolderSnapshot(ctx, tx, h, params.ObservedAt, false)
			if err != nil {
				return result, err
			}
			snapshots[h.HolderAddress] = id
			if created {
				result.SnapshotsCreated++
			}
		}
		if !result.InitialBaseline {
			for _, event := range params.BalanceEvents {
				if event.WalletAddress == nil {
					return result, ErrInvalidMarketEvent
				}
				address := strings.ToLower(*event.WalletAddress)
				if !current[address] {
					return result, ErrInvalidMarketEvent
				}
				if err := validateHolderBalanceEvent(event, oldHolders[address], params.Holders); err != nil {
					return result, err
				}
				if _, created, err := createHolderRefreshEvent(ctx, tx, event, snapshots[address]); err != nil {
					return result, err
				} else if created {
					result.BalanceEventsCreated++
				}
			}
			oldRanks := map[string]*int32{}
			newRanks := map[string]*int32{}
			for _, r := range old.RankBaseline {
				rank := r.Rank
				oldRanks[r.HolderAddress] = &rank
			}
			for _, r := range params.Ranks {
				rank := r.Rank
				newRanks[r.HolderAddress] = &rank
			}
			all := make([]string, 0, len(oldRanks)+len(newRanks))
			seen := map[string]bool{}
			for address := range oldRanks {
				seen[address] = true
				all = append(all, address)
			}
			for address := range newRanks {
				if !seen[address] {
					all = append(all, address)
				}
			}
			sort.Strings(all)
			for _, address := range all {
				oldRank, newRank := oldRanks[address], newRanks[address]
				if sameRank(oldRank, newRank) {
					continue
				}
				metadata, _ := json.Marshal(map[string]any{"holder_rank_schema_version": 1, "holder_address": address, "old_rank": oldRank, "new_rank": newRank,
					"old_covered_top_n": old.CoveredTopN, "new_covered_top_n": params.CoveredTopN, "old_observation_at": old.LastSuccessAt, "new_observation_at": params.ObservedAt, "exclusion_hash": params.ExclusionHash, "source_snapshot_atomic": false})
				event := CreateMarketEventParams{ChainKey: params.ChainKey, ChainID: params.ChainID, TokenAddress: params.TokenAddress, EventType: "holder_rank_changed",
					EventKey: fmt.Sprintf("holder-rank-v2:%d:%s:%s:%d", params.ChainID, params.TokenAddress, address, params.ObservedAt.UnixMicro()), WalletAddress: &address,
					Source: "nodit_holders_sample", Confirmed: true, OccurredAt: params.ObservedAt, Metadata: metadata, RawPayload: metadata}
				if _, created, err := createHolderRefreshEvent(ctx, tx, event, snapshots[address]); err != nil {
					return result, err
				} else if created {
					result.RankEventsCreated++
				}
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO market_holder_refresh_states(chain_id,chain_key,token_address,last_success_at,last_attempt_at,covered_top_n,source_item_count,rank_baseline,exclusion_hash,status)
		 VALUES($1,$2,$3,$4,$4,$5,$6,$7,$8,'complete') ON CONFLICT(chain_id,token_address) DO UPDATE
		 SET last_success_at=EXCLUDED.last_success_at,last_attempt_at=GREATEST(market_holder_refresh_states.last_attempt_at,EXCLUDED.last_attempt_at),
		 covered_top_n=EXCLUDED.covered_top_n,source_item_count=EXCLUDED.source_item_count,
		 rank_baseline=CASE WHEN market_holder_refresh_states.rank_baseline=EXCLUDED.rank_baseline THEN market_holder_refresh_states.rank_baseline ELSE EXCLUDED.rank_baseline END,
		 exclusion_hash=EXCLUDED.exclusion_hash,status=CASE WHEN EXCLUDED.last_success_at>=market_holder_refresh_states.last_attempt_at THEN 'complete' ELSE market_holder_refresh_states.status END`, params.ChainID, params.ChainKey, params.TokenAddress, params.ObservedAt, params.CoveredTopN, params.SourceItemCount, baseline, params.ExclusionHash)
		return result, err
	})
}

func validateHolderRefresh(p *MarketHolderRefreshParams) error {
	token, err := normalizeMarketAddress(p.TokenAddress)
	if err != nil || p.ChainID <= 0 || strings.TrimSpace(p.ChainKey) == "" || p.ObservedAt.IsZero() || !p.SourceComplete || p.CoveredTopN < 1 || p.CoveredTopN > 100 || len(p.Ranks) > int(p.CoveredTopN) || p.SourceItemCount < 0 || p.SourceItemCount > MarketHolderRefreshMaxHolders || len(p.Ranks) > int(p.SourceItemCount) || len(p.Holders) > 20 || len(p.BalanceEvents) > 20 || len(p.ExpectedBalances) > 20 || len(p.ExclusionHash) != 64 {
		return ErrInvalidMarketHolder
	}
	if _, err := hex.DecodeString(p.ExclusionHash); err != nil {
		return ErrInvalidMarketHolder
	}
	// A complete observation can legitimately have no eligible holders (for
	// example, all source addresses are excluded). Persist the empty array,
	// never JSON null, while retaining an independent copy for normalization.
	p.Ranks = append(make([]MarketHolderRank, 0, len(p.Ranks)), p.Ranks...)
	p.Holders = append([]UpsertMarketHolderParams(nil), p.Holders...)
	p.BalanceEvents = append([]CreateMarketEventParams(nil), p.BalanceEvents...)
	p.TokenAddress = token
	p.ObservedAt = p.ObservedAt.UTC().Truncate(time.Microsecond)
	ranks := map[string]MarketHolderRank{}
	for i, r := range p.Ranks {
		if len(r.BalanceRaw) == 0 || len(r.BalanceRaw) > 78 {
			return ErrInvalidMarketHolder
		}
		address, err := normalizeMarketAddress(r.HolderAddress)
		n, ok := new(big.Int).SetString(r.BalanceRaw, 10)
		if err != nil || !ok || n.Sign() <= 0 || n.BitLen() > 256 || r.Rank != int32(i+1) {
			return ErrInvalidMarketHolder
		}
		if _, exists := ranks[address]; exists {
			return ErrInvalidMarketHolder
		}
		if i > 0 {
			previous := p.Ranks[i-1]
			value, _ := new(big.Int).SetString(previous.BalanceRaw, 10)
			if cmp := value.Cmp(n); cmp < 0 || cmp == 0 && previous.HolderAddress > address {
				return ErrInvalidMarketHolder
			}
		}
		r.HolderAddress = address
		r.BalanceRaw = n.String()
		p.Ranks[i] = r
		ranks[address] = r
	}
	seen := map[string]bool{}
	for i, h := range p.Holders {
		r, exists := ranks[strings.ToLower(h.HolderAddress)]
		if !exists || r.Rank > 20 || h.ChainID != p.ChainID || h.TokenAddress != p.TokenAddress || h.Rank == nil || *h.Rank != r.Rank || h.BalanceRaw != r.BalanceRaw || h.Balance == "" || seen[r.HolderAddress] {
			return ErrInvalidMarketHolder
		}
		seen[r.HolderAddress] = true
		h.HolderAddress = r.HolderAddress
		h.ChainKey = p.ChainKey
		h.AddressKind = "wallet"
		h.RecordSnapshot = false
		h.SeenAt = p.ObservedAt
		h.Source = "nodit_holders"
		p.Holders[i] = h
	}
	if len(p.Holders) != min(20, len(p.Ranks)) {
		return ErrInvalidMarketHolder
	}
	for _, e := range p.BalanceEvents {
		if e.ChainID != p.ChainID || e.TokenAddress != p.TokenAddress || !e.OccurredAt.UTC().Truncate(time.Microsecond).Equal(p.ObservedAt) {
			return ErrInvalidMarketEvent
		}
	}
	return nil
}

func recordManagedHolderSnapshot(ctx context.Context, db DBTX, h MarketHolder, at time.Time, balanceObserved bool) (int64, bool, error) {
	var id int64
	err := db.QueryRow(ctx, `INSERT INTO market_holder_snapshots(chain_key,chain_id,token_address,holder_address,balance_raw,balance,supply_percent,rank,source,captured_at,
	 managed_observation,balance_observed,balance_observed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,TRUE,$11,$12)
	 ON CONFLICT(chain_id,token_address,holder_address,captured_at) DO NOTHING RETURNING id`, h.ChainKey, h.ChainID, h.TokenAddress, h.HolderAddress, h.BalanceRaw, h.Balance, h.SupplyPercent, h.Rank, h.Source, at, balanceObserved, h.LastSeenAt).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if isNoRows(err) {
		existing, readErr := collectOne[MarketHolderSnapshot](ctx, db, `SELECT `+marketHolderSnapshotColumns+` FROM market_holder_snapshots WHERE chain_id=$1 AND token_address=$2 AND holder_address=$3 AND captured_at=$4`, h.ChainID, h.TokenAddress, h.HolderAddress, at)
		if readErr != nil {
			return 0, false, readErr
		}
		if existing.BalanceRaw != h.BalanceRaw || !sameRank(existing.Rank, h.Rank) || existing.BalanceObserved != balanceObserved {
			return 0, false, ErrStaleMarketHolderRefresh
		}
		return existing.ID, false, nil
	}
	return 0, false, err
}

func createHolderRefreshEvent(ctx context.Context, db DBTX, p CreateMarketEventParams, snapshotID int64) (MarketEvent, bool, error) {
	if snapshotID > 0 {
		var metadata map[string]any
		_ = json.Unmarshal(p.Metadata, &metadata)
		if metadata == nil {
			metadata = map[string]any{}
		}
		metadata["holder_snapshot_id"] = snapshotID
		p.Metadata, _ = json.Marshal(metadata)
	}
	event, created, err := createMarketEvent(ctx, db, p)
	if err != nil {
		return event, created, err
	}
	if snapshotID > 0 {
		_, err = db.Exec(ctx, `INSERT INTO market_holder_snapshot_refs(snapshot_id,market_event_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, snapshotID, event.ID)
	}
	return event, created, err
}

func validateHolderBalanceEvent(e CreateMarketEventParams, old MarketHolder, current []UpsertMarketHolderParams) error {
	if e.WalletAddress == nil || e.TokenAmountRaw == nil || old.ID == 0 {
		return ErrInvalidMarketEvent
	}
	for _, h := range current {
		if h.HolderAddress != strings.ToLower(*e.WalletAddress) {
			continue
		}
		before, bok := new(big.Int).SetString(old.BalanceRaw, 10)
		after, aok := new(big.Int).SetString(h.BalanceRaw, 10)
		raw, rok := new(big.Int).SetString(*e.TokenAmountRaw, 10)
		if !bok || !aok || !rok {
			return ErrInvalidMarketEvent
		}
		direction := "holder_increase"
		delta := new(big.Int).Sub(after, before)
		if delta.Sign() < 0 {
			direction = "holder_decrease"
			delta.Neg(delta)
		}
		if delta.Sign() == 0 || e.EventType != direction || delta.Cmp(raw) != 0 {
			return ErrStaleMarketHolderRefresh
		}
		return nil
	}
	return ErrInvalidMarketEvent
}

func sameRank(a, b *int32) bool    { return a == nil && b == nil || a != nil && b != nil && *a == *b }
func sameString(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
