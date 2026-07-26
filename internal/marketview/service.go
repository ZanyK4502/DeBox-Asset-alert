package marketview

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
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketrules"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/subscription"
)

const (
	defaultChainKey = "bsc"
	defaultChainID  = int64(56)
)

type Repository interface {
	ListMarketProjects(context.Context, string, bool) ([]store.MarketProject, error)
	GetMarketProject(context.Context, int64, string) (*store.MarketProject, error)
	SetMarketProjectStatus(context.Context, int64, string, string, string) (store.MarketProject, error)
	ArchiveMarketProject(context.Context, int64, string) (store.MarketProject, error)
	UpsertMarketPool(context.Context, store.UpsertMarketPoolParams) (store.MarketPool, error)
	ListMarketProjectPoolViews(context.Context, int64, string) ([]store.MarketPoolView, error)
	LatestMarketSnapshot(context.Context, int64, string, *int64) (*store.MarketSnapshot, error)
	ListMarketSnapshots(context.Context, int64, string, int) ([]store.MarketSnapshot, error)
	ListMarketRules(context.Context, string, *int64) ([]store.MarketRule, error)
	DeleteMarketRule(context.Context, int64, string) (bool, error)
	ListMarketEvents(context.Context, int64, string, int64, int) ([]store.MarketEvent, error)
	ListMarketHolders(context.Context, int64, string, bool, int) ([]store.MarketHolder, error)
	ListMarketAddressLabels(context.Context, int64, string) ([]store.MarketAddressLabel, error)
	UpsertMarketAddressLabel(context.Context, store.UpsertMarketAddressLabelParams) (store.MarketAddressLabel, error)
	DeleteMarketAddressLabel(context.Context, int64, string) (bool, error)
	ListMarketProviderHealth(context.Context) ([]store.MarketProviderHealth, error)
}

type Entitlements interface {
	ActivePlan(context.Context, string) (plans.Plan, error)
	Entitlement(context.Context, string) (subscription.Entitlement, error)
	CreateMarketProject(context.Context, store.CreateMarketProjectParams) (store.MarketProject, error)
	RestoreMarketProject(context.Context, string, int64) (store.MarketProject, error)
	LinkMarketProjectPool(context.Context, store.LinkMarketProjectPoolParams) (store.MarketProjectPool, error)
	CreateMarketRule(context.Context, store.CreateMarketRuleParams) (store.MarketRule, error)
	RestoreMarketRule(context.Context, string, int64) (store.MarketRule, error)
	CreateMarketCombination(context.Context, store.CreateMarketCombinationParams) (store.MarketCombinationRule, error)
}

type ChainService interface {
	TokenMetadata(context.Context, string, string, string) (chain.TokenMetadata, error)
	PoolTokens(context.Context, string, string, string) (string, string, error)
}

type Dependencies struct {
	Repository   Repository
	Entitlements Entitlements
	Chain        ChainService
	Market       marketdata.Provider
	Catalog      *plans.Catalog
}

type Service struct {
	deps Dependencies
}

func New(dependencies Dependencies) *Service {
	return &Service{deps: dependencies}
}

type TokenQueryInput struct {
	ChainKey     string `json:"chain_key"`
	TokenAddress string `json:"token_address"`
}

type PoolPreview struct {
	Pair              marketdata.Pair `json:"pair"`
	Protocol          string          `json:"protocol"`
	ProtocolVersion   string          `json:"protocol_version"`
	ParserAdapter     string          `json:"parser_adapter"`
	Supported         bool            `json:"supported"`
	UnsupportedReason string          `json:"unsupported_reason"`
}

type TokenQueryResult struct {
	ChainKey string              `json:"chain_key"`
	ChainID  int64               `json:"chain_id"`
	Token    chain.TokenMetadata `json:"token"`
	Pools    []PoolPreview       `json:"pools"`
}

type CreateProjectInput struct {
	ChainKey      string   `json:"chain_key"`
	TokenAddress  string   `json:"token_address"`
	PoolAddresses []string `json:"pool_addresses"`
}

