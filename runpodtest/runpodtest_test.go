package runpodtest_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	runpod "github.com/cozy-creator/runpod-go-sdk"
	"github.com/cozy-creator/runpod-go-sdk/runpodtest"
)

func TestPodLifecycleWithStockOut(t *testing.T) {
	srv := runpodtest.New()
	defer srv.Close()
	client := srv.MustClient(runpod.WithRetryDelay(time.Millisecond))
	ctx := context.Background()

	srv.SetGPUStockOut("NVIDIA GeForce RTX 4090", true)

	// Fan-out: 4090 stock-out, falls back to 3090.
	pod, err := client.CreatePod(ctx, &runpod.CreatePodRequest{
		Name:              "w1",
		ImageName:         "img",
		GPUTypeIDs:        []string{"NVIDIA GeForce RTX 4090", "NVIDIA GeForce RTX 3090"},
		GPUCount:          1,
		ContainerDiskInGB: 10,
	})
	if err != nil {
		t.Fatalf("CreatePod: %v", err)
	}
	if pod.Machine == nil || pod.Machine.GPUTypeID != "NVIDIA GeForce RTX 3090" {
		t.Fatalf("expected fallback onto 3090, got %+v", pod.Machine)
	}

	// Single stock-out type -> typed capacity error.
	_, err = client.CreatePod(ctx, &runpod.CreatePodRequest{
		Name:              "w2",
		ImageName:         "img",
		GPUTypeIDs:        []string{"NVIDIA GeForce RTX 4090"},
		GPUCount:          1,
		ContainerDiskInGB: 10,
	})
	if !errors.Is(err, runpod.ErrNoCapacity) {
		t.Fatalf("expected ErrNoCapacity, got %v", err)
	}

	// Lifecycle.
	got, err := client.GetPod(ctx, pod.ID)
	if err != nil || got.DesiredStatus != "RUNNING" {
		t.Fatalf("GetPod: %v %+v", err, got)
	}
	if err := client.StopPod(ctx, pod.ID); err != nil {
		t.Fatalf("StopPod: %v", err)
	}
	if got := srv.Pod(pod.ID); got.DesiredStatus != "EXITED" {
		t.Fatalf("expected EXITED after stop, got %s", got.DesiredStatus)
	}
	if _, err := client.ResumePod(ctx, pod.ID); err != nil {
		t.Fatalf("ResumePod: %v", err)
	}
	pods, err := client.ListPods(ctx, nil)
	if err != nil || len(pods) != 1 {
		t.Fatalf("ListPods: %v %d", err, len(pods))
	}
	if err := client.TerminatePod(ctx, pod.ID); err != nil {
		t.Fatalf("TerminatePod: %v", err)
	}
	if _, err := client.GetPod(ctx, pod.ID); !errors.Is(err, runpod.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after terminate, got %v", err)
	}
}

func TestJobLifecycle(t *testing.T) {
	srv := runpodtest.New()
	defer srv.Close()
	client := srv.MustClient()
	ctx := context.Background()

	job, err := client.RunAsync(ctx, "ep1", map[string]string{"prompt": "hi"})
	if err != nil {
		t.Fatalf("RunAsync: %v", err)
	}
	if job.Status != "IN_QUEUE" {
		t.Fatalf("expected IN_QUEUE, got %s", job.Status)
	}

	if err := srv.CompleteJob("ep1", job.ID, map[string]string{"answer": "hello"}); err != nil {
		t.Fatalf("CompleteJob: %v", err)
	}
	done, err := client.WaitForJobCompletion(ctx, "ep1", job.ID, 5*time.Second)
	if err != nil {
		t.Fatalf("WaitForJobCompletion: %v", err)
	}
	var out struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal(done.Output, &out); err != nil || out.Answer != "hello" {
		t.Fatalf("output = %s", string(done.Output))
	}

	// runsync echoes input.
	sync, err := client.RunSync(ctx, "ep1", map[string]string{"echo": "x"})
	if err != nil || sync.Status != "COMPLETED" {
		t.Fatalf("RunSync: %v %+v", err, sync)
	}

	// health + purge
	queued, err := client.RunAsync(ctx, "ep1", nil)
	if err != nil {
		t.Fatalf("RunAsync: %v", err)
	}
	health, err := client.GetHealth(ctx, "ep1")
	if err != nil || health.JobsInQueue != 1 {
		t.Fatalf("GetHealth: %v %+v", err, health)
	}
	if err := client.PurgeQueue(ctx, "ep1"); err != nil {
		t.Fatalf("PurgeQueue: %v", err)
	}
	after, err := client.GetJobStatus(ctx, "ep1", queued.ID)
	if err != nil || after.Status != "CANCELLED" {
		t.Fatalf("purged job status: %v %+v", err, after)
	}
}

