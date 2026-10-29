package runpod_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func TestInventoryNeverRetriesFailedRequestAndRetainsPartialResults(t *testing.T) {
	calls := 0
	c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("count") == "2" {
			w.WriteHeader(500)
			fmt.Fprint(w, `{"title":"Failure","status":500,"detail":"failed"}`)
			return
		}
		writeGPUs(w, catalogGPU)
	})
	result, err := c.ObserveInventory(context.Background(), runpod.InventoryRequest{GPUOffers: []runpod.GPUOfferFilter{{GPUCount: 2, CloudType: "SECURE"}, {GPUCount: 1, CloudType: "SECURE"}}})
	if err != nil || calls != 2 || result.GPUOffers[0].Err == nil || result.GPUOffers[1].Err != nil {
		t.Fatalf("calls=%d result=%+v err=%v", calls, result, err)
	}
}
func TestInventoryStopsAfterRateLimitAndPreservesScopedErrors(t *testing.T) {
	calls := 0
	c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(429)
		fmt.Fprint(w, `{"title":"Limited","status":429,"detail":"quota"}`)
	})
	result, err := c.ObserveInventory(context.Background(), runpod.InventoryRequest{GPUOffers: []runpod.GPUOfferFilter{{GPUCount: 1, CloudType: "SECURE"}, {GPUCount: 2, CloudType: "SECURE"}}, CPUOffers: []runpod.CPUOfferRequest{{InstanceID: "cpu5c-4-10", DataCenterID: "dc1"}}})
	if !errors.Is(err, runpod.ErrRateLimited) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	if len(result.GPUOffers) != 2 || !errors.Is(result.GPUOffers[1].Err, runpod.ErrRateLimited) || !errors.Is(result.CPUOffers[0].Err, runpod.ErrRateLimited) || c.Cooldown() <= 0 {
		t.Fatalf("result=%+v", result)
	}
}
func TestInventoryCancellationSendsNoHTTP(t *testing.T) {
	c := newCatalogClient(t, func(http.ResponseWriter, *http.Request) { t.Fatal("HTTP after cancellation") })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := c.ObserveInventory(ctx, runpod.InventoryRequest{GPUOffers: []runpod.GPUOfferFilter{{CloudType: "SECURE"}}})
	if !errors.Is(err, context.Canceled) || !errors.Is(result.GPUOffers[0].Err, context.Canceled) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
