package chain

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestDecodeABIStringSupportsDynamicAndBytes32(t *testing.T) {
	dynamic := make([]byte, 96)
	dynamic[31] = 32
	dynamic[63] = 5
	copy(dynamic[64:], []byte("TOKEN"))
	value, err := decodeABIString("0x" + hex.EncodeToString(dynamic))
	if err != nil {
		t.Fatalf("decode dynamic string: %v", err)
	}
	if value != "TOKEN" {
		t.Fatalf("dynamic value = %q", value)
	}

	bytes32 := append([]byte("USDT"), make([]byte, 28)...)
	value, err = decodeABIString("0x" + hex.EncodeToString(bytes32))
	if err != nil {
		t.Fatalf("decode bytes32 string: %v", err)
	}
	if value != "USDT" {
		t.Fatalf("bytes32 value = %q", value)
	}
}

func TestDecodeABIStringRejectsOutOfBoundsLength(t *testing.T) {
	raw := make([]byte, 64)
	raw[31] = 32
	raw[63] = 64
	if _, err := decodeABIString("0x" + hex.EncodeToString(raw)); err == nil ||
		!strings.Contains(err.Error(), "length") {
		t.Fatalf("error = %v, want invalid length", err)
	}
}

func TestDecodeABIUintPreservesFullPrecision(t *testing.T) {
	value, err := decodeABIUint(
		"0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
	)
	if err != nil {
		t.Fatalf("decode uint: %v", err)
	}
	if value.BitLen() != 256 {
		t.Fatalf("bit length = %d, want 256", value.BitLen())
	}
}
