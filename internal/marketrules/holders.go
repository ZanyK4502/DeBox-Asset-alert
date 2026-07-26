package marketrules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const (
	defaultHolderRankLimit = 20
	holderFetchLimit       = 100
)

var systemHolderExclusions = map[string]string{
	"0x0000000000000000000000000000000000000000": "zero_address",
	"0x000000000000000000000000000000000000dead": "burn_address",
	marketparse.BSCFourMemeTokenManager:          "four_meme_manager",
	marketparse.BSCInfinityCLManager:             "infinity_manager",
	marketparse.BSCInfinityBinManager:            "infinity_manager",
	marketparse.BSCInfinityVault:                 "infinity_vault",
	marketparse.BSCPancakeV2Factory:              "factory",
	marketparse.BSCPancakeV3Factory:              "factory",
}

func (service *Service) RefreshDueHolders(ctx context.Context) (int64, error) {
	cutoff := service.now().Add(-service.settings.HolderRefreshInterval)
	projects, err := service.repository.ListMarketProjectsDueHolderRefresh(
		ctx,
		56,
		cutoff,
		10,
	)
	if err != nil {
		return 0, err
	}
	var refreshed int64
	var refreshErrors []error
	for _, project := range projects {
		if err := service.refreshProjectHolders(ctx, project); err != nil {
			refreshErrors = append(
				refreshErrors,
				fmt.Errorf("refresh holders for %s: %w", project.TokenAddress, err),
			)
			continue
		}
		refreshed++
	}
	return refreshed, errors.Join(refreshErrors...)
}

