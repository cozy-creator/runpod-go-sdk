//go:build live

package runpod_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

// liveClient only reads explicitly supplied credentials. It never loads .env.
func liveClient(t *testing.T) *runpod.Client {
	t.Helper()
	key := os.Getenv("RUNPOD_API_KEY")
	if key == "" {
		t.Skip("live test: RUNPOD_API_KEY not set")
	}
	return mustClient(t, key)
}
func liveReadContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func TestNetworkVolumesLive(t *testing.T) {
	volumes, err := liveClient(t).ListNetworkVolumes(liveReadContext(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range volumes {
		if v.ID == "" || v.DataCenter == "" {
			t.Fatalf("incomplete volume=%+v", v)
		}
		t.Logf("volume=%s size=%dGB datacenter=%s type=%s", v.ID, v.Size, v.DataCenter, v.Type)
	}
}
func TestContainerRegistryAuthsLive(t *testing.T) {
	auths, err := liveClient(t).ListContainerRegistryAuths(liveReadContext(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, auth := range auths {
		if auth.ID == "" {
			t.Fatal("registry missing id")
		}
		t.Logf("registry=%s name=%s", auth.ID, auth.Name)
	}
}
func TestGPUCatalogIDsLive(t *testing.T) {
	gpus, err := liveClient(t).ListGPUTypes(liveReadContext(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, gpu := range gpus {
		ids[strings.ToLower(gpu.ID)] = true
	}
	for _, spec := range runpod.GPUCatalog() {
		if !ids[strings.ToLower(spec.ID)] {
			t.Errorf("static GPU type %q missing from v2 catalog", spec.ID)
		}
	}
}
func TestGPUOffersLive(t *testing.T) {
	offers, err := liveClient(t).ListGPUOffers(liveReadContext(t), &runpod.GPUOfferFilter{InStockOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	// An empty market is valid provider evidence, not a test failure.
	for _, offer := range offers[:min(len(offers), 5)] {
		if offer.PriceSource != runpod.CatalogListPrice {
			t.Fatalf("missing catalog price provenance: %+v", offer)
		}
		t.Logf("GPU=%s cloud=%s stock=%s rate=%d USD micros/hour source=%s", offer.GPUTypeID, offer.CloudType, offer.StockStatus, offer.OnDemandPriceUSDMicrosPerHour, offer.PriceSource)
	}
}
func TestExistingPodsReadOnlyLive(t *testing.T) {
	client := liveClient(t)
	ctx := liveReadContext(t)
	pods, err := client.ListPods(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, pod := range pods[:min(len(pods), 3)] {
		snapshot, err := client.GetPodReadback(ctx, pod.ID)
		if err != nil {
			t.Fatalf("pod %s: %v", pod.ID, err)
		}
		if snapshot.REST.ID != pod.ID {
			t.Fatal("foreign pod response")
		}
		t.Logf("pod=%s status=%s", pod.ID, snapshot.REST.Status)
	}
}
