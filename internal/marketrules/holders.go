package marketrules

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketquality"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketwindow"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const defaultHolderRankLimit = 20

var commonHolderExclusions = map[string]string{
	"0x0000000000000000000000000000000000000000": "zero_address",
	"0x000000000000000000000000000000000000dead": "burn_address",
}

var bscHolderExclusions = map[string]string{
	marketparse.BSCFourMemeTokenManager: "four_meme_manager",
	marketparse.BSCInfinityCLManager:    "infinity_manager",
	marketparse.BSCInfinityBinManager:   "infinity_manager",
	marketparse.BSCInfinityVault:        "infinity_vault",
	marketparse.BSCPancakeV2Factory:     "factory",
	marketparse.BSCPancakeV3Factory:     "factory",
}

// RefreshDueHolders performs one bounded leased job. The independent runner
// schedules additional work; rule evaluation never calls the old full sweep.
func (service *Service) RefreshDueHolders(ctx context.Context) (int64, error) {
	runner, err := NewHolderRefreshRunner(service, HolderRefreshSettings{Workers: 1})
	if err != nil {
		return 0, err
	}
	result, err := runner.RunOnce(ctx)
	return result.Refreshed, err
}

type atomicHolderRepository interface {
	GetMarketHolderRefreshState(context.Context, int64, string) (*store.MarketHolderRefreshState, error)
	RecordMarketHolderRefreshAttempt(context.Context, int64, string, string, time.Time) error
	ListMarketHolderPoolAddresses(context.Context, int64, string) ([]string, error)
	ApplyMarketHolderRefresh(context.Context, store.MarketHolderRefreshParams) (store.MarketHolderRefreshResult, error)
}

func (service *Service) refreshProjectHolders(ctx context.Context, project store.MarketProject) error {
	budget := store.MarketHolderRefreshMaxPages
	return service.refreshProjectHoldersWithBudget(ctx, project, &budget)
}

func (service *Service) refreshProjectHoldersWithBudget(ctx context.Context, project store.MarketProject, budget *int) error {
	repository, ok := service.repository.(atomicHolderRepository)
	if !ok {
		return fmt.Errorf("atomic holder observation repository is unavailable")
	}
	previous, err := repository.GetMarketHolderRefreshState(ctx, project.ChainID, project.TokenAddress)
	if err != nil {
		return err
	}
	var expectedPreviousAt *time.Time
	if previous != nil && previous.LastSuccessAt != nil {
		value := *previous.LastSuccessAt
		expectedPreviousAt = &value
	}
	if err := repository.RecordMarketHolderRefreshAttempt(ctx, project.ChainID, project.ChainKey, project.TokenAddress, service.now()); err != nil {
		return err
	}
	params, err := service.prepareMarketHolderRefresh(ctx, project, budget, expectedPreviousAt)
	if err != nil {
		return err
	}
	_, err = repository.ApplyMarketHolderRefresh(ctx, params)
	return err
}

