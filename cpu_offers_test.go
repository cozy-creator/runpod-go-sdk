package runpod

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGetCPUOffer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !strings.Contains(request.Query, "cpuFlavors") ||
			!strings.Contains(request.Query, "diskLimitPerVcpu") ||
			!strings.Contains(request.Query, "specifics(input: { instanceId: $instanceId, dataCenterId: $dataCenterId })") {
			t.Fatalf("CPU quote query does not use exact specifics input: %s", request.Query)
		}
		if request.Variables["instanceId"] != "cpu5c-2-4" || request.Variables["dataCenterId"] != "US-KS-2" {
			t.Fatalf("CPU quote variables = %#v", request.Variables)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"cpuFlavors":[
			{"id":"cpu3c","displayName":"3C","minVcpu":1,"maxVcpu":16,"ramMultiplier":2,"specifics":null},
			{"id":"cpu5c","displayName":"5C","minVcpu":1,"maxVcpu":32,"ramMultiplier":2,"diskLimitPerVcpu":15,
			 "specifics":{"stockStatus":"High","securePrice":"0.125001"}}
		]}}`)
	}))
	defer server.Close()

	client, err := NewClient("test-key", WithGraphQLBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	offer, err := client.GetCPUOffer(t.Context(), CPUOfferRequest{
		InstanceID: "cpu5c-2-4", DataCenterID: "US-KS-2",
	})
	if err != nil {
		t.Fatalf("GetCPUOffer: %v", err)
	}
	if offer.CPUFamilyID != "cpu5c" || offer.CPUInstanceID != "cpu5c-2-4" ||
		offer.DataCenterID != "US-KS-2" || offer.VCPUCount != 2 || offer.MemoryInGB != 4 ||
		offer.StockStatus != "High" || offer.OnDemandPriceUSDMicrosPerHour != 125_001 || offer.MaxContainerDiskGB != 30 {
		t.Fatalf("CPU offer = %+v", offer)
	}
}

func TestGetCPUOfferPreservesPricedUnknownStock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Live RunPod shape observed on 2026-08-31: securePrice was exact while
		// stockStatus was omitted.
		fmt.Fprint(w, `{"data":{"cpuFlavors":[
			{"id":"cpu5c","displayName":"Compute-Optimized","minVcpu":2,"maxVcpu":32,
			 "ramMultiplier":2,"diskLimitPerVcpu":15,"specifics":{"securePrice":0.07}}
		]}}`)
	}))
	defer server.Close()

	client, err := NewClient("test-key", WithGraphQLBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	offer, err := client.GetCPUOffer(t.Context(), CPUOfferRequest{
		InstanceID: "cpu5c-2-4", DataCenterID: "EU-RO-1",
	})
	if err != nil {
		t.Fatalf("GetCPUOffer: %v", err)
	}
	if offer.StockStatus != CPUStockStatusUnknown || offer.OnDemandPriceUSDMicrosPerHour != 70_000 || offer.MaxContainerDiskGB != 30 {
		t.Fatalf("CPU offer = %+v", offer)
	}
}

func TestGetCPUOfferRefusesInexactPrice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"cpuFlavors":[{"id":"cpu5c","minVcpu":1,"maxVcpu":32,"ramMultiplier":2,"diskLimitPerVcpu":15,
			"specifics":{"stockStatus":"High","securePrice":0.0000001}}]}}`)
	}))
	defer server.Close()
	client, _ := NewClient("test-key", WithGraphQLBaseURL(server.URL))
	if _, err := client.GetCPUOffer(t.Context(), CPUOfferRequest{
		InstanceID: "cpu5c-2-4", DataCenterID: "US-KS-2",
	}); err == nil {
		t.Fatal("sub-micro CPU price was admitted")
	}
}

func TestGetCPUOfferValidatesExactShape(t *testing.T) {
	client, _ := NewClient("test-key")
	for _, request := range []CPUOfferRequest{
		{InstanceID: "cpu5c", DataCenterID: "US-KS-2"},
		{InstanceID: "cpu9c-2-4", DataCenterID: "US-KS-2"},
		{InstanceID: "cpu5c-2-4"},
	} {
		if _, err := client.GetCPUOffer(t.Context(), request); err == nil {
			t.Fatalf("invalid CPU quote request was admitted: %+v", request)
		}
	}
}

