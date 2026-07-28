package marketview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/assetcatalog"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketprotocol"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketrules"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/subscription"
)

const defaultChainKey = "bsc"

var ErrMarketDataUnavailable = errors.New("行情数据服务暂时繁忙，请稍后重试。")

type Repository interface {
	ListMarketProjects(context.Context, string, bool) ([]store.MarketProject, error)
	GetMarketProject(context.Context, int64, string) (*store.MarketProject, error)
	SetMarketProjectStatus(context.Context, int64, string, string, string) (store.MarketProject, error)
	ArchiveMarketProject(context.Context, int64, string) (store.MarketProject, error)
	DeleteArchivedMarketProject(context.Context, int64, string) error
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
	PoolFactory(context.Context, string, string, string) (string, error)
}

type AssetIdentityService interface {
	ResolveContract(context.Context, string, string) (*assetcatalog.Candidate, error)
	VerifyCrossChainIdentity(
		context.Context,
		assetcatalog.CrossChainVerifyInput,
	) (assetcatalog.CrossChainVerificationResult, error)
}

type multiChainProjectRepository interface {
	GetMarketProjectAsset(
		context.Context,
		int64,
		string,
	) (*store.MarketAsset, error)
	ListMarketProjectDeploymentViews(
		context.Context,
		int64,
		string,
	) ([]store.MarketProjectDeploymentView, error)
	ListLatestMarketProjectSnapshots(
		context.Context,
		int64,
		string,
	) ([]store.MarketSnapshot, error)
	ListMarketHolderViews(
		context.Context,
		int64,
		string,
		bool,
		int,
	) ([]store.MarketHolderView, error)
}

type multiChainProjectEntitlements interface {
	CreateMultiChainMarketProject(
		context.Context,
		store.CreateMultiChainMarketProjectParams,
	) (store.MarketProject, error)
}

type Dependencies struct {
	Repository   Repository
	Entitlements Entitlements
	Chain        ChainService
	Market       marketdata.Provider
	Assets       AssetIdentityService
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
	ChainKey          string          `json:"chain_key"`
	ChainID           int64           `json:"chain_id"`
	Protocol          string          `json:"protocol"`
	ProtocolVersion   string          `json:"protocol_version"`
	ParserAdapter     string          `json:"parser_adapter"`
	FactoryAddress    string          `json:"factory_address,omitempty"`
	FactoryVerified   bool            `json:"factory_verified"`
	Token0Address     string          `json:"token0_address,omitempty"`
	Token1Address     string          `json:"token1_address,omitempty"`
	MonitoringLevel   string          `json:"monitoring_level"`
	SupportedFeatures []string        `json:"supported_features,omitempty"`
	Supported         bool            `json:"supported"`
	UnsupportedReason string          `json:"unsupported_reason"`
}

type TokenQueryResult struct {
	ChainKey string              `json:"chain_key"`
	ChainID  int64               `json:"chain_id"`
	Token    chain.TokenMetadata `json:"token"`
	Pools    []PoolPreview       `json:"pools"`
}

type MultiTokenQueryInput struct {
	Deployments []TokenQueryInput `json:"deployments"`
}

type ChainPoolGroup struct {
	ChainKey       string              `json:"chain_key"`
	ChainID        int64               `json:"chain_id"`
	ChainName      string              `json:"chain_name"`
	Token          chain.TokenMetadata `json:"token"`
	FullMonitoring []PoolPreview       `json:"full_monitoring_pools"`
	QuoteOnly      []PoolPreview       `json:"quote_only_pools"`
	Error          string              `json:"error,omitempty"`
}

type MultiTokenQueryResult struct {
	Groups []ChainPoolGroup `json:"groups"`
}

type RecommendationPreviewInput struct {
	Deployments []RecommendationPreviewDeployment `json:"deployments"`
}

type RecommendationPreviewDeployment struct {
	ChainKey      string   `json:"chain_key"`
	TokenAddress  string   `json:"token_address"`
	PoolAddresses []string `json:"pool_addresses"`
}

type CreateProjectInput struct {
	ChainKey         string                         `json:"chain_key"`
	TokenAddress     string                         `json:"token_address"`
	PoolAddresses    []string                       `json:"pool_addresses"`
	CanonicalAssetID string                         `json:"canonical_asset_id"`
	IdentitySource   string                         `json:"identity_source"`
	LogoURL          string                         `json:"logo_url"`
	Deployments      []CreateProjectDeploymentInput `json:"deployments"`
}

type CreateProjectDeploymentInput struct {
	ChainKey           string   `json:"chain_key"`
	TokenAddress       string   `json:"token_address"`
	PoolAddresses      []string `json:"pool_addresses"`
	PrimaryPoolAddress string   `json:"primary_pool_address"`
}

