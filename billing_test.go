package runpod_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

const v2BillingRecord = `{"podId":"pod-one","startTime":"2026-10-28T10:00:00Z","endTime":"2026-10-28T11:00:00Z","totalAmount":0.25,"gpuAmount":0.2,"cpuAmount":0,"diskAmount":0.05}`
const v2BillingMetadata = `{"query":{"podId":"pod-one","startTime":"2026-10-28T10:00:00Z","endTime":"2026-10-28T12:00:00Z","bucketSize":"hour"},"recordCount":1,"uniquePodCount":1,"totals":{"totalAmount":0.25,"gpuAmount":0.2,"cpuAmount":0,"diskAmount":0.05}}`
const v2BillingResponse = `{"records":[` + v2BillingRecord + `],"metadata":` + v2BillingMetadata + `}`

func billingTestBounds() (time.Time, time.Time) {
	return time.Date(2026, 10, 28, 10, 15, 0, 123456789, time.UTC), time.Date(2026, 10, 28, 11, 30, 0, 0, time.UTC)
}

func billingClient(t *testing.T, body string) *runpod.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/billing/pods" {
			t.Errorf("request %s %s", r.Method, r.URL)
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return mustClient(t, "key", runpod.WithBaseURL(server.URL+"/v2"), runpod.WithMaxRetryAttempts(0))
}

func billingMutation(t *testing.T, edit func(map[string]any)) string {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(v2BillingResponse))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	edit(value)
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func billingRecord(value map[string]any) map[string]any {
	return value["records"].([]any)[0].(map[string]any)
}
func billingMetadata(value map[string]any) map[string]any { return value["metadata"].(map[string]any) }

func TestV2BillingSendsExactQueryAndPreservesResolvedWindowAndSignedAmounts(t *testing.T) {
	// The provider's independently rounded aggregate can legitimately differ
	// from the sum of rounded records. Preserve both, along with the exact body.
	const response = `{
 "records":[
  {"podId":"pod-one","startTime":"2026-10-28T10:00:00Z","endTime":"2026-10-28T11:00:00Z","totalAmount":6.072548240656033,"gpuAmount":6,"cpuAmount":0,"diskAmount":0.072548240656033},
  {"podId":"pod-one","startTime":"2026-10-28T11:00:00Z","endTime":"2026-10-28T12:00:00Z","totalAmount":-0.0000015,"gpuAmount":0,"cpuAmount":0,"diskAmount":-0.0000015}
 ],
 "metadata":{"query":{"podId":"pod-one","startTime":"2026-10-28T10:00:00Z","endTime":"2026-10-28T12:00:00Z","bucketSize":"hour"},"recordCount":2,"uniquePodCount":1,"totals":{"totalAmount":6.072546740656033,"gpuAmount":6,"cpuAmount":0,"diskAmount":0.072546740656033}}
}`
	start, end := billingTestBounds()
	wantQuery := url.Values{"podId": {"pod-one"}, "startTime": {start.Format(time.RFC3339Nano)}, "endTime": {end.Format(time.RFC3339Nano)}, "bucketSize": {"hour"}}.Encode()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/billing/pods" || r.URL.RawQuery != wantQuery {
			t.Errorf("query %s", r.URL)
		}
		if r.URL.Query().Has("grouping") {
			t.Error("sent removed grouping parameter")
		}
		if r.Header.Get("Authorization") != "Bearer key" {
			t.Error("missing bearer auth")
		}
		fmt.Fprint(w, response)
	}))
	defer server.Close()
	client := mustClient(t, "key", runpod.WithBaseURL(server.URL+"/v2"))
	history, err := client.GetPodBillingHistory(t.Context(), "pod-one", start, end)
	if err != nil {
		t.Fatal(err)
	}
	if history.NormalizedQuery != wantQuery || string(history.RawResponse) != response {
		t.Fatal("changed exact request or response evidence")
	}
	if !history.Query.StartTime.Equal(start.Truncate(time.Hour)) || !history.Query.EndTime.Equal(end.Truncate(time.Hour).Add(time.Hour)) || history.Query.PodID != "pod-one" || history.Query.BucketSize != "hour" {
		t.Fatalf("provider resolved query %+v", history.Query)
	}
	if len(history.Records) != 2 || history.TotalAmountUSDMicros != 6072546 || history.MetadataTotalAmountUSDMicros != 6072547 {
		t.Fatalf("rounded totals %+v", history)
	}
	if history.Records[0].GPUAmountUSDMicros != 6000000 || history.Records[0].DiskAmountUSDMicros != 72548 || history.Records[1].DiskAmountUSDMicros != -2 || history.Records[1].AmountUSDMicros != -2 {
		t.Fatalf("amounts %+v", history.Records)
	}
	if history.Records[0].BucketEnd.Sub(history.Records[0].BucketStart) != time.Hour {
		t.Fatalf("bucket range %+v", history.Records[0])
	}
}

