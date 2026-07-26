package marketdata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"strings"
)

// Decimal preserves the exact representation supplied by an upstream market
// provider. An empty Decimal represents a JSON null or an absent field.
type Decimal string

func (d Decimal) String() string {
	return string(d)
}

func (d Decimal) Valid() bool {
	return d != ""
}

func (d *Decimal) UnmarshalJSON(data []byte) error {
	value := strings.TrimSpace(string(data))
	if value == "" || value == "null" {
		*d = ""
		return nil
	}
	if len(value) >= 2 && value[0] == '"' {
		var decoded string
		if err := json.Unmarshal(data, &decoded); err != nil {
			return fmt.Errorf("decode decimal: %w", err)
		}
		value = strings.TrimSpace(decoded)
		if value == "" {
			*d = ""
			return nil
		}
	}
	if _, _, err := big.ParseFloat(value, 10, 256, big.ToNearestEven); err != nil {
		return fmt.Errorf("invalid decimal %q: %w", value, err)
	}
	*d = Decimal(value)
	return nil
}

func (d Decimal) MarshalJSON() ([]byte, error) {
	if !d.Valid() {
		return []byte("null"), nil
	}
	return json.Marshal(d.String())
}

func decodeJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON data")
		}
		return err
	}
	return nil
}
