package marketcollector

import (
	"context"
	"fmt"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/chain"
	"github.com/ZanyK4502/DeBox-Asset-alert/internal/marketparse"
)

// Verified project metadata in known takes precedence. Successful RPC precision
// may also be reused briefly across this service's tasks, never pool defaults.
func (service *Service) resolveTokenDecimals(ctx context.Context, tokenAddress string, known map[string]int) (int, error) {
	token, err := chain.ValidateAddress(tokenAddress)
	if err != nil {
		return 0, err
	}
	if decimals, ok := known[token]; ok {
		if decimals < 0 || decimals > 255 {
			return 0, fmt.Errorf("invalid known token decimals")
		}
		return decimals, nil
	}
	var decimals int
	if token == marketparse.NativeTokenAddress {
		decimals = 18
	} else {
		reader := service.chain
		if tokenDecimalsReaderNil(reader) {
			return 0, fmt.Errorf("token decimals reader is unavailable")
		}
		// Capture the reader and resolved profile for this call. A later flight
		// completion must never look up a replacement service.chain.
		key, fallback := service.settings.ChainKey, service.settings.ChainFallback
		profile, profileErr := chain.ChainProfile(key, fallback)
		if profileErr == nil {
			key, fallback = profile.Key, ""
		}
		read := func(ctx context.Context) (int32, error) {
			return reader.TokenDecimals(ctx, token, key, fallback)
		}
		source, cacheable := tokenDecimalsCacheSource(reader)
		if cacheable && profileErr == nil && profile.ChainID == service.settings.ChainID {
			decimals, err = service.tokenDecimals.read(ctx, source, tokenDecimalsKey{
				chainID: profile.ChainID, chainKey: profile.Key, token: token,
			}, service.now, read)
		} else {
			// Unknown/value identities retain the original direct-read path.
			decimals, err = readCheckedTokenDecimals(ctx, read)
		}
		if err != nil {
			return 0, fmt.Errorf("resolve token decimals: %w", err)
		}
	}
	if known != nil {
		known[token] = decimals
	}
	return decimals, nil
}
