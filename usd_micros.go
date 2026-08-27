package runpod

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

const usdMicrosPerUSD int64 = 1_000_000

var jsonDecimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

type usdMicrosParseErrorKind uint8

const (
	usdMicrosSubmicro usdMicrosParseErrorKind = iota + 1
	usdMicrosOverflow
)

// usdMicrosParseError keeps the exact-money refusal class available to other
// SDK wire decoders without changing the public error text.
type usdMicrosParseError struct {
	kind    usdMicrosParseErrorKind
	message string
}

func (e *usdMicrosParseError) Error() string { return e.message }

// USDMicrosPerHour is an exact hourly USD rate. Its JSON representation is
// RunPod's dollar decimal, never the integer micros held by Go callers.
type USDMicrosPerHour int64

func (r *USDMicrosPerHour) UnmarshalJSON(raw []byte) error {
	micros, err := parseJSONUSDMicros(raw)
	if err != nil {
		return err
	}
	if micros < 0 {
		return fmt.Errorf("USD rate cannot be negative")
	}
	*r = USDMicrosPerHour(micros)
	return nil
}

func (r USDMicrosPerHour) MarshalJSON() ([]byte, error) {
	micros := int64(r)
	if micros < 0 {
		return nil, fmt.Errorf("USD rate cannot be negative")
	}
	whole, fraction := micros/usdMicrosPerUSD, micros%usdMicrosPerUSD
	if fraction == 0 {
		return []byte(strconv.FormatInt(whole, 10)), nil
	}
	return []byte(strconv.FormatInt(whole, 10) + "." +
		strings.TrimRight(fmt.Sprintf("%06d", fraction), "0")), nil
}

// parseJSONUSDMicros accepts the two exact currency representations RunPod
// returns: a JSON number or a quoted JSON decimal. Null remains absence.
func parseJSONUSDMicros(raw json.RawMessage) (int64, error) {
	trimmed, err := parseJSONUSDDecimal(raw)
	if err != nil {
		return 0, err
	}
	return parseUSDMicros(trimmed)
}

// parseJSONUSDMicrosFloor is balance-only: RunPod reports account credits
// beyond micro-USD precision. Flooring preserves a conservative purchasing
// bound, while prices and billing records continue to require exact micros.
func parseJSONUSDMicrosFloor(raw json.RawMessage) (int64, error) {
	trimmed, err := parseJSONUSDDecimal(raw)
	if err != nil {
		return 0, err
	}
	value, ok := new(big.Rat).SetString(trimmed)
	if !ok {
		return 0, fmt.Errorf("invalid USD decimal %q", trimmed)
	}
	value.Mul(value, big.NewRat(usdMicrosPerUSD, 1))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(value.Num(), value.Denom(), remainder)
	if value.Sign() < 0 && remainder.Sign() != 0 {
		quotient.Sub(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() {
		return 0, &usdMicrosParseError{
			kind:    usdMicrosOverflow,
			message: fmt.Sprintf("USD decimal %q exceeds int64 USD micros", trimmed),
		}
	}
	return quotient.Int64(), nil
}

func parseJSONUSDDecimal(raw json.RawMessage) (string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", fmt.Errorf("USD decimal is absent")
	}
	if strings.HasPrefix(trimmed, `"`) {
		var quoted string
		if err := json.Unmarshal(raw, &quoted); err != nil {
			return "", fmt.Errorf("invalid quoted USD decimal: %w", err)
		}
		trimmed = quoted
	}
	if !jsonDecimalPattern.MatchString(trimmed) {
		return "", fmt.Errorf("invalid USD decimal %q", trimmed)
	}
	return trimmed, nil
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
		return 0, &usdMicrosParseError{
			kind:    usdMicrosSubmicro,
			message: fmt.Sprintf("USD decimal %q has sub-micro precision", raw),
		}
	}
	if !value.Num().IsInt64() {
		return 0, &usdMicrosParseError{
			kind:    usdMicrosOverflow,
			message: fmt.Sprintf("USD decimal %q exceeds int64 USD micros", raw),
		}
	}
	return value.Num().Int64(), nil
}
