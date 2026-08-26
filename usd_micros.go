package runpod

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

const usdMicrosPerUSD int64 = 1_000_000

var jsonDecimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

// parseJSONUSDMicros accepts the two exact currency representations RunPod
// returns: a JSON number or a quoted JSON decimal. Null remains absence.
func parseJSONUSDMicros(raw json.RawMessage) (int64, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return 0, fmt.Errorf("USD decimal is absent")
	}
	if strings.HasPrefix(trimmed, `"`) {
		var quoted string
		if err := json.Unmarshal(raw, &quoted); err != nil {
			return 0, fmt.Errorf("invalid quoted USD decimal: %w", err)
		}
		trimmed = quoted
	}
	if !jsonDecimalPattern.MatchString(trimmed) {
		return 0, fmt.Errorf("invalid USD decimal %q", trimmed)
	}
	return parseUSDMicros(trimmed)
}

// parseUSDMicros converts one exact provider JSON decimal to integer USD
// micros. Sub-micro values and int64 overflow refuse; the SDK never rounds or
// converts provider money through binary floating point.
func parseUSDMicros(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	value, ok := new(big.Rat).SetString(raw)
	if !ok {
		return 0, fmt.Errorf("invalid USD decimal %q", raw)
	}
	value.Mul(value, big.NewRat(usdMicrosPerUSD, 1))
	if !value.IsInt() {
		return 0, fmt.Errorf("USD decimal %q has sub-micro precision", raw)
	}
	if !value.Num().IsInt64() {
		return 0, fmt.Errorf("USD decimal %q exceeds int64 USD micros", raw)
	}
	return value.Num().Int64(), nil
}
