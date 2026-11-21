package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

const WalletBalanceReadSource = "nodit_rpc_erc20_balance:v1"

var (
	ErrWalletBalanceSourceUnavailable        = errors.New("wallet balance read source or precision is unavailable")
	ErrWalletBalanceSourceTransitionRequired = errors.New("wallet balance source transition requires an approved recovery boundary")
)

type walletBalanceReadState struct {
	WatchRuleID     int64     `db:"watch_rule_id"`
	ChainID         int64     `db:"chain_id"`
	ChainKey        string    `db:"chain_key"`
	WalletAddress   string    `db:"wallet_address"`
	TokenAddress    string    `db:"token_address"`
	ReadSource      string    `db:"read_source"`
	Decimals        int16     `db:"decimals"`
	BaselineValue   string    `db:"baseline_value"`
	BaselineChecked time.Time `db:"baseline_checked_at"`
	ReadObservedAt  time.Time `db:"read_observed_at"`
}

const walletBalanceReadStateColumns = `watch_rule_id,chain_id,chain_key,wallet_address,token_address,read_source,decimals,baseline_value,baseline_checked_at,read_observed_at`

func walletERC20BalanceRule(rule WatchRule) bool {
	if rule.TokenAddress == nil || strings.TrimSpace(*rule.TokenAddress) == "" {
		return false
	}
	switch rule.RuleType {
	case "balance_change", "incoming", "outgoing", "balance_threshold", "balance_threshold_high":
		return true
	}
	return false
}

