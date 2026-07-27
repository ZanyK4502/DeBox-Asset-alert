package assetcatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultCoinGeckoDemoBaseURL = "https://api.coingecko.com/api/v3"
	defaultCoinGeckoProBaseURL  = "https://pro-api.coingecko.com/api/v3"
	defaultCoinGeckoTimeout     = 15 * time.Second
	defaultSearchCacheTTL       = 10 * time.Minute
	defaultIdentityCacheTTL     = 6 * time.Hour
	defaultCoinGeckoStaleTTL    = 7 * 24 * time.Hour
	defaultCoinGeckoRetries     = 2
	defaultCircuitOpen          = 30 * time.Second
	maxCoinGeckoResponseBytes   = 64 << 20
	maxCoinGeckoErrorBytes      = 500
	maxCoinGeckoCacheEntries    = 512
)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Limiter interface {
	Wait(context.Context) error
}

type CoinGeckoSettings struct {
	Tier       string
	APIKey     string
	BaseURL    string
	HTTPClient HTTPDoer
	Limiter    Limiter
}

type CoinGeckoClient struct {
	tier       string
	apiKey     string
	baseURL    string
	httpClient HTTPDoer
	limiter    Limiter

	mu                  sync.Mutex
	cache               map[string]cachedResponse
	inflight            map[string]*responseCall
	consecutiveFailures int
	circuitOpenUntil    time.Time

	indexMu       sync.Mutex
	index         *identityIndex
	indexExpires  time.Time
	indexInflight *indexCall

	authoritativeIndex         *identityIndex
	authoritativeIndexExpires  time.Time
	authoritativeIndexInflight *indexCall
}

type cachedResponse struct {
	body       []byte
	expiresAt  time.Time
	staleUntil time.Time
}

type responseCall struct {
	done chan struct{}
	body []byte
	err  error
}

type indexCall struct {
	done  chan struct{}
	index *identityIndex
	err   error
}

type identityIndex struct {
	byID       map[string]coinListItem
	byContract map[string]string
}

type coinListItem struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Symbol    string            `json:"symbol"`
	Platforms map[string]string `json:"platforms"`
}

type searchEnvelope struct {
	Coins []searchCoin `json:"coins"`
}

type searchCoin struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Symbol        string `json:"symbol"`
	MarketCapRank *int   `json:"market_cap_rank"`
	Thumb         string `json:"thumb"`
	Large         string `json:"large"`
}

type CoinGeckoHTTPError struct {
	StatusCode int
	RetryAfter string
}

func (e *CoinGeckoHTTPError) Error() string {
	return fmt.Sprintf("CoinGecko API error %d", e.StatusCode)
}

func NewCoinGeckoClient(settings CoinGeckoSettings) (*CoinGeckoClient, error) {
	tier := strings.ToLower(strings.TrimSpace(settings.Tier))
	if tier == "" {
		tier = "demo"
	}
	if tier != "demo" && tier != "pro" {
		return nil, fmt.Errorf("COINGECKO_API_TIER must be demo or pro")
	}
	apiKey := strings.TrimSpace(settings.APIKey)
	if tier == "pro" && apiKey == "" {
		return nil, fmt.Errorf("COINGECKO_API_KEY is required for pro tier")
	}
	baseURL := strings.TrimSpace(settings.BaseURL)
	if baseURL == "" {
		if tier == "pro" {
			baseURL = defaultCoinGeckoProBaseURL
		} else {
			baseURL = defaultCoinGeckoDemoBaseURL
		}
	}
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "https" && !isLocalTestURL(parsed)) {
		return nil, fmt.Errorf("invalid CoinGecko base URL")
	}
	httpClient := settings.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultCoinGeckoTimeout}
	}
	limiter := settings.Limiter
	if limiter == nil {
		interval := 650 * time.Millisecond
		if tier == "pro" {
			interval = 220 * time.Millisecond
		}
		limiter = newIntervalLimiter(interval)
	}
	return &CoinGeckoClient{
		tier:       tier,
		apiKey:     apiKey,
		baseURL:    parsed.String(),
		httpClient: httpClient,
		limiter:    limiter,
		cache:      make(map[string]cachedResponse),
		inflight:   make(map[string]*responseCall),
	}, nil
}

