package marketdata

import (
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

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
)

const (
	defaultDexScreenerBaseURL      = "https://api.dexscreener.com"
	defaultDexScreenerTimeout      = 15 * time.Second
	defaultDexScreenerRateInterval = 350 * time.Millisecond
	defaultDiscoveryCacheTTL       = 2 * time.Minute
	defaultQuoteCacheTTL           = 15 * time.Second
	defaultDiscoveryStaleTTL       = time.Hour
	defaultQuoteStaleTTL           = 2 * time.Minute
	defaultDexScreenerMaxRetries   = 2
	defaultDexScreenerRetryDelay   = 2 * time.Second
	defaultDexScreenerMaxBackoff   = 30 * time.Second
	maxDexScreenerBatchSize        = 30
	maxDexScreenerCacheEntries     = 2048
	maxDexScreenerErrorBody        = 500
)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Limiter interface {
	Wait(context.Context) error
}

type DexScreenerOption func(*DexScreenerClient)

func WithDexScreenerHTTPClient(client HTTPDoer) DexScreenerOption {
	return func(target *DexScreenerClient) {
		if client != nil {
			target.httpClient = client
		}
	}
}

func WithDexScreenerLimiter(limiter Limiter) DexScreenerOption {
	return func(target *DexScreenerClient) {
		if limiter != nil {
			target.limiter = limiter
		}
	}
}

func WithDexScreenerCacheTTLs(discovery, quotes time.Duration) DexScreenerOption {
	return func(target *DexScreenerClient) {
		if discovery >= 0 {
			target.discoveryCacheTTL = discovery
		}
		if quotes >= 0 {
			target.quoteCacheTTL = quotes
		}
	}
}

type DexScreenerClient struct {
	baseURL           string
	httpClient        HTTPDoer
	limiter           Limiter
	discoveryCacheTTL time.Duration
	quoteCacheTTL     time.Duration
	discoveryStaleTTL time.Duration
	quoteStaleTTL     time.Duration
	maxRetries        int
	retryDelay        time.Duration
	maxBackoff        time.Duration

	mu                sync.Mutex
	cache             map[string]cachedPairs
	inflight          map[string]*pairCall
	cooldownUntil     time.Time
	rateLimitFailures int
}

type cachedPairs struct {
	pairs      []Pair
	expiresAt  time.Time
	staleUntil time.Time
}

type pairCall struct {
	done  chan struct{}
	pairs []Pair
	err   error
}

type DexScreenerHTTPError struct {
	StatusCode int
	RetryAfter string
	Body       string
}

func (e *DexScreenerHTTPError) Error() string {
	return fmt.Sprintf("DexScreener API error %d", e.StatusCode)
}

func NewDexScreenerClient(baseURL string, options ...DexScreenerOption) (*DexScreenerClient, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultDexScreenerBaseURL
	}
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid DexScreener base URL")
	}
	client := &DexScreenerClient{
		baseURL:           parsed.String(),
		httpClient:        &http.Client{Timeout: defaultDexScreenerTimeout},
		limiter:           newIntervalLimiter(defaultDexScreenerRateInterval),
		discoveryCacheTTL: defaultDiscoveryCacheTTL,
		quoteCacheTTL:     defaultQuoteCacheTTL,
		discoveryStaleTTL: defaultDiscoveryStaleTTL,
		quoteStaleTTL:     defaultQuoteStaleTTL,
		maxRetries:        defaultDexScreenerMaxRetries,
		retryDelay:        defaultDexScreenerRetryDelay,
		maxBackoff:        defaultDexScreenerMaxBackoff,
		cache:             make(map[string]cachedPairs),
		inflight:          make(map[string]*pairCall),
	}
	for _, option := range options {
		option(client)
	}
	return client, nil
}

func (c *DexScreenerClient) DiscoverPools(
	ctx context.Context,
	chainID, tokenAddress string,
) ([]Pair, error) {
	normalizedChain, addresses, err := normalizeRequest(chainID, []string{tokenAddress})
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf(
		"/token-pairs/v1/%s/%s",
		url.PathEscape(normalizedChain),
		url.PathEscape(addresses[0]),
	)
	cacheKey := "discover:" + normalizedChain + ":" + addresses[0]
	return c.cachedFetch(
		ctx,
		cacheKey,
		c.discoveryCacheTTL,
		c.discoveryStaleTTL,
		func(ctx context.Context) ([]Pair, error) {
			pairs, err := c.getPairs(ctx, path, false)
			if err != nil {
				return nil, err
			}
			return filterPairs(pairs, normalizedChain, addresses, false)
		},
	)
}