type ProjectDetail struct {
	Project        store.MarketProject          `json:"project"`
	Pools          []store.MarketPoolView       `json:"pools"`
	LatestSnapshot *store.MarketSnapshot        `json:"latest_snapshot"`
	Rules          []store.MarketRule           `json:"rules"`
	Holders        []store.MarketHolder         `json:"holders"`
	Labels         []store.MarketAddressLabel   `json:"labels"`
	ProviderHealth []store.MarketProviderHealth `json:"provider_health"`
}

type CreateRuleInput struct {
	MarketPoolID          *int64 `json:"market_pool_id"`
	RuleType              string `json:"rule_type"`
	ThresholdValue        string `json:"threshold_value"`
	ThresholdUnit         string `json:"threshold_unit"`
	WindowMinutes         *int32 `json:"window_minutes"`
	Sensitivity           string `json:"sensitivity"`
	CooldownSeconds       int32  `json:"cooldown_seconds"`
	DeliveryMode          string `json:"delivery_mode"`
	CycleType             string `json:"cycle_type"`
	CycleMinutes          int32  `json:"cycle_minutes"`
	TriggerCountThreshold int64  `json:"trigger_count_threshold"`
	NotificationChatID    string `json:"notification_chat_id"`
	NotificationChatType  string `json:"notification_chat_type"`
	NotificationLabel     string `json:"notification_label"`
	NotificationLanguage  string `json:"notification_language"`
}

type PoolSelectionInput struct {
	MarketPoolID int64 `json:"market_pool_id"`
	Selected     bool  `json:"selected"`
	IsPrimary    bool  `json:"is_primary"`
}

type AddressLabelInput struct {
	Address   string `json:"address"`
	LabelType string `json:"label_type"`
	Label     string `json:"label"`
	Excluded  bool   `json:"excluded"`
}

func (s *Service) QueryToken(
	ctx context.Context,
	deboxUserID string,
	input TokenQueryInput,
) (TokenQueryResult, error) {
	if err := s.requireMarketQuery(ctx, deboxUserID); err != nil {
		return TokenQueryResult{}, err
	}
	chainKey, err := normalizeChain(input.ChainKey)
	if err != nil {
		return TokenQueryResult{}, err
	}
	tokenAddress, err := chain.ValidateAddress(input.TokenAddress)
	if err != nil {
		return TokenQueryResult{}, errors.New("代币合约地址无效。")
	}
	token, err := s.deps.Chain.TokenMetadata(ctx, tokenAddress, chainKey, defaultChainKey)
	if err != nil {
		return TokenQueryResult{}, fmt.Errorf("无法读取该代币合约：%w", err)
	}
	pairs, err := s.deps.Market.DiscoverPools(ctx, chainKey, tokenAddress)
	if err != nil {
		return TokenQueryResult{}, fmt.Errorf("查询交易池失败：%w", err)
	}
	previews := make([]PoolPreview, 0, len(pairs))
	for _, pair := range pairs {
		protocol, version, adapter, supported := classifyPair(pair)
		reason := ""
		if !supported {
			reason = "当前交易池可查询，但尚不能用于链上事件解析。"
		}
		previews = append(previews, PoolPreview{
			Pair: pair, Protocol: protocol, ProtocolVersion: version,
			ParserAdapter: adapter, Supported: supported, UnsupportedReason: reason,
		})
	}
	sort.SliceStable(previews, func(i, j int) bool {
		if previews[i].Supported != previews[j].Supported {
			return previews[i].Supported
		}
		return decimalGreater(
			previews[i].Pair.Liquidity.USD.String(),
			previews[j].Pair.Liquidity.USD.String(),
		)
	})
	return TokenQueryResult{
		ChainKey: chainKey,
		ChainID:  defaultChainID,
		Token:    token,
		Pools:    previews,
	}, nil
}