func (c *CoinGeckoClient) Search(
	ctx context.Context,
	query string,
	limit int,
) ([]Candidate, error) {
	query, err := normalizeQuery(query)
	if err != nil {
		return nil, err
	}
	limit = normalizeLimit(limit)
	index, err := c.loadIdentityIndex(ctx)
	if err != nil {
		return nil, err
	}
	path := "/search?query=" + url.QueryEscape(query)
	body, err := c.cachedGET(
		ctx, path, defaultSearchCacheTTL, defaultCoinGeckoStaleTTL,
	)
	if err != nil {
		return nil, err
	}
	var envelope searchEnvelope
	if err := decodeCoinGeckoJSON(body, &envelope); err != nil {
		return nil, fmt.Errorf("%w: invalid CoinGecko search response", ErrUnavailable)
	}
	candidates := make([]Candidate, 0, len(envelope.Coins))
	seen := make(map[string]struct{}, len(envelope.Coins))
	for _, result := range envelope.Coins {
		item, ok := index.byID[strings.TrimSpace(result.ID)]
		if !ok {
			continue
		}
		candidate := candidateFromIdentity(item)
		if len(candidate.Deployments) == 0 {
			continue
		}
		if _, exists := seen[candidate.CanonicalAssetID]; exists {
			continue
		}
		seen[candidate.CanonicalAssetID] = struct{}{}
		candidate.Name = strings.TrimSpace(result.Name)
		candidate.Symbol = strings.ToUpper(strings.TrimSpace(result.Symbol))
		candidate.MarketCapRank = result.MarketCapRank
		candidate.remoteLogoURL = firstNonEmpty(result.Large, result.Thumb)
		candidates = append(candidates, candidate)
	}
	sortCandidates(candidates, query)
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, nil
}

func (c *CoinGeckoClient) ResolveContract(
	ctx context.Context,
	chainKey string,
	contractAddress string,
) (*Candidate, error) {
	return c.resolveContractWithIndex(
		ctx,
		chainKey,
		contractAddress,
		c.loadIdentityIndex,
	)
}

// ResolveContractAuthoritative never uses the stale-response fallback. It is
// reserved for the final cross-chain creation check, where an unavailable
// authority must fail closed instead of accepting old fallback data.
func (c *CoinGeckoClient) ResolveContractAuthoritative(
	ctx context.Context,
	chainKey string,
	contractAddress string,
) (*Candidate, error) {
	return c.resolveContractWithIndex(
		ctx,
		chainKey,
		contractAddress,
		c.loadAuthoritativeIdentityIndex,
	)
}