func (service *Service) refreshProjectHolders(
	ctx context.Context,
	project store.MarketProject,
) error {
	page, err := service.holders.TokenHoldersByContract(
		ctx,
		project.TokenAddress,
		project.ChainKey,
		"bsc",
		chain.TokenHoldersOptions{RPP: holderFetchLimit, WithCount: true},
	)
	if err != nil {
		return err
	}
	oldHolders, err := service.repository.ListMarketHolders(
		ctx,
		project.ID,
		project.DeBoxUserID,
		true,
		500,
	)
	if err != nil {
		return err
	}
	oldByAddress := make(map[string]store.MarketHolder, len(oldHolders))
	for _, holder := range oldHolders {
		oldByAddress[strings.ToLower(holder.HolderAddress)] = holder
	}
	snapshots, err := service.repository.ListMarketSnapshots(
		ctx,
		project.ID,
		project.DeBoxUserID,
		1,
	)
	if err != nil {
		return err
	}
	var priceUSD *string
	if len(snapshots) > 0 {
		priceUSD = snapshots[0].PriceUSD
	}
	pools, err := service.repository.ListMarketProjectPools(
		ctx,
		project.ID,
		project.DeBoxUserID,
	)
	if err != nil {
		return err
	}
	exclusions := make(map[string]string, len(systemHolderExclusions)+len(pools)+1)
	for address, reason := range systemHolderExclusions {
		exclusions[address] = reason
	}
	exclusions[project.TokenAddress] = "token_contract"
	for _, pool := range pools {
		if pool.PoolAddress != nil {
			exclusions[strings.ToLower(*pool.PoolAddress)] = "market_pool"
		}
	}

	type rankedHolder struct {
		address    string
		balanceRaw string
	}
	candidates := make([]rankedHolder, 0, len(page.Items))
	for _, item := range page.Items {
		address := strings.ToLower(item.OwnerAddress)
		if _, excluded := exclusions[address]; excluded || isZeroInteger(item.BalanceRaw) {
			continue
		}
		candidates = append(candidates, rankedHolder{
			address:    address,
			balanceRaw: item.BalanceRaw,
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, _ := new(big.Int).SetString(candidates[i].balanceRaw, 10)
		right, _ := new(big.Int).SetString(candidates[j].balanceRaw, 10)
		return left.Cmp(right) > 0
	})
	if len(candidates) > defaultHolderRankLimit {
		candidates = candidates[:defaultHolderRankLimit]
	}
	now := service.now()
	currentAddresses := make([]string, 0, len(candidates))
	currentSet := make(map[string]struct{}, len(candidates))
	initialBaseline := len(oldHolders) == 0
	for index, candidate := range candidates {
		rank := int32(index + 1)
		balance, err := chain.FormatUnits(candidate.balanceRaw, int(project.TokenDecimals))
		if err != nil {
			return err
		}
		supplyPercent := percentage(candidate.balanceRaw, project.TotalSupplyRaw)
		old, existed := oldByAddress[candidate.address]
		if _, err := service.repository.UpsertMarketHolder(
			ctx,
			store.UpsertMarketHolderParams{
				ChainKey:       project.ChainKey,
				ChainID:        project.ChainID,
				TokenAddress:   project.TokenAddress,
				HolderAddress:  candidate.address,
				BalanceRaw:     candidate.balanceRaw,
				Balance:        balance,
				SupplyPercent:  supplyPercent,
				Rank:           &rank,
				AddressKind:    "wallet",
				Source:         "nodit_holders",
				SeenAt:         now,
				RecordSnapshot: true,
			},
		); err != nil {
			return err
		}
		currentAddresses = append(currentAddresses, candidate.address)
		currentSet[candidate.address] = struct{}{}
		if initialBaseline {
			continue
		}
		if existed {
			if err := service.recordHolderBalanceChange(
				ctx,
				project,
				old,
				candidate.balanceRaw,
				balance,
				rank,
				priceUSD,
				now,
			); err != nil {
				return err
			}
		}
		if !existed || old.Rank == nil || *old.Rank > defaultHolderRankLimit {
			if err := service.recordHolderRankEvent(
				ctx,
				project,
				candidate.address,
				"holder_rank_entered",
				old.Rank,
				&rank,
				now,
			); err != nil {
				return err
			}
		}
	}
	if !initialBaseline {
		for _, old := range oldHolders {
			if old.Rank == nil || *old.Rank > defaultHolderRankLimit {
				continue
			}
			if _, remains := currentSet[strings.ToLower(old.HolderAddress)]; remains {
				continue
			}
			if err := service.recordHolderRankEvent(
				ctx,
				project,
				old.HolderAddress,
				"holder_rank_exited",
				old.Rank,
				nil,
				now,
			); err != nil {
				return err
			}
		}
	}
	_, err = service.repository.ClearMarketHolderRanksOutsideSnapshot(
		ctx,
		project.ChainID,
		project.TokenAddress,
		currentAddresses,
		now,
	)
	return err
}

func (service *Service) recordHolderBalanceChange(
	ctx context.Context,
	project store.MarketProject,
	old store.MarketHolder,
	newRaw string,
	newBalance string,
	newRank int32,
	priceUSD *string,
	occurredAt time.Time,
) error {
	oldValue, oldOK := new(big.Int).SetString(old.BalanceRaw, 10)
	newValue, newOK := new(big.Int).SetString(newRaw, 10)
	if !oldOK || !newOK {
		return fmt.Errorf("invalid holder balance")
	}
	comparison := newValue.Cmp(oldValue)
	if comparison == 0 {
		return nil
	}
	eventType := "holder_increase"
	delta := new(big.Int).Sub(newValue, oldValue)
	if comparison < 0 {
		eventType = "holder_decrease"
		delta.Neg(delta)
	}
	formatted, err := chain.FormatUnits(delta.String(), int(project.TokenDecimals))
	if err != nil {
		return err
	}
	metadata, _ := json.Marshal(map[string]any{
		"holder_address": old.HolderAddress,
		"old_balance":    old.Balance,
		"new_balance":    newBalance,
		"old_rank":       old.Rank,
		"new_rank":       newRank,
	})
	wallet := strings.ToLower(old.HolderAddress)
	raw := delta.String()
	usdValue := multiplyDecimals(formatted, priceUSD)
	eventKey := holderEventKey(project, wallet, eventType, occurredAt)
	_, _, err = service.repository.CreateMarketEvent(
		ctx,
		store.CreateMarketEventParams{
			ChainKey:       project.ChainKey,
			ChainID:        project.ChainID,
			TokenAddress:   project.TokenAddress,
			EventType:      eventType,
			EventKey:       eventKey,
			WalletAddress:  &wallet,
			TokenAmountRaw: &raw,
			TokenAmount:    &formatted,
			USDValue:       usdValue,
			Source:         "nodit_holders",
			Confidence:     "1.0000",
			Confirmed:      true,
			OccurredAt:     occurredAt,
			Metadata:       metadata,
			RawPayload:     metadata,
		},
	)
	return err
}

func (service *Service) recordHolderRankEvent(
	ctx context.Context,
	project store.MarketProject,
	address string,
	eventType string,
	oldRank *int32,
	newRank *int32,
	occurredAt time.Time,
) error {
	address = strings.ToLower(address)
	metadata, _ := json.Marshal(map[string]any{
		"holder_address": address,
		"old_rank":       oldRank,
		"new_rank":       newRank,
	})
	_, _, err := service.repository.CreateMarketEvent(
		ctx,
		store.CreateMarketEventParams{
			ChainKey:      project.ChainKey,
			ChainID:       project.ChainID,
			TokenAddress:  project.TokenAddress,
			EventType:     eventType,
			EventKey:      holderEventKey(project, address, eventType, occurredAt),
			WalletAddress: &address,
			Source:        "nodit_holders",
			Confidence:    "1.0000",
			Confirmed:     true,
			OccurredAt:    occurredAt,
			Metadata:      metadata,
			RawPayload:    metadata,
		},
	)
	return err
}

func holderEventKey(
	project store.MarketProject,
	address string,
	eventType string,
	occurredAt time.Time,
) string {
	return fmt.Sprintf(
		"holder:%d:%s:%s:%d",
		project.ChainID,
		strings.ToLower(address),
		eventType,
		occurredAt.UnixNano(),
	)
}

func percentage(value string, total *string) *string {
	if total == nil {
		return nil
	}
	return ratioPercent(value, *total)
}

func isZeroInteger(value string) bool {
	number, ok := new(big.Int).SetString(strings.TrimSpace(value), 10)
	return !ok || number.Sign() <= 0
}

func multiplyDecimals(value string, multiplier *string) *string {
	if multiplier == nil {
		return nil
	}
	left, leftOK := rat(value)
	right, rightOK := rat(*multiplier)
	if !leftOK || !rightOK {
		return nil
	}
	result := decimalString(new(big.Rat).Mul(left, right))
	return &result
}
