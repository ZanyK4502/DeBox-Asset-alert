package marketcollector

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketprotocol"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const (
	discoveryProjectBatch     = 200
	discoveryMaxPairsPerToken = 512
	discoveryMaxPoolFacts     = 1024
	discoveryMaxLinks         = 1000
	discoveryMaxCacheBytes    = 8 << 20 // serialized candidates; fixed maps separately bounded above
	discoveryTaskLifetime     = 45 * time.Second
)

var ErrDiscoveryCapacity = errors.New("market discovery batch capacity exceeded")

type discoveryProgress struct {
	mu      sync.Mutex
	after   store.MarketDiscoveryCursor
	pending bool
}

type discoveryPager interface {
	ListMarketDiscoveryProjectsPage(context.Context, int64, store.MarketDiscoveryCursor, int) ([]store.MarketProject, error)
}

type discoveryTokenResult struct {
	pairs []marketdata.Pair
	err   error
}

type discoveryPoolResult struct {
	id  int64
	err error
}

type discoveryFactKey struct {
	chainID int64
	pool    string
	version int
}

func (service *Service) discoveryPending() bool {
	service.discovery.mu.Lock()
	defer service.discovery.mu.Unlock()
	return service.discovery.pending
}

// A task retains at most 201 project rows, 200 token results, 1024 pool facts,
// 1000 upsert results, 2248 decimal values and 8 MiB serialized candidate data.
// Task maps die on return (also after the45s deadline). Only the keyset cursor
// and a separate bounded15-minute cache of factory-bound immutable pool facts
// survive. No quote or project classification is retained across tasks.
func (service *Service) discoverPools(ctx context.Context) error {
	return service.discoverPoolsWithin(ctx, discoveryTaskLifetime)
}

func (service *Service) discoverPoolsWithin(outerCtx context.Context, lifetime time.Duration) error {
	service.discovery.mu.Lock()
	defer service.discovery.mu.Unlock()
	ctx, cancel := context.WithTimeout(outerCtx, lifetime)
	defer cancel()
	service.discovery.pending = true // query/cancellation failures remain due
	projects, paged, err := service.discoveryProjects(ctx, service.discovery.after)
	if err != nil {
		return err
	}
	more := len(projects) > discoveryProjectBatch
	if more {
		projects = projects[:discoveryProjectBatch] // bounded SQL lookahead, never an unbounded load
	}
	knownDecimals := make(map[string]int, 2*discoveryMaxPoolFacts)
	for _, project := range projects {
		if token, err := chain.ValidateAddress(project.TokenAddress); err == nil {
			knownDecimals[token] = int(project.TokenDecimals)
		}
	}
	tokens := make(map[string]discoveryTokenResult)
	facts := make(map[discoveryFactKey]marketprotocol.PoolFacts)
	upserts := make(map[[32]byte]discoveryPoolResult)
	decimalFailures := make(map[string]error)
	bytesUsed, links := 0, 0
	var failures []error // at most one per token plus bounded candidate/link failures
	interrupted := func(project store.MarketProject) error {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) && outerCtx.Err() == nil && paged {
			// A slow token must not pin the start of every subsequent batch.
			// This project remains a reported failure and is retried next
			// traversal; its already-created links/selection stay intact.
			service.discovery.after = discoveryCursor(project)
		}
		return errors.Join(append(failures, ctx.Err())...)
	}
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		token := strings.ToLower(project.TokenAddress)
		result, exists := tokens[token]
		if !exists {
			started := time.Now()
			result.pairs, result.err = service.market.DiscoverPools(ctx, service.settings.ChainKey, token)
			_ = service.recordHealth(ctx, "dexscreener", "pool_discovery", started, result.err, nil)
			if err := ctx.Err(); err != nil {
				return interrupted(project)
			}
			if result.err == nil {
				if len(result.pairs) > discoveryMaxPairsPerToken {
					result.err = fmt.Errorf("%w: more than %d pools for one token", ErrDiscoveryCapacity, discoveryMaxPairsPerToken)
				} else {
					body, encodeErr := json.Marshal(result.pairs)
					if encodeErr != nil {
						result.err = encodeErr
					} else if len(body) > discoveryMaxCacheBytes {
						result.err = fmt.Errorf("%w: token candidate bytes", ErrDiscoveryCapacity)
					} else if bytesUsed+len(body) > discoveryMaxCacheBytes {
						// The next fresh bounded task can handle this token. Do not
						// move its cursor or keep the response beyond this task.
						return errors.Join(append(failures, ErrDiscoveryCapacity)...)
					} else {
						bytesUsed += len(body)
					}
				}
			}
			if result.err != nil {
				result.pairs = nil
				failures = append(failures, fmt.Errorf("discover pools for token: %w", result.err))
			} else {
				// Copy before sorting: the Provider may retain its own cache slice.
				result.pairs = append([]marketdata.Pair(nil), result.pairs...)
				sort.SliceStable(result.pairs, func(i, j int) bool {
					return decimalGreater(result.pairs[i].Liquidity.USD.String(), result.pairs[j].Liquidity.USD.String())
				})
			}
			tokens[token] = result
		}
		if result.err == nil {
			if links+len(result.pairs) > discoveryMaxLinks || len(facts)+len(result.pairs) > discoveryMaxPoolFacts {
				return errors.Join(append(failures, ErrDiscoveryCapacity)...)
			}
			for _, pair := range result.pairs {
				if err := ctx.Err(); err != nil {
					return interrupted(project)
				}
				key := discoveryFactKey{service.settings.ChainID, strings.ToLower(pair.PairAddress), marketprotocol.PoolFactsVersion}
				links++ // rejected input also consumes the bounded work budget
				// Never allow a foreign-chain candidate to populate a local fact key.
				if chain.NormalizeChainKey(pair.ChainID, "") != service.settings.ChainKey {
					failures = append(failures, fmt.Errorf("discovered pool belongs to another chain"))
					continue
				}
				poolFacts, ok := facts[key]
				if !ok {
					poolFacts = service.readDiscoveryPoolFacts(ctx, pair)
					facts[key] = poolFacts // failures stay failures, never zero/verified
				}
				if err := service.persistDiscoveredPairFacts(ctx, project, pair, poolFacts, knownDecimals, upserts, decimalFailures); err != nil {
					failures = append(failures, err)
					// Do not select a lower-liquidity pool merely because the
					// first candidate's metadata or write failed. Other projects
					// and tokens still continue independently.
					break
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return interrupted(project)
		}
		if paged {
			service.discovery.after = discoveryCursor(project)
		}
	}
	if !more {
		service.discovery.after = store.MarketDiscoveryCursor{}
		service.discovery.pending = false
	}
	return errors.Join(failures...)
}

