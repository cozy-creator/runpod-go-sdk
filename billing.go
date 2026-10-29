package runpod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxPodBillingResponseBytes = 16 << 20

// PodBillingRecord is one REST v2 billing bucket. Bucket boundaries are not
// evidence of the pod's billed runtime; v2 does not expose that duration.
type PodBillingRecord struct {
	PodID               string
	BucketStart         time.Time
	BucketEnd           time.Time
	AmountUSDMicros     int64
	GPUAmountUSDMicros  int64
	CPUAmountUSDMicros  int64
	DiskAmountUSDMicros int64
}

type PodBillingQuery struct {
	StartTime  time.Time `json:"startTime"`
	EndTime    time.Time `json:"endTime"`
	BucketSize string    `json:"bucketSize"`
	PodID      string    `json:"podId"`
}

// PodBillingHistory preserves both the requested query and the provider's
// resolved query (which snaps to bucket edges), plus the complete raw response.
// TotalAmountUSDMicros is the checked sum of individually rounded records;
// MetadataTotalAmountUSDMicros retains the independently rounded provider total.
type PodBillingHistory struct {
	NormalizedQuery              string
	RawResponse                  []byte
	Query                        PodBillingQuery
	Records                      []PodBillingRecord
	TotalAmountUSDMicros         int64
	MetadataTotalAmountUSDMicros int64
}

// PodBillingEvidenceErrorKind is a stable provider-wire refusal class. Callers
// can persist it without parsing RunPod JSON or matching error text.
type PodBillingEvidenceErrorKind string

const (
	PodBillingEvidenceSchemaAmbiguity  PodBillingEvidenceErrorKind = "schema_ambiguity"
	PodBillingEvidenceAmountOverflow   PodBillingEvidenceErrorKind = "amount_overflow"
	PodBillingEvidenceResponseTooLarge PodBillingEvidenceErrorKind = "response_too_large"
)

// PodBillingEvidenceError preserves the bounded provider response and exact
// normalized query for a response that could not become typed billing history.
// RawResponse is empty for ResponseTooLarge because a partial body is not exact
// evidence. Diagnostic detail is available through errors.Unwrap; Kind is the
// stable value for durable decisions.
type PodBillingEvidenceError struct {
	Kind            PodBillingEvidenceErrorKind
	NormalizedQuery string
	RawResponse     []byte

	cause error
}

func (e *PodBillingEvidenceError) Error() string {
	if e == nil {
		return "pod billing evidence refused"
	}
	if e.cause == nil {
		return fmt.Sprintf("pod billing evidence refused (%s)", e.Kind)
	}
	return fmt.Sprintf("pod billing evidence refused (%s): %v", e.Kind, e.cause)
}