func TestV2BillingPreservesEmptyZeroCostEvidence(t *testing.T) {
	body := billingMutation(t, func(value map[string]any) {
		value["records"] = []any{}
		metadata := billingMetadata(value)
		metadata["recordCount"] = 0
		metadata["uniquePodCount"] = 0
		metadata["totals"] = map[string]any{"totalAmount": 0, "gpuAmount": 0, "cpuAmount": 0, "diskAmount": 0}
	})
	client := billingClient(t, body)
	start, end := billingTestBounds()
	result, err := client.GetPodBillingHistory(t.Context(), "pod-one", start, end)
	if err != nil || result.Records == nil || len(result.Records) != 0 || result.TotalAmountUSDMicros != 0 || result.MetadataTotalAmountUSDMicros != 0 || string(result.RawResponse) != body {
		t.Fatalf("zero evidence %+v %v", result, err)
	}
}

func TestV2BillingRejectsIncompleteForeignAndMalformedEvidence(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing records", func(v map[string]any) { delete(v, "records") }},
		{"null records", func(v map[string]any) { v["records"] = nil }},
		{"missing metadata", func(v map[string]any) { delete(v, "metadata") }},
		{"null metadata", func(v map[string]any) { v["metadata"] = nil }},
		{"missing query", func(v map[string]any) { delete(billingMetadata(v), "query") }},
		{"null query", func(v map[string]any) { billingMetadata(v)["query"] = nil }},
		{"missing totals", func(v map[string]any) { delete(billingMetadata(v), "totals") }},
		{"null totals", func(v map[string]any) { billingMetadata(v)["totals"] = nil }},
		{"missing count", func(v map[string]any) { delete(billingMetadata(v), "recordCount") }},
		{"null count", func(v map[string]any) { billingMetadata(v)["recordCount"] = nil }},
		{"wrong count", func(v map[string]any) { billingMetadata(v)["recordCount"] = 2 }},
		{"wrong unique count", func(v map[string]any) { billingMetadata(v)["uniquePodCount"] = 0 }},
		{"missing pod", func(v map[string]any) { delete(billingRecord(v), "podId") }},
		{"null pod", func(v map[string]any) { billingRecord(v)["podId"] = nil }},
		{"foreign pod", func(v map[string]any) { billingRecord(v)["podId"] = "other" }},
		{"missing bucket start", func(v map[string]any) { delete(billingRecord(v), "startTime") }},
		{"null bucket end", func(v map[string]any) { billingRecord(v)["endTime"] = nil }},
		{"invalid bucket time", func(v map[string]any) { billingRecord(v)["startTime"] = "2026-10-28 10:00:00" }},
		{"outside resolved window", func(v map[string]any) { billingRecord(v)["startTime"] = "2026-10-28T09:00:00Z" }},
		{"inverted bucket", func(v map[string]any) { billingRecord(v)["endTime"] = "2026-10-28T09:00:00Z" }},
		{"nonhour bucket", func(v map[string]any) { billingRecord(v)["endTime"] = "2026-10-28T10:30:00Z" }},
		{"unaligned bucket", func(v map[string]any) {
			billingRecord(v)["startTime"] = "2026-10-28T10:15:00Z"
			billingRecord(v)["endTime"] = "2026-10-28T11:15:00Z"
		}},
		{"duplicate bucket", func(v map[string]any) {
			v["records"] = append(v["records"].([]any), billingRecord(v))
			billingMetadata(v)["recordCount"] = 2
		}},
		{"foreign query", func(v map[string]any) { billingMetadata(v)["query"].(map[string]any)["podId"] = "other" }},
		{"wrong granularity", func(v map[string]any) { billingMetadata(v)["query"].(map[string]any)["bucketSize"] = "day" }},
		{"wrong resolved start", func(v map[string]any) {
			billingMetadata(v)["query"].(map[string]any)["startTime"] = "2026-10-28T09:00:00Z"
		}},
		{"wrong resolved end", func(v map[string]any) {
			billingMetadata(v)["query"].(map[string]any)["endTime"] = "2026-10-28T13:00:00Z"
		}},
	}
	for _, field := range []string{"totalAmount", "gpuAmount", "cpuAmount", "diskAmount"} {
		field := field
		cases = append(cases, struct {
			name   string
			mutate func(map[string]any)
		}{"missing record " + field, func(v map[string]any) { delete(billingRecord(v), field) }}, struct {
			name   string
			mutate func(map[string]any)
		}{"null record " + field, func(v map[string]any) { billingRecord(v)[field] = nil }}, struct {
			name   string
			mutate func(map[string]any)
		}{"missing metadata " + field, func(v map[string]any) { delete(billingMetadata(v)["totals"].(map[string]any), field) }})
	}
	start, end := billingTestBounds()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := billingMutation(t, tc.mutate)
			result, err := billingClient(t, body).GetPodBillingHistory(t.Context(), "pod-one", start, end)
			var evidence *runpod.PodBillingEvidenceError
			if result != nil || !errors.As(err, &evidence) || evidence.Kind != runpod.PodBillingEvidenceSchemaAmbiguity {
				t.Fatalf("classification %+v %v", result, err)
			}
			if string(evidence.RawResponse) != body || evidence.NormalizedQuery == "" {
				t.Fatalf("lost refusal evidence %+v", evidence)
			}
		})
	}
	for _, body := range []string{`null`, `[]`, `{`, `{"records":[{"amount":1,"time":"2026-10-28 10:00:00"}]}`} {
		if _, err := billingClient(t, body).GetPodBillingHistory(t.Context(), "pod-one", start, end); err == nil {
			t.Fatalf("accepted obsolete or malformed shape %s", body)
		}
	}
}

