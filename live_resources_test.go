//go:build live

package runpod_test

import (
	"context"
	"os"
	"strings"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func liveClient(t *testing.T) *runpod.Client {
	t.Helper()
	apiKey := os.Getenv("RUNPOD_API_KEY")
	if apiKey == "" {
		t.Skip("live test: RUNPOD_API_KEY not set")
	}
	return mustClient(t, apiKey)
}

// TestNetworkVolumesLive verifies REST parity against the real API.
func TestNetworkVolumesLive(t *testing.T) {
	client := liveClient(t)
	volumes, err := client.ListNetworkVolumes(context.Background())
	if err != nil {
		t.Fatalf("live list network volumes: %v", err)
	}
	for _, v := range volumes {
		if v.ID == "" {
			t.Fatalf("live volume with empty id: %+v", v)
		}
		t.Logf("volume id=%s name=%s size=%dGB dc=%s", v.ID, v.Name, v.Size, v.DataCenterID)
	}
}

// TestContainerRegistryAuthsLive verifies the REST endpoint against the real API.
func TestContainerRegistryAuthsLive(t *testing.T) {
	client := liveClient(t)
	auths, err := client.ListContainerRegistryAuths(context.Background())
	if err != nil {
		t.Fatalf("live list container registry auths: %v", err)
	}
	for _, a := range auths {
		t.Logf("registry auth id=%s name=%s", a.ID, a.Name)
	}
}

// TestGPUCatalogIDsLive verifies every static catalog entry's type ID exists
// in RunPod's live gpuTypes catalog.
func TestGPUCatalogIDsLive(t *testing.T) {
	client := liveClient(t)
	gpus, err := client.ListGPUTypes(context.Background(), nil)
	if err != nil {
		t.Fatalf("live ListGPUTypes: %v", err)
	}
	liveIDs := map[string]bool{}
	for _, g := range gpus {
		liveIDs[strings.ToLower(strings.TrimSpace(g.ID))] = true
	}
	for _, spec := range runpod.GPUCatalog() {
		if !liveIDs[strings.ToLower(spec.ID)] {
			t.Errorf("catalog GPU type %q not found in live gpuTypes", spec.ID)
		}
	}
}

// TestGPUOffersLive verifies the aliased per-cloud lowestPrice query
// validates against the real GraphQL schema.
func TestGPUOffersLive(t *testing.T) {
	client := liveClient(t)
	offers, err := client.ListGPUOffers(context.Background(), &runpod.GPUOfferFilter{InStockOnly: true})
	if err != nil {
		t.Fatalf("live ListGPUOffers: %v", err)
	}
	if len(offers) == 0 {
		t.Fatal("expected at least one in-stock offer")
	}
	for _, o := range offers[:min(len(offers), 5)] {
		t.Logf("offer %s cloud=%s stock=%s ondemand=$%.3f minbid=$%.3f",
			o.GPUTypeID, o.CloudType, o.StockStatus, o.OnDemandPrice, o.MinimumBidPrice)
	}
}

// TestSpotPodReclaimLive creates a cheap interruptible pod with a bid and
// polls it briefly. Costs real money: additionally gated on
// RUNPOD_LIVE_SPOT=1. Reclaim rarely happens inside the poll window; the
// test asserts the create/bid path works and logs observed transitions.
func TestSpotPodReclaimLive(t *testing.T) {
	if os.Getenv("RUNPOD_LIVE_SPOT") != "1" {
		t.Skip("live spot test: RUNPOD_LIVE_SPOT not set")
	}
	client := liveClient(t)
	ctx := context.Background()

	offers, err := client.ListGPUOffers(ctx, &runpod.GPUOfferFilter{InStockOnly: true})
	if err != nil || len(offers) == 0 {
		t.Fatalf("no offers: %v", err)
	}
	offer := offers[0] // cheapest in stock

	pod, err := client.CreateSpotPod(ctx, &runpod.CreatePodRequest{
		Name:              "sdk-live-spot-test",
		ImageName:         "runpod/base:0.6.2-cuda12.4.1",
		GPUTypeIDs:        []string{offer.GPUTypeID},
		GPUCount:          1,
		ContainerDiskInGB: 10,
		CloudType:         offer.CloudType,
		BidPerGPU:         offer.MinimumBidPrice,
	})
	if err != nil {
		t.Fatalf("CreateSpotPod: %v", err)
	}
	defer func() {
		if err := client.TerminatePod(context.Background(), pod.ID); err != nil {
			t.Errorf("cleanup TerminatePod: %v", err)
		}
	}()
	t.Logf("spot pod %s created on %s bid=$%.3f", pod.ID, offer.GPUTypeID, offer.MinimumBidPrice)

	got, err := client.GetPod(ctx, pod.ID)
	if err != nil {
		t.Fatalf("GetPod: %v", err)
	}
	if !got.Interruptible {
		t.Errorf("pod not marked interruptible: %+v", got)
	}
	t.Logf("status=%s", got.DesiredStatus)
}
