package chain

import (
	"math/big"
	"strings"
	"testing"
)

func TestChainProfilesAndAliases(t *testing.T) {
	profile, err := ChainProfile("BNB_CHAIN", "")
	if err != nil {
		t.Fatalf("ChainProfile() error = %v", err)
	}
	if profile.Key != "bsc" || profile.Chain != "bnb" || profile.ChainID != 56 || profile.NativeSymbol != "BNB" {
		t.Fatalf("unexpected BSC profile: %#v", profile)
	}

	supported := SupportedChains()
	if len(supported) != 6 {
		t.Fatalf("SupportedChains() length = %d", len(supported))
	}
	if supported[0].Key != "bsc" || supported[5].Key != "optimism" {
		t.Fatalf("unexpected supported chain order: %#v", supported)
	}
	if _, err := ChainProfile("solana", ""); err == nil || !strings.Contains(err.Error(), "supported chains") {
		t.Fatalf("unsupported chain error = %v", err)
	}
}

func TestAddressAndTransactionHashValidation(t *testing.T) {
	address, err := ValidateAddress(" 0xABCDEFabcdefABCDEFabcdefABCDEFabcdefABCD ")
	if err != nil {
		t.Fatalf("ValidateAddress() error = %v", err)
	}
	if address != "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd" {
		t.Fatalf("normalized address = %q", address)
	}
	hash, err := ValidateTransactionHash("0x" + strings.Repeat("AB", 32))
	if err != nil {
		t.Fatalf("ValidateTransactionHash() error = %v", err)
	}
	if hash != "0x"+strings.Repeat("ab", 32) {
		t.Fatalf("normalized transaction hash = %q", hash)
	}
	if _, err := ValidateAddress("0x1234"); err == nil {
		t.Fatal("ValidateAddress() error = nil")
	}
	if _, err := ValidateTransactionHash("0x1234"); err == nil {
		t.Fatal("ValidateTransactionHash() error = nil")
	}
}

func TestUnitFormattingAndTransferEncoding(t *testing.T) {
	value, err := FormatUnits("1234500000000000000", 18)
	if err != nil {
		t.Fatalf("FormatUnits() error = %v", err)
	}
	if value != "1.2345" {
		t.Fatalf("FormatUnits() = %q", value)
	}
	units, err := AmountToUnits("12.345678", 6)
	if err != nil {
		t.Fatalf("AmountToUnits() error = %v", err)
	}
	if units.Cmp(big.NewInt(12345678)) != 0 {
		t.Fatalf("AmountToUnits() = %s", units)
	}
	if _, err := AmountToUnits("1.0000001", 6); err == nil {
		t.Fatal("AmountToUnits() accepted excess decimal places")
	}

	address := "0x1111111111111111111111111111111111111111"
	encoded, err := EncodeERC20Transfer(address, big.NewInt(25))
	if err != nil {
		t.Fatalf("EncodeERC20Transfer() error = %v", err)
	}
	want := "0xa9059cbb" + strings.Repeat("0", 24) + strings.Repeat("1", 40) + strings.Repeat("0", 62) + "19"
	if encoded != want {
		t.Fatalf("EncodeERC20Transfer() = %q, want %q", encoded, want)
	}
}
