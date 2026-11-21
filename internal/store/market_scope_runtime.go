package store

import (
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"sort"
)

func (s *Store) marketMonitoringChainIDs() []int64 {
	if s.marketScopePolicies == nil {
		return nil
	}
	ids := make([]int64, 0, 6)
	seen := make(map[int64]bool, 6)
	for _, policy := range s.marketScopePolicies {
		if policy.MarketProjectLimit < 1 {
			continue
		}
		for _, key := range policy.AllowedMarketChainKeys {
			p, err := chain.ChainProfile(key, "")
			if err == nil && !seen[p.ChainID] {
				ids = append(ids, p.ChainID)
				seen[p.ChainID] = true
			}
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
