package runpod

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"
)

func TestParseJSONUSDMicros(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int64
	}{
		{name: "number", raw: `0.123456`, want: 123456},
		{name: "quoted decimal", raw: `"0.74"`, want: 740000},
		{name: "present zero", raw: `0`, want: 0},
		{name: "exponent", raw: `"1e-6"`, want: 1},
		{name: "int64 maximum", raw: `"9223372036854.775807"`, want: math.MaxInt64},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseJSONUSDMicros(json.RawMessage(test.raw))
			if err != nil || got != test.want {
				t.Fatalf("parseJSONUSDMicros(%s) = %d, %v; want %d, nil", test.raw, got, err, test.want)
			}
		})
	}
}

func TestParseJSONUSDMicrosRefusesInvalidValues(t *testing.T) {
	for _, raw := range []string{
		`null`,
		`"1/2"`,
		`"0.0000001"`,
		`"9223372036854.775808"`,
		`"01.0"`,
		`true`,
		strconv.Quote("not money"),
	} {
		t.Run(raw, func(t *testing.T) {
			if got, err := parseJSONUSDMicros(json.RawMessage(raw)); err == nil {
				t.Fatalf("parseJSONUSDMicros(%s) = %d, nil; want refusal", raw, got)
			}
		})
	}
}

func TestUSDMicrosPerHourJSONUsesExactDollarDecimals(t *testing.T) {
	for micros, want := range map[USDMicrosPerHour]string{
		0: "0", 1: "0.000001", 250_000: "0.25", 1_000_001: "1.000001",
	} {
		got, err := json.Marshal(micros)
		if err != nil || string(got) != want {
			t.Fatalf("Marshal(%d) = %s, %v; want %s, nil", micros, got, err, want)
		}
		var roundTrip USDMicrosPerHour
		if err := json.Unmarshal(got, &roundTrip); err != nil || roundTrip != micros {
			t.Fatalf("Unmarshal(%s) = %d, %v; want %d, nil", got, roundTrip, err, micros)
		}
	}
}
