package runpod_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func TestCPUOfferUsesPerVCPUPriceAndExactShape(t *testing.T) {
	c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/catalog/cpus/cpu5c" || r.URL.Query().Get("vcpuCount") != "4" || r.URL.Query().Get("product") != "POD" || r.URL.Query().Get("include") != "AVAILABILITY" {
			t.Errorf("URL=%s", r.URL)
		}
		fmt.Fprint(w, catalogCPU)
	})
	offer, err := c.GetCPUOffer(context.Background(), runpod.CPUOfferRequest{InstanceID: "cpu5c-4-10", DataCenterID: "dc1"})
	if err != nil || offer.OnDemandPriceUSDMicrosPerHour != 44936 || offer.MemoryInGB != 10 || offer.StockStatus != "MEDIUM" || offer.PriceSource != runpod.CatalogListPrice {
		t.Fatalf("offer=%+v err=%v", offer, err)
	}
}
func TestCPUOfferRefusesIncompleteOrInexactEvidence(t *testing.T) {
	rows := []string{
		strings.Replace(catalogCPU, "0.011234", "0.0000001", 1),
		strings.Replace(catalogCPU, "0.011234", "9223372036854.775807", 1),
		strings.Replace(catalogCPU, `"securePerVcpu":0.011234`, `"securePerVcpu":null`, 1),
		strings.Replace(catalogCPU, `"ramGbPerVcpu":2.5`, `"ramGbPerVcpu":null`, 1),
		strings.Replace(catalogCPU, `"availability":"HIGH"`, `"availability":""`, 1),
		strings.Replace(catalogCPU, `"id":"cpu5c"`, `"id":"cpu3c"`, 1),
	}
	for i, row := range rows {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, row) })
			if _, err := c.GetCPUOffer(context.Background(), runpod.CPUOfferRequest{InstanceID: "cpu5c-4-10", DataCenterID: "dc1"}); err == nil {
				t.Fatal("accepted unusable evidence")
			}
		})
	}
}
func TestCPUOfferRejectsInvalidCPUCountBeforeHTTP(t *testing.T) {
	c := newCatalogClient(t, func(http.ResponseWriter, *http.Request) { t.Fatal("HTTP for invalid request") })
	for _, id := range []string{"cpu5c", "cpu5c-1-2", "cpu5c-3-6", "cpu5c-999999999999999999999-2"} {
		if _, err := c.GetCPUOffer(context.Background(), runpod.CPUOfferRequest{InstanceID: id, DataCenterID: "dc1"}); err == nil {
			t.Fatalf("accepted %s", id)
		}
	}
}
func TestCPUOfferRejectsRAMMismatch(t *testing.T) {
	c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, catalogCPU) })
	if _, err := c.GetCPUOffer(context.Background(), runpod.CPUOfferRequest{InstanceID: "cpu5c-4-8", DataCenterID: "dc1"}); err == nil {
		t.Fatal("accepted RAM mismatch")
	}
}
