package runpod

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	podBillingGrouping   = "podId"
	podBillingBucketSize = "hour"

	// maxPodBillingResponseBytes bounds the exact provider evidence retained
	// for one hourly-history query.
	maxPodBillingResponseBytes = 16 << 20
)

// PodBillingRecord is one provider-reported hourly billing aggregate. Money
// is signed integer USD micros so corrective provider records remain evidence
// without passing through float64.
type PodBillingRecord struct {
	PodID           string    `json:"podId"`
	BucketStart     time.Time `json:"time"`
	AmountUSDMicros int64     `json:"amountUsdMicros"`
	TimeBilledMs    int64     `json:"timeBilledMs"`
}

// PodBillingHistory is the complete evidence returned by one billing read.
// NormalizedQuery is the URL-encoded query string sent on the wire. RawResponse
// preserves the response body byte-for-byte; callers should treat both byte
// slices and Records as immutable.
type PodBillingHistory struct {
	NormalizedQuery      string
	RawResponse          []byte
	Records              []PodBillingRecord
	TotalAmountUSDMicros int64
}

// PodBillingEvidenceErrorKind is a stable provider-wire refusal class. Callers
// can persist it without parsing RunPod JSON or matching error text.
type PodBillingEvidenceErrorKind string

const (
	PodBillingEvidenceSchemaAmbiguity  PodBillingEvidenceErrorKind = "schema_ambiguity"
	PodBillingEvidenceSubmicroAmount   PodBillingEvidenceErrorKind = "submicro_amount"
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

// GetPodBillingHistory retrieves RunPod's hourly billing aggregates for one
// exact pod and UTC interval. The request always forces grouping=podId and
// bucketSize=hour. An empty JSON array is valid zero-cost evidence; malformed,
// incomplete, foreign-pod, sub-micro, or overflowing evidence is refused.
func (c *Client) GetPodBillingHistory(ctx context.Context, podID string, startTime, endTime time.Time) (*PodBillingHistory, error) {
	if podID == "" || podID != strings.TrimSpace(podID) {
		return nil, NewValidationError("podID", "must be a non-empty exact pod id")
	}
	if startTime.IsZero() {
		return nil, NewValidationError("startTime", "cannot be zero")
	}
	if endTime.IsZero() {
		return nil, NewValidationError("endTime", "cannot be zero")
	}
	if !isUTC(startTime) {
		return nil, NewValidationError("startTime", "must be UTC")
	}
	if !isUTC(endTime) {
		return nil, NewValidationError("endTime", "must be UTC")
	}
	if !endTime.After(startTime) {
		return nil, NewValidationError("endTime", "must be after startTime")
	}

	q := url.Values{}
	q.Set("bucketSize", podBillingBucketSize)
	q.Set("endTime", endTime.UTC().Format(time.RFC3339Nano))
	q.Set("grouping", podBillingGrouping)
	q.Set("podId", podID)
	q.Set("startTime", startTime.UTC().Format(time.RFC3339Nano))
	normalizedQuery := q.Encode()

	resp, err := c.makeRequest(ctx, http.MethodGet, "/billing/pods?"+normalizedQuery, nil)
	if err != nil {
		return nil, fmt.Errorf("get pod billing history: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPodBillingResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("get pod billing history: read response body: %w", err)
	}
	if len(body) > maxPodBillingResponseBytes {
		return nil, newPodBillingEvidenceError(
			PodBillingEvidenceResponseTooLarge,
			normalizedQuery,
			nil,
			"response exceeds %d bytes",
			maxPodBillingResponseBytes,
		)
	}
	if c.debug {
		c.logger.Printf("[DEBUG] Response Status: %d", resp.StatusCode)
		c.logger.Printf("[DEBUG] Response Body: %s", string(body))
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("get pod billing history: %w", c.parseErrorResponse(resp.StatusCode, resp.Header, body))
	}

	records, total, decodeErr := decodePodBillingHistory(body, podID)
	if decodeErr != nil {
		return nil, decodeErr.withEvidence(normalizedQuery, body)
	}
	return &PodBillingHistory{
		NormalizedQuery:      normalizedQuery,
		RawResponse:          append([]byte(nil), body...),
		Records:              records,
		TotalAmountUSDMicros: total,
	}, nil
}

func (e *PodBillingEvidenceError) withEvidence(query string, raw []byte) *PodBillingEvidenceError {
	return &PodBillingEvidenceError{
		Kind:            e.Kind,
		NormalizedQuery: query,
		RawResponse:     append([]byte(nil), raw...),
		cause:           e.cause,
	}
}

func newPodBillingEvidenceError(kind PodBillingEvidenceErrorKind, query string, raw []byte, format string, args ...any) *PodBillingEvidenceError {
	return &PodBillingEvidenceError{
		Kind:            kind,
		NormalizedQuery: query,
		RawResponse:     append([]byte(nil), raw...),
		cause:           fmt.Errorf(format, args...),
	}
}

func isUTC(t time.Time) bool {
	_, offset := t.Zone()
	return offset == 0
}

type rawPodBillingRecord struct {
	Amount       json.RawMessage `json:"amount"`
	PodID        *string         `json:"podId"`
	Time         *string         `json:"time"`
	TimeBilledMs *int64          `json:"timeBilledMs"`
}

func decodePodBillingHistory(body []byte, requestedPodID string) ([]PodBillingRecord, int64, *PodBillingEvidenceError) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, 0, newPodBillingEvidenceError(PodBillingEvidenceSchemaAmbiguity, "", nil, "response must be a JSON array")
	}

	var rawRecords []rawPodBillingRecord
	if err := json.Unmarshal(body, &rawRecords); err != nil {
		return nil, 0, newPodBillingEvidenceError(PodBillingEvidenceSchemaAmbiguity, "", nil, "decode response: %v", err)
	}
	records := make([]PodBillingRecord, 0, len(rawRecords))
	var total int64
	for i, raw := range rawRecords {
		if raw.PodID == nil || *raw.PodID == "" {
			return nil, 0, newPodBillingEvidenceError(PodBillingEvidenceSchemaAmbiguity, "", nil, "record %d omitted podId", i)
		}
		if *raw.PodID != requestedPodID {
			return nil, 0, newPodBillingEvidenceError(PodBillingEvidenceSchemaAmbiguity, "", nil, "record %d names foreign pod %q, requested %q", i, *raw.PodID, requestedPodID)
		}
		if raw.Time == nil || *raw.Time == "" {
			return nil, 0, newPodBillingEvidenceError(PodBillingEvidenceSchemaAmbiguity, "", nil, "record %d omitted time", i)
		}
		bucketTime, err := time.Parse(time.RFC3339Nano, *raw.Time)
		if err != nil {
			return nil, 0, newPodBillingEvidenceError(PodBillingEvidenceSchemaAmbiguity, "", nil, "record %d has invalid time: %v", i, err)
		}
		if len(raw.Amount) == 0 || bytes.Equal(bytes.TrimSpace(raw.Amount), []byte("null")) {
			return nil, 0, newPodBillingEvidenceError(PodBillingEvidenceSchemaAmbiguity, "", nil, "record %d omitted amount", i)
		}
		amountMicros, err := parseJSONUSDMicros(raw.Amount)
		if err != nil {
			kind := PodBillingEvidenceSchemaAmbiguity
			if moneyErr, ok := err.(*usdMicrosParseError); ok {
				switch moneyErr.kind {
				case usdMicrosSubmicro:
					kind = PodBillingEvidenceSubmicroAmount
				case usdMicrosOverflow:
					kind = PodBillingEvidenceAmountOverflow
				}
			}
			return nil, 0, newPodBillingEvidenceError(kind, "", nil, "record %d amount: %v", i, err)
		}
		if raw.TimeBilledMs == nil {
			return nil, 0, newPodBillingEvidenceError(PodBillingEvidenceSchemaAmbiguity, "", nil, "record %d omitted timeBilledMs", i)
		}
		if *raw.TimeBilledMs < 0 {
			return nil, 0, newPodBillingEvidenceError(PodBillingEvidenceSchemaAmbiguity, "", nil, "record %d has negative timeBilledMs", i)
		}
		if (amountMicros > 0 && total > math.MaxInt64-amountMicros) ||
			(amountMicros < 0 && total < math.MinInt64-amountMicros) {
			return nil, 0, newPodBillingEvidenceError(PodBillingEvidenceAmountOverflow, "", nil, "record %d makes total USD micros overflow", i)
		}
		total += amountMicros
		records = append(records, PodBillingRecord{
			PodID:           *raw.PodID,
			BucketStart:     bucketTime.UTC(),
			AmountUSDMicros: amountMicros,
			TimeBilledMs:    *raw.TimeBilledMs,
		})
	}
	return records, total, nil
}
