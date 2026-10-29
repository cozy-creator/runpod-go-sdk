package runpod_test

import (
	"context"
	"net/http"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func TestInventoryRepeatedWidthsCacheWithoutAliasBatching(t *testing.T) {
	calls := 0
	c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) { calls++; writeGPUs(w, catalogGPU) })
	request := runpod.InventoryRequest{}
	for i := 0; i < 104; i++ {
		request.GPUOffers = append(request.GPUOffers, runpod.GPUOfferFilter{GPUCount: i%4 + 1, CloudType: "SECURE"})
	}
	result, err := c.ObserveInventory(context.Background(), request)
	if err != nil || calls != 4 || len(result.GPUOffers) != 104 {
		t.Fatalf("calls=%d count=%d err=%v", calls, len(result.GPUOffers), err)
	}
	for i, r := range result.GPUOffers {
		if r.Err != nil || len(r.Offers) != 1 || r.Offers[0].GPUCount != i%4+1 {
			t.Fatalf("result[%d]=%+v", i, r)
		}
	}
}
