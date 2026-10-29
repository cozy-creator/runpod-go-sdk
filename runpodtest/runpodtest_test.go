package runpodtest_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	runpod "github.com/cozy-creator/runpod-go-sdk"
	"github.com/cozy-creator/runpod-go-sdk/runpodtest"
)

func gpuRequest(name string) *runpod.CreatePodRequest {
	return &runpod.CreatePodRequest{Name: name, Image: "worker:image", GPU: &runpod.CreateGPUConfig{ID: "NVIDIA GeForce RTX 4090", Count: 1}, Disk: 10}
}
func TestNativePodLifecycleWithExplicitStockFallback(t *testing.T) {
	srv := runpodtest.New()
	defer srv.Close()
	client := srv.MustClient()
	ctx := context.Background()
	srv.SetGPUStockOut("NVIDIA GeForce RTX 4090", true)
	request := gpuRequest("worker")
	pod, err := client.CreatePodWithFallback(ctx, request, []string{"NVIDIA GeForce RTX 4090", "NVIDIA GeForce RTX 3090"}, nil)
	if err != nil || pod.GPU == nil || pod.GPU.ID != "NVIDIA GeForce RTX 3090" || request.GPU.ID != "NVIDIA GeForce RTX 4090" {
		t.Fatalf("pod=%+v request=%+v err=%v", pod, request, err)
	}
	if _, err := client.CreatePod(ctx, gpuRequest("unavailable")); !errors.Is(err, runpod.ErrNoCapacity) {
		t.Fatalf("capacity error=%v", err)
	}
	got, err := client.GetPod(ctx, pod.ID)
	if err != nil || got.Status != "RUNNING" || got.Image != "worker:image" {
		t.Fatalf("pod=%+v err=%v", got, err)
	}
	name := "renamed"
	updated, err := client.UpdatePod(ctx, pod.ID, &runpod.UpdatePodRequest{Name: &name})
	if err != nil || updated.Name != name {
		t.Fatalf("patch=%+v err=%v", updated, err)
	}
	if err := client.StopPod(ctx, pod.ID); err != nil {
		t.Fatal(err)
	}
	if srv.Pod(pod.ID).Status != "EXITED" {
		t.Fatal("stop action did not stop pod")
	}
	if resumed, err := client.ResumePod(ctx, pod.ID); err != nil || resumed.Status != "RUNNING" {
		t.Fatalf("resume=%+v err=%v", resumed, err)
	}
	if restarted, err := client.RestartPod(ctx, pod.ID); err != nil || restarted.Status != "RUNNING" {
		t.Fatalf("restart=%+v err=%v", restarted, err)
	}
	if err := client.TerminatePod(ctx, pod.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetPod(ctx, pod.ID); !errors.Is(err, runpod.ErrNotFound) {
		t.Fatalf("terminated get=%v", err)
	}
}
func TestInferenceUsesItsOwnConfiguredService(t *testing.T) {
	control := runpodtest.New()
	defer control.Close()
	inference := runpodtest.New()
	defer inference.Close()
	control.AddPod(&runpod.Pod{ID: "pod1", Name: "worker", Status: "RUNNING"})
	client := control.MustClient(runpod.WithServerlessBaseURL(inference.URL()))
	pods, err := client.ListPods(context.Background(), nil)
	if err != nil || len(pods) != 1 {
		t.Fatalf("pods=%+v err=%v", pods, err)
	}
	job, err := client.RunSync(context.Background(), "endpoint", map[string]string{"prompt": "hello"})
	if err != nil || job.Status != "COMPLETED" {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	var out map[string]string
	if err := json.Unmarshal(job.Output, &out); err != nil || out["prompt"] != "hello" {
		t.Fatalf("output=%s err=%v", job.Output, err)
	}
	if len(control.AuthorizationHeaders()) != 1 || len(inference.AuthorizationHeaders()) != 1 {
		t.Fatal("management and inference requests did not use their separate service bases")
	}
}
func TestJobLifecycle(t *testing.T) {
	srv := runpodtest.New()
	defer srv.Close()
	client := srv.MustClient()
	ctx := context.Background()
	job, err := client.RunAsync(ctx, "ep1", map[string]string{"prompt": "hi"})
	if err != nil || job.Status != "IN_QUEUE" {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	if err := srv.CompleteJob("ep1", job.ID, map[string]string{"answer": "hello"}); err != nil {
		t.Fatal(err)
	}
	done, err := client.WaitForJobCompletion(ctx, "ep1", job.ID, time.Second)
	if err != nil || !strings.Contains(string(done.Output), "hello") {
		t.Fatalf("done=%+v err=%v", done, err)
	}
	queued, err := client.RunAsync(ctx, "ep1", nil)
	if err != nil {
		t.Fatal(err)
	}
	health, err := client.GetHealth(ctx, "ep1")
	if err != nil || health.JobsInQueue != 1 {
		t.Fatalf("health=%+v err=%v", health, err)
	}
	if err := client.PurgeQueue(ctx, "ep1"); err != nil {
		t.Fatal(err)
	}
	after, err := client.GetJobStatus(ctx, "ep1", queued.ID)
	if err != nil || after.Status != "CANCELLED" {
		t.Fatalf("after=%+v err=%v", after, err)
	}
}
func TestVolumeAndRegistryCredentialLifecycle(t *testing.T) {
	srv := runpodtest.New()
	defer srv.Close()
	client := srv.MustClient()
	ctx := context.Background()
	vol, err := client.CreateNetworkVolume(ctx, &runpod.CreateNetworkVolumeRequest{Name: "models", Size: 100, DataCenter: "EU-RO-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.UpdateNetworkVolume(ctx, vol.ID, &runpod.UpdateNetworkVolumeRequest{Size: 50}); err == nil {
		t.Fatal("shrink accepted")
	}
	grown, err := client.UpdateNetworkVolume(ctx, vol.ID, &runpod.UpdateNetworkVolumeRequest{Size: 200})
	if err != nil || grown.Size != 200 {
		t.Fatalf("volume=%+v err=%v", grown, err)
	}
	vols, err := client.ListNetworkVolumes(ctx)
	if err != nil || len(vols) != 1 || vols[0].DataCenter != "EU-RO-1" {
		t.Fatalf("volumes=%+v err=%v", vols, err)
	}
	if err := client.DeleteNetworkVolume(ctx, vol.ID); err != nil {
		t.Fatal(err)
	}
	auth, err := client.CreateContainerRegistryAuth(ctx, &runpod.CreateContainerRegistryAuthRequest{Name: "private", Username: "user", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateContainerRegistryAuth(ctx, &runpod.CreateContainerRegistryAuthRequest{Name: "private", Username: "user", Password: "secret"}); err == nil {
		t.Fatal("duplicate registry name accepted")
	}
	auths, err := client.ListContainerRegistryAuths(ctx)
	if err != nil || len(auths) != 1 || auths[0].ID != auth.ID {
		t.Fatalf("registries=%+v err=%v", auths, err)
	}
	if err := client.DeleteContainerRegistryAuth(ctx, auth.ID); err != nil {
		t.Fatal(err)
	}
}
func TestNetworkVolumePodAttachmentAndDeletionFence(t *testing.T) {
	srv := runpodtest.New()
	defer srv.Close()
	client := srv.MustClient()
	ctx := context.Background()
	volume := &runpod.NetworkVolume{ID: "volume1", Name: "cache", Size: 100, DataCenter: "US-KS-2", Type: "STANDARD"}
	srv.AddNetworkVolume(volume)
	request := gpuRequest("attached")
	request.Cloud = "SECURE"
	request.DataCenterIDs = []string{"US-KS-2"}
	request.Mounts = &runpod.Mounts{Network: []runpod.VolumeMount{{VolumeID: volume.ID, Path: "/runpod-volume"}}}
	pod, err := client.CreatePod(ctx, request)
	if err != nil || pod.DataCenterID == nil || *pod.DataCenterID != "US-KS-2" || pod.Mounts == nil || len(pod.Mounts.Network) != 1 || pod.Mounts.Network[0].VolumeID != volume.ID || pod.Mounts.Network[0].Path != "/runpod-volume" {
		t.Fatalf("pod=%+v err=%v", pod, err)
	}
	if err := client.DeleteNetworkVolume(ctx, volume.ID); err == nil {
		t.Fatal("attached volume deletion succeeded")
	}
	if err := client.TerminatePod(ctx, pod.ID); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteNetworkVolume(ctx, volume.ID); err != nil {
		t.Fatal(err)
	}
}
func TestNetworkVolumeAttachmentRejectsUnsupportedPlacement(t *testing.T) {
	for _, scenario := range []string{"community", "wrong datacenter", "multiple datacenters", "unknown volume", "persistent mount", "relative mount"} {
		t.Run(scenario, func(t *testing.T) {
			srv := runpodtest.New()
			defer srv.Close()
			client := srv.MustClient()
			srv.AddNetworkVolume(&runpod.NetworkVolume{ID: "v1", Name: "models", Size: 100, DataCenter: "dc1", Type: "STANDARD"})
			request := gpuRequest("attached")
			request.Cloud = "SECURE"
			request.DataCenterIDs = []string{"dc1"}
			request.Mounts = &runpod.Mounts{Network: []runpod.VolumeMount{{VolumeID: "v1", Path: "/models"}}}
			switch scenario {
			case "community":
				request.Cloud = "COMMUNITY"
			case "wrong datacenter":
				request.DataCenterIDs = []string{"dc2"}
			case "multiple datacenters":
				request.DataCenterIDs = []string{"dc1", "dc2"}
			case "unknown volume":
				request.Mounts.Network[0].VolumeID = "missing"
			case "persistent mount":
				request.Mounts.Persistent = &runpod.PersistentMount{Size: 10, Path: "/workspace"}
			case "relative mount":
				request.Mounts.Network[0].Path = "models"
			}
			if _, err := client.CreatePod(context.Background(), request); err == nil {
				t.Fatal("invalid attachment accepted")
			}
		})
	}
}
func TestNetworkAttachmentUsesDefaultSecureCloud(t *testing.T) {
	srv := runpodtest.New()
	defer srv.Close()
	srv.AddNetworkVolume(&runpod.NetworkVolume{ID: "v1", Name: "models", Size: 100, DataCenter: "dc1", Type: "STANDARD"})
	request := gpuRequest("attached")
	request.Mounts = &runpod.Mounts{Network: []runpod.VolumeMount{{VolumeID: "v1", Path: "/models"}}}
	pod, err := srv.MustClient().CreatePod(context.Background(), request)
	if err != nil || pod.Cloud != "SECURE" || pod.Mounts.Network[0].VolumeID != "v1" {
		t.Fatalf("pod=%+v err=%v", pod, err)
	}
}
func TestBearerCredentialsReachV2AfterRotation(t *testing.T) {
	srv := runpodtest.New()
	defer srv.Close()
	for _, key := range []string{"before-rotation", "after-rotation"} {
		client, err := srv.ClientWithAPIKey(key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.ListPods(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
	}
	headers := srv.AuthorizationHeaders()
	if len(headers) != 2 || headers[0] != "Bearer before-rotation" || headers[1] != "Bearer after-rotation" {
		t.Fatalf("headers=%q", headers)
	}
	response, err := http.Get(srv.URL() + "/v2/pods")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatalf("unauthenticated status=%d", response.StatusCode)
	}
}
func TestCursorPaginationAndExactNameLookup(t *testing.T) {
	srv := runpodtest.New()
	defer srv.Close()
	for i := 0; i < 205; i++ {
		name := "obligation-1"
		if i == 17 {
			name = "other"
		}
		srv.AddPod(&runpod.Pod{ID: fmt.Sprintf("pod-%03d", i), Name: name, Status: "RUNNING"})
	}
	client := srv.MustClient()
	pods, err := client.ListPods(context.Background(), &runpod.ListOptions{Limit: 2})
	if err != nil || len(pods) != 205 {
		t.Fatalf("pods=%d err=%v", len(pods), err)
	}
	matches, err := client.FindPodsByName(context.Background(), "obligation-1")
	if err != nil || len(matches) != 204 {
		t.Fatalf("matches=%d err=%v", len(matches), err)
	}
	for i := 1; i < len(matches); i++ {
		if matches[i-1].ID >= matches[i].ID || matches[i].Name != "obligation-1" {
			t.Fatalf("bad exact name match %d", i)
		}
	}
}
func TestNetworkVolumeSnapshotFollowsCursorPages(t *testing.T) {
	srv := runpodtest.New()
	defer srv.Close()
	for i := 0; i < 1001; i++ {
		srv.AddNetworkVolume(&runpod.NetworkVolume{ID: fmt.Sprintf("vol-%04d", i), Name: "models", Size: 10, DataCenter: "dc1", Type: "STANDARD"})
	}
	volumes, err := srv.MustClient().ListNetworkVolumes(context.Background())
	if err != nil || len(volumes) != 1001 {
		t.Fatalf("volumes=%d err=%v", len(volumes), err)
	}
}
func TestFaultInjectionKeepsCreateFailureAmbiguous(t *testing.T) {
	srv := runpodtest.New()
	defer srv.Close()
	client := srv.MustClient(runpod.WithMaxRetryAttempts(1), runpod.WithRetryDelay(time.Millisecond))
	srv.FailNext(429, `{"title":"Limited","status":429,"detail":"rate limited"}`, "0")
	if _, err := client.ListPods(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	srv.FailNext(500, `{"title":"Failure","status":500,"detail":"internal"}`, "")
	if _, err := client.CreateNetworkVolume(context.Background(), &runpod.CreateNetworkVolumeRequest{Name: "models", Size: 10, DataCenter: "dc1"}); err == nil {
		t.Fatal("POST server failure disappeared")
	} else {
		var apiError *runpod.APIError
		if !errors.As(err, &apiError) || !apiError.IsServerError() {
			t.Fatalf("error=%v", err)
		}
	}
	volumes, err := client.ListNetworkVolumes(context.Background())
	if err != nil || len(volumes) != 0 {
		t.Fatalf("failed create retried: volumes=%+v err=%v", volumes, err)
	}
}
func TestNativeCatalogStockAndExactAggregatePrice(t *testing.T) {
	srv := runpodtest.New()
	defer srv.Close()
	rate := runpod.USDMicrosPerHour(123457)
	srv.SetGPUTypes([]runpod.GPUType{{ID: "gpu1", Name: "One", Memory: 24, Manufacturer: "NVIDIA", Secure: true, Price: runpod.GPUPrices{Secure: &rate}, MaxCount: runpod.GPUCounts{Secure: 8}, Availability: "HIGH"}})
	client := srv.MustClient()
	offers, err := client.ListGPUOffers(context.Background(), &runpod.GPUOfferFilter{CloudType: "SECURE", GPUCount: 4, InStockOnly: true})
	if err != nil || len(offers) != 1 || offers[0].OnDemandPriceUSDMicrosPerHour != 493828 || offers[0].PriceSource != runpod.CatalogListPrice {
		t.Fatalf("offers=%+v err=%v", offers, err)
	}
	srv.SetGPUStockOut("gpu1", true)
	offers, err = client.ListGPUOffers(context.Background(), &runpod.GPUOfferFilter{CloudType: "SECURE", InStockOnly: true})
	if err != nil || len(offers) != 0 {
		t.Fatalf("maxCount became stock: offers=%+v err=%v", offers, err)
	}
}
func TestLegacyRoutesAreUnavailable(t *testing.T) {
	srv := runpodtest.New()
	defer srv.Close()
	for _, path := range []string{"/graphql", "/v1/pods", "/pods", "/v2/networkvolumes", "/v2/containerregistryauth", "/v2/pods/existing/start"} {
		request, err := http.NewRequest(http.MethodGet, srv.URL()+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer key")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 404 {
			t.Errorf("legacy path %s status=%d", path, response.StatusCode)
		}
	}
}
