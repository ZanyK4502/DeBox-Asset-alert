package marketrules

import "github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"

// Filter before loading/initializing target state, so an excluded historical
// chain keeps its conditions, cooldown and processing baseline unchanged.
func (service *Service) marketChainAllowed(key string, id int64) bool {
	if service.settings.MonitoringChainKeys == nil {
		return true
	}
	profile, err := chain.ChainProfile(key, "")
	if err != nil || profile.ChainID != id {
		return false
	}
	for _, allowed := range service.settings.MonitoringChainKeys {
		p, e := chain.ChainProfile(allowed, "")
		if e == nil && p.Key == profile.Key {
			return true
		}
	}
	return false
}