// Sampling and preparation contain no write transaction. A scheduled job uses
// the lease's pre-HTTP expected baseline; manual local tests retain the legacy
// helper above, which captures that baseline before its provider request.
func (service *Service) prepareMarketHolderRefresh(ctx context.Context, project store.MarketProject, budget *int, expectedPreviousAt *time.Time) (store.MarketHolderRefreshParams, error) {
	repository, ok := service.repository.(atomicHolderRepository)
	if !ok {
		return store.MarketHolderRefreshParams{}, fmt.Errorf("atomic holder observation repository is unavailable")
	}
	poolAddresses, err := repository.ListMarketHolderPoolAddresses(ctx, project.ChainID, project.TokenAddress)
	if err != nil {
		return store.MarketHolderRefreshParams{}, err
	}
	exclusions := make(map[string]string, len(commonHolderExclusions)+len(bscHolderExclusions)+len(poolAddresses)+1)
	for address, reason := range commonHolderExclusions {
		exclusions[address] = reason
	}
	if project.ChainKey == "bsc" {
		for address, reason := range bscHolderExclusions {
			exclusions[address] = reason
		}
	}
	exclusions[strings.ToLower(project.TokenAddress)] = "token_contract"
	for _, address := range poolAddresses {
		exclusions[strings.ToLower(address)] = "market_pool"
	}
	keys := make([]string, 0, len(exclusions))
	for address, reason := range exclusions {
		keys = append(keys, address+":"+reason)
	}
	sort.Strings(keys)
	exclusionHash := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(keys, "\n"))))
	items, err := service.completeHolderSample(ctx, project, budget)
	if err != nil {
		return store.MarketHolderRefreshParams{}, err
	}
	candidates := make([]chain.TokenHolder, 0, len(items))
	for _, item := range items {
		if _, excluded := exclusions[strings.ToLower(item.OwnerAddress)]; excluded || isZeroInteger(item.BalanceRaw) {
			continue
		}
		candidates = append(candidates, item)
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, _ := new(big.Int).SetString(candidates[i].BalanceRaw, 10)
		right, _ := new(big.Int).SetString(candidates[j].BalanceRaw, 10)
		if cmp := left.Cmp(right); cmp != 0 {
			return cmp > 0
		}
		return candidates[i].OwnerAddress < candidates[j].OwnerAddress
	})
	if len(candidates) > int(marketwindow.MaxHolderRank) {
		candidates = candidates[:marketwindow.MaxHolderRank]
	}
	oldHolders, snapshots, err := service.projectHolderBaseline(ctx, project)
	if err != nil {
		return store.MarketHolderRefreshParams{}, err
	}
	oldByAddress := map[string]store.MarketHolder{}
	for _, holder := range oldHolders {
		oldByAddress[strings.ToLower(holder.HolderAddress)] = holder
	}
	var valuationSnapshot *store.MarketSnapshot
	if len(snapshots) > 0 {
		valuationSnapshot = &snapshots[0]
	}
	now := service.now().UTC().Truncate(time.Microsecond)
	params := store.MarketHolderRefreshParams{ChainKey: project.ChainKey, ChainID: project.ChainID, TokenAddress: project.TokenAddress, ObservedAt: now,
		SourceComplete: true, SourceItemCount: int32(len(items)), CoveredTopN: marketwindow.MaxHolderRank, ExclusionHash: exclusionHash, Ranks: []store.MarketHolderRank{},
		ExpectedBalances: map[string]string{}}
	params.ExpectedPreviousAt = expectedPreviousAt
	for index, candidate := range candidates {
		address := strings.ToLower(candidate.OwnerAddress)
		rank := int32(index + 1)
		params.Ranks = append(params.Ranks, store.MarketHolderRank{HolderAddress: address, BalanceRaw: candidate.BalanceRaw, Rank: rank})
		if index >= defaultHolderRankLimit {
			continue
		}
		balance, err := chain.FormatUnits(candidate.BalanceRaw, int(project.TokenDecimals))
		if err != nil {
			return store.MarketHolderRefreshParams{}, err
		}
		params.Holders = append(params.Holders, store.UpsertMarketHolderParams{ChainKey: project.ChainKey, ChainID: project.ChainID, TokenAddress: project.TokenAddress,
			HolderAddress: address, BalanceRaw: candidate.BalanceRaw, Balance: balance, SupplyPercent: percentage(candidate.BalanceRaw, project.TotalSupplyRaw), Rank: &rank, AddressKind: "wallet", Source: "nodit_holders", SeenAt: now})
		if old, exists := oldByAddress[address]; exists {
			event, err := holderBalanceChangeParams(project, old, candidate.BalanceRaw, balance, rank, valuationSnapshot, now)
			if err != nil {
				return store.MarketHolderRefreshParams{}, err
			}
			if event != nil {
				params.BalanceEvents = append(params.BalanceEvents, *event)
				params.ExpectedBalances[address] = old.BalanceRaw
			}
		}
	}
	return params, nil
}