func (service *Service) discoveryTokenDecimals(ctx context.Context, address string, known map[string]int, failures map[string]error) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	key, err := chain.ValidateAddress(address)
	if err != nil {
		return 0, err
	}
	if err, ok := failures[key]; ok {
		return 0, err
	}
	value, err := service.resolveTokenDecimals(ctx, key, known)
	if err != nil && ctx.Err() == nil && failures != nil {
		failures[key] = err // failure is not inserted into the verified-value map
	}
	return value, err
}

func discoveryCursor(project store.MarketProject) store.MarketDiscoveryCursor {
	value := store.MarketDiscoveryCursor{TokenAddress: project.TokenAddress, ProjectID: project.ID}
	if project.MarketProjectDeploymentID != nil {
		value.ProjectDeploymentID = *project.MarketProjectDeploymentID
	}
	return value
}

func (service *Service) discoveryProjects(ctx context.Context, after store.MarketDiscoveryCursor) ([]store.MarketProject, bool, error) {
	if pager, ok := service.repository.(discoveryPager); ok {
		projects, err := pager.ListMarketDiscoveryProjectsPage(ctx, service.settings.ChainID, after, discoveryProjectBatch+1)
		if len(projects) > discoveryProjectBatch+1 {
			return nil, true, ErrDiscoveryCapacity
		}
		return projects, true, err
	}
	// Compatibility for old small repositories/test doubles only. Production
	// Store always uses the keyset query; a full legacy page is not truncated.
	projects, err := service.repository.ListActiveMarketProjectsForCollection(ctx, service.settings.ChainID, discoveryProjectBatch+1)
	if len(projects) > discoveryProjectBatch {
		return nil, false, fmt.Errorf("%w: repository needs keyset discovery paging", ErrDiscoveryCapacity)
	}
	return projects, false, err
}

func (service *Service) upsertDiscoveredPool(ctx context.Context, params store.UpsertMarketPoolParams, cache map[[32]byte]discoveryPoolResult) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	// Include all immutable identity, parser and exact provider evidence fields.
	// SeenAt is only bookkeeping; omitting it never changes quote fetchedAt.
	fingerprintParams := params
	fingerprintParams.SeenAt = time.Time{}
	body, err := json.Marshal(fingerprintParams)
	if err != nil {
		return 0, err
	}
	key := sha256.Sum256(body)
	if cached, ok := cache[key]; ok {
		return cached.id, cached.err
	}
	pool, err := service.repository.UpsertMarketPool(ctx, params)
	if cache != nil {
		cache[key] = discoveryPoolResult{id: pool.ID, err: err}
	}
	return pool.ID, err
}