type ProjectDetail struct {
	Project        store.MarketProject                 `json:"project"`
	Asset          *store.MarketAsset                  `json:"asset,omitempty"`
	Pools          []store.MarketPoolView              `json:"pools"`
	LatestSnapshot *store.MarketSnapshot               `json:"latest_snapshot"`
	Snapshots      []store.MarketSnapshot              `json:"snapshots"`
	Rules          []store.MarketRule                  `json:"rules"`
	Combinations   []store.MarketCombinationRule       `json:"combinations"`
	Holders        []store.MarketHolderView            `json:"holders"`
	Labels         []store.MarketAddressLabel          `json:"labels"`
	ProviderHealth []store.MarketProviderHealth        `json:"provider_health"`
	Deployments    []store.MarketProjectDeploymentView `json:"deployments"`
}

type EventFilterInput struct {
	BeforeID      int64
	Limit         int
	ChainKey      string
	EventType     string
	MarketPoolID  int64
	WalletAddress string
}

type CreateRuleInput struct {
	MarketPoolID               *int64  `json:"market_pool_id"`
	DeploymentScope            string  `json:"deployment_scope"`
	MarketProjectDeploymentIDs []int64 `json:"market_project_deployment_ids"`
	PoolScope                  string  `json:"pool_scope"`
	MarketProjectPoolIDs       []int64 `json:"market_project_pool_ids"`
	CooldownScope              string  `json:"cooldown_scope"`
	RuleType                   string  `json:"rule_type"`
	ThresholdValue             string  `json:"threshold_value"`
	ThresholdUnit              string  `json:"threshold_unit"`
	WindowMinutes              *int32  `json:"window_minutes"`
	Sensitivity                string  `json:"sensitivity"`
	CooldownSeconds            int32   `json:"cooldown_seconds"`
	RepeatWhileActive          bool    `json:"repeat_while_active"`
	DeliveryMode               string  `json:"delivery_mode"`
	CycleType                  string  `json:"cycle_type"`
	CycleMinutes               int32   `json:"cycle_minutes"`
	TriggerCountThreshold      int64   `json:"trigger_count_threshold"`
	NotificationChatID         string  `json:"notification_chat_id"`
	NotificationChatType       string  `json:"notification_chat_type"`
	NotificationLabel          string  `json:"notification_label"`
	NotificationLanguage       string  `json:"notification_language"`
}

type PoolSelectionInput struct {
	MarketPoolID int64 `json:"market_pool_id"`
	Selected     bool  `json:"selected"`
	IsPrimary    bool  `json:"is_primary"`
}

type CreateCombinationInput struct {
	Note                 string                         `json:"note"`
	CycleType            string                         `json:"cycle_type"`
	CycleMinutes         int32                          `json:"cycle_minutes"`
	NotificationChatID   string                         `json:"notification_chat_id"`
	NotificationChatType string                         `json:"notification_chat_type"`
	NotificationLabel    string                         `json:"notification_label"`
	NotificationLanguage string                         `json:"notification_language"`
	Members              []CreateCombinationMemberInput `json:"members"`
}

type CreateCombinationMemberInput struct {
	SourceType           string `json:"source_type"`
	WatchRuleID          *int64 `json:"watch_rule_id"`
	MarketRuleID         *int64 `json:"market_rule_id"`
	RequiredTriggerCount int64  `json:"required_trigger_count"`
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
	return s.queryToken(ctx, input)
}

func (s *Service) QueryTokens(
	ctx context.Context,
	deboxUserID string,
	input MultiTokenQueryInput,
) (MultiTokenQueryResult, error) {
	if err := s.requireMarketQuery(ctx, deboxUserID); err != nil {
		return MultiTokenQueryResult{}, err
	}
	if len(input.Deployments) == 0 || len(input.Deployments) > len(chain.SupportedChains()) {
		return MultiTokenQueryResult{}, errors.New("请选择 1 到 6 条链上的代币合约。")
	}

	seenChains := make(map[string]struct{}, len(input.Deployments))
	normalized := make([]TokenQueryInput, 0, len(input.Deployments))
	for _, deployment := range input.Deployments {
		profile, err := chain.ChainProfile(deployment.ChainKey, "")
		if err != nil {
			return MultiTokenQueryResult{}, err
		}
		if _, exists := seenChains[profile.Key]; exists {
			return MultiTokenQueryResult{}, fmt.Errorf("同一条链不能重复提交：%s", profile.Name)
		}
		seenChains[profile.Key] = struct{}{}
		tokenAddress, err := chain.ValidateAddress(deployment.TokenAddress)
		if err != nil {
			return MultiTokenQueryResult{}, fmt.Errorf(
				"%s 的代币合约地址无效",
				profile.Name,
			)
		}
		normalized = append(normalized, TokenQueryInput{
			ChainKey:     profile.Key,
			TokenAddress: tokenAddress,
		})
	}

	groups := make([]ChainPoolGroup, 0, len(input.Deployments))
	for _, deployment := range normalized {
		profile, err := chain.ChainProfile(deployment.ChainKey, "")
		if err != nil {
			return MultiTokenQueryResult{}, err
		}

		group := ChainPoolGroup{
			ChainKey:  profile.Key,
			ChainID:   profile.ChainID,
			ChainName: profile.Name,
		}
		query, queryErr := s.queryToken(ctx, TokenQueryInput{
			ChainKey:     profile.Key,
			TokenAddress: deployment.TokenAddress,
		})
		if queryErr != nil {
			if errors.Is(queryErr, ErrMarketDataUnavailable) {
				group.Error = "该链行情数据暂时不可用，请稍后重试。"
				groups = append(groups, group)
				continue
			}
			return MultiTokenQueryResult{}, queryErr
		}
		group.Token = query.Token
		for _, pool := range query.Pools {
			if pool.Supported {
				group.FullMonitoring = append(group.FullMonitoring, pool)
			} else {
				group.QuoteOnly = append(group.QuoteOnly, pool)
			}
		}
		groups = append(groups, group)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		return supportedChainOrder(groups[i].ChainKey) <
			supportedChainOrder(groups[j].ChainKey)
	})
	return MultiTokenQueryResult{Groups: groups}, nil
}