func (s *Service) CreateProject(
	ctx context.Context,
	deboxUserID string,
	input CreateProjectInput,
) (ProjectDetail, error) {
	query, err := s.QueryToken(ctx, deboxUserID, TokenQueryInput{
		ChainKey: input.ChainKey, TokenAddress: input.TokenAddress,
	})
	if err != nil {
		return ProjectDetail{}, err
	}
	totalSupply := query.Token.TotalSupplyRaw
	project, err := s.deps.Entitlements.CreateMarketProject(
		ctx,
		store.CreateMarketProjectParams{
			DeBoxUserID:    deboxUserID,
			ChainKey:       query.ChainKey,
			ChainID:        query.ChainID,
			TokenAddress:   query.Token.Address,
			TokenName:      query.Token.Name,
			TokenSymbol:    query.Token.Symbol,
			TokenDecimals:  query.Token.Decimals,
			TotalSupplyRaw: &totalSupply,
			FourMemeStatus: "unknown",
			Metadata:       mustJSON(map[string]any{"source": "onchain"}),
		},
	)
	if err != nil {
		return ProjectDetail{}, err
	}
	selected := normalizedAddressSet(input.PoolAddresses)
	if len(selected) == 0 {
		for _, preview := range query.Pools {
			if preview.Supported {
				selected[preview.Pair.PairAddress] = struct{}{}
				break
			}
		}
	}
	if len(selected) > 0 {
		matched := false
		for _, preview := range query.Pools {
			if _, exists := selected[preview.Pair.PairAddress]; exists && preview.Supported {
				matched = true
				break
			}
		}
		if !matched {
			_, _ = s.deps.Repository.SetMarketProjectStatus(
				ctx, project.ID, deboxUserID, "archived", "no_supported_pool_selected",
			)
			return ProjectDetail{}, errors.New("没有选择可用于事件解析的交易池。")
		}
	}
	primarySet := false
	for _, preview := range query.Pools {
		pool, persistErr := s.persistPool(ctx, query.Token, preview)
		if persistErr != nil {
			_, _ = s.deps.Repository.SetMarketProjectStatus(
				ctx, project.ID, deboxUserID, "archived", "pool_setup_failed",
			)
			return ProjectDetail{}, persistErr
		}
		_, wanted := selected[preview.Pair.PairAddress]
		wanted = wanted && preview.Supported
		isPrimary := wanted && !primarySet
		if wanted {
			primarySet = true
		}
		if _, linkErr := s.deps.Entitlements.LinkMarketProjectPool(
			ctx,
			store.LinkMarketProjectPoolParams{
				DeBoxUserID:     deboxUserID,
				MarketProjectID: project.ID,
				MarketPoolID:    pool.ID,
				Selected:        wanted,
				IsPrimary:       isPrimary,
				DiscoverySource: marketdata.SourceDexScreener,
			},
		); linkErr != nil {
			_, _ = s.deps.Repository.SetMarketProjectStatus(
				ctx, project.ID, deboxUserID, "archived", "pool_setup_failed",
			)
			return ProjectDetail{}, linkErr
		}
	}
	return s.Project(ctx, deboxUserID, project.ID)
}

func (s *Service) ListProjects(
	ctx context.Context,
	deboxUserID string,
	includeArchived bool,
) ([]store.MarketProject, error) {
	return s.deps.Repository.ListMarketProjects(ctx, deboxUserID, includeArchived)
}

func (s *Service) Project(
	ctx context.Context,
	deboxUserID string,
	projectID int64,
) (ProjectDetail, error) {
	project, err := s.deps.Repository.GetMarketProject(ctx, projectID, deboxUserID)
	if err != nil {
		return ProjectDetail{}, err
	}
	if project == nil {
		return ProjectDetail{}, store.ErrNotFound
	}
	pools, err := s.deps.Repository.ListMarketProjectPoolViews(ctx, projectID, deboxUserID)
	if err != nil {
		return ProjectDetail{}, err
	}
	rules, err := s.deps.Repository.ListMarketRules(ctx, deboxUserID, &projectID)
	if err != nil {
		return ProjectDetail{}, err
	}
	holders, err := s.deps.Repository.ListMarketHolders(ctx, projectID, deboxUserID, true, 100)
	if err != nil {
		return ProjectDetail{}, err
	}
	labels, err := s.deps.Repository.ListMarketAddressLabels(ctx, projectID, deboxUserID)
	if err != nil {
		return ProjectDetail{}, err
	}
	health, err := s.deps.Repository.ListMarketProviderHealth(ctx)
	if err != nil {
		return ProjectDetail{}, err
	}
	snapshot, err := s.deps.Repository.LatestMarketSnapshot(
		ctx, project.ChainID, project.TokenAddress, project.MainPoolID,
	)
	if err != nil {
		return ProjectDetail{}, err
	}
	return ProjectDetail{
		Project: *project, Pools: pools, LatestSnapshot: snapshot, Rules: rules,
		Holders: holders, Labels: labels, ProviderHealth: health,
	}, nil
}

