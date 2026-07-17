package runpod_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func TestListGPUOffers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string                 `json:"query"`
			Variables map[string]interface{} `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !strings.Contains(req.Query, "secure: lowestPrice") || !strings.Contains(req.Query, "community: lowestPrice") {
			t.Fatalf("offers query must alias lowestPrice per cloud: %s", req.Query)
		}
		if req.Variables["gpuCount"] != float64(2) {
			t.Fatalf("gpuCount variable not passed: %v", req.Variables)
		}
		if req.Variables["dataCenterId"] != "US-KS-2" || req.Variables["minMemoryInGb"] != float64(64) || req.Variables["minVcpuCount"] != float64(8) {
			t.Fatalf("lowestPrice variables = %#v", req.Variables)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"gpuTypes":[
			{"id":"gpu-both","displayName":"Both","memoryInGb":24,"secureCloud":true,"communityCloud":true,
			 "secure":{"minimumBidPrice":0.2,"uninterruptablePrice":0.6,"stockStatus":"High"},
			 "community":{"minimumBidPrice":0.1,"uninterruptablePrice":0.4,"stockStatus":"Low"}},
			{"id":"gpu-secure-only","displayName":"SecureOnly","memoryInGb":80,"secureCloud":true,"communityCloud":false,
			 "secure":{"minimumBidPrice":1.0,"uninterruptablePrice":2.0,"stockStatus":"High"},
			 "community":null},
			{"id":"gpu-out","displayName":"Out","memoryInGb":16,"secureCloud":true,"communityCloud":false,
			 "secure":{"minimumBidPrice":0.05,"uninterruptablePrice":0.2,"stockStatus":"Unavailable"},
			 "community":null}
		]}}`))
	}))
	defer server.Close()

	client := mustClient(t, "test_key", runpod.WithGraphQLBaseURL(server.URL))
	offers, err := client.ListGPUOffers(context.Background(), &runpod.GPUOfferFilter{
		GPUCount: 2, InStockOnly: true, DataCenterID: "US-KS-2", MinMemoryInGB: 64, MinVCPUCount: 8,
	})
	if err != nil {
		t.Fatalf("ListGPUOffers: %v", err)
	}

	// gpu-out excluded (unavailable); gpu-both yields two offers; sorted by
	// on-demand price ascending.
	if len(offers) != 3 {
		t.Fatalf("expected 3 offers, got %d: %+v", len(offers), offers)
	}
	if offers[0].GPUTypeID != "gpu-both" || offers[0].CloudType != "COMMUNITY" || offers[0].OnDemandPrice != 0.4 {
		t.Errorf("cheapest offer wrong: %+v", offers[0])
	}
	if offers[1].CloudType != "SECURE" || offers[1].GPUTypeID != "gpu-both" {
		t.Errorf("second offer wrong: %+v", offers[1])
	}
	if offers[2].GPUTypeID != "gpu-secure-only" || offers[2].MinimumBidPrice != 1.0 {
		t.Errorf("third offer wrong: %+v", offers[2])
	}
	for _, offer := range offers {
		if offer.GPUCount != 2 {
			t.Fatalf("offer GPUCount = %d, want 2: %+v", offer.GPUCount, offer)
		}
	}
}

func TestListGPUOffersRejectsInvalidShape(t *testing.T) {
	client := mustClient(t, "test_key")
	_, err := client.ListGPUOffers(t.Context(), &runpod.GPUOfferFilter{MinDisk: -1})
	var validation *runpod.ValidationError
	if !errors.As(err, &validation) || validation.Field != "minDisk" {
		t.Fatalf("error = %v", err)
	}
}

func TestBidPerGPUValidation(t *testing.T) {
	client := mustClient(t, "test_key")
	ctx := context.Background()

	req := &runpod.CreatePodRequest{
		Name:              "n",
		ImageName:         "img",
		GPUTypeIDs:        []string{"A"},
		GPUCount:          1,
		ContainerDiskInGB: 10,
		BidPerGPU:         0.25,
	}
	if _, err := client.CreatePod(ctx, req); err == nil {
		t.Fatal("BidPerGPU without Interruptible must fail validation")
	}

	req.BidPerGPU = -1
	req.Interruptible = true
	if _, err := client.CreatePod(ctx, req); err == nil {
		t.Fatal("negative BidPerGPU must fail validation")
	}
}

func TestCreateSpotPodSendsBid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body["interruptible"] != true {
			t.Fatalf("interruptible not set: %v", body)
		}
		if body["bidPerGpu"] != 0.25 {
			t.Fatalf("bidPerGpu not marshalled: %v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"pod-spot","interruptible":true}`))
	}))
	defer server.Close()

	client := mustClient(t, "test_key", runpod.WithBaseURL(server.URL))
	pod, err := client.CreateSpotPod(context.Background(), &runpod.CreatePodRequest{
		Name:              "n",
		ImageName:         "img",
		GPUTypeIDs:        []string{"A"},
		GPUCount:          1,
		ContainerDiskInGB: 10,
		BidPerGPU:         0.25,
	})
	if err != nil {
		t.Fatalf("CreateSpotPod: %v", err)
	}
	if pod.ID != "pod-spot" {
		t.Fatalf("unexpected pod %+v", pod)
	}
}