func TestV2BillingOverflowRetainsStableRefusalKind(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"positive record overflow", func(v map[string]any) { billingRecord(v)["totalAmount"] = json.Number("9223372036854.775808") }},
		{"negative record overflow", func(v map[string]any) { billingRecord(v)["diskAmount"] = json.Number("-9223372036854.775809") }},
		{"metadata overflow", func(v map[string]any) {
			billingMetadata(v)["totals"].(map[string]any)["gpuAmount"] = json.Number("9223372036854.775808")
		}},
		{"record sum overflow", func(v map[string]any) {
			first := billingRecord(v)
			first["totalAmount"] = json.Number("9223372036854.775807")
			first["gpuAmount"] = json.Number("9223372036854.775807")
			first["diskAmount"] = 0
			second := map[string]any{"podId": "pod-one", "startTime": "2026-10-28T11:00:00Z", "endTime": "2026-10-28T12:00:00Z", "totalAmount": json.Number("0.000001"), "gpuAmount": json.Number("0.000001"), "cpuAmount": 0, "diskAmount": 0}
			v["records"] = append(v["records"].([]any), second)
			billingMetadata(v)["recordCount"] = 2
		}},
		{"record sum underflow", func(v map[string]any) {
			first := billingRecord(v)
			first["totalAmount"] = json.Number("-9223372036854.775808")
			first["gpuAmount"] = json.Number("-9223372036854.775808")
			first["diskAmount"] = 0
			second := map[string]any{"podId": "pod-one", "startTime": "2026-10-28T11:00:00Z", "endTime": "2026-10-28T12:00:00Z", "totalAmount": json.Number("-0.000001"), "gpuAmount": json.Number("-0.000001"), "cpuAmount": 0, "diskAmount": 0}
			v["records"] = append(v["records"].([]any), second)
			billingMetadata(v)["recordCount"] = 2
		}},
	}
	start, end := billingTestBounds()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := billingMutation(t, tc.mutate)
			_, err := billingClient(t, body).GetPodBillingHistory(t.Context(), "pod-one", start, end)
			var evidence *runpod.PodBillingEvidenceError
			if !errors.As(err, &evidence) || evidence.Kind != runpod.PodBillingEvidenceAmountOverflow || string(evidence.RawResponse) != body {
				t.Fatalf("overflow kind/evidence %v", err)
			}
		})
	}
}

