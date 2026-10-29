package runpod_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func TestGPUOffersScopeBothCloudsAndMultiplyPerGPURates(t *testing.T) {
	calls := 0
	c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("count") != "2" {
			t.Error("missing count filter")
		}
		if calls == 1 && r.URL.Query().Get("cloud") != "SECURE" {
			t.Error("first cloud")
		}
		if calls == 2 && r.URL.Query().Get("cloud") != "COMMUNITY" {
			t.Error("second cloud")
		}
		writeGPUs(w, catalogGPU)
	})
	offers, err := c.ListGPUOffers(context.Background(), &runpod.GPUOfferFilter{GPUCount: 2, DataCenterID: "dc1"})
	if err != nil || len(offers) != 2 || calls != 2 {
		t.Fatalf("offers=%+v calls=%d err=%v", offers, calls, err)
	}
	if offers[0].CloudType != "COMMUNITY" || offers[0].OnDemandPriceUSDMicrosPerHour != 460000 || offers[1].OnDemandPriceUSDMicrosPerHour != 620000 || offers[0].StockStatus != "LOW" || offers[0].PriceSource != runpod.CatalogListPrice {
		t.Fatalf("offers=%+v", offers)
	}
}
func TestGPUOffersMissingPriceIsUnavailable(t *testing.T) {
	row := strings.Replace(catalogGPU, `"secure":0.31`, `"secure":null`, 1)
	c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) { writeGPUs(w, row) })
	offers, err := c.ListGPUOffers(context.Background(), nil)
	if err != nil || len(offers) != 1 || offers[0].CloudType != "COMMUNITY" {
		t.Fatalf("offers=%+v err=%v", offers, err)
	}
}
func TestGPUOffersRefuseInexactOrOverflowingMoney(t *testing.T) {
	for _, price := range []string{"0.0000001", "-0.1", "9223372036854.775808", "9223372036854.775807"} {
		t.Run(price, func(t *testing.T) {
			c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) {
				writeGPUs(w, strings.Replace(catalogGPU, "0.31", price, 1))
			})
			if _, err := c.ListGPUOffers(context.Background(), &runpod.GPUOfferFilter{CloudType: "SECURE", GPUCount: 2}); err == nil {
				t.Fatal("accepted inexact/overflow price")
			}
		})
	}
}
func TestGPUOfferStockNeverUsesMaxCount(t *testing.T) {
	row := strings.Replace(catalogGPU, `"availability":"HIGH"`, `"availability":"NONE"`, 1)
	c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) { writeGPUs(w, row) })
	offers, err := c.ListGPUOffers(context.Background(), &runpod.GPUOfferFilter{CloudType: "SECURE", InStockOnly: true})
	if err != nil || len(offers) != 0 {
		t.Fatalf("offers=%+v err=%v", offers, err)
	}
}
func TestGPUOffersRefuseUnknownDatacenterAvailability(t *testing.T) {
	row := strings.Replace(catalogGPU, `,"dataCenters":[{"id":"dc1","name":"DC 1","availability":"LOW"}]`, "", 1)
	c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) { writeGPUs(w, row) })
	if _, err := c.ListGPUOffers(context.Background(), &runpod.GPUOfferFilter{CloudType: "SECURE", DataCenterID: "dc1"}); err == nil {
		t.Fatal("missing datacenters became NONE")
	}
}
func TestGPUOffersMissingSelectedDatacenterIsKnownUnavailable(t *testing.T) {
	c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) { writeGPUs(w, catalogGPU) })
	offers, err := c.ListGPUOffers(context.Background(), &runpod.GPUOfferFilter{CloudType: "SECURE", DataCenterID: "other", InStockOnly: true})
	if err != nil || len(offers) != 0 {
		t.Fatalf("offers=%+v err=%v", offers, err)
	}
}