func (c *DexScreenerClient) PairsByTokens(
	ctx context.Context,
	chainID string,
	tokenAddresses []string,
) ([]Pair, error) {
	normalizedChain, addresses, err := normalizeRequest(chainID, tokenAddresses)
	if err != nil {
		return nil, err
	}
	return c.fetchBatches(ctx, normalizedChain, addresses, "tokens", c.quoteCacheTTL)
}

func (c *DexScreenerClient) PairsByAddresses(
	ctx context.Context,
	chainID string,
	pairAddresses []string,
) ([]Pair, error) {
	normalizedChain, addresses, err := normalizeRequest(chainID, pairAddresses)
	if err != nil {
		return nil, err
	}
	return c.fetchBatches(ctx, normalizedChain, addresses, "pairs", c.quoteCacheTTL)
}

// SearchPairs is used only as the long-tail asset-search fallback. Unlike
// DiscoverPools, it searches globally and then keeps valid pairs from the six
// EVM chains supported by the product.
func (c *DexScreenerClient) SearchPairs(
	ctx context.Context,
	query string,
) ([]Pair, error) {
	query = strings.Join(strings.Fields(strings.TrimSpace(query)), " ")
	if len(query) < 2 || len(query) > 80 {
		return nil, fmt.Errorf("invalid DexScreener search query")
	}
	cacheKey := "search:" + strings.ToLower(query)
	return c.cachedFetch(
		ctx,
		cacheKey,
		c.discoveryCacheTTL,
		c.discoveryStaleTTL,
		func(ctx context.Context) ([]Pair, error) {
			return c.getSearchPairs(
				ctx,
				"/latest/dex/search?q="+url.QueryEscape(query),
			)
		},
	)
}

func (c *DexScreenerClient) fetchBatches(
	ctx context.Context,
	chainID string,
	addresses []string,
	mode string,
	cacheTTL time.Duration,
) ([]Pair, error) {
	result := make([]Pair, 0)
	for start := 0; start < len(addresses); start += maxDexScreenerBatchSize {
		end := start + maxDexScreenerBatchSize
		if end > len(addresses) {
			end = len(addresses)
		}
		batch := addresses[start:end]
		cacheKey := mode + ":" + chainID + ":" + strings.Join(batch, ",")
		pairs, err := c.cachedFetch(ctx, cacheKey, cacheTTL, c.quoteStaleTTL, func(ctx context.Context) ([]Pair, error) {
			var path string
			if mode == "tokens" {
				path = fmt.Sprintf("/tokens/v1/%s/%s", url.PathEscape(chainID), strings.Join(batch, ","))
			} else {
				path = fmt.Sprintf("/latest/dex/pairs/%s/%s", url.PathEscape(chainID), strings.Join(batch, ","))
			}
			pairs, err := c.getPairs(ctx, path, mode == "pairs")
			if err != nil {
				return nil, err
			}
			return filterPairs(pairs, chainID, batch, mode == "pairs")
		})
		if err != nil {
			return nil, err
		}
		result = append(result, pairs...)
	}
	return deduplicatePairs(result), nil
}

func (c *DexScreenerClient) getPairs(
	ctx context.Context,
	path string,
	wrapped bool,
) ([]Pair, error) {
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if err := c.waitForCooldown(ctx); err != nil {
			return nil, err
		}
		pairs, err := c.getPairsOnce(ctx, path, wrapped)
		if err == nil {
			c.clearRateLimitBackoff()
			return pairs, nil
		}
		lastErr = err
		if !IsTemporaryError(err) {
			return nil, err
		}
		var apiError *DexScreenerHTTPError
		if errors.As(err, &apiError) && apiError.StatusCode == http.StatusTooManyRequests {
			c.noteRateLimit(apiError.RetryAfter)
			if attempt == c.maxRetries {
				return nil, err
			}
			continue
		}
		if attempt == c.maxRetries {
			return nil, err
		}
		if err := waitContext(ctx, c.retryBackoff(attempt)); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *DexScreenerClient) getPairsOnce(
	ctx context.Context,
	path string,
	wrapped bool,
) ([]Pair, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("create DexScreener request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("DexScreener request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxDexScreenerErrorBody))
		return nil, &DexScreenerHTTPError{
			StatusCode: response.StatusCode,
			RetryAfter: strings.TrimSpace(response.Header.Get("Retry-After")),
			Body:       strings.TrimSpace(string(body)),
		}
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read DexScreener response: %w", err)
	}
	var pairs []Pair
	if wrapped {
		var envelope struct {
			Pairs []Pair `json:"pairs"`
		}
		if err := decodeJSON(body, &envelope); err != nil {
			return nil, fmt.Errorf("decode DexScreener response: %w", err)
		}
		pairs = envelope.Pairs
	} else if err := decodeJSON(body, &pairs); err != nil {
		return nil, fmt.Errorf("decode DexScreener response: %w", err)
	}
	return normalizePairs(pairs)
}