func (s *Service) ArchiveProject(
	ctx context.Context,
	deboxUserID string,
	projectID int64,
) (store.MarketProject, error) {
	return s.deps.Repository.ArchiveMarketProject(ctx, projectID, deboxUserID)
}

func (s *Service) RestoreProject(
	ctx context.Context,
	deboxUserID string,
	projectID int64,
) (store.MarketProject, error) {
	return s.deps.Entitlements.RestoreMarketProject(ctx, deboxUserID, projectID)
}

func (s *Service) SelectPool(
	ctx context.Context,
	deboxUserID string,
	projectID int64,
	input PoolSelectionInput,
) (ProjectDetail, error) {
	if input.MarketPoolID <= 0 {
		return ProjectDetail{}, errors.New("交易池 ID 无效。")
	}
	_, err := s.deps.Entitlements.LinkMarketProjectPool(
		ctx,
		store.LinkMarketProjectPoolParams{
			DeBoxUserID:     deboxUserID,
			MarketProjectID: projectID,
			MarketPoolID:    input.MarketPoolID,
			Selected:        input.Selected || input.IsPrimary,
			IsPrimary:       input.IsPrimary,
			DiscoverySource: "user",
		},
	)
	if err != nil {
		return ProjectDetail{}, err
	}
	return s.Project(ctx, deboxUserID, projectID)
}

func (s *Service) CreateRule(
	ctx context.Context,
	deboxUserID string,
	projectID int64,
	input CreateRuleInput,
) (store.MarketRule, error) {
	chatType := strings.ToLower(strings.TrimSpace(input.NotificationChatType))
	if chatType == "" {
		chatType = "private"
	}
	chatID := strings.TrimSpace(input.NotificationChatID)
	if chatType == "private" {
		chatID = deboxUserID
	}
	language := strings.ToLower(strings.TrimSpace(input.NotificationLanguage))
	if language == "" {
		language = "zh"
	}
	deliveryMode := strings.ToLower(strings.TrimSpace(input.DeliveryMode))
	if deliveryMode == "" {
		deliveryMode = "realtime"
	}
	cycleType := strings.ToLower(strings.TrimSpace(input.CycleType))
	if cycleType == "" {
		cycleType = "fixed"
	}
	if input.CooldownSeconds <= 0 {
		input.CooldownSeconds = 300
	}
	if input.CycleMinutes <= 0 {
		input.CycleMinutes = 60
	}
	if input.TriggerCountThreshold <= 0 {
		input.TriggerCountThreshold = 1
	}
	return s.deps.Entitlements.CreateMarketRule(ctx, store.CreateMarketRuleParams{
		DeBoxUserID:           deboxUserID,
		MarketProjectID:       projectID,
		MarketPoolID:          input.MarketPoolID,
		RuleType:              strings.ToLower(strings.TrimSpace(input.RuleType)),
		ThresholdValue:        strings.TrimSpace(input.ThresholdValue),
		ThresholdUnit:         strings.ToLower(strings.TrimSpace(input.ThresholdUnit)),
		WindowMinutes:         input.WindowMinutes,
		Sensitivity:           strings.ToLower(strings.TrimSpace(input.Sensitivity)),
		CooldownSeconds:       input.CooldownSeconds,
		RuleScope:             "standalone",
		DeliveryMode:          deliveryMode,
		CycleType:             cycleType,
		CycleMinutes:          input.CycleMinutes,
		TriggerCountThreshold: input.TriggerCountThreshold,
		NotificationChatID:    chatID,
		NotificationChatType:  chatType,
		NotificationLabel:     strings.TrimSpace(input.NotificationLabel),
		NotificationLanguage:  language,
		State:                 json.RawMessage(`{}`),
	})
}

func (s *Service) DeleteRule(
	ctx context.Context,
	deboxUserID string,
	ruleID int64,
) error {
	deleted, err := s.deps.Repository.DeleteMarketRule(ctx, ruleID, deboxUserID)
	if err != nil {
		return err
	}
	if !deleted {
		return store.ErrNotFound
	}
	return nil
}

