package runpod_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func inventoryRequestFixture() runpod.InventoryRequest {
	var request runpod.InventoryRequest
	for _, width := range []int{1, 2, 4, 6, 8} {
		request.Availability = append(request.Availability, runpod.GPUAvailabilityFilter{GPUCount: width, MinCUDAVersion: "13.0"})
	}
	request.GPUOffers = []runpod.GPUOfferFilter{
		{GPUCount: 1, CloudType: "SECURE", DataCenterID: "DC-1", IDs: []string{"one"}},
		{GPUCount: 2, CloudType: "COMMUNITY", IDs: []string{"one"}},
		{GPUCount: 4, DataCenterID: "DC-2", IDs: []string{"one"}},
	}
	for _, vcpus := range []int{2, 4, 8, 16} {
		for dc := range 26 {
			request.CPUOffers = append(request.CPUOffers, runpod.CPUOfferRequest{InstanceID: fmt.Sprintf("cpu5c-%d-%d", vcpus, vcpus*2), DataCenterID: fmt.Sprintf("DC-%d", dc)})
		}
	}
	return request
}

func inventoryResponseFixture(t *testing.T, w http.ResponseWriter, r *http.Request, failedRoot string) {
	t.Helper()
	var request struct {
		Query     string
		Variables map[string]json.RawMessage
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		t.Error(err)
		http.Error(w, "request", 400)
		return
	}
	data := make(map[string]interface{})
	if strings.Contains(request.Query, "dataCenters{") {
		if strings.Count(request.Query, "dataCenters{") != 1 {
			t.Error("repeated census root")
		}
		row := map[string]interface{}{"id": "DC-1", "name": "one", "storageSupport": true}
		for i := 0; ; i++ {
			raw, exists := request.Variables[fmt.Sprintf("f%d", i)]
			if !exists {
				break
			}
			var filter runpod.GPUAvailabilityFilter
			if err := json.Unmarshal(raw, &filter); err != nil || filter.GPUCount <= 0 || filter.MinCUDAVersion != "13.0" {
				t.Errorf("invalid census variable%d: %s", i, raw)
			}
			row[fmt.Sprintf("q%d", i)] = []map[string]interface{}{{"gpuTypeId": "one", "available": true, "stockStatus": "High"}}
		}
		if failedRoot == "dataCenters" {
			row["q0"] = nil
		}
		data["dataCenters"] = []interface{}{row}
	}
	if strings.Contains(request.Query, "gpuTypes{") {
		if strings.Count(request.Query, "gpuTypes{") != 1 {
			t.Error("repeated GPU root")
		}
		row := map[string]interface{}{"id": "one", "displayName": "One", "memoryInGb": 80, "secureCloud": true, "communityCloud": true}
		for i := 0; ; i++ {
			raw, exists := request.Variables[fmt.Sprintf("v%d_gpuCount", i)]
			if !exists {
				break
			}
			var width int
			_ = json.Unmarshal(raw, &width)
			for _, cloud := range []string{"secure", "community"} {
				alias := fmt.Sprintf("q%d_%s", i, cloud)
				if strings.Contains(request.Query, alias+":") {
					row[alias] = map[string]interface{}{"uninterruptablePrice": fmt.Sprintf("%d.000001", width), "stockStatus": "High"}
				}
			}
		}
		data["gpuTypes"] = []interface{}{row}
	}
	if strings.Contains(request.Query, "cpuFlavors{") {
		if strings.Count(request.Query, "cpuFlavors{") != 1 {
			t.Error("repeated CPU root")
		}
		row := map[string]interface{}{"id": "cpu5c", "minVcpu": 1, "maxVcpu": 64, "ramMultiplier": 2, "diskLimitPerVcpu": 10}
		for i := 0; ; i++ {
			raw, exists := request.Variables[fmt.Sprintf("instanceId%d", i)]
			if !exists {
				break
			}
			var instance, center string
			_ = json.Unmarshal(raw, &instance)
			_ = json.Unmarshal(request.Variables[fmt.Sprintf("dataCenterId%d", i)], &center)
			if instance == "" || center == "" {
				t.Errorf("missing CPU identity%d", i)
			}
			row[fmt.Sprintf("q%d", i)] = map[string]interface{}{"securePrice": "0.070001", "stockStatus": "High"}
		}
		if failedRoot == "cpuFlavors" {
			row["q0"] = nil
		}
		data["cpuFlavors"] = []interface{}{row}
	}
	if strings.Contains(request.Query, "myself{") {
		if strings.Count(request.Query, "myself{") != 1 {
			t.Error("repeated account root")
		}
		data["myself"] = map[string]interface{}{"networkVolumes": []map[string]interface{}{{"id": "volume-one", "name": "cozy-cache-DC-1", "size": 500, "dataCenterId": "DC-1"}}}
	}
	body := map[string]interface{}{"data": data}
	if failedRoot != "" && strings.Contains(request.Query, failedRoot+"{") {
		body["errors"] = []map[string]interface{}{{"message": "one alias unavailable", "path": []interface{}{failedRoot, 0, "q0"}, "extensions": map[string]string{"code": "ONE_ALIAS"}}}
	}
	_ = json.NewEncoder(w).Encode(body)
}