func (c *DexScreenerClient) getSearchPairs(
	ctx context.Context,
	path string,
) ([]Pair, error) {
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if err := c.waitForCooldown(ctx); err != nil {
			return nil, err
		}
		pairs, err := c.getSearchPairsOnce(ctx, path)
		if err == nil {
			c.clearRateLimitBackoff()
			return pairs, nil
		}
		lastErr = err
		if !IsTemporaryError(err) {
			return nil, err
		}
		var apiError *DexScreenerHTTPError
		if errors.As(err, &apiError) &&
			apiError.StatusCode == http.StatusTooManyRequests {
			c.noteRateLimit(apiError.RetryAfter)
			if attempt == c.maxRetries {
				return nil, err
			}
			continue
		}
		if attempt == c.maxRetries {
			return nil, err
		}
		if err := waitContext(ctx, c.retryBackoff(attempt)); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *DexScreenerClient) getSearchPairsOnce(
	ctx context.Context,
	path string,
) ([]Pair, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodGet, c.baseURL+path, nil,
	)
	if err != nil {
		return nil, fmt.Errorf("create DexScreener request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("DexScreener request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(
			io.LimitReader(response.Body, maxDexScreenerErrorBody),
		)
		return nil, &DexScreenerHTTPError{
			StatusCode: response.StatusCode,
			RetryAfter: strings.TrimSpace(response.Header.Get("Retry-After")),
			Body:       strings.TrimSpace(string(body)),
		}
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read DexScreener response: %w", err)
	}
	var envelope struct {
		Pairs []Pair `json:"pairs"`
	}
	if err := decodeJSON(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode DexScreener response: %w", err)
	}
	result := make([]Pair, 0, len(envelope.Pairs))
	for _, pair := range envelope.Pairs {
		switch strings.ToLower(strings.TrimSpace(pair.ChainID)) {
		case "bsc", "ethereum", "base", "polygon", "arbitrum", "optimism":
		default:
			continue
		}
		normalized, normalizeErr := normalizePairs([]Pair{pair})
		if normalizeErr == nil && len(normalized) == 1 {
			result = append(result, normalized[0])
		}
	}
	return deduplicatePairs(result), nil
}

func (c *DexScreenerClient) cachedFetch(
	ctx context.Context,
	key string,
	ttl time.Duration,
	staleTTL time.Duration,
	fetch func(context.Context) ([]Pair, error),
) ([]Pair, error) {
	now := time.Now()
	var stale []Pair
	c.mu.Lock()
	if item, ok := c.cache[key]; ok && now.Before(item.expiresAt) {
		pairs := clonePairs(item.pairs)
		c.mu.Unlock()
		return pairs, nil
	} else if ok {
		if now.Before(item.staleUntil) {
			stale = clonePairs(item.pairs)
			if now.Before(c.cooldownUntil) {
				c.mu.Unlock()
				return stale, nil
			}
		} else {
			delete(c.cache, key)
		}
	}
	if active, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-active.done:
			return clonePairs(active.pairs), active.err
		}
	}
	active := &pairCall{done: make(chan struct{})}
	c.inflight[key] = active
	c.mu.Unlock()

	pairs, err := fetch(ctx)
	cacheFresh := err == nil
	if err != nil && stale != nil && IsTemporaryError(err) {
		pairs = stale
		err = nil
		cacheFresh = false
	}

	c.mu.Lock()
	active.pairs = clonePairs(pairs)
	active.err = err
	if cacheFresh && ttl > 0 {
		cachedAt := time.Now()
		c.evictCacheEntries(cachedAt)
		c.cache[key] = cachedPairs{
			pairs:      clonePairs(pairs),
			expiresAt:  cachedAt.Add(ttl),
			staleUntil: cachedAt.Add(ttl).Add(staleTTL),
		}
	}
	delete(c.inflight, key)
	close(active.done)
	c.mu.Unlock()
	return pairs, err
}

