package runpod_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func TestInventoryVolumeFailureDoesNotEraseMarketResults(t *testing.T) {
	c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "network-volumes") {
			fmt.Fprint(w, `{"networkVolumes":null,"pagination":{"nextCursor":null,"hasNextPage":false}}`)
			return
		}
		writeGPUs(w, catalogGPU)
	})
	result, err := c.ObserveInventory(context.Background(), runpod.InventoryRequest{NetworkVolumes: true, GPUOffers: []runpod.GPUOfferFilter{{CloudType: "SECURE"}}})
	if err != nil || result.NetworkVolumesErr == nil || result.GPUOffers[0].Err != nil || len(result.GPUOffers[0].Offers) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
func TestInventoryVolumeSnapshotWalksEveryCursor(t *testing.T) {
	calls := 0
	c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v2/network-volumes" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if r.URL.Query().Get("cursor") == "next" {
			fmt.Fprint(w, `{"networkVolumes":[],"pagination":{"nextCursor":null,"hasNextPage":false}}`)
			return
		}
		fmt.Fprint(w, `{"networkVolumes":[{"id":"v1","name":"Models","size":100,"dataCenter":"dc1","type":"STANDARD"}],"pagination":{"nextCursor":"next","hasNextPage":true}}`)
	})
	result, err := c.ObserveInventory(context.Background(), runpod.InventoryRequest{NetworkVolumes: true})
	if err != nil || result.NetworkVolumesErr != nil || len(result.NetworkVolumes) != 1 || calls != 2 {
		t.Fatalf("calls=%d result=%+v err=%v", calls, result, err)
	}
}
func TestInventoryDoesNotRequestOptionalVolumes(t *testing.T) {
	c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "network-volumes") {
			t.Fatal("unrequested volumes")
		}
		writeGPUs(w, catalogGPU)
	})
	if _, err := c.ObserveInventory(context.Background(), runpod.InventoryRequest{GPUOffers: []runpod.GPUOfferFilter{{CloudType: "SECURE"}}}); err != nil {
		t.Fatal(err)
	}
}
