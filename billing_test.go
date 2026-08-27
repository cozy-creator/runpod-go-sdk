package runpod_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func TestGetPodBillingHistorySendsNormalizedExactQueryAndReturnsEvidence(t *testing.T) {
	const podID = "pod-one"
	const response = "[\n" +
		`  {"amount":1.250001,"diskSpaceBilledGb":50,"podId":"pod-one","time":"2026-08-25T10:00:00+00:00","timeBilledMs":3600000},` + "\n" +
		`  {"amount":"-0.000001","gpuTypeId":"NVIDIA H200","podId":"pod-one","time":"2026-08-25T11:00:00Z","timeBilledMs":1}` + "\n" +
		"]\n"
	start := time.Date(2026, 8, 25, 10, 0, 0, 123_000_000, time.UTC)
	end := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	wantValues := url.Values{
		"bucketSize": {"hour"},
		"endTime":    {end.Format(time.RFC3339Nano)},
		"grouping":   {"podId"},
		"podId":      {podID},
		"startTime":  {start.Format(time.RFC3339Nano)},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/billing/pods" {
			t.Errorf("request = %s %s; want GET /billing/pods", r.Method, r.URL.Path)
		}
		if r.URL.RawQuery != wantValues.Encode() {
			t.Errorf("raw query = %q; want %q", r.URL.RawQuery, wantValues.Encode())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test_key" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	defer server.Close()

	client := mustClient(t, "test_key", runpod.WithBaseURL(server.URL), runpod.WithMaxRetryAttempts(0))
	got, err := client.GetPodBillingHistory(context.Background(), podID, start, end)
	if err != nil {
		t.Fatalf("GetPodBillingHistory: %v", err)
	}
	if got.NormalizedQuery != wantValues.Encode() {
		t.Errorf("NormalizedQuery = %q; want %q", got.NormalizedQuery, wantValues.Encode())
	}
	if string(got.RawResponse) != response {
		t.Errorf("RawResponse changed:\n got %q\nwant %q", got.RawResponse, response)
	}
	if got.TotalAmountUSDMicros != 1_250_000 {
		t.Errorf("TotalAmountUSDMicros = %d; want 1250000", got.TotalAmountUSDMicros)
	}
	if len(got.Records) != 2 {
		t.Fatalf("len(Records) = %d; want 2", len(got.Records))
	}
	wantTimes := []time.Time{
		time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 25, 11, 0, 0, 0, time.UTC),
	}
	wantAmounts := []int64{1_250_001, -1}
	wantBilled := []int64{3_600_000, 1}
	for i, record := range got.Records {
		if record.PodID != podID || !record.BucketStart.Equal(wantTimes[i]) ||
			record.AmountUSDMicros != wantAmounts[i] || record.TimeBilledMs != wantBilled[i] {
			t.Errorf("record %d = %+v", i, record)
		}
	}
}

func TestGetPodBillingHistoryPreservesEmptyZeroCostEvidence(t *testing.T) {
	const response = " [ ]\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(response))
	}))
	defer server.Close()

	client := mustClient(t, "test_key", runpod.WithBaseURL(server.URL), runpod.WithMaxRetryAttempts(0))
	start := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	got, err := client.GetPodBillingHistory(context.Background(), "pod-one", start, start.Add(time.Hour))
	if err != nil {
		t.Fatalf("GetPodBillingHistory: %v", err)
	}
	if string(got.RawResponse) != response || got.TotalAmountUSDMicros != 0 || len(got.Records) != 0 {
		t.Fatalf("zero-cost evidence = %+v raw=%q", got, got.RawResponse)
	}
}

func TestGetPodBillingHistoryValidatesExactPodAndUTCBoundsBeforeHTTP(t *testing.T) {
	httpCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()
	client := mustClient(t, "test_key", runpod.WithBaseURL(server.URL), runpod.WithMaxRetryAttempts(0))
	start := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	tests := []struct {
		name       string
		podID      string
		start, end time.Time
	}{
		{name: "empty pod", podID: "", start: start, end: end},
		{name: "normalized pod would differ", podID: " pod-one ", start: start, end: end},
		{name: "zero start", podID: "pod-one", start: time.Time{}, end: end},
		{name: "non UTC start", podID: "pod-one", start: start.In(time.FixedZone("west", -7*60*60)), end: end},
		{name: "non UTC end", podID: "pod-one", start: start, end: end.In(time.FixedZone("east", 2*60*60))},
		{name: "equal bounds", podID: "pod-one", start: start, end: start},
		{name: "reversed bounds", podID: "pod-one", start: end, end: start},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got, err := client.GetPodBillingHistory(context.Background(), test.podID, test.start, test.end); err == nil || got != nil {
				t.Fatalf("GetPodBillingHistory = %+v, %v; want nil, error", got, err)
			}
		})
	}
	if httpCalls != 0 {
		t.Fatalf("HTTP calls = %d; want 0", httpCalls)
	}
}