func TestObserveInventoryReadsFiveWidthsGPUPricesAnd104CPUOffers(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); inventoryResponseFixture(t, w, r, "") }))
	defer server.Close()
	client := mustClient(t, "fixture", runpod.WithGraphQLBaseURL(server.URL))
	request := inventoryRequestFixture()
	request.NetworkVolumes = true
	result, err := client.ObserveInventory(t.Context(), request)
	if err != nil || len(result.DataCenters) != 5 || len(result.GPUOffers) != 3 || len(result.CPUOffers) != 104 {
		t.Fatalf("combined observation: counts%d/%d/%d err=%v", len(result.DataCenters), len(result.GPUOffers), len(result.CPUOffers), err)
	}
	for i, item := range result.DataCenters {
		if item.Err != nil || len(item.DataCenters) != 1 || len(item.DataCenters[0].GPUAvailability) != 1 {
			t.Fatalf("census%d: %+v", i, item)
		}
	}
	for i, item := range result.GPUOffers {
		want := 1
		if i == 2 {
			want = 2
		}
		if item.Err != nil || len(item.Offers) != want {
			t.Fatalf("GPU%d: %+v", i, item)
		}
		for _, offer := range item.Offers {
			if offer.GPUCount != request.GPUOffers[i].GPUCount || int64(offer.OnDemandPriceUSDMicrosPerHour) != int64(offer.GPUCount)*1000000+1 {
				t.Fatalf("GPU identity/price changed: %+v", offer)
			}
		}
	}
	for i, item := range result.CPUOffers {
		if item.Err != nil || item.Offer.CPUInstanceID != request.CPUOffers[i].InstanceID || item.Offer.DataCenterID != request.CPUOffers[i].DataCenterID || item.Offer.OnDemandPriceUSDMicrosPerHour != 70001 {
			t.Fatalf("CPU%d: %+v", i, item)
		}
	}
	if result.NetworkVolumesErr != nil || len(result.NetworkVolumes) != 1 || result.NetworkVolumes[0].ID != "volume-one" || result.NetworkVolumes[0].Size != 500 || result.NetworkVolumes[0].DataCenterID != "DC-1" {
		t.Fatalf("volume snapshot: %+v %v", result.NetworkVolumes, result.NetworkVolumesErr)
	}
	// Census with volumes, three prices, and 104 CPU quotes in two.
	if hits.Load() != 4 {
		t.Fatalf("inventory sent %d HTTP requests, want 4", hits.Load())
	}
}

func TestObserveInventoryScopesPartialErrorsByRootAndAlias(t *testing.T) {
	for _, failedRoot := range []string{"dataCenters", "cpuFlavors"} {
		t.Run(failedRoot, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { inventoryResponseFixture(t, w, r, failedRoot) }))
			defer server.Close()
			client := mustClient(t, "fixture", runpod.WithGraphQLBaseURL(server.URL))
			result, err := client.ObserveInventory(t.Context(), inventoryRequestFixture())
			if err != nil {
				t.Fatal(err)
			}
			census, CPU := result.DataCenters[0], result.CPUOffers[0]
			if failedRoot == "dataCenters" {
				if census.Err == nil || CPU.Err != nil || CPU.Offer.OnDemandPriceUSDMicrosPerHour != 70001 {
					t.Fatalf("censusq0 poisoned CPUq0: %+v %+v", census, CPU)
				}
			} else if CPU.Err == nil || census.Err != nil || len(census.DataCenters) != 1 {
				t.Fatalf("CPUq0 poisoned censusq0: %+v %+v", CPU, census)
			}
			if result.CPUOffers[1].Err != nil || result.DataCenters[1].Err != nil || result.GPUOffers[0].Err != nil {
				t.Fatal("partial error poisoned unrelated siblings/roots")
			}
		})
	}
}

func TestObserveInventoryNeverRetriesAndRetainsCooldown(t *testing.T) {
	for _, header := range []string{"", "1", "6000"} {
		t.Run("Retry-After="+header, func(t *testing.T) {
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				if header != "" {
					w.Header().Set("Retry-After", header)
				}
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer server.Close()
			client := mustClient(t, "fixture", runpod.WithGraphQLBaseURL(server.URL), runpod.WithRetryDelay(time.Millisecond))
			calls := 1
			if header == "6000" {
				calls = 2
			}
			for range calls {
				if _, err := client.ObserveInventory(t.Context(), inventoryRequestFixture()); !errors.Is(err, runpod.ErrRateLimited) {
					t.Fatalf("lost429: %v", err)
				}
			}
			if hits.Load() != 1 {
				t.Fatalf("inventory retried during cooldown: HTTP%d", hits.Load())
			}
		})
	}
}

func TestObserveInventoryEmptyOrMixedInvalidRequestSendsNoHTTP(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); http.Error(w, "unexpected", 500) }))
	defer server.Close()
	client := mustClient(t, "fixture", runpod.WithGraphQLBaseURL(server.URL))
	if _, err := client.ObserveInventory(t.Context(), runpod.InventoryRequest{}); err != nil {
		t.Fatal(err)
	}
	request := inventoryRequestFixture()
	request.CPUOffers[0].InstanceID = "invalid"
	if _, err := client.ObserveInventory(t.Context(), request); err == nil {
		t.Fatal("mixed invalid batch passed")
	}
	if hits.Load() != 0 {
		t.Fatalf("empty/invalid inventory sent%d HTTP requests", hits.Load())
	}
}
