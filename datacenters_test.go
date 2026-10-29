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