func (c *CoinGeckoClient) resolveContractWithIndex(
	ctx context.Context,
	chainKey string,
	contractAddress string,
	load func(context.Context) (*identityIndex, error),
) (*Candidate, error) {
	profile, ok := profileByChainKey(chainKey)
	if !ok {
		return nil, ErrInvalidQuery
	}
	deployment, ok := normalizeDeployment(profile.PlatformID, contractAddress)
	if !ok {
		return nil, ErrInvalidQuery
	}
	index, err := load(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := index.byContract[deployment.ChainKey+":"+deployment.ContractAddress]
	if !ok || id == "" {
		return nil, ErrNotFound
	}
	item, ok := index.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	candidate := candidateFromIdentity(item)
	return &candidate, nil
}

func candidateFromIdentity(item coinListItem) Candidate {
	deployments := make([]Deployment, 0, len(item.Platforms))
	for platformID, address := range item.Platforms {
		if deployment, ok := normalizeDeployment(platformID, address); ok {
			deployments = append(deployments, deployment)
		}
	}
	sort.Slice(deployments, func(i, j int) bool {
		return chainOrder(deployments[i].ChainKey) < chainOrder(deployments[j].ChainKey)
	})
	status := IdentitySingleChain
	if len(deployments) > 1 {
		status = IdentityVerified
	}
	return Candidate{
		CanonicalAssetID: strings.TrimSpace(item.ID),
		Name:             strings.TrimSpace(item.Name),
		Symbol:           strings.ToUpper(strings.TrimSpace(item.Symbol)),
		IdentitySource:   SourceCoinGecko,
		IdentityStatus:   status,
		Deployments:      deployments,
	}
}

func (c *CoinGeckoClient) loadIdentityIndex(ctx context.Context) (*identityIndex, error) {
	now := time.Now()
	c.indexMu.Lock()
	if c.index != nil && now.Before(c.indexExpires) {
		index := c.index
		c.indexMu.Unlock()
		return index, nil
	}
	if active := c.indexInflight; active != nil {
		c.indexMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-active.done:
			return active.index, active.err
		}
	}
	active := &indexCall{done: make(chan struct{})}
	c.indexInflight = active
	c.indexMu.Unlock()

	body, err := c.cachedGET(
		ctx,
		"/coins/list?include_platform=true&status=active",
		defaultIdentityCacheTTL,
		defaultCoinGeckoStaleTTL,
	)
	var result *identityIndex
	if err == nil {
		var items []coinListItem
		if decodeErr := decodeCoinGeckoJSON(body, &items); decodeErr != nil {
			err = fmt.Errorf("%w: invalid CoinGecko identity response", ErrUnavailable)
		} else {
			result = buildIdentityIndex(items)
		}
	}

	c.indexMu.Lock()
	if err == nil {
		c.index = result
		c.indexExpires = time.Now().Add(defaultIdentityCacheTTL)
	}
	active.index = result
	active.err = err
	c.indexInflight = nil
	close(active.done)
	c.indexMu.Unlock()
	return result, err
}

func (c *CoinGeckoClient) loadAuthoritativeIdentityIndex(
	ctx context.Context,
) (*identityIndex, error) {
	now := time.Now()
	c.indexMu.Lock()
	if c.authoritativeIndex != nil &&
		now.Before(c.authoritativeIndexExpires) {
		index := c.authoritativeIndex
		c.indexMu.Unlock()
		return index, nil
	}
	if active := c.authoritativeIndexInflight; active != nil {
		c.indexMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-active.done:
			return active.index, active.err
		}
	}
	active := &indexCall{done: make(chan struct{})}
	c.authoritativeIndexInflight = active
	c.indexMu.Unlock()

	body, err := c.getWithRetry(
		ctx,
		"/coins/list?include_platform=true&status=active",
	)
	var result *identityIndex
	if err == nil {
		var items []coinListItem
		if decodeErr := decodeCoinGeckoJSON(body, &items); decodeErr != nil {
			err = fmt.Errorf(
				"%w: invalid CoinGecko identity response",
				ErrUnavailable,
			)
		} else {
			result = buildIdentityIndex(items)
		}
	}

	c.indexMu.Lock()
	if err == nil {
		expiresAt := time.Now().Add(defaultIdentityCacheTTL)
		c.authoritativeIndex = result
		c.authoritativeIndexExpires = expiresAt
		c.index = result
		c.indexExpires = expiresAt
	}
	active.index = result
	active.err = err
	c.authoritativeIndexInflight = nil
	close(active.done)
	c.indexMu.Unlock()
	return result, err
}

func buildIdentityIndex(items []coinListItem) *identityIndex {
	result := &identityIndex{
		byID:       make(map[string]coinListItem, len(items)),
		byContract: make(map[string]string),
	}
	for _, item := range items {
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" {
			continue
		}
		result.byID[item.ID] = item
		for platformID, address := range item.Platforms {
			deployment, ok := normalizeDeployment(platformID, address)
			if !ok {
				continue
			}
			key := deployment.ChainKey + ":" + deployment.ContractAddress
			if current, exists := result.byContract[key]; !exists {
				result.byContract[key] = item.ID
			} else if current != item.ID {
				// Conflicting upstream identities are never silently merged.
				result.byContract[key] = ""
			}
		}
	}
	return result
}

