package marketcollector

import (
	"context"
	"fmt"
	"sort"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
)

// Cluster routes chain-scoped webhook deliveries and exposes the enabled
// collectors in deterministic order. Each Service retains its own cursor,
// confirmation depth, task locks and recovery loop.
type Cluster struct {
	services map[string]*Service
	ordered  []*Service
	fallback string
}

func NewCluster(services []*Service, fallback string) (*Cluster, error) {
	cluster := &Cluster{
		services: make(map[string]*Service, len(services)),
	}
	if profile, err := chain.ChainProfile(fallback, "bsc"); err == nil {
		cluster.fallback = profile.Key
	} else {
		return nil, err
	}
	for _, service := range services {
		if service == nil || !service.Enabled() {
			continue
		}
		profile, err := chain.ChainProfile(service.settings.ChainKey, "")
		if err != nil || profile.ChainID != service.settings.ChainID {
			return nil, fmt.Errorf("%w: %s", ErrUnsupportedChain, service.settings.ChainKey)
		}
		if _, exists := cluster.services[profile.Key]; exists {
			return nil, fmt.Errorf("duplicate market collector chain: %s", profile.Key)
		}
		cluster.services[profile.Key] = service
	}
	keys := make([]string, 0, len(cluster.services))
	for key := range cluster.services {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		cluster.ordered = append(cluster.ordered, cluster.services[key])
	}
	return cluster, nil
}

func (cluster *Cluster) Services() []*Service {
	if cluster == nil {
		return nil
	}
	return append([]*Service(nil), cluster.ordered...)
}

func (cluster *Cluster) AcceptWebhookForChain(
	ctx context.Context,
	chainKey string,
	category string,
	headers map[string][]string,
	rawBody []byte,
) (WebhookAcceptance, error) {
	if cluster == nil || len(cluster.services) == 0 {
		return WebhookAcceptance{}, ErrCollectorDisabled
	}
	if chainKey == "" {
		chainKey = cluster.fallback
	}
	profile, err := chain.ChainProfile(chainKey, "")
	if err != nil {
		return WebhookAcceptance{}, ErrUnsupportedChain
	}
	service := cluster.services[profile.Key]
	if service == nil {
		return WebhookAcceptance{}, ErrUnsupportedChain
	}
	return service.AcceptWebhook(ctx, category, headers, rawBody)
}
