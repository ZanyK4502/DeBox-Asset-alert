package marketparse

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"golang.org/x/crypto/sha3"
)

func Topic(signature string) string {
	hasher := sha3.NewLegacyKeccak256()
	_, _ = hasher.Write([]byte(signature))
	return "0x" + hex.EncodeToString(hasher.Sum(nil))
}

func decodeHex(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "0x") || len(value)%2 != 0 {
		return nil, fmt.Errorf("hex value must have 0x prefix and even length")
	}
	result, err := hex.DecodeString(value[2:])
	if err != nil {
		return nil, fmt.Errorf("invalid hex")
	}
	return result, nil
}

func words(data string, minimum int) ([][]byte, error) {
	raw, err := decodeHex(data)
	if err != nil {
		return nil, err
	}
	if len(raw)%32 != 0 || len(raw)/32 < minimum {
		return nil, fmt.Errorf("ABI data has %d bytes; need at least %d words", len(raw), minimum)
	}
	result := make([][]byte, 0, len(raw)/32)
	for offset := 0; offset < len(raw); offset += 32 {
		result = append(result, raw[offset:offset+32])
	}
	return result, nil
}

func exactWords(data string, count int) ([][]byte, error) {
	result, err := words(data, count)
	if err != nil {
		return nil, err
	}
	if len(result) != count {
		return nil, fmt.Errorf("ABI data has %d words; need exactly %d", len(result), count)
	}
	return result, nil
}

func validateWordArray(data []byte, offsetWord []byte) error {
	offsetValue := unsigned(offsetWord)
	if !offsetValue.IsUint64() {
		return fmt.Errorf("array offset overflow")
	}
	offset := offsetValue.Uint64()
	if offset%32 != 0 || offset > uint64(len(data)) || uint64(len(data))-offset < 32 {
		return fmt.Errorf("invalid array offset")
	}
	lengthValue := unsigned(data[offset : offset+32])
	if !lengthValue.IsUint64() {
		return fmt.Errorf("array length overflow")
	}
	length := lengthValue.Uint64()
	start := offset + 32
	if start > uint64(len(data)) || length > (uint64(len(data))-start)/32 {
		return fmt.Errorf("invalid array length")
	}
	return nil
}

func unsigned(word []byte) *big.Int {
	return new(big.Int).SetBytes(word)
}

func signed(word []byte) *big.Int {
	value := new(big.Int).SetBytes(word)
	if len(word) > 0 && word[0]&0x80 != 0 {
		modulus := new(big.Int).Lsh(big.NewInt(1), uint(len(word)*8))
		value.Sub(value, modulus)
	}
	return value
}

func wordAddress(word []byte) (string, error) {
	if len(word) != 32 {
		return "", fmt.Errorf("address word must be 32 bytes")
	}
	for _, value := range word[:12] {
		if value != 0 {
			return "", fmt.Errorf("address word has non-zero padding")
		}
	}
	return "0x" + hex.EncodeToString(word[12:]), nil
}

func topicAddress(topic string) (string, error) {
	raw, err := decodeHex(topic)
	if err != nil || len(raw) != 32 {
		return "", fmt.Errorf("invalid indexed address")
	}
	return wordAddress(raw)
}

func dynamicString(data []byte, offsetWord []byte) (string, error) {
	offsetValue := unsigned(offsetWord)
	if !offsetValue.IsUint64() {
		return "", fmt.Errorf("string offset overflow")
	}
	offset := offsetValue.Uint64()
	if offset%32 != 0 || offset > uint64(len(data)) || uint64(len(data))-offset < 32 {
		return "", fmt.Errorf("invalid string offset")
	}
	lengthValue := unsigned(data[offset : offset+32])
	if !lengthValue.IsUint64() {
		return "", fmt.Errorf("string length overflow")
	}
	length := lengthValue.Uint64()
	start := offset + 32
	if start > uint64(len(data)) || length > uint64(len(data))-start {
		return "", fmt.Errorf("invalid string length")
	}
	return string(data[start : start+length]), nil
}

func exactOppositeDeltas(amount0, amount1 *big.Int) bool {
	return amount0.Sign() != 0 && amount1.Sign() != 0 && amount0.Sign() != amount1.Sign()
}

func cloneInt(value *big.Int) *big.Int {
	if value == nil {
		return nil
	}
	return new(big.Int).Set(value)
}

func absolute(value *big.Int) *big.Int {
	return new(big.Int).Abs(value)
}