func (s *Service) PreviewRecommendations(
	ctx context.Context,
	deboxUserID string,
	input RecommendationPreviewInput,
) ([]marketrules.Recommendation, error) {
	if err := s.requireMarketQuery(ctx, deboxUserID); err != nil {
		return nil, err
	}
	if len(input.Deployments) == 0 ||
		len(input.Deployments) > len(chain.SupportedChains()) {
		return nil, errors.New("请选择 1 到 6 条链上的监控池。")
	}

	var selectedPair *marketdata.Pair
	var selectedToken string
	var selectedProfile chain.Profile
	selectedLiquidity := new(big.Rat).SetInt64(-1)
	seenChains := make(map[string]struct{}, len(input.Deployments))
	for _, deployment := range input.Deployments {
		profile, err := chain.ChainProfile(deployment.ChainKey, "")
		if err != nil {
			return nil, err
		}
		if _, exists := seenChains[profile.Key]; exists {
			return nil, errors.New("每条链只能提交一次推荐值查询。")
		}
		seenChains[profile.Key] = struct{}{}
		tokenAddress, err := chain.ValidateAddress(deployment.TokenAddress)
		if err != nil {
			return nil, errors.New("代币合约地址无效。")
		}
		if len(deployment.PoolAddresses) == 0 || len(deployment.PoolAddresses) > 20 {
			return nil, errors.New("每条链请选择 1 到 20 个交易池。")
		}
		poolAddresses := make([]string, 0, len(deployment.PoolAddresses))
		for _, address := range deployment.PoolAddresses {
			normalized, normalizeErr := chain.ValidateAddress(address)
			if normalizeErr != nil {
				return nil, errors.New("交易池地址无效。")
			}
			poolAddresses = append(poolAddresses, normalized)
		}
		pairs, err := s.deps.Market.PairsByAddresses(
			ctx, profile.Key, poolAddresses,
		)
		if err != nil {
			if marketdata.IsTemporaryError(err) {
				return nil, ErrMarketDataUnavailable
			}
			return nil, fmt.Errorf("刷新推荐行情失败：%w", err)
		}
		for index := range pairs {
			pair := &pairs[index]
			if !strings.EqualFold(pair.BaseToken.Address, tokenAddress) &&
				!strings.EqualFold(pair.QuoteToken.Address, tokenAddress) {
				continue
			}
			liquidity := new(big.Rat)
			if _, ok := liquidity.SetString(pair.Liquidity.USD.String()); !ok {
				liquidity.SetInt64(0)
			}
			if selectedPair == nil || liquidity.Cmp(selectedLiquidity) > 0 {
				selectedPair = pair
				selectedToken = tokenAddress
				selectedProfile = profile
				selectedLiquidity.Set(liquidity)
			}
		}
	}
	if selectedPair == nil {
		return nil, errors.New("暂时无法读取所选交易池的最新行情。")
	}
	snapshot := recommendationSnapshotFromPair(
		*selectedPair, selectedToken, selectedProfile,
	)
	return marketrules.RecommendThresholds(snapshot, nil), nil
}

func recommendationSnapshotFromPair(
	pair marketdata.Pair,
	tokenAddress string,
	profile chain.Profile,
) store.MarketSnapshot {
	price := recommendationPairPrice(pair, tokenAddress)
	buys5m, sells5m := recommendationTransactionPointers(pair.Transactions["m5"])
	buys1h, sells1h := recommendationTransactionPointers(pair.Transactions["h1"])
	buys24h, sells24h := recommendationTransactionPointers(pair.Transactions["h24"])
	return store.MarketSnapshot{
		ChainKey:     profile.Key,
		ChainID:      profile.ChainID,
		TokenAddress: tokenAddress,
		PriceUSD:     price,
		LiquidityUSD: recommendationDecimalPointer(pair.Liquidity.USD),
		FDVUSD:       recommendationDecimalPointer(pair.FDV),
		MarketCapUSD: recommendationDecimalPointer(pair.MarketCap),
		Volume5mUSD:  recommendationDecimalPointer(pair.Volume["m5"]),
		Volume15mUSD: recommendationDecimalPointer(pair.Volume["m15"]),
		Volume1hUSD:  recommendationDecimalPointer(pair.Volume["h1"]),
		Volume6hUSD:  recommendationDecimalPointer(pair.Volume["h6"]),
		Volume24hUSD: recommendationDecimalPointer(pair.Volume["h24"]),
		Buys5m:       buys5m,
		Sells5m:      sells5m,
		Buys1h:       buys1h,
		Sells1h:      sells1h,
		Buys24h:      buys24h,
		Sells24h:     sells24h,
		Source:       marketdata.SourceDexScreener,
		CapturedAt:   time.Now().UTC(),
	}
}

