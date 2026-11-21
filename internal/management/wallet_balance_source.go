package management

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
)

var walletBalanceValuePattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)

// Creation must seed the same source as subsequent wallet observations. This
// evidence accompanies the initial value into the rule creation transaction;
// it is never inferred from the later database creation timestamp.
func walletBalanceCreationEvidence(value chain.BalanceResult, requestedWallet, requestedToken, chainKey string, readAt time.Time) (string, *int, *time.Time, error) {
	if strings.TrimSpace(requestedToken) == "" {
		return "", nil, nil, nil
	}
	token, err := chain.ValidateAddress(requestedToken)
	if err != nil {
		return "", nil, nil, err
	}
	wallet, err := chain.ValidateAddress(requestedWallet)
	if err != nil {
		return "", nil, nil, err
	}
	profile, err := chain.ChainProfile(chainKey, "")
	if err != nil {
		return "", nil, nil, err
	}
	if value.TokenAddress == nil || !strings.EqualFold(*value.TokenAddress, token) ||
		!strings.EqualFold(value.WalletAddress, wallet) || value.ChainID != profile.ChainID ||
		chain.NormalizeChainKey(value.ChainKey, "") != profile.Key ||
		value.ReadSource != chain.WalletBalanceReadSource || value.ReadDecimals == nil ||
		*value.ReadDecimals < 0 || *value.ReadDecimals > 255 || value.Decimals != *value.ReadDecimals {
		return "", nil, nil, errors.New("ERC-20 wallet balance source or precision unavailable")
	}
	if len(value.Value) > 512 || !walletBalanceValuePattern.MatchString(value.Value) {
		return "", nil, nil, errors.New("invalid ERC-20 wallet balance")
	}
	units, err := chain.AmountToUnits(value.Value, value.Decimals)
	if err != nil || units.Sign() < 0 || units.BitLen() > 256 {
		return "", nil, nil, errors.New("invalid ERC-20 wallet balance")
	}
	decimals := *value.ReadDecimals
	at := readAt.UTC().Truncate(time.Microsecond)
	return value.ReadSource, &decimals, &at, nil
}
