package runpod_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func TestInventoryBatchesShareRootsAndQuote104CPUShapes(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var req struct {
			Query     string
			Variables map[string]json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(req.Query, "dataCenters{"):
			if strings.Count(req.Query, "dataCenters{") != 1 {
				t.Error("repeated census root")
			}
			row := map[string]interface{}{"id": "DC-1", "name": "one", "location": "US", "storageSupport": true}
			for i, width := range []int{1, 2, 4, 6, 8} {
				var filter runpod.GPUAvailabilityFilter
				if err := json.Unmarshal(req.Variables[fmt.Sprintf("f%d", i)], &filter); err != nil {
					t.Fatal(err)
				}
				if filter.GPUCount != width || filter.MinCUDAVersion != "13.0" {
					t.Errorf("census filter%d: %+v", i, filter)
				}
				row[fmt.Sprintf("q%d", i)] = []map[string]interface{}{{"gpuTypeId": "gpu-one", "available": true, "stockStatus": "High"}}
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{"dataCenters": []interface{}{row}}})
		case strings.Contains(req.Query, "gpuTypes{"):
			if strings.Count(req.Query, "gpuTypes{") != 1 {
				t.Error("repeated GPU root")
			}
			row := map[string]interface{}{"id": "gpu-one", "displayName": "One", "memoryInGb": 80, "secureCloud": true, "communityCloud": true}
			for i := range 3 {
				var count int
				_ = json.Unmarshal(req.Variables[fmt.Sprintf("v%d_gpuCount", i)], &count)
				if count != i+1 {
					t.Errorf("GPU count%d=%d", i, count)
				}
				for _, cloud := range []string{"secure", "community"} {
					alias := fmt.Sprintf("q%d_%s", i, cloud)
					if strings.Contains(req.Query, alias+":") {
						row[alias] = map[string]interface{}{"uninterruptablePrice": fmt.Sprintf("%d.000001", count), "minimumBidPrice": "0.000001", "stockStatus": "High"}
					}
				}
			}
			if strings.Contains(req.Query, "q0_community:") || strings.Contains(req.Query, "q1_secure:") {
				t.Error("queried unrequested cloud")
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{"gpuTypes": []interface{}{row}}})
		case strings.Contains(req.Query, "cpuFlavors{"):
			if strings.Count(req.Query, "cpuFlavors{") != 1 {
				t.Error("repeated CPU root")
			}
			row := map[string]interface{}{"id": "cpu5c", "displayName": "CPU", "minVcpu": 1, "maxVcpu": 64, "ramMultiplier": 2, "diskLimitPerVcpu": 10}
			for i := 0; req.Variables[fmt.Sprintf("instanceId%d", i)] != nil; i++ {
				var instance, center string
				_ = json.Unmarshal(req.Variables[fmt.Sprintf("instanceId%d", i)], &instance)
				_ = json.Unmarshal(req.Variables[fmt.Sprintf("dataCenterId%d", i)], &center)
				if instance == "" || center == "" {
					t.Errorf("missing quote identity%d", i)
				}
				row[fmt.Sprintf("q%d", i)] = map[string]interface{}{"stockStatus": "High", "securePrice": "0.070001"}
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{"cpuFlavors": []interface{}{row}}})
		default:
			t.Errorf("unexpected query: %s", req.Query)
			http.Error(w, "query", 400)
		}
	}))
	defer server.Close()
	client := mustClient(t, "fixture", runpod.WithGraphQLBaseURL(server.URL))
	var filters []runpod.GPUAvailabilityFilter
	for _, width := range []int{1, 2, 4, 6, 8} {
		filters = append(filters, runpod.GPUAvailabilityFilter{GPUCount: width, MinCUDAVersion: "13.0"})
	}
	centers, err := client.ListDataCentersBatch(t.Context(), filters)
	if err != nil || len(centers) != 5 {
		t.Fatalf("census: %v %v", centers, err)
	}
	for _, result := range centers {
		if result.Err != nil || len(result.DataCenters) != 1 || len(result.DataCenters[0].GPUAvailability) != 1 {
			t.Fatalf("census alias: %+v", result)
		}
	}
	offers, err := client.ListGPUOffersBatch(t.Context(), []runpod.GPUOfferFilter{
		{GPUCount: 1, CloudType: "SECURE", IDs: []string{"gpu-one"}, DataCenterID: "DC-1"},
		{GPUCount: 2, CloudType: "COMMUNITY", IDs: []string{"gpu-one"}},
		{GPUCount: 3},
	})
	if err != nil || len(offers) != 3 {
		t.Fatalf("GPU batch: %v %v", offers, err)
	}
	for i, result := range offers {
		want := 1
		if i == 2 {
			want = 2
		}
		if result.Err != nil || len(result.Offers) != want {
			t.Fatalf("GPU alias%d: %+v", i, result)
		}
		for _, offer := range result.Offers {
			if offer.GPUCount != i+1 || offer.OnDemandPriceUSDMicrosPerHour != runpod.USDMicrosPerHour((i+1)*1000000+1) {
				t.Fatalf("wrong exact GPU price: %+v", offer)
			}
		}
	}
	var requests []runpod.CPUOfferRequest
	for _, vcpus := range []int{2, 4, 8, 16} {
		for dc := range 26 {
			requests = append(requests, runpod.CPUOfferRequest{InstanceID: fmt.Sprintf("cpu5c-%d-%d", vcpus, vcpus*2), DataCenterID: fmt.Sprintf("DC-%d", dc)})
		}
	}
	quotes, err := client.GetCPUOffersBatch(t.Context(), requests)
	if err != nil || len(quotes) != 104 {
		t.Fatalf("CPU batch: len%d %v", len(quotes), err)
	}
	for i, result := range quotes {
		if result.Err != nil || result.Offer.CPUInstanceID != requests[i].InstanceID || result.Offer.DataCenterID != requests[i].DataCenterID || result.Offer.OnDemandPriceUSDMicrosPerHour != 70001 {
			t.Fatalf("CPU alias%d: %+v", i, result)
		}
	}
	if hits.Load() != 4 {
		t.Fatalf("three batches sent%d HTTP requests, want 4 (104 CPU quotes in two)", hits.Load())
	}
}