func TestVolumeAndRegistryAuthCRUD(t *testing.T) {
	srv := runpodtest.New()
	defer srv.Close()
	client := srv.MustClient()
	ctx := context.Background()

	vol, err := client.CreateNetworkVolume(ctx, &runpod.CreateNetworkVolumeRequest{
		Name: "models", Size: 100, DataCenterID: "EU-RO-1",
	})
	if err != nil {
		t.Fatalf("CreateNetworkVolume: %v", err)
	}
	if _, err := client.UpdateNetworkVolume(ctx, vol.ID, &runpod.UpdateNetworkVolumeRequest{Size: 50}); err == nil {
		t.Fatal("shrink must fail")
	}
	grown, err := client.UpdateNetworkVolume(ctx, vol.ID, &runpod.UpdateNetworkVolumeRequest{Size: 200})
	if err != nil || grown.Size != 200 {
		t.Fatalf("grow: %v %+v", err, grown)
	}
	vols, err := client.ListNetworkVolumes(ctx)
	if err != nil || len(vols) != 1 {
		t.Fatalf("ListNetworkVolumes: %v %d", err, len(vols))
	}
	if err := client.DeleteNetworkVolume(ctx, vol.ID); err != nil {
		t.Fatalf("DeleteNetworkVolume: %v", err)
	}

	auth, err := client.CreateContainerRegistryAuth(ctx, &runpod.CreateContainerRegistryAuthRequest{
		Name: "reg", Username: "u", Password: "p",
	})
	if err != nil {
		t.Fatalf("CreateContainerRegistryAuth: %v", err)
	}
	if _, err := client.CreateContainerRegistryAuth(ctx, &runpod.CreateContainerRegistryAuthRequest{
		Name: "reg", Username: "u", Password: "p",
	}); err == nil {
		t.Fatal("duplicate name must fail")
	}
	auths, err := client.ListContainerRegistryAuths(ctx)
	if err != nil || len(auths) != 1 {
		t.Fatalf("ListContainerRegistryAuths: %v %d", err, len(auths))
	}
	if err := client.DeleteContainerRegistryAuth(ctx, auth.ID); err != nil {
		t.Fatalf("DeleteContainerRegistryAuth: %v", err)
	}
}

func TestNetworkVolumePodAttachment(t *testing.T) {
	srv := runpodtest.New()
	defer srv.Close()
	client := srv.MustClient()
	ctx := context.Background()

	volume, err := client.CreateNetworkVolume(ctx, &runpod.CreateNetworkVolumeRequest{
		Name: "endpoint-cache", Size: 100, DataCenterID: "US-KS-2",
	})
	if err != nil {
		t.Fatalf("CreateNetworkVolume: %v", err)
	}
	pod, err := client.CreatePod(ctx, &runpod.CreatePodRequest{
		Name: "worker", ImageName: "image", GPUTypeIDs: []string{"NVIDIA GeForce RTX 4090"},
		GPUCount: 1, ContainerDiskInGB: 100, CloudType: "SECURE",
		DataCenterIDs: []string{"US-KS-2"}, NetworkVolumeID: volume.ID,
		VolumeMountPath: "/runpod-volume",
	})
	if err != nil {
		t.Fatalf("CreatePod: %v", err)
	}
	if pod.NetworkVolume == nil || pod.NetworkVolume.ID != volume.ID || pod.NetworkVolumeID != volume.ID {
		t.Fatalf("attachment identity missing from pod response: %+v", pod)
	}
	if pod.VolumeMountPath != "/runpod-volume" {
		t.Fatalf("mount path = %q, want /runpod-volume", pod.VolumeMountPath)
	}
	if pod.Machine == nil || pod.Machine.DataCenterID != "US-KS-2" {
		t.Fatalf("pod landed outside volume datacenter: %+v", pod.Machine)
	}
	listed, err := client.ListPods(ctx, nil)
	if err != nil || len(listed) != 1 {
		t.Fatalf("ListPods: %v %+v", err, listed)
	}
	if listed[0].VolumeMountPath != "/runpod-volume" {
		t.Fatalf("listed mount path = %q, want /runpod-volume", listed[0].VolumeMountPath)
	}
	if err := client.DeleteNetworkVolume(ctx, volume.ID); err == nil {
		t.Fatal("attached volume deletion must fail")
	}
}