func TestGetPodBillingHistoryRefusesAmbiguousEvidence(t *testing.T) {
	const podID = "pod-one"
	start := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	wantQuery := url.Values{
		"bucketSize": {"hour"},
		"endTime":    {start.Add(time.Hour).Format(time.RFC3339Nano)},
		"grouping":   {"podId"},
		"podId":      {podID},
		"startTime":  {start.Format(time.RFC3339Nano)},
	}.Encode()
	tests := []struct {
		name     string
		body     string
		want     string
		wantKind string
	}{
		{name: "top level null", body: `null`, want: "JSON array", wantKind: "schema_ambiguity"},
		{name: "missing amount", body: `[ {"podId":"pod-one","time":"2026-08-25T10:00:00Z","timeBilledMs":1} ]`, want: "omitted amount", wantKind: "schema_ambiguity"},
		{name: "missing pod", body: `[ {"amount":1,"time":"2026-08-25T10:00:00Z","timeBilledMs":1} ]`, want: "omitted podId", wantKind: "schema_ambiguity"},
		{name: "missing time", body: `[ {"amount":1,"podId":"pod-one","timeBilledMs":1} ]`, want: "omitted time", wantKind: "schema_ambiguity"},
		{name: "missing billed time", body: `[ {"amount":1,"podId":"pod-one","time":"2026-08-25T10:00:00Z"} ]`, want: "omitted timeBilledMs", wantKind: "schema_ambiguity"},
		{name: "foreign pod", body: `[ {"amount":1,"podId":"pod-two","time":"2026-08-25T10:00:00Z","timeBilledMs":1} ]`, want: "foreign pod", wantKind: "schema_ambiguity"},
		{name: "sub micro", body: `[ {"amount":0.0000001,"podId":"pod-one","time":"2026-08-25T10:00:00Z","timeBilledMs":1} ]`, want: "sub-micro", wantKind: "submicro_amount"},
		{name: "amount overflow", body: `[ {"amount":"9223372036854.775808","podId":"pod-one","time":"2026-08-25T10:00:00Z","timeBilledMs":1} ]`, want: "exceeds int64", wantKind: "amount_overflow"},
		{name: "negative billed time", body: `[ {"amount":1,"podId":"pod-one","time":"2026-08-25T10:00:00Z","timeBilledMs":-1} ]`, want: "negative timeBilledMs", wantKind: "schema_ambiguity"},
		{name: "total overflow", body: `[{"amount":"9223372036854.775807","podId":"pod-one","time":"2026-08-25T10:00:00Z","timeBilledMs":1},{"amount":"0.000001","podId":"pod-one","time":"2026-08-25T11:00:00Z","timeBilledMs":1}]`, want: "total USD micros overflow", wantKind: "amount_overflow"},
		{name: "total underflow", body: `[{"amount":"-9223372036854.775808","podId":"pod-one","time":"2026-08-25T10:00:00Z","timeBilledMs":1},{"amount":-0.000001,"podId":"pod-one","time":"2026-08-25T11:00:00Z","timeBilledMs":1}]`, want: "total USD micros overflow", wantKind: "amount_overflow"},
		{name: "response too large", body: strings.Repeat(" ", (16<<20)+1), want: "response exceeds 16777216 bytes", wantKind: "response_too_large"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			client := mustClient(t, "test_key", runpod.WithBaseURL(server.URL), runpod.WithMaxRetryAttempts(0))
			got, err := client.GetPodBillingHistory(context.Background(), podID, start, start.Add(time.Hour))
			if err == nil || got != nil {
				t.Fatalf("GetPodBillingHistory = %+v, %v; want nil, error", got, err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %q; want substring %q", err, test.want)
			}
			var evidenceErr *runpod.PodBillingEvidenceError
			if !errors.As(err, &evidenceErr) {
				t.Fatalf("error type = %T; want *PodBillingEvidenceError", err)
			}
			if string(evidenceErr.Kind) != test.wantKind {
				t.Errorf("Kind = %q; want %q", evidenceErr.Kind, test.wantKind)
			}
			if evidenceErr.NormalizedQuery != wantQuery {
				t.Errorf("NormalizedQuery = %q; want %q", evidenceErr.NormalizedQuery, wantQuery)
			}
			if test.wantKind == "response_too_large" {
				if len(evidenceErr.RawResponse) != 0 {
					t.Errorf("oversize RawResponse has %d bytes; want 0", len(evidenceErr.RawResponse))
				}
			} else if string(evidenceErr.RawResponse) != test.body {
				t.Errorf("RawResponse changed: got %q want %q", evidenceErr.RawResponse, test.body)
			}
		})
	}
}