func (s *Service) RestoreRule(
	ctx context.Context,
	deboxUserID string,
	ruleID int64,
) (store.MarketRule, error) {
	return s.deps.Entitlements.RestoreMarketRule(ctx, deboxUserID, ruleID)
}

func (s *Service) Recommendations(
	ctx context.Context,
	deboxUserID string,
	projectID int64,
) ([]marketrules.Recommendation, error) {
	project, err := s.deps.Repository.GetMarketProject(ctx, projectID, deboxUserID)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, store.ErrNotFound
	}
	snapshot, err := s.deps.Repository.LatestMarketSnapshot(
		ctx, project.ChainID, project.TokenAddress, project.MainPoolID,
	)
	if err != nil {
		return nil, err
	}
	if snapshot == nil {
		snapshot = &store.MarketSnapshot{
			ChainKey: project.ChainKey, ChainID: project.ChainID,
			TokenAddress: project.TokenAddress,
		}
	}
	events, err := s.deps.Repository.ListMarketEvents(
		ctx, projectID, deboxUserID, 0, 100,
	)
	if err != nil {
		return nil, err
	}
	return marketrules.RecommendThresholds(*snapshot, events), nil
}

func (s *Service) Events(
	ctx context.Context,
	deboxUserID string,
	projectID int64,
	beforeID int64,
	limit int,
) ([]store.MarketEvent, error) {
	if limit == 0 {
		limit = 50
	}
	if beforeID < 0 || limit < 1 || limit > 100 {
		return nil, errors.New("市场事件分页参数无效。")
	}
	return s.deps.Repository.ListMarketEvents(ctx, projectID, deboxUserID, beforeID, limit)
}

func (s *Service) SaveAddressLabel(
	ctx context.Context,
	deboxUserID string,
	projectID int64,
	input AddressLabelInput,
) (store.MarketAddressLabel, error) {
	return s.deps.Repository.UpsertMarketAddressLabel(
		ctx,
		store.UpsertMarketAddressLabelParams{
			DeBoxUserID:     deboxUserID,
			MarketProjectID: projectID,
			Address:         input.Address,
			LabelType:       input.LabelType,
			Label:           strings.TrimSpace(input.Label),
			Excluded:        input.Excluded,
		},
	)
}

func (s *Service) DeleteAddressLabel(
	ctx context.Context,
	deboxUserID string,
	labelID int64,
) error {
	deleted, err := s.deps.Repository.DeleteMarketAddressLabel(ctx, labelID, deboxUserID)
	if err != nil {
		return err
	}
	if !deleted {
		return store.ErrNotFound
	}
	return nil
}

func (s *Service) requireMarketQuery(ctx context.Context, deboxUserID string) error {
	plan, err := s.deps.Entitlements.ActivePlan(ctx, deboxUserID)
	if err != nil {
		return err
	}
	if !plan.MarketQuery {
		return errors.New("当前套餐不支持项目币行情查询。")
	}
	return nil
}

