package runpod

import (
	"fmt"
	"math/big"
	"strings"
)

const usdMicrosPerUSD int64 = 1_000_000

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