func TestAccountIDStableAcrossAPIKeyRotation(t *testing.T) {
	srv := runpodtest.New()
	defer srv.Close()
	srv.SetAccountID("account-123")

	before, err := srv.ClientWithAPIKey("key-before-rotation")
	if err != nil {
		t.Fatalf("client before rotation: %v", err)
	}
	after, err := srv.ClientWithAPIKey("key-after-rotation")
	if err != nil {
		t.Fatalf("client after rotation: %v", err)
	}

	beforeID, err := before.GetAccountID(context.Background())
	if err != nil {
		t.Fatalf("GetAccountID before rotation: %v", err)
	}
	afterID, err := after.GetAccountID(context.Background())
	if err != nil {
		t.Fatalf("GetAccountID after rotation: %v", err)
	}
	if beforeID != "account-123" || afterID != beforeID {
		t.Fatalf("account IDs before/after rotation = %q/%q", beforeID, afterID)
	}
	gotAuth := srv.AuthorizationHeaders()
	wantAuth := []string{"Bearer key-before-rotation", "Bearer key-after-rotation"}
	if len(gotAuth) != len(wantAuth) {
		t.Fatalf("authorization headers = %q, want %q", gotAuth, wantAuth)
	}
	for i := range wantAuth {
		if gotAuth[i] != wantAuth[i] {
			t.Fatalf("authorization header %d = %q, want %q", i, gotAuth[i], wantAuth[i])
		}
	}
}

func TestAccountIDRejectsMissingProviderIdentity(t *testing.T) {
	srv := runpodtest.New()
	defer srv.Close()
	srv.SetAccountID("  ")

	if _, err := srv.MustClient().GetAccountID(context.Background()); err == nil {
		t.Fatal("GetAccountID must reject a response without myself.id")
	}
}

func TestNetworkVolumePodAttachmentRejectsUnsafePlacement(t *testing.T) {
	srv := runpodtest.New()
	defer srv.Close()
	client := srv.MustClient()
	ctx := context.Background()
	volume, err := client.CreateNetworkVolume(ctx, &runpod.CreateNetworkVolumeRequest{
		Name: "endpoint-cache", Size: 100, DataCenterID: "US-KS-2",
	})
	if err != nil {
		t.Fatalf("CreateNetworkVolume: %v", err)
	}
	base := runpod.CreatePodRequest{
		Name: "worker", ImageName: "image", GPUTypeIDs: []string{"NVIDIA GeForce RTX 4090"},
		GPUCount: 1, ContainerDiskInGB: 100, CloudType: "SECURE",
		DataCenterIDs: []string{"US-KS-2"}, NetworkVolumeID: volume.ID,
	}
	tests := []struct {
		name   string
		mutate func(*runpod.CreatePodRequest)
	}{
		{"community cloud", func(req *runpod.CreatePodRequest) { req.CloudType = "COMMUNITY" }},
		{"missing datacenter", func(req *runpod.CreatePodRequest) { req.DataCenterIDs = nil }},
		{"multiple datacenters", func(req *runpod.CreatePodRequest) { req.DataCenterIDs = []string{"US-KS-2", "EU-RO-1"} }},
		{"wrong datacenter", func(req *runpod.CreatePodRequest) { req.DataCenterIDs = []string{"EU-RO-1"} }},
		{"ordinary volume disk", func(req *runpod.CreatePodRequest) { req.VolumeInGB = 100 }},
		{"relative mount path", func(req *runpod.CreatePodRequest) { req.VolumeMountPath = "workspace" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := base
			test.mutate(&req)
			if _, err := client.CreatePod(ctx, &req); err == nil {
				t.Fatal("unsafe network-volume placement must fail")
			}
		})
	}
}