func recommendationPairPrice(
	pair marketdata.Pair,
	tokenAddress string,
) *string {
	if strings.EqualFold(pair.BaseToken.Address, tokenAddress) {
		return recommendationDecimalPointer(pair.PriceUSD)
	}
	if !strings.EqualFold(pair.QuoteToken.Address, tokenAddress) ||
		!pair.PriceUSD.Valid() || !pair.PriceNative.Valid() {
		return nil
	}
	baseUSD, ok := new(big.Rat).SetString(pair.PriceUSD.String())
	if !ok {
		return nil
	}
	basePerQuote, ok := new(big.Rat).SetString(pair.PriceNative.String())
	if !ok || basePerQuote.Sign() == 0 {
		return nil
	}
	value := new(big.Rat).Quo(baseUSD, basePerQuote)
	result := strings.TrimRight(strings.TrimRight(value.FloatString(18), "0"), ".")
	if result == "" {
		result = "0"
	}
	return &result
}

func recommendationDecimalPointer(value marketdata.Decimal) *string {
	if !value.Valid() {
		return nil
	}
	result := value.String()
	return &result
}

func recommendationTransactionPointers(
	value marketdata.TransactionCounts,
) (*int64, *int64) {
	buys, sells := value.Buys, value.Sells
	return &buys, &sells
}

func (s *Service) queryToken(
	ctx context.Context,
	input TokenQueryInput,
) (TokenQueryResult, error) {
	chainKey, err := normalizeChain(input.ChainKey)
	if err != nil {
		return TokenQueryResult{}, err
	}
	profile, err := chain.ChainProfile(chainKey, "")
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
		if marketdata.IsTemporaryError(err) {
			return TokenQueryResult{}, ErrMarketDataUnavailable
		}
		return TokenQueryResult{}, fmt.Errorf("查询交易池失败：%w", err)
	}
	previews := make([]PoolPreview, len(pairs))
	verificationSlots := make(chan struct{}, 6)
	var verificationWait sync.WaitGroup
	for index, pair := range pairs {
		index, pair := index, pair
		verificationWait.Add(1)
		go func() {
			defer verificationWait.Done()
			verificationSlots <- struct{}{}
			classification := marketprotocol.VerifyPair(
				ctx,
				s.deps.Chain,
				chainKey,
				tokenAddress,
				pair,
			)
			<-verificationSlots
			previews[index] = PoolPreview{
				Pair:              pair,
				ChainKey:          classification.ChainKey,
				ChainID:           classification.ChainID,
				Protocol:          classification.Protocol,
				ProtocolVersion:   classification.ProtocolVersion,
				ParserAdapter:     classification.ParserAdapter,
				FactoryAddress:    classification.FactoryAddress,
				FactoryVerified:   classification.FactoryVerified,
				Token0Address:     classification.Token0Address,
				Token1Address:     classification.Token1Address,
				MonitoringLevel:   classification.MonitoringLevel,
				SupportedFeatures: classification.SupportedFeature,
				Supported:         classification.Supported,
				UnsupportedReason: classification.UnsupportedReason,
			}
		}()
	}
	verificationWait.Wait()
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
		ChainID:  profile.ChainID,
		Token:    token,
		Pools:    previews,
	}, nil
}

func supportedChainOrder(chainKey string) int {
	for index, profile := range chain.SupportedChains() {
		if profile.Key == chainKey {
			return index
		}
	}
	return len(chain.SupportedChains())
}