func (c *CoinGeckoClient) cachedGET(
	ctx context.Context,
	path string,
	ttl time.Duration,
	staleTTL time.Duration,
) ([]byte, error) {
	now := time.Now()
	var stale []byte
	c.mu.Lock()
	if item, ok := c.cache[path]; ok && now.Before(item.expiresAt) {
		body := append([]byte(nil), item.body...)
		c.mu.Unlock()
		return body, nil
	} else if ok {
		if now.Before(item.staleUntil) {
			stale = append([]byte(nil), item.body...)
			if now.Before(c.circuitOpenUntil) {
				c.mu.Unlock()
				return stale, nil
			}
		} else {
			delete(c.cache, path)
		}
	}
	if active, ok := c.inflight[path]; ok {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-active.done:
			return append([]byte(nil), active.body...), active.err
		}
	}
	active := &responseCall{done: make(chan struct{})}
	c.inflight[path] = active
	c.mu.Unlock()

	body, err := c.getWithRetry(ctx, path)
	if err != nil && stale != nil && isCoinGeckoTemporary(err) {
		body = stale
		err = nil
	}

	c.mu.Lock()
	active.body = append([]byte(nil), body...)
	active.err = err
	if err == nil && ttl > 0 {
		c.evictCache(time.Now())
		cachedAt := time.Now()
		c.cache[path] = cachedResponse{
			body:       append([]byte(nil), body...),
			expiresAt:  cachedAt.Add(ttl),
			staleUntil: cachedAt.Add(ttl + staleTTL),
		}
	}
	delete(c.inflight, path)
	close(active.done)
	c.mu.Unlock()
	return body, err
}

func (c *CoinGeckoClient) getWithRetry(
	ctx context.Context,
	path string,
) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= defaultCoinGeckoRetries; attempt++ {
		if err := c.checkCircuit(); err != nil {
			return nil, err
		}
		body, err := c.getOnce(ctx, path)
		if err == nil {
			c.noteSuccess()
			return body, nil
		}
		lastErr = err
		if !isCoinGeckoTemporary(err) || attempt == defaultCoinGeckoRetries {
			c.noteFailure(err)
			return nil, err
		}
		c.noteFailure(err)
		delay := time.Duration(1<<attempt) * time.Second
		var apiError *CoinGeckoHTTPError
		if errors.As(err, &apiError) {
			if suggested := parseRetryAfter(apiError.RetryAfter); suggested > delay {
				delay = suggested
			}
		}
		if delay > 15*time.Second {
			delay = 15 * time.Second
		}
		if err := waitContext(ctx, delay); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *CoinGeckoClient) getOnce(ctx context.Context, path string) ([]byte, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: create CoinGecko request", ErrUnavailable)
	}
	request.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		header := "x-cg-demo-api-key"
		if c.tier == "pro" {
			header = "x-cg-pro-api-key"
		}
		request.Header.Set(header, c.apiKey)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: CoinGecko request failed", ErrUnavailable)
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusBadRequest {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxCoinGeckoErrorBytes))
		return nil, &CoinGeckoHTTPError{
			StatusCode: response.StatusCode,
			RetryAfter: strings.TrimSpace(response.Header.Get("Retry-After")),
		}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxCoinGeckoResponseBytes+1))
	if err != nil || len(body) > maxCoinGeckoResponseBytes {
		return nil, fmt.Errorf("%w: invalid CoinGecko response size", ErrUnavailable)
	}
	return body, nil
}

func (c *CoinGeckoClient) checkCircuit() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.circuitOpenUntil) {
		return ErrUnavailable
	}
	return nil
}

func (c *CoinGeckoClient) noteFailure(err error) {
	if !isCoinGeckoTemporary(err) {
		return
	}
	c.mu.Lock()
	c.consecutiveFailures++
	if c.consecutiveFailures >= 3 {
		c.circuitOpenUntil = time.Now().Add(defaultCircuitOpen)
	}
	c.mu.Unlock()
}