func TestNetworkVolumePodAttachmentDefaultsToSecureCloud(t *testing.T) {
	srv := runpodtest.New()
	defer srv.Close()
	client := srv.MustClient()
	ctx := context.Background()

	volume, err := client.CreateNetworkVolume(ctx, &runpod.CreateNetworkVolumeRequest{
		Name: "endpoint-cache", Size: 100, DataCenterID: "US-KS-2",
	})
	if err != nil {
		t.Fatalf("CreateNetworkVolume: %v", err)
	}
	pod, err := client.CreatePod(ctx, &runpod.CreatePodRequest{
		Name: "worker", ImageName: "image", GPUTypeIDs: []string{"NVIDIA GeForce RTX 4090"},
		GPUCount: 1, ContainerDiskInGB: 100,
		DataCenterIDs: []string{"US-KS-2"}, NetworkVolumeID: volume.ID,
		VolumeMountPath: "/workspace",
	})
	if err != nil {
		t.Fatalf("CreatePod with default Secure Cloud: %v", err)
	}
	if pod.NetworkVolumeID != volume.ID || pod.VolumeMountPath != "/workspace" {
		t.Fatalf("default-Secure attachment = %+v", pod)
	}
}

func TestFaultInjection(t *testing.T) {
	srv := runpodtest.New()
	defer srv.Close()
	client := srv.MustClient(
		runpod.WithMaxRetryAttempts(1),
		runpod.WithRetryDelay(time.Millisecond),
	)
	ctx := context.Background()

	// 429 with Retry-After is retried and then succeeds.
	srv.FailNext(429, `{"error":"rate limited"}`, "0")
	if _, err := client.ListPods(ctx, nil); err != nil {
		t.Fatalf("expected retry to recover from injected 429: %v", err)
	}

	// Injected 500 on POST is not retried and surfaces as APIError.
	srv.FailNext(500, `{"error":"internal"}`, "")
	_, err := client.CreateNetworkVolume(ctx, &runpod.CreateNetworkVolumeRequest{
		Name: "x", Size: 10, DataCenterID: "EU",
	})
	var apiErr *runpod.APIError
	if !errors.As(err, &apiErr) || !apiErr.IsServerError() {
		t.Fatalf("expected 5xx APIError, got %v", err)
	}
}

func TestGPUTypesQuery(t *testing.T) {
	srv := runpodtest.New()
	defer srv.Close()
	client := srv.MustClient()
	ctx := context.Background()

	gpus, err := client.ListGPUTypes(ctx, nil)
	if err != nil {
		t.Fatalf("ListGPUTypes: %v", err)
	}
	if len(gpus) != len(runpod.GPUCatalog()) {
		t.Fatalf("expected default catalog (%d), got %d", len(runpod.GPUCatalog()), len(gpus))
	}

	offers, err := client.ListGPUOffers(ctx, &runpod.GPUOfferFilter{InStockOnly: true})
	if err != nil {
		t.Fatalf("ListGPUOffers: %v", err)
	}
	if len(offers) == 0 {
		t.Fatal("expected offers from default catalog")
	}
}

func TestGPUTypesQueryRejectsRemovedLowestPriceFields(t *testing.T) {
	srv := runpodtest.New()
	defer srv.Close()
	client := srv.MustClient()
	ctx := context.Background()

	for _, field := range []string{"interruptablePrice", "cudaVersion"} {
		query := `query { gpuTypes { id lowestPrice(input: { gpuCount: 1 }) { ` + field + ` } } }`
		err := client.GraphQL(ctx, query, nil, nil)
		if err == nil {
			t.Fatalf("expected validation error for removed field %q", field)
		}
	}

	// Word boundaries: uninterruptablePrice and minCudaVersion must still pass.
	query := `query($minCudaVersion: String) { gpuTypes { id lowestPrice(input: { gpuCount: 1, minCudaVersion: $minCudaVersion }) { uninterruptablePrice } } }`
	if err := client.GraphQL(ctx, query, map[string]interface{}{"minCudaVersion": "12.8"}, nil); err != nil {
		t.Fatalf("valid query rejected: %v", err)
	}
}