func TestCPUOfferBatchKeepsPartialErrorsAndExactPriceChecks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"cpuFlavors":[{"id":"cpu5c","minVcpu":1,"maxVcpu":64,"ramMultiplier":2,"diskLimitPerVcpu":10,"q0":{"securePrice":"0.070001","stockStatus":"High"},"q1":null,"q2":{"securePrice":"0.0000001"}}]},"errors":[{"message":"one quote unavailable","path":["cpuFlavors",0,"q1"],"extensions":{"code":"NO_STOCK"}}]}`))
	}))
	defer server.Close()
	client := mustClient(t, "fixture", runpod.WithGraphQLBaseURL(server.URL))
	requests := []runpod.CPUOfferRequest{{InstanceID: "cpu5c-2-4", DataCenterID: "DC-1"}, {InstanceID: "cpu5c-4-8", DataCenterID: "DC-2"}, {InstanceID: "cpu5c-8-16", DataCenterID: "DC-3"}, {InstanceID: "cpu5c-16-32", DataCenterID: "DC-4"}}
	results, err := client.GetCPUOffersBatch(t.Context(), requests)
	if err != nil || len(results) != 4 {
		t.Fatalf("batch: %v %v", results, err)
	}
	if results[0].Err != nil || results[0].Offer.OnDemandPriceUSDMicrosPerHour != 70001 {
		t.Fatalf("lost successful sibling: %+v", results[0])
	}
	var partial *runpod.GraphQLResponseError
	if !errors.As(results[1].Err, &partial) || string(partial.Errors[0].Extensions["code"]) != `"NO_STOCK"` {
		t.Fatalf("lost provider partial error: %+v", results[1])
	}
	if results[2].Err == nil || results[3].Err == nil {
		t.Fatalf("inexact/missing quotes passed: %+v", results)
	}
}

func TestBatchValidationAndEmptyInputsSendNoHTTP(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); http.Error(w, "unexpected", 500) }))
	defer server.Close()
	client := mustClient(t, "fixture", runpod.WithGraphQLBaseURL(server.URL))
	if result, err := client.ListDataCentersBatch(t.Context(), nil); err != nil || len(result) != 0 {
		t.Fatal(result, err)
	}
	if result, err := client.ListGPUOffersBatch(t.Context(), nil); err != nil || len(result) != 0 {
		t.Fatal(result, err)
	}
	if result, err := client.GetCPUOffersBatch(t.Context(), nil); err != nil || len(result) != 0 {
		t.Fatal(result, err)
	}
	_, a := client.ListDataCentersBatch(t.Context(), []runpod.GPUAvailabilityFilter{{GPUCount: -1}})
	_, b := client.ListGPUOffersBatch(t.Context(), []runpod.GPUOfferFilter{{CloudType: "invalid"}})
	_, c := client.GetCPUOffersBatch(t.Context(), []runpod.CPUOfferRequest{{InstanceID: "invalid", DataCenterID: "DC"}})
	for _, err := range []error{a, b, c} {
		var invalid *runpod.ValidationError
		if !errors.As(err, &invalid) {
			t.Fatalf("missing validation error: %v", err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("invalid/empty batches sent%d requests", hits.Load())
	}
}

func TestBatchRejectsUnscopedOrMissingRootErrors(t *testing.T) {
	for _, body := range []string{`{"errors":[{"message":"refused"}]}`, `{"data":{"gpuTypes":null}}`, `{"data":{"gpuTypes":[]},"errors":[{"message":"unknown path","path":["gpuTypes",0,"other"]}]}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			client := mustClient(t, "fixture", runpod.WithGraphQLBaseURL(server.URL))
			if result, err := client.ListGPUOffersBatch(t.Context(), []runpod.GPUOfferFilter{{GPUCount: 2}}); err == nil || result != nil {
				t.Fatalf("invalid response passed: %+v %v", result, err)
			}
		})
	}
}