func (s *Service) CreateProject(
	ctx context.Context,
	deboxUserID string,
	input CreateProjectInput,
) (ProjectDetail, error) {
	if len(input.Deployments) > 0 {
		return s.createMultiChainProject(ctx, deboxUserID, input)
	}
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

func (s *Service) createMultiChainProject(
	ctx context.Context,
	deboxUserID string,
	input CreateProjectInput,
) (ProjectDetail, error) {
	if s.deps.Assets == nil {
		return ProjectDetail{}, errors.New("资产身份校验服务不可用。")
	}
	if len(input.Deployments) == 0 ||
		len(input.Deployments) > len(chain.SupportedChains()) {
		return ProjectDetail{}, errors.New("请选择 1 到 6 条链上的代币合约。")
	}
	queryInput := MultiTokenQueryInput{
		Deployments: make([]TokenQueryInput, len(input.Deployments)),
	}
	contracts := make([]assetcatalog.ManualContractInput, len(input.Deployments))
	for index, deployment := range input.Deployments {
		queryInput.Deployments[index] = TokenQueryInput{
			ChainKey: deployment.ChainKey, TokenAddress: deployment.TokenAddress,
		}
		contracts[index] = assetcatalog.ManualContractInput{
			ChainKey: deployment.ChainKey, ContractAddress: deployment.TokenAddress,
		}
	}
	query, err := s.QueryTokens(ctx, deboxUserID, queryInput)
	if err != nil {
		return ProjectDetail{}, err
	}
	requestByChain := make(map[string]CreateProjectDeploymentInput, len(input.Deployments))
	for _, deployment := range input.Deployments {
		profile, profileErr := chain.ChainProfile(deployment.ChainKey, "")
		if profileErr != nil {
			return ProjectDetail{}, profileErr
		}
		requestByChain[profile.Key] = deployment
	}

	identitySource := strings.ToLower(strings.TrimSpace(input.IdentitySource))
	canonicalAssetID := strings.ToLower(strings.TrimSpace(input.CanonicalAssetID))
	logoURL := strings.TrimSpace(input.LogoURL)
	if !strings.HasPrefix(logoURL, "/api/market/assets/logo?source=") {
		logoURL = ""
	}
	verificationStatus := assetcatalog.IdentitySingleChain
	canonicalName := ""
	canonicalSymbol := ""
	verifiedAt := time.Now().UTC()
	evidenceByChain := make(map[string]assetcatalog.IdentityEvidenceRecord)
	if len(input.Deployments) > 1 {
		if identitySource != assetcatalog.SourceCoinGecko || canonicalAssetID == "" {
			return ProjectDetail{}, assetcatalog.ErrCrossChainIdentityUnverified
		}
		verification, verifyErr := s.deps.Assets.VerifyCrossChainIdentity(
			ctx,
			assetcatalog.CrossChainVerifyInput{
				CanonicalAssetID: canonicalAssetID,
				Contracts:        contracts,
			},
		)
		if verifyErr != nil {
			return ProjectDetail{}, verifyErr
		}
		canonicalName = verification.CanonicalName
		canonicalSymbol = verification.Symbol
		identitySource = verification.IdentitySource
		verificationStatus = verification.VerificationStatus
		verifiedAt = verification.VerifiedAt
		for _, evidence := range verification.Evidence {
			evidenceByChain[evidence.ChainKey] = evidence
		}
	} else {
		group := query.Groups[0]
		canonicalName = group.Token.Name
		canonicalSymbol = group.Token.Symbol
		if canonicalAssetID != "" && identitySource == assetcatalog.SourceCoinGecko {
			candidate, resolveErr := s.deps.Assets.ResolveContract(
				ctx, group.ChainKey, group.Token.Address,
			)
			if resolveErr != nil {
				return ProjectDetail{}, resolveErr
			}
			if candidate.CanonicalAssetID != canonicalAssetID ||
				!candidateHasContract(*candidate, group.ChainKey, group.Token.Address) {
				return ProjectDetail{}, assetcatalog.ErrCrossChainIdentityConflict
			}
			canonicalName = candidate.Name
			canonicalSymbol = candidate.Symbol
			logoURL = candidate.LogoURL
		} else {
			identitySource = assetcatalog.SourceOnChain
			canonicalAssetID = fmt.Sprintf(
				"eip155:%d/erc20:%s", group.ChainID, group.Token.Address,
			)
		}
	}

	createParams := store.CreateMultiChainMarketProjectParams{
		DeBoxUserID:        deboxUserID,
		CanonicalName:      canonicalName,
		Symbol:             canonicalSymbol,
		LogoURL:            logoURL,
		IdentitySource:     identitySource,
		CanonicalAssetID:   canonicalAssetID,
		VerificationStatus: verificationStatus,
		Metadata: mustJSON(map[string]any{
			"creation_source":  "h5_four_step_wizard",
			"deployment_count": len(query.Groups),
		}),
		Deployments: make(
			[]store.CreateMultiChainMarketDeploymentParams, 0, len(query.Groups),
		),
	}
	for _, group := range query.Groups {
		if group.Error != "" {
			return ProjectDetail{}, errors.New(group.Error)
		}
		request, exists := requestByChain[group.ChainKey]
		if !exists {
			return ProjectDetail{}, errors.New("交易池查询结果与所选链不一致。")
		}
		selectedAddresses := normalizedAddressSet(request.PoolAddresses)
		if len(selectedAddresses) == 0 {
			return ProjectDetail{}, fmt.Errorf(
				"%s 至少选择一个完整监控交易池。", group.ChainName,
			)
		}
		allPools := append(
			append([]PoolPreview(nil), group.FullMonitoring...),
			group.QuoteOnly...,
		)
		poolParams := make(
			[]store.CreateMultiChainMarketPoolParams, 0, len(allPools),
		)
		selectedCount := 0
		primaryAddress := strings.ToLower(strings.TrimSpace(request.PrimaryPoolAddress))
		if _, exists := selectedAddresses[primaryAddress]; !exists {
			return ProjectDetail{}, fmt.Errorf(
				"%s 的主池必须属于已选择的完整监控交易池。", group.ChainName,
			)
		}
		for _, preview := range allPools {
			pairAddress := strings.ToLower(strings.TrimSpace(preview.Pair.PairAddress))
			pool, persistErr := s.persistPool(ctx, group.Token, preview)
			if persistErr != nil {
				return ProjectDetail{}, persistErr
			}
			_, selected := selectedAddresses[pairAddress]
			selected = selected && preview.Supported
			poolParams = append(poolParams, store.CreateMultiChainMarketPoolParams{
				MarketPoolID: pool.ID,
				Selected:     selected,
				IsPrimary:    selected && pairAddress == primaryAddress,
			})
			if selected {
				selectedCount++
			}
		}
		if selectedCount != len(selectedAddresses) {
			return ProjectDetail{}, fmt.Errorf(
				"%s 包含无效或仅支持行情的交易池。", group.ChainName,
			)
		}
		totalSupply := group.Token.TotalSupplyRaw
		deploymentParams := store.CreateMultiChainMarketDeploymentParams{
			ChainKey:           group.ChainKey,
			ChainID:            group.ChainID,
			TokenAddress:       group.Token.Address,
			TokenName:          group.Token.Name,
			TokenSymbol:        group.Token.Symbol,
			TokenDecimals:      group.Token.Decimals,
			TotalSupplyRaw:     &totalSupply,
			VerificationStatus: verificationStatus,
			VerificationSource: identitySource,
			VerificationEvidence: mustJSON(map[string]any{
				"canonical_asset_id": canonicalAssetID,
				"verified_at":        verifiedAt,
			}),
			VerifiedAt: &verifiedAt,
			Metadata: mustJSON(map[string]any{
				"chain_name": group.ChainName,
			}),
			Pools: poolParams,
		}
		if evidence, ok := evidenceByChain[group.ChainKey]; ok {
			deploymentParams.VerificationEvidence = evidence.Payload
			deploymentParams.Evidence = &store.CreateMarketAssetEvidenceParams{
				EvidenceKey:     evidence.EvidenceKey,
				Source:          evidence.Source,
				EvidenceType:    evidence.EvidenceType,
				ExternalAssetID: evidence.ExternalAssetID,
				Verdict:         evidence.Verdict,
				Confidence:      evidence.Confidence,
				Payload:         evidence.Payload,
				ObservedAt:      evidence.ObservedAt,
			}
		}
		createParams.Deployments = append(
			createParams.Deployments, deploymentParams,
		)
	}
	entitlements, ok := s.deps.Entitlements.(multiChainProjectEntitlements)
	if !ok {
		return ProjectDetail{}, errors.New("多链项目创建服务不可用。")
	}
	project, err := entitlements.CreateMultiChainMarketProject(ctx, createParams)
	if err != nil {
		return ProjectDetail{}, err
	}
	return s.Project(ctx, deboxUserID, project.ID)
}

func candidateHasContract(
	candidate assetcatalog.Candidate,
	chainKey string,
	tokenAddress string,
) bool {
	for _, deployment := range candidate.Deployments {
		if deployment.ChainKey == chainKey &&
			deployment.ContractAddress == tokenAddress {
			return true
		}
	}
	return false
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
	var asset *store.MarketAsset
	deployments := []store.MarketProjectDeploymentView{}
	snapshots := []store.MarketSnapshot{}
	holderViews := []store.MarketHolderView{}
	if repository, ok := s.deps.Repository.(multiChainProjectRepository); ok {
		asset, err = repository.GetMarketProjectAsset(
			ctx, projectID, deboxUserID,
		)
		if err != nil {
			return ProjectDetail{}, err
		}
		deployments, err = repository.ListMarketProjectDeploymentViews(
			ctx, projectID, deboxUserID,
		)
		if err != nil {
			return ProjectDetail{}, err
		}
		snapshots, err = repository.ListLatestMarketProjectSnapshots(
			ctx, projectID, deboxUserID,
		)
		if err != nil {
			return ProjectDetail{}, err
		}
		holderViews, err = repository.ListMarketHolderViews(
			ctx, projectID, deboxUserID, true, 500,
		)
		if err != nil {
			return ProjectDetail{}, err
		}
	}
	rules, err := s.deps.Repository.ListMarketRules(ctx, deboxUserID, &projectID)
	if err != nil {
		return ProjectDetail{}, err
	}
	if len(holderViews) == 0 {
		holders, holderErr := s.deps.Repository.ListMarketHolders(
			ctx, projectID, deboxUserID, true, 100,
		)
		if holderErr != nil {
			return ProjectDetail{}, holderErr
		}
		holderViews = make([]store.MarketHolderView, len(holders))
		for index := range holders {
			holderViews[index] = store.MarketHolderView{
				MarketHolder: holders[index],
				ChangeType:   "unchanged",
			}
		}
	}
	labels, err := s.deps.Repository.ListMarketAddressLabels(ctx, projectID, deboxUserID)
	if err != nil {
		return ProjectDetail{}, err
	}
	health, err := s.deps.Repository.ListMarketProviderHealth(ctx)
	if err != nil {
		return ProjectDetail{}, err
	}
	combinations := []store.MarketCombinationRule{}
	if repository, ok := s.deps.Repository.(interface {
		ListMarketCombinationRules(
			context.Context,
			string,
		) ([]store.MarketCombinationRule, error)
	}); ok {
		combinations, err = repository.ListMarketCombinationRules(ctx, deboxUserID)
		if err != nil {
			return ProjectDetail{}, err
		}
	}
	ruleIDs := make(map[int64]struct{}, len(rules))
	for _, rule := range rules {
		ruleIDs[rule.ID] = struct{}{}
	}
	projectCombinations := make([]store.MarketCombinationRule, 0)
	for _, combination := range combinations {
		for _, member := range combination.Members {
			if member.MarketRuleID == nil {
				continue
			}
			if _, belongs := ruleIDs[*member.MarketRuleID]; belongs {
				projectCombinations = append(projectCombinations, combination)
				break
			}
		}
	}
	snapshot, err := s.deps.Repository.LatestMarketSnapshot(
		ctx, project.ChainID, project.TokenAddress, project.MainPoolID,
	)
	if err != nil {
		return ProjectDetail{}, err
	}
	return ProjectDetail{
		Project: *project, Asset: asset, Pools: pools,
		LatestSnapshot: snapshot, Snapshots: snapshots, Rules: rules,
		Combinations: projectCombinations, Holders: holderViews,
		Labels: labels, ProviderHealth: health, Deployments: deployments,
	}, nil
}

func (s *Service) ArchiveProject(
	ctx context.Context,
	deboxUserID string,
	projectID int64,
) (store.MarketProject, error) {
	return s.deps.Repository.ArchiveMarketProject(ctx, projectID, deboxUserID)
}

func (s *Service) DeleteArchivedProject(
	ctx context.Context,
	deboxUserID string,
	projectID int64,
) error {
	return s.deps.Repository.DeleteArchivedMarketProject(ctx, projectID, deboxUserID)
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
	ruleType := strings.ToLower(strings.TrimSpace(input.RuleType))
	repeatWhileActive := input.RepeatWhileActive &&
		(ruleType == plans.MarketPriceIncrease || ruleType == plans.MarketPriceDecrease)
	return s.deps.Entitlements.CreateMarketRule(ctx, store.CreateMarketRuleParams{
		DeBoxUserID:     deboxUserID,
		MarketProjectID: projectID,
		MarketPoolID:    input.MarketPoolID,
		DeploymentScope: input.DeploymentScope,
		MarketProjectDeploymentIDs: append(
			[]int64(nil), input.MarketProjectDeploymentIDs...,
		),
		PoolScope:             input.PoolScope,
		MarketProjectPoolIDs:  append([]int64(nil), input.MarketProjectPoolIDs...),
		CooldownScope:         input.CooldownScope,
		RuleType:              ruleType,
		ThresholdValue:        strings.TrimSpace(input.ThresholdValue),
		ThresholdUnit:         strings.ToLower(strings.TrimSpace(input.ThresholdUnit)),
		WindowMinutes:         input.WindowMinutes,
		Sensitivity:           strings.ToLower(strings.TrimSpace(input.Sensitivity)),
		CooldownSeconds:       input.CooldownSeconds,
		RepeatWhileActive:     repeatWhileActive,
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

func (s *Service) ListCombinations(
	ctx context.Context,
	deboxUserID string,
) ([]store.MarketCombinationRule, error) {
	repository, ok := s.deps.Repository.(interface {
		ListMarketCombinationRules(
			context.Context,
			string,
		) ([]store.MarketCombinationRule, error)
	})
	if !ok {
		return nil, errors.New("market combination repository is unavailable")
	}
	return repository.ListMarketCombinationRules(ctx, deboxUserID)
}

func (s *Service) CreateCombination(
	ctx context.Context,
	deboxUserID string,
	input CreateCombinationInput,
) (store.MarketCombinationRule, error) {
	chatType := strings.ToLower(strings.TrimSpace(input.NotificationChatType))
	if chatType != "group" {
		chatType = "private"
	}
	chatID := strings.TrimSpace(input.NotificationChatID)
	if chatType == "private" {
		chatID = deboxUserID
	}
	members := make([]store.CreateMarketCombinationMemberParams, len(input.Members))
	for index, member := range input.Members {
		members[index] = store.CreateMarketCombinationMemberParams{
			SourceType:           strings.ToLower(strings.TrimSpace(member.SourceType)),
			WatchRuleID:          member.WatchRuleID,
			MarketRuleID:         member.MarketRuleID,
			RequiredTriggerCount: member.RequiredTriggerCount,
		}
	}
	return s.deps.Entitlements.CreateMarketCombination(
		ctx,
		store.CreateMarketCombinationParams{
			DeBoxUserID:          deboxUserID,
			Note:                 strings.TrimSpace(input.Note),
			CycleType:            strings.ToLower(strings.TrimSpace(input.CycleType)),
			CycleMinutes:         input.CycleMinutes,
			NotificationChatID:   chatID,
			NotificationChatType: chatType,
			NotificationLabel:    strings.TrimSpace(input.NotificationLabel),
			NotificationLanguage: input.NotificationLanguage,
			Members:              members,
		},
	)
}

func (s *Service) ArchiveCombination(
	ctx context.Context,
	deboxUserID string,
	combinationID int64,
) error {
	repository, available := s.deps.Repository.(interface {
		ArchiveMarketCombinationRule(context.Context, int64, string) (bool, error)
	})
	if !available {
		return errors.New("market combination repository is unavailable")
	}
	ok, err := repository.ArchiveMarketCombinationRule(
		ctx, combinationID, deboxUserID,
	)
	if err != nil {
		return err
	}
	if !ok {
		return store.ErrNotFound
	}
	return nil
}

func (s *Service) RestoreCombination(
	ctx context.Context,
	deboxUserID string,
	combinationID int64,
) (store.MarketCombinationRule, error) {
	entitlements, ok := s.deps.Entitlements.(interface {
		RestoreMarketCombination(
			context.Context,
			string,
			int64,
		) (store.MarketCombinationRule, error)
	})
	if !ok {
		return store.MarketCombinationRule{}, errors.New(
			"market combination entitlements are unavailable",
		)
	}
	return entitlements.RestoreMarketCombination(
		ctx, deboxUserID, combinationID,
	)
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
	input EventFilterInput,
) ([]store.MarketEvent, error) {
	if input.Limit == 0 {
		input.Limit = 50
	}
	if input.BeforeID < 0 || input.Limit < 1 || input.Limit > 100 ||
		input.MarketPoolID < 0 {
		return nil, errors.New("市场事件分页参数无效。")
	}
	input.ChainKey = strings.ToLower(strings.TrimSpace(input.ChainKey))
	input.EventType = strings.ToLower(strings.TrimSpace(input.EventType))
	input.WalletAddress = strings.ToLower(strings.TrimSpace(input.WalletAddress))
	if input.WalletAddress != "" {
		address, err := chain.ValidateAddress(input.WalletAddress)
		if err != nil {
			return nil, errors.New("事件地址筛选必须是有效的 EVM 地址。")
		}
		input.WalletAddress = address
	}
	if input.ChainKey != "" {
		profile, err := chain.ChainProfile(input.ChainKey, "")
		if err != nil {
			return nil, err
		}
		input.ChainKey = profile.Key
	}
	if len(input.EventType) > 64 {
		return nil, errors.New("事件类型筛选无效。")
	}
	if repository, ok := s.deps.Repository.(interface {
		ListMarketEventsFiltered(
			context.Context,
			int64,
			string,
			store.MarketEventFilter,
		) ([]store.MarketEvent, error)
	}); ok {
		return repository.ListMarketEventsFiltered(
			ctx,
			projectID,
			deboxUserID,
			store.MarketEventFilter{
				BeforeID:      input.BeforeID,
				Limit:         input.Limit,
				ChainKey:      input.ChainKey,
				EventType:     input.EventType,
				MarketPoolID:  input.MarketPoolID,
				WalletAddress: input.WalletAddress,
			},
		)
	}
	return s.deps.Repository.ListMarketEvents(
		ctx, projectID, deboxUserID, input.BeforeID, input.Limit,
	)
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
		return errors.New("当前套餐不支持代币行情查询。")
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
	if preview.Supported {
		first, second, err := s.deps.Chain.PoolTokens(
			ctx, pair.PairAddress, preview.ChainKey, preview.ChainKey,
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
				ctx, token0Address, preview.ChainKey, preview.ChainKey,
			)
			if err != nil {
				return store.MarketPool{}, fmt.Errorf("读取交易池 token0 信息失败：%w", err)
			}
			token0Symbol, token0Decimals = metadata.Symbol, metadata.Decimals
		}
		if token1Address != token.Address {
			metadata, err := s.deps.Chain.TokenMetadata(
				ctx, token1Address, preview.ChainKey, preview.ChainKey,
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
	if preview.FactoryVerified && preview.FactoryAddress != "" {
		value := preview.FactoryAddress
		factoryAddress = &value
	}
	raw, err := marketprotocol.EncodePairMetadata(
		pair,
		preview.MonitoringLevel,
		preview.UnsupportedReason,
		preview.SupportedFeatures,
		preview.FactoryAddress,
	)
	if err != nil {
		return store.MarketPool{}, fmt.Errorf("编码交易池验证信息失败：%w", err)
	}
	verification := "unsupported"
	if preview.Supported {
		verification = "verified"
	}
	return s.deps.Repository.UpsertMarketPool(ctx, store.UpsertMarketPoolParams{
		ChainKey:             preview.ChainKey,
		ChainID:              preview.ChainID,
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
	case "ethereum", "base", "polygon", "arbitrum", "optimism":
		return strings.ToLower(strings.TrimSpace(value)), nil
	default:
		return "", errors.New(
			"代币市场监控仅支持 BNB Chain、Ethereum、Base、Polygon、Arbitrum 和 Optimism。",
		)
	}
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
