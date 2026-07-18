package runpod_test

import (
	"context"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
	"github.com/cozy-creator/runpod-go-sdk/runpodtest"
)

func TestGetPodTerminalError(t *testing.T) {
	srv := runpodtest.New()
	t.Cleanup(srv.Close)
	sdk := srv.MustClient()
	ctx := context.Background()

	// Alive pod: no verdict.
	srv.AddPod(&runpod.Pod{ID: "alive", DesiredStatus: "RUNNING"})
	if _, dead, err := sdk.GetPodTerminalError(ctx, "alive", nil); dead || err != nil {
		t.Fatalf("alive pod: dead=%v err=%v", dead, err)
	}

	// Missing pod: pod_missing / unknown.
	v, dead, err := sdk.GetPodTerminalError(ctx, "vanished", nil)
	if err != nil || !dead || v.Reason != "pod_missing" || v.Class != runpod.PodErrorUnknown {
		t.Fatalf("missing pod: %+v dead=%v err=%v", v, dead, err)
	}

	// Exited, no prober: unknown with provider message + image preserved.
	// Prose markers in lastStatusChange no longer classify anything (rp#16).
	srv.AddPod(&runpod.Pod{ID: "exited", DesiredStatus: "EXITED", ImageName: "org/app:v1", LastStatusChange: "Exited by Runpod: now"})
	v, dead, err = sdk.GetPodTerminalError(ctx, "exited", nil)
	if err != nil || !dead || v.Class != runpod.PodErrorUnknown || v.ImageRef != "org/app:v1" || v.Message != "Exited by Runpod: now" {
		t.Fatalf("exited pod: %+v dead=%v err=%v", v, dead, err)
	}
	if v.ContainerObserved {
		t.Fatalf("no runtime/telemetry evidence must mean ContainerObserved=false: %+v", v)
	}

	// Typed observation facts: exit code, machine identity, SKU.
	exit := 1
	srv.AddPod(&runpod.Pod{
		ID: "crashed", DesiredStatus: "EXITED", ImageName: "org/app:v1",
		LastStatusChange: "Exited by Runpod: now",
		MachineID:        "machine-a",
		Machine:          &runpod.Machine{ID: "machine-a", GPUTypeID: "NVIDIA GeForce RTX 4090", DataCenterID: "EU-RO-1"},
		Runtime:          &runpod.PodRuntime{ContainerExitCode: &exit, UptimeSeconds: 93},
	})
	v, dead, err = sdk.GetPodTerminalError(ctx, "crashed", nil)
	if err != nil || !dead {
		t.Fatalf("crashed pod: dead=%v err=%v", dead, err)
	}
	if v.ExitCode == nil || *v.ExitCode != 1 || v.MachineID != "machine-a" ||
		v.GPUTypeID != "NVIDIA GeForce RTX 4090" || v.DataCenterID != "EU-RO-1" ||
		v.UptimeSeconds != 93 || !v.ContainerObserved {
		t.Fatalf("typed observation facts: %+v", v)
	}

	// CPU pods report the GPU-type sentinel "unknown" — normalized away.
	srv.AddPod(&runpod.Pod{
		ID: "cpu-exited", DesiredStatus: "EXITED",
		Machine: &runpod.Machine{ID: "machine-c", GPUTypeID: "unknown"},
	})
	if v, _, _ := sdk.GetPodTerminalError(ctx, "cpu-exited", nil); v.GPUTypeID != "" || v.MachineID != "machine-c" {
		t.Fatalf("cpu sentinel normalization: %+v", v)
	}

	// Spot reclaim.
	srv.AddPod(&runpod.Pod{ID: "spot", DesiredStatus: "EXITED", Interruptible: true})
	if v, _, _ := sdk.GetPodTerminalError(ctx, "spot", nil); v.Class != runpod.PodErrorInterrupted {
		t.Fatalf("spot classification: %+v", v)
	}

	// Registry probe: image classes stay typed; probe-OK no longer claims
	// host_fault — class stays unknown with ProbeOutcome recorded.
	cases := []struct {
		outcome string
		want    runpod.PodErrorClass
	}{
		{runpod.RegistryProbeUnauthorized, runpod.PodErrorImageAuth},
		{runpod.RegistryProbeNotFound, runpod.PodErrorImageMissing},
		{runpod.RegistryProbeInvalidRef, runpod.PodErrorImageMissing},
		{runpod.RegistryProbeOK, runpod.PodErrorUnknown},
		{runpod.RegistryProbeUnreachable, runpod.PodErrorUnknown},
	}
	for _, tc := range cases {
		opts := &runpod.PodTerminalErrorOptions{RegistryProbe: func(context.Context, string) (string, string) {
			return tc.outcome, "probe detail"
		}}
		v, dead, err := sdk.GetPodTerminalError(ctx, "exited", opts)
		if err != nil || !dead || v.Class != tc.want || v.ProbeOutcome != tc.outcome {
			t.Fatalf("probe %s: %+v dead=%v err=%v", tc.outcome, v, dead, err)
		}
	}
}
