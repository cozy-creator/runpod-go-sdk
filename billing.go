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
		return nil, fmt.Errorf("get pod billing history: response exceeds %d bytes", maxPodBillingResponseBytes)
	}
	if c.debug {
		c.logger.Printf("[DEBUG] Response Status: %d", resp.StatusCode)
		c.logger.Printf("[DEBUG] Response Body: %s", string(body))
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("get pod billing history: %w", c.parseErrorResponse(resp.StatusCode, resp.Header, body))
	}

	records, total, err := decodePodBillingHistory(body, podID)
	if err != nil {
		return nil, fmt.Errorf("get pod billing history: %w", err)
	}
	return &PodBillingHistory{
		NormalizedQuery:      normalizedQuery,
		RawResponse:          append([]byte(nil), body...),
		Records:              records,
		TotalAmountUSDMicros: total,
	}, nil
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

func decodePodBillingHistory(body []byte, requestedPodID string) ([]PodBillingRecord, int64, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, 0, fmt.Errorf("response must be a JSON array")
	}

	var rawRecords []rawPodBillingRecord
	if err := json.Unmarshal(body, &rawRecords); err != nil {
		return nil, 0, fmt.Errorf("decode response: %w", err)
	}
	records := make([]PodBillingRecord, 0, len(rawRecords))
	var total int64
	for i, raw := range rawRecords {
		if raw.PodID == nil || *raw.PodID == "" {
			return nil, 0, fmt.Errorf("record %d omitted podId", i)
		}
		if *raw.PodID != requestedPodID {
			return nil, 0, fmt.Errorf("record %d names foreign pod %q, requested %q", i, *raw.PodID, requestedPodID)
		}
		if raw.Time == nil || *raw.Time == "" {
			return nil, 0, fmt.Errorf("record %d omitted time", i)
		}
		bucketTime, err := time.Parse(time.RFC3339Nano, *raw.Time)
		if err != nil {
			return nil, 0, fmt.Errorf("record %d has invalid time: %w", i, err)
		}
		if len(raw.Amount) == 0 || bytes.Equal(bytes.TrimSpace(raw.Amount), []byte("null")) {
			return nil, 0, fmt.Errorf("record %d omitted amount", i)
		}
		amountMicros, err := parseJSONUSDMicros(raw.Amount)
		if err != nil {
			return nil, 0, fmt.Errorf("record %d amount: %w", i, err)
		}
		if raw.TimeBilledMs == nil {
			return nil, 0, fmt.Errorf("record %d omitted timeBilledMs", i)
		}
		if *raw.TimeBilledMs < 0 {
			return nil, 0, fmt.Errorf("record %d has negative timeBilledMs", i)
		}
		if (amountMicros > 0 && total > math.MaxInt64-amountMicros) ||
			(amountMicros < 0 && total < math.MinInt64-amountMicros) {
			return nil, 0, fmt.Errorf("record %d makes total USD micros overflow", i)
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
