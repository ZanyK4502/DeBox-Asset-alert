package assetcatalog

import (
	"math/big"
	"strings"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
)

func normalizeTokenMetadata(
	expectedAddress string,
	metadata chain.TokenMetadata,
) (chain.TokenMetadata, bool) {
	address, err := chain.ValidateAddress(metadata.Address)
	if err != nil || address != expectedAddress {
		return chain.TokenMetadata{}, false
	}
	symbol := strings.ToUpper(strings.TrimSpace(metadata.Symbol))
	if symbol == "" || metadata.Decimals < 0 || metadata.Decimals > 255 {
		return chain.TokenMetadata{}, false
	}
	totalSupply := new(big.Int)
	if _, ok := totalSupply.SetString(
		strings.TrimSpace(metadata.TotalSupplyRaw),
		10,
	); !ok || totalSupply.Sign() < 0 {
		return chain.TokenMetadata{}, false
	}
	name := strings.TrimSpace(metadata.Name)
	if name == "" {
		name = symbol
	}
	return chain.TokenMetadata{
		Address:        address,
		Name:           name,
		Symbol:         symbol,
		Decimals:       metadata.Decimals,
		TotalSupplyRaw: totalSupply.String(),
	}, true
}