// The provider documents cursor traversal and total count, but no descending
// order or same-block snapshot. Only a bounded complete enumeration proves a
// sampled TopN. A large token is explicitly unavailable, never a sorted prefix.
func (service *Service) completeHolderSample(ctx context.Context, project store.MarketProject, budget *int) ([]chain.TokenHolder, error) {
	items := make([]chain.TokenHolder, 0, store.MarketHolderRefreshPageSize)
	seenAddresses, seenCursors := map[string]bool{}, map[string]bool{}
	cursor := ""
	var total *int64
	for pageNumber := 0; pageNumber < store.MarketHolderRefreshMaxPages; pageNumber++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if budget == nil || *budget <= 0 {
			return nil, &holderSamplingFailure{code: "source_capacity", cause: fmt.Errorf("holder request budget exhausted")}
		}
		*budget--
		page, err := service.holders.TokenHoldersByContract(ctx, project.TokenAddress, project.ChainKey, "bsc", chain.TokenHoldersOptions{Cursor: cursor, RPP: store.MarketHolderRefreshPageSize, WithCount: true})
		if err != nil {
			return nil, &holderSamplingFailure{code: "source_error", cause: err}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if page.Count != nil && *page.Count > store.MarketHolderRefreshMaxHolders {
			return nil, &holderSamplingFailure{code: "source_capacity", cause: fmt.Errorf("holder count exceeds %d complete-enumeration capacity", store.MarketHolderRefreshMaxHolders)}
		}
		if page.Count == nil || *page.Count < 0 || len(page.Items) > store.MarketHolderRefreshPageSize || total != nil && *total != *page.Count {
			return nil, &holderSamplingFailure{code: "source_incomplete", cause: fmt.Errorf("missing or inconsistent holder count")}
		}
		if total == nil {
			value := *page.Count
			total = &value
		}
		for _, item := range page.Items {
			if len(item.BalanceRaw) == 0 || len(item.BalanceRaw) > 78 {
				return nil, &holderSamplingFailure{code: "source_incomplete", cause: fmt.Errorf("holder balance exceeds uint256 byte budget")}
			}
			for _, digit := range item.BalanceRaw {
				if digit < '0' || digit > '9' {
					return nil, &holderSamplingFailure{code: "source_incomplete", cause: fmt.Errorf("holder balance must be an unsigned decimal integer")}
				}
			}
			address, err := chain.ValidateAddress(item.OwnerAddress)
			raw, valid := new(big.Int).SetString(item.BalanceRaw, 10)
			if err != nil || !valid || raw.Sign() < 0 || raw.BitLen() > 256 || seenAddresses[address] {
				return nil, &holderSamplingFailure{code: "source_incomplete", cause: fmt.Errorf("invalid or repeated holder address/balance")}
			}
			seenAddresses[address] = true
			item.OwnerAddress = address
			item.BalanceRaw = raw.String()
			items = append(items, item)
		}
		if int64(len(items)) > *total || len(items) > store.MarketHolderRefreshMaxHolders {
			return nil, &holderSamplingFailure{code: "source_incomplete", cause: fmt.Errorf("holder count exceeded")}
		}
		if int64(len(items)) == *total {
			return items, nil
		}
		if len(page.Items) == 0 || page.Cursor == "" || seenCursors[page.Cursor] {
			return nil, &holderSamplingFailure{code: "source_incomplete", cause: fmt.Errorf("missing page or repeated cursor")}
		}
		seenCursors[page.Cursor] = true
		cursor = page.Cursor
	}
	return nil, &holderSamplingFailure{code: "source_capacity", cause: fmt.Errorf("holder pagination exceeds %d pages/%d addresses", store.MarketHolderRefreshMaxPages, store.MarketHolderRefreshMaxHolders)}
}

