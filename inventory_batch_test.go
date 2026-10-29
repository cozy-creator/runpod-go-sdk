package runpod_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func TestInventoryReusesIdenticalV2ReadsAcrossViews(t *testing.T) {
	paths := map[string]int{}
	c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths[r.URL.String()]++
		switch {
		case strings.HasSuffix(r.URL.Path, "datacenters"):
			fmt.Fprint(w, catalogDCs)
		case strings.HasSuffix(r.URL.Path, "gpus"):
			writeGPUs(w, catalogGPU)
		case strings.Contains(r.URL.Path, "/cpus/"):
			fmt.Fprint(w, catalogCPU)
		default:
			t.Errorf("path=%s", r.URL.Path)
		}
	})
	secure := true
	request := runpod.InventoryRequest{
		Availability: []runpod.GPUAvailabilityFilter{{GPUCount: 2, SecureCloud: &secure}, {GPUCount: 2, SecureCloud: &secure}},
		GPUOffers:    []runpod.GPUOfferFilter{{GPUCount: 2, CloudType: "SECURE"}, {GPUCount: 2, CloudType: "SECURE", IDs: []string{"gpu/a"}}},
		CPUOffers:    []runpod.CPUOfferRequest{{InstanceID: "cpu5c-4-10", DataCenterID: "dc1"}, {InstanceID: "cpu5c-4-10", DataCenterID: "other"}},
	}
	result, err := c.ObserveInventory(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 {
		t.Fatalf("expected base DC, GPU and CPU reads: %v", paths)
	}
	for path, count := range paths {
		if count != 1 {
			t.Errorf("%s read %d times", path, count)
		}
	}
	if result.DataCenters[1].Err != nil || result.GPUOffers[1].Err != nil || len(result.GPUOffers[1].Offers) != 1 || result.CPUOffers[0].Offer.StockStatus != "MEDIUM" || result.CPUOffers[1].Offer.StockStatus != "NONE" {
		t.Fatalf("result=%+v", result)
	}
}
func TestInventoryValidationAndEmptyPassSendNoHTTP(t *testing.T) {
	c := newCatalogClient(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected HTTP") })
	if _, err := c.ObserveInventory(context.Background(), runpod.InventoryRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ObserveInventory(context.Background(), runpod.InventoryRequest{GPUOffers: []runpod.GPUOfferFilter{{CloudType: "SECURE"}}, CPUOffers: []runpod.CPUOfferRequest{{InstanceID: "bad"}}}); err == nil {
		t.Fatal("accepted invalid input")
	}
}
func TestInventoryKeepsGoodResultWhenAnotherPriceIsInexact(t *testing.T) {
	c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) {
		row := catalogGPU
		if r.URL.Query().Get("count") == "2" {
			row = strings.Replace(row, "0.31", "0.0000001", 1)
		}
		writeGPUs(w, row)
	})
	result, err := c.ObserveInventory(context.Background(), runpod.InventoryRequest{GPUOffers: []runpod.GPUOfferFilter{{CloudType: "SECURE", GPUCount: 1}, {CloudType: "SECURE", GPUCount: 2}}})
	if err != nil || result.GPUOffers[0].Err != nil || len(result.GPUOffers[0].Offers) != 1 || result.GPUOffers[1].Err == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
