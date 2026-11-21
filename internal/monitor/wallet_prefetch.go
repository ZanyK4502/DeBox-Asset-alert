package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/plans"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/store"
)

const (
	walletPrefetchWorkers = 2
	walletPrefetchWindow  = 4
	// At most four in-flight/ready balance payloads can be retained in addition
	// to the existing 16 MiB batch memo. These small ABI-backed results never
	// retain receipt lists or raw provider response maps.
	walletPrefetchPayloadBytes = 64 << 10
)

type walletPrefetchSource interface{ WalletBalancePrefetchWorkers() int }

type walletPrefetchRequest struct {
	key                               walletReadKey
	wallet, token, chainKey, fallback string
}

type walletPrefetchResult struct {
	done     chan struct{}
	entry    walletReadEntry
	consumed bool // serial consumer only; workers never read it
	release  func()
}

func (e *Executor) walletPrefetchRequests(rules []store.WatchRule) []walletPrefetchRequest {
	requests := make([]walletPrefetchRequest, 0, min(len(rules), walletReadCacheKeys))
	seen := make(map[walletReadKey]bool)
	for index, rule := range rules {
		if index >= walletReadCacheKeys {
			break
		}
		switch rule.RuleType {
		case plans.BalanceChange, plans.Incoming, plans.Outgoing, plans.BalanceThreshold, plans.HighBalanceThreshold:
		default:
			continue
		}
		plan, err := e.plan(rule.EffectivePlanCode)
		if err != nil || ruleAllowedByPlan(rule, plan) != nil {
			continue
		}
		profile, err := chain.ChainProfile(rule.ChainKey, e.deps.DefaultChainKey)
		if err != nil || int64(rule.ChainID) != profile.ChainID {
			continue
		}
		key, err := walletBalanceKey(rule.WalletAddress, stringValue(rule.TokenAddress), rule.ChainKey, e.deps.DefaultChainKey)
		if err != nil || seen[key] {
			continue
		}
		seen[key] = true
		requests = append(requests, walletPrefetchRequest{key: key, wallet: rule.WalletAddress, token: stringValue(rule.TokenAddress), chainKey: rule.ChainKey, fallback: e.deps.DefaultChainKey})
	}
	return requests
}

// Only an explicitly concurrency-safe source opts in. Old embedders retain
// sequential reads. Two fixed workers stream ahead by at most four unique
// keys, while the caller evaluates and commits in the original rule order.
func (r *walletCycleReads) startBalancePrefetch(ctx context.Context, requests []walletPrefetchRequest) func() {
	source, ok := r.next.(walletPrefetchSource)
	if !ok || source.WalletBalancePrefetchWorkers() < 1 || len(requests) == 0 {
		return func() {}
	}
	workers := min(source.WalletBalancePrefetchWorkers(), walletPrefetchWorkers, len(requests))
	workCtx, cancel := context.WithCancel(ctx)
	credits := make(chan struct{}, walletPrefetchWindow)
	for range walletPrefetchWindow {
		credits <- struct{}{}
	}
	r.prefetch = make(map[walletReadKey]*walletPrefetchResult, len(requests))
	for _, request := range requests {
		var released sync.Once
		r.prefetch[request.key] = &walletPrefetchResult{done: make(chan struct{}), release: func() { released.Do(func() { credits <- struct{}{} }) }}
	}
	var next atomic.Int64
	var requestsStarted, failures atomic.Int64
	var workersDone sync.WaitGroup
	workersDone.Add(workers)
	for range workers {
		go func() {
			defer workersDone.Done()
			for {
				if workCtx.Err() != nil {
					return
				}
				index := int(next.Add(1) - 1)
				if index >= len(requests) {
					return
				}
				select {
				case <-workCtx.Done():
					return
				case <-credits:
				}
				if workCtx.Err() != nil {
					return
				}
				request := requests[index]
				pending := r.prefetch[request.key]
				requestsStarted.Add(1)
				entry := r.readPrefetchedBalance(workCtx, request)
				if entry.err != nil {
					failures.Add(1)
				}
				pending.entry = entry
				close(pending.done)
			}
		}()
	}
	var stopped sync.Once
	return func() {
		stopped.Do(func() {
			cancel()
			workersDone.Wait()
			r.metrics.Requests += int(requestsStarted.Load())
			r.metrics.Failures += int(failures.Load())
			r.prefetch = nil
		})
	}
}

func (r *walletCycleReads) readPrefetchedBalance(ctx context.Context, request walletPrefetchRequest) (entry walletReadEntry) {
	entry.fetchedAt = r.now()
	defer func() {
		if recovered := recover(); recovered != nil {
			entry.data = nil
			entry.err = &walletReadFailure{cause: fmt.Errorf("wallet prefetch source panicked")}
			entry.bytes = len(entry.err.Error())
		}
	}()
	value, err := r.next.Balance(ctx, request.wallet, request.token, request.chainKey, request.fallback)
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && (r.now().Before(entry.fetchedAt) || r.now().Sub(entry.fetchedAt) >= r.maxAge) {
		err = fmt.Errorf("wallet source read exceeded observation age budget")
	}
	if err == nil {
		entry.data, err = json.Marshal(value)
		if err == nil && len(entry.data) > walletPrefetchPayloadBytes {
			err = fmt.Errorf("wallet prefetched balance exceeds bounded payload")
		}
	}
	if err != nil {
		entry.data = nil
		entry.err = &walletReadFailure{cause: err}
		entry.bytes = min(len(err.Error()), walletPrefetchPayloadBytes)
	} else {
		entry.bytes = len(entry.data)
	}
	return entry
}