func walletBalanceAddress(value string) bool {
	if len(value) != 42 || !strings.HasPrefix(value, "0x") {
		return false
	}
	for _, r := range value[2:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// Validate the represented uint256 without accepting exponent, fraction or
// negative syntax that a successful ERC-20 RPC read cannot produce.
func walletBalanceValueValid(value string, decimals int) bool {
	if len(value) == 0 || len(value) > 335 || decimals < 0 || decimals > 255 {
		return false
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" || len(parts) == 2 && (parts[1] == "" || len(parts[1]) > decimals) {
		return false
	}
	for _, part := range parts {
		for _, c := range part {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	raw := parts[0] + fraction + strings.Repeat("0", decimals-len(fraction))
	n, ok := new(big.Int).SetString(raw, 10)
	return ok && n.Sign() >= 0 && n.BitLen() <= 256
}

func walletBalanceEvidenceValid(rule WatchRule, source string, decimals *int, observedAt *time.Time, value string) bool {
	if !walletERC20BalanceRule(rule) || source != WalletBalanceReadSource || decimals == nil || observedAt == nil || observedAt.IsZero() || !walletBalanceValueValid(value, *decimals) {
		return false
	}
	return rule.ChainID > 0 && len(rule.ChainKey) > 0 && len(rule.ChainKey) <= 32 &&
		walletBalanceAddress(strings.ToLower(strings.TrimSpace(rule.WalletAddress))) &&
		walletBalanceAddress(strings.ToLower(strings.TrimSpace(*rule.TokenAddress)))
}

// The rule row is already locked. This read and all provenance changes share
// its existing short transaction; no network or external write happens here.
func prepareWalletBalanceSource(ctx context.Context, db DBTX, rule WatchRule, params RecordWalletObservationParams) (bool, bool, error) {
	hasEvidence := params.BalanceReadSource != "" || params.BalanceReadDecimals != nil
	if !walletERC20BalanceRule(rule) {
		if hasEvidence {
			return false, false, ErrWalletBalanceSourceUnavailable
		}
		return false, false, nil
	}
	state, err := collectOptional[walletBalanceReadState](ctx, db, `SELECT `+walletBalanceReadStateColumns+` FROM wallet_balance_read_states WHERE watch_rule_id=$1`, rule.ID)
	if err != nil {
		return false, false, fmt.Errorf("read wallet balance provenance: %w", err)
	}
	if !hasEvidence {
		if state != nil {
			return false, false, ErrWalletBalanceSourceUnavailable
		}
		return false, false, nil
	}
	if !walletBalanceEvidenceValid(rule, params.BalanceReadSource, params.BalanceReadDecimals, params.ReadObservedAt, params.CurrentValue) {
		return false, false, ErrWalletBalanceSourceUnavailable
	}
	var now time.Time
	if err := db.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return false, false, err
	}
	observed := params.ReadObservedAt.UTC().Truncate(time.Microsecond)
	if observed.After(now) {
		return false, false, ErrWalletBalanceSourceUnavailable
	}
	if rule.LastValue == nil {
		return true, false, nil
	}
	known := state != nil && state.ChainID == int64(rule.ChainID) &&
		state.ChainKey == strings.ToLower(strings.TrimSpace(rule.ChainKey)) &&
		state.WalletAddress == strings.ToLower(strings.TrimSpace(rule.WalletAddress)) &&
		state.TokenAddress == strings.ToLower(strings.TrimSpace(*rule.TokenAddress)) &&
		state.ReadSource == params.BalanceReadSource && int(state.Decimals) == *params.BalanceReadDecimals &&
		state.BaselineValue == *rule.LastValue && rule.LastCheckedAt != nil && state.BaselineChecked.Equal(rule.LastCheckedAt.UTC().Truncate(time.Microsecond))
	if known {
		if observed.Before(state.ReadObservedAt) {
			return false, false, ErrWalletBalanceSourceUnavailable
		}
		return true, false, nil
	}
	cutoff, err := notificationSourceCutoff(ctx, db)
	if err != nil {
		return false, false, err
	}
	if cutoff == nil || rule.LastCheckedAt == nil || !rule.LastCheckedAt.Before(*cutoff) || observed.Before(*cutoff) {
		return false, false, ErrWalletBalanceSourceTransitionRequired
	}
	previousSource := "legacy_unverified"
	var previousDecimals *int16
	if state != nil {
		previousSource, previousDecimals = state.ReadSource, &state.Decimals
	}
	contextJSON, err := json.Marshal(map[string]any{
		"previous_provenance": state,
		"next_identity": map[string]any{
			"chain_id": rule.ChainID, "chain_key": strings.ToLower(strings.TrimSpace(rule.ChainKey)),
			"wallet_address": strings.ToLower(strings.TrimSpace(rule.WalletAddress)), "token_address": strings.ToLower(strings.TrimSpace(*rule.TokenAddress)),
		},
	})
	if err != nil {
		return false, false, err
	}
	_, err = db.Exec(ctx, `INSERT INTO wallet_balance_source_transitions(watch_rule_id,debox_user_id,previous_source,previous_decimals,previous_value,previous_checked_at,next_source,next_decimals,next_value,cutoff_at,read_observed_at,source_context,reason) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb,'wallet balance source joined at approved recovery boundary; no cross-source alert') ON CONFLICT(watch_rule_id,previous_checked_at,cutoff_at) DO NOTHING`, rule.ID, rule.DeBoxUserID, previousSource, previousDecimals, *rule.LastValue, *rule.LastCheckedAt, params.BalanceReadSource, *params.BalanceReadDecimals, params.CurrentValue, *cutoff, observed, string(contextJSON))
	if err != nil {
		return false, false, fmt.Errorf("record wallet balance source transition: %w", err)
	}
	return true, true, nil
}

func commitWalletBalanceSource(ctx context.Context, db DBTX, rule WatchRule, params RecordWalletObservationParams, checkedAt time.Time) error {
	_, err := db.Exec(ctx, `INSERT INTO wallet_balance_read_states(watch_rule_id,chain_id,chain_key,wallet_address,token_address,read_source,decimals,baseline_value,baseline_checked_at,read_observed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(watch_rule_id) DO UPDATE SET chain_id=EXCLUDED.chain_id,chain_key=EXCLUDED.chain_key,wallet_address=EXCLUDED.wallet_address,token_address=EXCLUDED.token_address,read_source=EXCLUDED.read_source,decimals=EXCLUDED.decimals,baseline_value=EXCLUDED.baseline_value,baseline_checked_at=EXCLUDED.baseline_checked_at,read_observed_at=EXCLUDED.read_observed_at,updated_at=clock_timestamp()`, rule.ID, rule.ChainID, strings.ToLower(strings.TrimSpace(rule.ChainKey)), strings.ToLower(strings.TrimSpace(rule.WalletAddress)), strings.ToLower(strings.TrimSpace(*rule.TokenAddress)), params.BalanceReadSource, *params.BalanceReadDecimals, params.CurrentValue, checkedAt, params.ReadObservedAt.UTC().Truncate(time.Microsecond))
	if err != nil {
		return fmt.Errorf("commit wallet balance provenance: %w", err)
	}
	return nil
}
