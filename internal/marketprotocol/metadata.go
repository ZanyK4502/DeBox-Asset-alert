package marketprotocol

import (
	"encoding/json"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketdata"
)

// EncodePairMetadata keeps the provider pair fields at the top level for
// backward compatibility and adds the exact monitoring classification used by
// the UI after a pool has been persisted.
func EncodePairMetadata(
	pair marketdata.Pair,
	monitoringLevel string,
	unsupportedReason string,
	supportedFeatures []string,
	factoryAddress string,
) (json.RawMessage, error) {
	return json.Marshal(struct {
		marketdata.Pair
		MonitoringLevel   string   `json:"monitoring_level"`
		UnsupportedReason string   `json:"unsupported_reason,omitempty"`
		SupportedFeatures []string `json:"supported_features,omitempty"`
		FactoryAddress    string   `json:"factory_address,omitempty"`
	}{
		Pair:              pair,
		MonitoringLevel:   monitoringLevel,
		UnsupportedReason: unsupportedReason,
		SupportedFeatures: supportedFeatures,
		FactoryAddress:    factoryAddress,
	})
}