// IsTemporaryError reports failures that callers may safely retry or satisfy
// with a recent cached response.
func IsTemporaryError(err error) bool {
	if err == nil ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var apiError *DexScreenerHTTPError
	if errors.As(err, &apiError) {
		return apiError.StatusCode == http.StatusTooManyRequests ||
			apiError.StatusCode == http.StatusBadGateway ||
			apiError.StatusCode == http.StatusServiceUnavailable ||
			apiError.StatusCode == http.StatusGatewayTimeout
	}
	var networkError net.Error
	return errors.As(err, &networkError)
}

func (c *DexScreenerClient) waitForCooldown(ctx context.Context) error {
	c.mu.Lock()
	wait := time.Until(c.cooldownUntil)
	c.mu.Unlock()
	return waitContext(ctx, wait)
}

func (c *DexScreenerClient) noteRateLimit(retryAfter string) {
	c.mu.Lock()
	c.rateLimitFailures++
	delay := c.retryBackoff(c.rateLimitFailures - 1)
	if suggested := parseRetryAfter(retryAfter, time.Now()); suggested > delay {
		delay = suggested
	}
	if delay > c.maxBackoff {
		delay = c.maxBackoff
	}
	until := time.Now().Add(delay)
	if until.After(c.cooldownUntil) {
		c.cooldownUntil = until
	}
	c.mu.Unlock()
}

func (c *DexScreenerClient) clearRateLimitBackoff() {
	c.mu.Lock()
	c.rateLimitFailures = 0
	c.cooldownUntil = time.Time{}
	c.mu.Unlock()
}