func TestGetCPUOfferDiskLimit(t *testing.T) {
	for _, test := range []struct {
		instance, field string
		want            int
	}{
		{"cpu3c-2-4", `,"diskLimitPerVcpu":10`, 20},
		{"cpu5c-2-4", `,"diskLimitPerVcpu":15`, 30},
		{"cpu3c-8-16", `,"diskLimitPerVcpu":10`, 80},
		{"cpu5c-4-8", `,"diskLimitPerVcpu":15`, 60},
		// The provider value, not a second family table, determines the result.
		{"cpu5c-2-4", `,"diskLimitPerVcpu":11`, 22},
		{"cpu5c-3-6", `,"diskLimitPerVcpu":12.5`, 37},
		{"cpu5c-2-4", ``, 0},
		{"cpu5c-2-4", `,"diskLimitPerVcpu":null`, 0},
		{"cpu5c-2-4", `,"diskLimitPerVcpu":0`, 0},
		{"cpu5c-2-4", `,"diskLimitPerVcpu":-15`, 0},
		{"cpu5c-2-4", `,"diskLimitPerVcpu":0.1`, 0},
		{"cpu5c-2-4", `,"diskLimitPerVcpu":"NaN"`, 0},
		{"cpu5c-2-4", `,"diskLimitPerVcpu":true`, 0},
		{"cpu5c-2-4", `,"diskLimitPerVcpu":1e999`, 0},
		{"cpu5c-2-4", `,"diskLimitPerVcpu":1e308`, 0},
		// The product is the first nonrepresentable positive int on this platform.
		{"cpu5c-2-4", `,"diskLimitPerVcpu":` + strconv.FormatFloat(math.Ldexp(1, strconv.IntSize-2), 'g', -1, 64), 0},
	} {
		t.Run(test.instance+test.field, func(t *testing.T) {
			var calls atomic.Int32
			family := strings.Split(test.instance, "-")[0]
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var request struct {
					Query string `json:"query"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if !strings.Contains(request.Query, "diskLimitPerVcpu") {
					t.Error("disk limit missing from existing offer query")
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"data":{"cpuFlavors":[{"id":%q,"minVcpu":1,"maxVcpu":32,"ramMultiplier":2%s,"specifics":{"stockStatus":"High","securePrice":"0.07"}}]}}`, family, test.field)
			}))
			defer server.Close()
			client, err := NewClient("test-key", WithGraphQLBaseURL(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			offer, err := client.GetCPUOffer(t.Context(), CPUOfferRequest{InstanceID: test.instance, DataCenterID: "US-KS-2"})
			if test.want == 0 {
				if err == nil {
					t.Fatalf("invalid provider disk limit accepted: %+v", offer)
				}
			} else if err != nil || offer.MaxContainerDiskGB != test.want {
				t.Fatalf("offer=%+v err=%v; want disk %dGB", offer, err, test.want)
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("made %d requests for one CPU offer", got)
			}
		})
	}
}

func TestCPUReadbackCoherence(t *testing.T) {
	graphQLVCPU := 2
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `{"id":"cpu-pod","name":"cpu","desiredStatus":"RUNNING","imageName":"image@sha256:abc",
				"gpuCount":0,"cpuFlavorId":"cpu5c","vcpuCount":2,"memoryInGb":4,"machineId":"machine-1",
				"machine":{"id":"machine-1","gpuTypeId":"unknown","dataCenterId":"US-KS-2"},"costPerHr":"0.125001"}`)
			return
		}
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(request.Query, "cpuFlavorId") {
			t.Fatalf("GraphQL readback omits cpuFlavorId: %s", request.Query)
		}
		fmt.Fprintf(w, `{"data":{"pod":{"id":"cpu-pod","name":"cpu","desiredStatus":"RUNNING",
			"imageName":"image@sha256:abc","gpuCount":0,"cpuFlavorId":"cpu5c","vcpuCount":%d,
			"memoryInGb":4,"machineId":"machine-1","machine":{"id":"machine-1","gpuTypeId":"unknown",
			"dataCenterId":"US-KS-2"},"costPerHr":"0.125001"}}}`, graphQLVCPU)
	}))
	defer server.Close()
	client, _ := NewClient("test-key", WithBaseURL(server.URL), WithGraphQLBaseURL(server.URL))

	readback, err := client.GetPodReadback(t.Context(), "cpu-pod", nil)
	if err != nil {
		t.Fatalf("GetPodReadback: %v", err)
	}
	for _, field := range []string{"cpu_flavor_id", "vcpu_count", "memory_in_gb"} {
		if status := readbackStatus(readback.Checks, field); status != PodReadbackCheckAgree {
			t.Fatalf("%s status = %s; checks=%+v", field, status, readback.Checks)
		}
	}

	graphQLVCPU = 4
	readback, err = client.GetPodReadback(t.Context(), "cpu-pod", nil)
	if err != nil {
		t.Fatalf("GetPodReadback mismatch: %v", err)
	}
	if readback.Coherence != PodReadbackConflicting ||
		readbackStatus(readback.Checks, "vcpu_count") != PodReadbackCheckDisagree {
		t.Fatalf("CPU vCPU mismatch was not preserved: %+v", readback)
	}
}

func readbackStatus(checks []PodReadbackCheck, field string) PodReadbackCheckStatus {
	for _, check := range checks {
		if check.Field == field {
			return check.Status
		}
	}
	return "absent"
}
