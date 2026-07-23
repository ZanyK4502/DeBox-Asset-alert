package chain

import (
	"fmt"
	"math/big"
	"strings"
)

const transferSelector = "a9059cbb"

func FormatUnits(raw string, decimals int) (string, error) {
	if decimals < 0 {
		return "", fmt.Errorf("decimals must not be negative")
	}
	value, ok := new(big.Rat).SetString(strings.TrimSpace(raw))
	if !ok {
		return "", fmt.Errorf("invalid numeric value: %q", raw)
	}
	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	value.Quo(value, new(big.Rat).SetInt(divisor))
	text := value.FloatString(decimals)
	if strings.Contains(text, ".") {
		text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	}
	if text == "" || text == "-0" {
		return "0", nil
	}
	return text, nil
}

func AmountToUnits(value string, decimals int) (*big.Int, error) {
	if decimals < 0 {
		return nil, fmt.Errorf("decimals must not be negative")
	}
	amount, ok := new(big.Rat).SetString(strings.TrimSpace(value))
	if !ok {
		return nil, fmt.Errorf("invalid payment amount: %q", value)
	}
	multiplier := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	amount.Mul(amount, new(big.Rat).SetInt(multiplier))
	if !amount.IsInt() {
		return nil, fmt.Errorf("payment amount has too many decimal places")
	}
	return new(big.Int).Set(amount.Num()), nil
}

func EncodeERC20Transfer(recipientAddress string, amountUnits *big.Int) (string, error) {
	recipient, err := ValidateAddress(recipientAddress)
	if err != nil {
		return "", err
	}
	if amountUnits == nil || amountUnits.Sign() < 0 {
		return "", fmt.Errorf("amount units must be non-negative")
	}
	return "0x" + transferSelector +
		fmt.Sprintf("%064s", recipient[2:]) +
		fmt.Sprintf("%064x", amountUnits), nil
}