func (c *CoinGeckoClient) noteSuccess() {
	c.mu.Lock()
	c.consecutiveFailures = 0
	c.circuitOpenUntil = time.Time{}
	c.mu.Unlock()
}

func (c *CoinGeckoClient) evictCache(now time.Time) {
	if len(c.cache) < maxCoinGeckoCacheEntries {
		return
	}
	for key, item := range c.cache {
		if !now.Before(item.staleUntil) {
			delete(c.cache, key)
		}
	}
	for len(c.cache) >= maxCoinGeckoCacheEntries {
		var oldestKey string
		var oldest time.Time
		for key, item := range c.cache {
			if oldestKey == "" || item.staleUntil.Before(oldest) {
				oldestKey = key
				oldest = item.staleUntil
			}
		}
		delete(c.cache, oldestKey)
	}
}

func isCoinGeckoTemporary(err error) bool {
	if err == nil ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, ErrUnavailable) {
		return true
	}
	var apiError *CoinGeckoHTTPError
	if errors.As(err, &apiError) {
		return apiError.StatusCode == http.StatusTooManyRequests ||
			apiError.StatusCode == http.StatusBadGateway ||
			apiError.StatusCode == http.StatusServiceUnavailable ||
			apiError.StatusCode == http.StatusGatewayTimeout
	}
	var networkError net.Error
	return errors.As(err, &networkError)
}

func sortCandidates(candidates []Candidate, query string) {
	query = strings.ToLower(strings.TrimSpace(query))
	sort.SliceStable(candidates, func(i, j int) bool {
		leftExactName := strings.EqualFold(candidates[i].Name, query)
		rightExactName := strings.EqualFold(candidates[j].Name, query)
		if leftExactName != rightExactName {
			return leftExactName
		}
		leftExactSymbol := strings.EqualFold(candidates[i].Symbol, query)
		rightExactSymbol := strings.EqualFold(candidates[j].Symbol, query)
		if leftExactSymbol != rightExactSymbol {
			return leftExactSymbol
		}
		leftRank := rankOrMax(candidates[i].MarketCapRank)
		rightRank := rankOrMax(candidates[j].MarketCapRank)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if len(candidates[i].Deployments) != len(candidates[j].Deployments) {
			return len(candidates[i].Deployments) > len(candidates[j].Deployments)
		}
		if (candidates[i].remoteLogoURL != "") != (candidates[j].remoteLogoURL != "") {
			return candidates[i].remoteLogoURL != ""
		}
		return candidates[i].CanonicalAssetID < candidates[j].CanonicalAssetID
	})
}

func rankOrMax(value *int) int {
	if value == nil || *value <= 0 {
		return int(^uint(0) >> 1)
	}
	return *value
}

func chainOrder(chainKey string) int {
	for index, profile := range supportedPlatforms {
		if profile.ChainKey == chainKey {
			return index
		}
	}
	return len(supportedPlatforms)
}

func decodeCoinGeckoJSON(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values")
	}
	return nil
}

func isLocalTestURL(value *url.URL) bool {
	if value == nil || value.Scheme != "http" {
		return false
	}
	host := strings.ToLower(value.Hostname())
	if host == "localhost" {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if deadline, err := http.ParseTime(value); err == nil && deadline.After(time.Now()) {
		return time.Until(deadline)
	}
	return 0
}

func waitContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type intervalLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func newIntervalLimiter(interval time.Duration) *intervalLimiter {
	return &intervalLimiter{interval: interval}
}

func (l *intervalLimiter) Wait(ctx context.Context) error {
	l.mu.Lock()
	now := time.Now()
	delay := l.next.Sub(now)
	if delay < 0 {
		delay = 0
	}
	l.next = now.Add(delay).Add(l.interval)
	l.mu.Unlock()
	return waitContext(ctx, delay)
}