func TestV2BillingBoundsAndResponseLimit(t *testing.T) {
	start, end := billingTestBounds()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer server.Close()
	client := mustClient(t, "key", runpod.WithBaseURL(server.URL))
	for _, tc := range []struct {
		pod        string
		start, end time.Time
	}{
		{"", start, end}, {" pod-one ", start, end}, {"pod-one", time.Time{}, end}, {"pod-one", start, start}, {"pod-one", end, start},
		{"pod-one", start.In(time.FixedZone("west", -7*3600)), end}, {"pod-one", start, end.In(time.FixedZone("east", 3600))},
	} {
		if _, err := client.GetPodBillingHistory(context.Background(), tc.pod, tc.start, tc.end); err == nil {
			t.Fatalf("accepted invalid input %+v", tc)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid inputs sent %d requests", calls)
	}
	body := strings.Repeat(" ", (16<<20)+1)
	_, err := billingClient(t, body).GetPodBillingHistory(t.Context(), "pod-one", start, end)
	var evidence *runpod.PodBillingEvidenceError
	if !errors.As(err, &evidence) || evidence.Kind != runpod.PodBillingEvidenceResponseTooLarge || len(evidence.RawResponse) != 0 || evidence.NormalizedQuery == "" {
		t.Fatalf("oversize response %+v %v", evidence, err)
	}
}

func TestV2BillingAlreadyAlignedEndIsNotWidened(t *testing.T) {
	start := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)
	history, err := billingClient(t, v2BillingResponse).GetPodBillingHistory(t.Context(), "pod-one", start, end)
	if err != nil || !history.Query.EndTime.Equal(end) {
		t.Fatalf("aligned end %+v %v", history, err)
	}
}

func TestV2BillingRejectsContradictoryTotalsWithoutLosingEvidence(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"empty records with nonzero total", func(v map[string]any) {
			v["records"] = []any{}
			m := billingMetadata(v)
			m["recordCount"] = 0
			m["uniquePodCount"] = 0
		}},
		{"metadata total mismatch", func(v map[string]any) { billingMetadata(v)["totals"].(map[string]any)["totalAmount"] = 100 }},
		{"metadata GPU mismatch", func(v map[string]any) { billingMetadata(v)["totals"].(map[string]any)["gpuAmount"] = 100 }},
		{"metadata CPU mismatch", func(v map[string]any) { billingMetadata(v)["totals"].(map[string]any)["cpuAmount"] = 100 }},
		{"metadata disk mismatch", func(v map[string]any) { billingMetadata(v)["totals"].(map[string]any)["diskAmount"] = 100 }},
		{"record components mismatch", func(v map[string]any) { billingRecord(v)["gpuAmount"] = 100 }},
		{"negative record components mismatch", func(v map[string]any) { billingRecord(v)["diskAmount"] = -100 }},
		{"empty records with zero total but nonzero component", func(v map[string]any) {
			v["records"] = []any{}
			m := billingMetadata(v)
			m["recordCount"] = 0
			m["uniquePodCount"] = 0
			m["totals"] = map[string]any{"totalAmount": 0, "gpuAmount": 1, "cpuAmount": 0, "diskAmount": 0}
		}},
	}
	start, end := billingTestBounds()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := billingMutation(t, tc.mutate)
			result, err := billingClient(t, body).GetPodBillingHistory(t.Context(), "pod-one", start, end)
			var evidence *runpod.PodBillingEvidenceError
			if result != nil || !errors.As(err, &evidence) || evidence.Kind != runpod.PodBillingEvidenceSchemaAmbiguity || string(evidence.RawResponse) != body || evidence.NormalizedQuery == "" {
				t.Fatalf("contradictory money qualified: %+v %v", result, err)
			}
		})
	}
}