func (e *PodBillingEvidenceError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// GetPodBillingHistory requests hourly v2 buckets for one exact pod. Every
// signed decimal rounds directly to USD micros, never through float64.
func (c *Client) GetPodBillingHistory(ctx context.Context, podID string, startTime, endTime time.Time) (*PodBillingHistory, error) {
	if podID == "" || podID != strings.TrimSpace(podID) {
		return nil, NewValidationError("podID", "must be a non-empty exact pod id")
	}
	if startTime.IsZero() || endTime.IsZero() || !endTime.After(startTime) {
		return nil, NewValidationError("time range", "requires nonzero increasing bounds")
	}
	if !isUTC(startTime) || !isUTC(endTime) {
		return nil, NewValidationError("time range", "must be UTC")
	}
	q := url.Values{"podId": {podID}, "startTime": {startTime.Format(time.RFC3339Nano)}, "endTime": {endTime.Format(time.RFC3339Nano)}, "bucketSize": {"hour"}}
	query := q.Encode()
	resp, err := c.makeRequest(ctx, http.MethodGet, "/billing/pods?"+query, nil)
	if err != nil {
		return nil, fmt.Errorf("get pod billing history: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPodBillingResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read billing response: %w", err)
	}
	if len(body) > maxPodBillingResponseBytes {
		return nil, newPodBillingEvidenceError(PodBillingEvidenceResponseTooLarge, query, nil, "response exceeds %d bytes", maxPodBillingResponseBytes)
	}
	if resp.StatusCode >= 400 {
		return nil, c.parseErrorResponse(resp.StatusCode, resp.Header, body)
	}
	history, decodeErr := decodePodBillingHistory(body, podID, startTime, endTime)
	if decodeErr != nil {
		decodeErr.NormalizedQuery = query
		decodeErr.RawResponse = append([]byte(nil), body...)
		return nil, decodeErr
	}
	history.NormalizedQuery = query
	history.RawResponse = append([]byte(nil), body...)
	return history, nil
}

func isUTC(t time.Time) bool { _, offset := t.Zone(); return offset == 0 }

func newPodBillingEvidenceError(kind PodBillingEvidenceErrorKind, query string, raw []byte, format string, args ...any) *PodBillingEvidenceError {
	return &PodBillingEvidenceError{Kind: kind, NormalizedQuery: query, RawResponse: append([]byte(nil), raw...), cause: fmt.Errorf(format, args...)}
}

type billingAmounts struct {
	Total json.RawMessage `json:"totalAmount"`
	GPU   json.RawMessage `json:"gpuAmount"`
	CPU   json.RawMessage `json:"cpuAmount"`
	Disk  json.RawMessage `json:"diskAmount"`
}

func decodePodBillingHistory(body []byte, podID string, requestedStart, requestedEnd time.Time) (*PodBillingHistory, *PodBillingEvidenceError) {
	refuse := func(format string, args ...any) (*PodBillingHistory, *PodBillingEvidenceError) {
		return nil, newPodBillingEvidenceError(PodBillingEvidenceSchemaAmbiguity, "", nil, format, args...)
	}
	var payload struct {
		Records *[]struct {
			PodID     string    `json:"podId"`
			StartTime time.Time `json:"startTime"`
			EndTime   time.Time `json:"endTime"`
			billingAmounts
		} `json:"records"`
		Metadata *struct {
			Query          *PodBillingQuery `json:"query"`
			RecordCount    *int             `json:"recordCount"`
			UniquePodCount *int             `json:"uniquePodCount"`
			Totals         *billingAmounts  `json:"totals"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return refuse("decode billing response: %v", err)
	}
	meta := payload.Metadata
	if payload.Records == nil || meta == nil || meta.Query == nil || meta.RecordCount == nil || meta.UniquePodCount == nil || meta.Totals == nil {
		return refuse("billing response omitted records or metadata")
	}
	query := meta.Query
	if query.PodID != podID || query.BucketSize != "hour" || query.StartTime.IsZero() || !query.EndTime.After(query.StartTime) {
		return refuse("billing metadata does not identify the requested hourly pod window")
	}
	expectedStart := requestedStart.UTC().Truncate(time.Hour)
	expectedEnd := requestedEnd.UTC().Truncate(time.Hour)
	if expectedEnd.Before(requestedEnd) {
		expectedEnd = expectedEnd.Add(time.Hour)
	}
	if !query.StartTime.Equal(expectedStart) || !query.EndTime.Equal(expectedEnd) {
		return refuse("billing metadata resolved a different hourly window than requested")
	}
	records := *payload.Records
	unique := 0
	if len(records) > 0 {
		unique = 1
	}
	if *meta.RecordCount != len(records) || *meta.UniquePodCount != unique {
		return refuse("billing metadata count disagrees with records")
	}
	amounts := func(raw billingAmounts) ([4]int64, error) {
		var out [4]int64
		for i, b := range []json.RawMessage{raw.Total, raw.GPU, raw.CPU, raw.Disk} {
			v, err := parseJSONUSDMicrosNearest(b)
			if err != nil {
				return out, err
			}
			out[i] = v
		}
		return out, nil
	}
	amountError := func(err error, format string, args ...any) (*PodBillingHistory, *PodBillingEvidenceError) {
		var parseErr *usdMicrosParseError
		kind := PodBillingEvidenceSchemaAmbiguity
		if errors.As(err, &parseErr) && parseErr.kind == usdMicrosOverflow {
			kind = PodBillingEvidenceAmountOverflow
		}
		return nil, newPodBillingEvidenceError(kind, "", nil, format, args...)
	}
	totals, err := amounts(*meta.Totals)
	if err != nil {
		return amountError(err, "invalid billing totals: %v", err)
	}
	out := &PodBillingHistory{Query: *query, Records: make([]PodBillingRecord, 0, len(records)), MetadataTotalAmountUSDMicros: totals[0]}
	seen := map[time.Time]bool{}
	sums := [4]*big.Int{new(big.Int), new(big.Int), new(big.Int), new(big.Int)}
	for i, record := range records {
		if record.PodID != podID || record.StartTime.IsZero() || !record.EndTime.After(record.StartTime) || record.StartTime.Before(query.StartTime) || record.EndTime.After(query.EndTime) || !record.StartTime.Equal(record.StartTime.UTC().Truncate(time.Hour)) || record.EndTime.Sub(record.StartTime) != time.Hour || seen[record.StartTime.UTC()] {
			return refuse("record %d has foreign pod, invalid range, or duplicate bucket", i)
		}
		seen[record.StartTime.UTC()] = true
		a, err := amounts(record.billingAmounts)
		if err != nil {
			return amountError(err, "record %d has invalid amounts: %v", i, err)
		}
		// The four independently rounded values can differ by at most two
		// micros: half a micro each for total, GPU, CPU, and disk.
		components := new(big.Int).Add(big.NewInt(a[1]), big.NewInt(a[2]))
		components.Add(components, big.NewInt(a[3]))
		difference := new(big.Int).Sub(big.NewInt(a[0]), components)
		if difference.Abs(difference).Cmp(big.NewInt(2)) > 0 {
			return refuse("record %d total disagrees with its GPU, CPU, and disk components", i)
		}
		for component, value := range a {
			sums[component].Add(sums[component], big.NewInt(value))
		}

		out.Records = append(out.Records, PodBillingRecord{PodID: podID, BucketStart: record.StartTime, BucketEnd: record.EndTime, AmountUSDMicros: a[0], GPUAmountUSDMicros: a[1], CPUAmountUSDMicros: a[2], DiskAmountUSDMicros: a[3]})
	}
	if !sums[0].IsInt64() {
		return nil, newPodBillingEvidenceError(PodBillingEvidenceAmountOverflow, "", nil, "billing total overflows USD micros")
	}
	// Rounding N records separately, then rounding their aggregate, permits
	// floor((N+1)/2) micros of difference. An empty list permits none.
	tolerance := big.NewInt(int64((len(records) + 1) / 2))
	for component, value := range totals {
		difference := new(big.Int).Sub(big.NewInt(value), sums[component])
		if difference.Abs(difference).Cmp(tolerance) > 0 {
			return refuse("billing metadata %s disagrees with records", []string{"totalAmount", "gpuAmount", "cpuAmount", "diskAmount"}[component])
		}
	}
	out.TotalAmountUSDMicros = sums[0].Int64()
	return out, nil
}
