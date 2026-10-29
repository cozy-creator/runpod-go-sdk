package runpod_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func TestDataCentersJoinScopedV2AvailabilityAndStorageFacts(t *testing.T) {
	calls := 0
	c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/v2/catalog/datacenters":
			fmt.Fprint(w, catalogDCs)
		case "/v2/catalog/gpus":
			if r.URL.Query().Get("count") != "4" || r.URL.Query().Get("minCudaVersion") != "12.8" {
				t.Errorf("query=%s", r.URL.RawQuery)
			}
			writeGPUs(w, catalogGPU)
		default:
			t.Errorf("path=%s", r.URL.Path)
		}
	})
	dcs, err := c.ListDataCenters(context.Background(), &runpod.GPUAvailabilityFilter{GPUCount: 4, MinCUDAVersion: "12.8"})
	if err != nil || len(dcs) != 1 || calls != 3 {
		t.Fatalf("dcs=%+v calls=%d err=%v", dcs, calls, err)
	}
	if !dcs[0].StorageSupport || dcs[0].Location != "EUROPE" || len(dcs[0].GPUAvailability) != 2 || dcs[0].GPUAvailability[0].CloudType != "SECURE" || dcs[0].GPUAvailability[1].CloudType != "COMMUNITY" {
		t.Fatalf("dcs=%+v", dcs)
	}
}
func TestDataCentersRefuseMissingStorageFacts(t *testing.T) {
	body := strings.Replace(catalogDCs, `"networkVolumeTypes":["STANDARD"],`, "", 1)
	c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
	if _, err := c.ListDataCenters(context.Background(), nil); err == nil {
		t.Fatal("unknown storage became unsupported")
	}
}
func TestDataCentersRejectUnknownJoin(t *testing.T) {
	c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "datacenters") {
			fmt.Fprint(w, catalogDCs)
		} else {
			writeGPUs(w, strings.Replace(catalogGPU, `"id":"dc1"`, `"id":"other"`, 1))
		}
	})
	if _, err := c.ListDataCenters(context.Background(), nil); err == nil {
		t.Fatal("accepted unknown datacenter join")
	}
}

func TestDataCenterProjectionUsesOnlyExplicitScopedRows(t *testing.T) {
	// A real v2 observation reported global stock without optional location
	// details. Preserve other located GPUs; global stock cannot name a DC.
	unlocated := strings.Replace(catalogGPU, `"id":"gpu/a"`, `"id":"gpu/unlocated"`, 1)
	unlocated = strings.Replace(unlocated, `,"dataCenters":[{"id":"dc1","name":"DC 1","availability":"LOW"}]`, "", 1)
	c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "datacenters") {
			fmt.Fprint(w, catalogDCs)
		} else {
			fmt.Fprintf(w, `{"gpus":[%s,%s]}`, unlocated, catalogGPU)
		}
	})
	secure := true
	rows, err := c.ListDataCenters(t.Context(), &runpod.GPUAvailabilityFilter{SecureCloud: &secure, GPUCount: 1})
	if err != nil || len(rows) != 1 || len(rows[0].GPUAvailability) != 1 || rows[0].GPUAvailability[0].GPUTypeID != "gpu/a" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	// An exact-location quote still cannot turn missing detail into stock or
	// known absence merely because global availability is positive.
	if _, err := c.ListGPUOffers(t.Context(), &runpod.GPUOfferFilter{IDs: []string{"gpu/unlocated"}, CloudType: "SECURE", DataCenterID: "dc1"}); err == nil {
		t.Fatal("invented scoped stock from global-only evidence")
	}
}