func (c *DexScreenerClient) retryBackoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 8 {
		attempt = 8
	}
	delay := c.retryDelay * time.Duration(1<<attempt)
	if delay > c.maxBackoff {
		return c.maxBackoff
	}
	return delay
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if deadline, err := http.ParseTime(value); err == nil && deadline.After(now) {
		return deadline.Sub(now)
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

// evictCacheEntries is called with c.mu held. It keeps a long-running worker
// from accumulating an entry for every token batch it has ever observed.
func (c *DexScreenerClient) evictCacheEntries(now time.Time) {
	if len(c.cache) < maxDexScreenerCacheEntries {
		return
	}
	for key, item := range c.cache {
		if !now.Before(item.staleUntil) {
			delete(c.cache, key)
		}
	}
	for len(c.cache) >= maxDexScreenerCacheEntries {
		var oldestKey string
		var oldestExpiry time.Time
		for key, item := range c.cache {
			if oldestKey == "" || item.staleUntil.Before(oldestExpiry) {
				oldestKey = key
				oldestExpiry = item.staleUntil
			}
		}
		if oldestKey == "" {
			return
		}
		delete(c.cache, oldestKey)
	}
}

func normalizeRequest(chainID string, addresses []string) (string, []string, error) {
	normalizedChain := strings.ToLower(strings.TrimSpace(chainID))
	switch normalizedChain {
	case "bnb", "bnbchain", "bnb_chain":
		normalizedChain = ChainBSC
	}
	switch normalizedChain {
	case ChainBSC,
		ChainEthereum,
		ChainBase,
		ChainPolygon,
		ChainArbitrum,
		ChainOptimism:
	default:
		return "", nil, fmt.Errorf("unsupported market chain: %s", normalizedChain)
	}
	if len(addresses) == 0 {
		return "", nil, fmt.Errorf("at least one address is required")
	}
	unique := make(map[string]struct{}, len(addresses))
	normalized := make([]string, 0, len(addresses))
	for _, address := range addresses {
		value, err := chain.ValidateAddress(address)
		if err != nil {
			return "", nil, err
		}
		if _, exists := unique[value]; exists {
			continue
		}
		unique[value] = struct{}{}
		normalized = append(normalized, value)
	}
	sort.Strings(normalized)
	return normalizedChain, normalized, nil
}

func normalizePairs(pairs []Pair) ([]Pair, error) {
	result := make([]Pair, 0, len(pairs))
	for _, pair := range pairs {
		pair.ChainID = strings.ToLower(strings.TrimSpace(pair.ChainID))
		pair.DexID = strings.ToLower(strings.TrimSpace(pair.DexID))
		var err error
		pair.BaseToken.Address, err = chain.ValidateAddress(pair.BaseToken.Address)
		if err != nil {
			return nil, fmt.Errorf("invalid base token address from DexScreener: %w", err)
		}
		pair.QuoteToken.Address, err = chain.ValidateAddress(pair.QuoteToken.Address)
		if err != nil {
			return nil, fmt.Errorf("invalid quote token address from DexScreener: %w", err)
		}
		pair.PairAddress, err = normalizePairKey(pair)
		if err != nil {
			return nil, fmt.Errorf("invalid pair address from DexScreener: %w", err)
		}
		result = append(result, pair)
	}
	return result, nil
}

func normalizePairKey(pair Pair) (string, error) {
	if address, err := chain.ValidateAddress(pair.PairAddress); err == nil {
		return address, nil
	}
	// Four.meme bonding-curve quotes do not have a pool contract. DexScreener
	// identifies them with "<token-address>:4meme". Preserve that value as a
	// pool key while continuing to reject non-address keys from every other DEX.
	if pair.DexID != "fourmeme" {
		return "", fmt.Errorf("invalid EVM pair address")
	}
	parts := strings.Split(strings.ToLower(strings.TrimSpace(pair.PairAddress)), ":")
	if len(parts) != 2 || parts[1] != "4meme" {
		return "", fmt.Errorf("invalid Four.meme pair key")
	}
	tokenAddress, err := chain.ValidateAddress(parts[0])
	if err != nil ||
		(tokenAddress != pair.BaseToken.Address && tokenAddress != pair.QuoteToken.Address) {
		return "", fmt.Errorf("invalid Four.meme token pair key")
	}
	return tokenAddress + ":4meme", nil
}

func filterPairs(
	pairs []Pair,
	chainID string,
	addresses []string,
	matchPairAddress bool,
) ([]Pair, error) {
	expected := make(map[string]struct{}, len(addresses))
	for _, address := range addresses {
		expected[address] = struct{}{}
	}
	result := make([]Pair, 0, len(pairs))
	for _, pair := range pairs {
		if pair.ChainID != chainID {
			continue
		}
		if matchPairAddress {
			if _, ok := expected[pair.PairAddress]; ok {
				result = append(result, pair)
			}
			continue
		}
		_, baseMatch := expected[pair.BaseToken.Address]
		_, quoteMatch := expected[pair.QuoteToken.Address]
		if baseMatch || quoteMatch {
			result = append(result, pair)
		}
	}
	return deduplicatePairs(result), nil
}

func deduplicatePairs(pairs []Pair) []Pair {
	seen := make(map[string]struct{}, len(pairs))
	result := make([]Pair, 0, len(pairs))
	for _, pair := range pairs {
		key := pair.ChainID + ":" + pair.PairAddress
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, pair)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].ChainID == result[j].ChainID {
			return result[i].PairAddress < result[j].PairAddress
		}
		return result[i].ChainID < result[j].ChainID
	})
	return result
}

func clonePairs(pairs []Pair) []Pair {
	if pairs == nil {
		return nil
	}
	result := make([]Pair, len(pairs))
	for index, pair := range pairs {
		result[index] = pair
		result[index].Labels = append([]string(nil), pair.Labels...)
		result[index].Transactions = cloneMap(pair.Transactions)
		result[index].Volume = cloneMap(pair.Volume)
		result[index].PriceChange = cloneMap(pair.PriceChange)
		result[index].Info = append(json.RawMessage(nil), pair.Info...)
		result[index].Boosts = append(json.RawMessage(nil), pair.Boosts...)
	}
	return result
}

func cloneMap[K comparable, V any](source map[K]V) map[K]V {
	if source == nil {
		return nil
	}
	result := make(map[K]V, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
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
	wait := time.Duration(0)
	if l.next.After(now) {
		wait = l.next.Sub(now)
		l.next = l.next.Add(l.interval)
	} else {
		l.next = now.Add(l.interval)
	}
	l.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

var _ Provider = (*DexScreenerClient)(nil)