func (s *Service) persistPool(
	ctx context.Context,
	token chain.TokenMetadata,
	preview PoolPreview,
) (store.MarketPool, error) {
	pair := preview.Pair
	token0Address := pair.BaseToken.Address
	token1Address := pair.QuoteToken.Address
	if preview.ParserAdapter == marketparse.AdapterV2 ||
		preview.ParserAdapter == marketparse.AdapterV3 {
		first, second, err := s.deps.Chain.PoolTokens(
			ctx, pair.PairAddress, defaultChainKey, defaultChainKey,
		)
		if err != nil {
			return store.MarketPool{}, fmt.Errorf("验证交易池代币顺序失败：%w", err)
		}
		token0Address, token1Address = first, second
	}
	token0Symbol, token1Symbol := pair.BaseToken.Symbol, pair.QuoteToken.Symbol
	token0Decimals, token1Decimals := int32(18), int32(18)
	if token0Address == token.Address {
		token0Symbol, token0Decimals = token.Symbol, token.Decimals
	}
	if token1Address == token.Address {
		token1Symbol, token1Decimals = token.Symbol, token.Decimals
	}
	if preview.Supported {
		if token0Address != token.Address {
			metadata, err := s.deps.Chain.TokenMetadata(
				ctx, token0Address, defaultChainKey, defaultChainKey,
			)
			if err != nil {
				return store.MarketPool{}, fmt.Errorf("读取交易池 token0 信息失败：%w", err)
			}
			token0Symbol, token0Decimals = metadata.Symbol, metadata.Decimals
		}
		if token1Address != token.Address {
			metadata, err := s.deps.Chain.TokenMetadata(
				ctx, token1Address, defaultChainKey, defaultChainKey,
			)
			if err != nil {
				return store.MarketPool{}, fmt.Errorf("读取交易池 token1 信息失败：%w", err)
			}
			token1Symbol, token1Decimals = metadata.Symbol, metadata.Decimals
		}
	}
	var poolAddress *string
	if value, err := chain.ValidateAddress(pair.PairAddress); err == nil {
		poolAddress = &value
	}
	var factoryAddress *string
	switch preview.ParserAdapter {
	case marketparse.AdapterV2:
		value := marketparse.BSCPancakeV2Factory
		factoryAddress = &value
	case marketparse.AdapterV3:
		value := marketparse.BSCPancakeV3Factory
		factoryAddress = &value
	}
	raw, _ := json.Marshal(pair)
	verification := "unsupported"
	if preview.Supported {
		verification = "verified"
	}
	return s.deps.Repository.UpsertMarketPool(ctx, store.UpsertMarketPoolParams{
		ChainKey:             defaultChainKey,
		ChainID:              defaultChainID,
		Protocol:             preview.Protocol,
		ProtocolVersion:      preview.ProtocolVersion,
		PoolKey:              pair.PairAddress,
		PoolAddress:          poolAddress,
		FactoryAddress:       factoryAddress,
		FactoryVerified:      preview.Supported,
		Token0Address:        token0Address,
		Token0Symbol:         token0Symbol,
		Token0Decimals:       token0Decimals,
		Token1Address:        token1Address,
		Token1Symbol:         token1Symbol,
		Token1Decimals:       token1Decimals,
		LiquidityUSD:         decimalOrZero(pair.Liquidity.USD),
		SupportsEventParsing: preview.Supported,
		ParserAdapter:        preview.ParserAdapter,
		VerificationStatus:   verification,
		Metadata:             raw,
		SeenAt:               time.Now().UTC(),
	})
}

func normalizeChain(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "bsc", "bnb", "bnbchain", "bnb_chain":
		return defaultChainKey, nil
	default:
		return "", errors.New("项目币市场监控当前仅支持 BNB Chain。")
	}
}

func classifyPair(pair marketdata.Pair) (string, string, string, bool) {
	protocol := strings.ToLower(strings.TrimSpace(pair.DexID))
	labels := strings.ToLower(strings.Join(pair.Labels, " "))
	fingerprint := protocol + " " + labels
	if strings.Contains(protocol, "pancake") {
		switch {
		case strings.Contains(fingerprint, "infinity"), strings.Contains(fingerprint, "v4"):
			return "pancakeswap_infinity", labels, "", false
		case strings.Contains(fingerprint, "v3"):
			return "pancakeswap", "v3", marketparse.AdapterV3, true
		case strings.Contains(fingerprint, "v2"),
			protocol == "pancakeswap" && strings.TrimSpace(labels) == "":
			return "pancakeswap", "v2", marketparse.AdapterV2, true
		default:
			return protocol, labels, "", false
		}
	}
	if protocol == "" {
		protocol = "unknown"
	}
	return protocol, labels, "", false
}

func normalizedAddressSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if address, err := chain.ValidateAddress(value); err == nil {
			result[address] = struct{}{}
		}
	}
	return result
}

func decimalGreater(left string, right string) bool {
	leftValue, leftOK := new(big.Rat).SetString(left)
	rightValue, rightOK := new(big.Rat).SetString(right)
	if !leftOK {
		return false
	}
	if !rightOK {
		return true
	}
	return leftValue.Cmp(rightValue) > 0
}

func decimalOrZero(value marketdata.Decimal) string {
	if !value.Valid() {
		return "0"
	}
	return value.String()
}

func mustJSON(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}