func TestBatchGPUExactDecoderMatchesSingleQuote(t *testing.T) {
	var batch atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		price := `{"uninterruptablePrice":"1.000001","minimumBidPrice":"0.100001","stockStatus":"High","availableGpuCounts":[2]}`
		secure, community := "secure", "community"
		if batch.Load() {
			secure, community = "q0_secure", "q0_community"
		}
		_, _ = fmt.Fprintf(w, `{"data":{"gpuTypes":[{"id":"one","displayName":"One","memoryInGb":24,"secureCloud":true,"communityCloud":true,"%s":%s,"%s":%s}]}}`, secure, price, community, price)
	}))
	defer server.Close()
	client := mustClient(t, "fixture", runpod.WithGraphQLBaseURL(server.URL))
	filter := runpod.GPUOfferFilter{GPUCount: 2, InStockOnly: true, IDs: []string{"one"}}
	single, err := client.ListGPUOffers(t.Context(), &filter)
	if err != nil {
		t.Fatal(err)
	}
	batch.Store(true)
	results, err := client.ListGPUOffersBatch(t.Context(), []runpod.GPUOfferFilter{filter})
	if err != nil || results[0].Err != nil || !reflect.DeepEqual(single, results[0].Offers) {
		t.Fatalf("batch changed exact decoded offer: %+v %v vs %+v", results, err, single)
	}
}

func TestBatchGPUKeepsGoodAliasWhenAnotherPriceIsInexact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"gpuTypes":[{"id":"one","secureCloud":true,"q0_secure":{"uninterruptablePrice":"1.000001","stockStatus":"High"},"q1_secure":{"uninterruptablePrice":"0.0000001","stockStatus":"High"}}]}}`))
	}))
	defer server.Close()
	client := mustClient(t, "fixture", runpod.WithGraphQLBaseURL(server.URL))
	results, err := client.ListGPUOffersBatch(t.Context(), []runpod.GPUOfferFilter{{GPUCount: 1, CloudType: "SECURE"}, {GPUCount: 2, CloudType: "SECURE"}})
	if err != nil || len(results) != 2 || results[0].Err != nil || len(results[0].Offers) != 1 || results[1].Err == nil {
		t.Fatalf("price alias failure changed sibling: %+v %v", results, err)
	}
}

func TestEveryBatchHonorsExistingClientCooldown(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "6000")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	client := mustClient(t, "fixture", runpod.WithBaseURL(server.URL), runpod.WithGraphQLBaseURL(server.URL), runpod.WithMaxRetryAttempts(0))
	if err := client.Get(t.Context(), "/pods", nil); !errors.Is(err, runpod.ErrRateLimited) {
		t.Fatal(err)
	}
	_, a := client.ListDataCentersBatch(t.Context(), []runpod.GPUAvailabilityFilter{{GPUCount: 1}})
	_, b := client.ListGPUOffersBatch(t.Context(), []runpod.GPUOfferFilter{{GPUCount: 1}})
	_, c := client.GetCPUOffersBatch(t.Context(), []runpod.CPUOfferRequest{{InstanceID: "cpu5c-2-4", DataCenterID: "DC"}})
	for _, err := range []error{a, b, c} {
		if !errors.Is(err, runpod.ErrRateLimited) {
			t.Fatalf("batch bypassed cooldown: %v", err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("batches sent%d requests during cooldown", hits.Load())
	}
}