func (service *Service) projectHolderBaseline(
	ctx context.Context,
	project store.MarketProject,
) ([]store.MarketHolder, []store.MarketSnapshot, error) {
	if repository, ok := service.repository.(multiChainRuleRepository); ok &&
		project.MarketProjectDeploymentID != nil {
		target := store.MarketRuleTarget{
			TargetKey:                 fmt.Sprintf("deployment:%d", *project.MarketProjectDeploymentID),
			MarketProjectDeploymentID: project.MarketProjectDeploymentID,
			ChainKey:                  project.ChainKey,
			ChainID:                   project.ChainID,
			TokenAddress:              project.TokenAddress,
			TokenName:                 project.TokenName,
			TokenSymbol:               project.TokenSymbol,
			TokenDecimals:             project.TokenDecimals,
		}
		holders, err := repository.ListMarketHoldersForTarget(ctx, target, 500)
		if err != nil {
			return nil, nil, err
		}
		snapshots, err := repository.ListMarketSnapshotsForTarget(ctx, target, 1)
		if err != nil {
			return nil, nil, err
		}
		return holders, snapshots, nil
	}
	holders, err := service.repository.ListMarketHolders(
		ctx,
		project.ID,
		project.DeBoxUserID,
		true,
		500,
	)
	if err != nil {
		return nil, nil, err
	}
	snapshots, err := service.repository.ListMarketSnapshots(
		ctx,
		project.ID,
		project.DeBoxUserID,
		1,
	)
	if err != nil {
		return nil, nil, err
	}
	return holders, snapshots, nil
}

func (service *Service) recordHolderBalanceChange(
	ctx context.Context,
	project store.MarketProject,
	old store.MarketHolder,
	newRaw string,
	newBalance string,
	newRank int32,
	valuationSnapshot *store.MarketSnapshot,
	occurredAt time.Time,
) error {
	params, err := holderBalanceChangeParams(project, old, newRaw, newBalance, newRank, valuationSnapshot, occurredAt)
	if err != nil || params == nil {
		return err
	}
	_, _, err = service.repository.CreateMarketEvent(ctx, *params)
	return err
}

func holderBalanceChangeParams(
	project store.MarketProject,
	old store.MarketHolder,
	newRaw string,
	newBalance string,
	newRank int32,
	valuationSnapshot *store.MarketSnapshot,
	occurredAt time.Time,
) (*store.CreateMarketEventParams, error) {
	oldValue, oldOK := new(big.Int).SetString(old.BalanceRaw, 10)
	newValue, newOK := new(big.Int).SetString(newRaw, 10)
	if !oldOK || !newOK {
		return nil, fmt.Errorf("invalid holder balance")
	}
	comparison := newValue.Cmp(oldValue)
	if comparison == 0 {
		return nil, nil
	}
	eventType := "holder_increase"
	delta := new(big.Int).Sub(newValue, oldValue)
	if comparison < 0 {
		eventType = "holder_decrease"
		delta.Neg(delta)
	}
	formatted, err := chain.FormatUnits(delta.String(), int(project.TokenDecimals))
	if err != nil {
		return nil, err
	}
	quote := marketquality.Quote{Status: marketquality.StatusUnknown}
	valuationAt := occurredAt.UTC().Truncate(time.Microsecond)
	var priceUSD *string
	if valuationSnapshot != nil {
		quote = valuationSnapshot.QuoteQuality.Clone()
		if quote.AvailableAt(valuationAt) && positiveDecimal(valuationSnapshot.PriceUSD) {
			priceUSD = valuationSnapshot.PriceUSD
		}
	}
	metadata, _ := json.Marshal(map[string]any{
		"holder_address":           old.HolderAddress,
		"old_balance":              old.Balance,
		"new_balance":              newBalance,
		"old_rank":                 old.Rank,
		"new_rank":                 newRank,
		"valuation_schema_version": 1,
		"valuation_complete":       priceUSD != nil,
		"valuation_quote_quality":  quote,
		"valuation_at":             valuationAt,
	})
	wallet := strings.ToLower(old.HolderAddress)
	raw := delta.String()
	usdValue := multiplyDecimals(formatted, priceUSD)
	eventKey := holderEventKey(project, wallet, eventType, occurredAt)
	return &store.CreateMarketEventParams{
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
	}, nil
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
