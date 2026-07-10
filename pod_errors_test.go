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
	srv.AddPod(&runpod.Pod{ID: "exited", DesiredStatus: "EXITED", ImageName: "org/app:v1", LastStatusChange: "Exited by Runpod: now"})
	v, dead, err = sdk.GetPodTerminalError(ctx, "exited", nil)
	if err != nil || !dead || v.Class != runpod.PodErrorUnknown || v.ImageRef != "org/app:v1" || v.Message != "Exited by Runpod: now" {
		t.Fatalf("exited pod: %+v dead=%v err=%v", v, dead, err)
	}

	// Message markers win.
	srv.AddPod(&runpod.Pod{ID: "autherr", DesiredStatus: "EXITED", LastStatusChange: "Pod initialization error IMAGE_AUTH_ERROR:unauthorized"})
	if v, _, _ := sdk.GetPodTerminalError(ctx, "autherr", nil); v.Class != runpod.PodErrorImageAuth {
		t.Fatalf("marker classification: %+v", v)
	}

	// Spot reclaim.
	srv.AddPod(&runpod.Pod{ID: "spot", DesiredStatus: "EXITED", Interruptible: true})
	if v, _, _ := sdk.GetPodTerminalError(ctx, "spot", nil); v.Class != runpod.PodErrorInterrupted {
		t.Fatalf("spot classification: %+v", v)
	}

	// Registry probe refinement.
	cases := []struct {
		outcome string
		want    runpod.PodErrorClass
	}{
		{runpod.RegistryProbeUnauthorized, runpod.PodErrorImageAuth},
		{runpod.RegistryProbeNotFound, runpod.PodErrorImageMissing},
		{runpod.RegistryProbeInvalidRef, runpod.PodErrorImageMissing},
		{runpod.RegistryProbeOK, runpod.PodErrorHostFault},
		{runpod.RegistryProbeUnreachable, runpod.PodErrorUnknown},
	}
	for _, tc := range cases {
		opts := &runpod.PodTerminalErrorOptions{RegistryProbe: func(context.Context, string) (string, string) {
			return tc.outcome, "probe detail"
		}}
		v, dead, err := sdk.GetPodTerminalError(ctx, "exited", opts)
		if err != nil || !dead || v.Class != tc.want {
			t.Fatalf("probe %s: %+v dead=%v err=%v", tc.outcome, v, dead, err)
		}
	}
}

func TestClassifyPodErrorMessage(t *testing.T) {
	cases := map[string]runpod.PodErrorClass{
		"Pod initialization error IMAGE_AUTH_ERROR:unauthorized": runpod.PodErrorImageAuth,
		"pull access denied for org/app":                         runpod.PodErrorImageAuth,
		"IMAGE_NOT_FOUND":                                        runpod.PodErrorImageMissing,
		"manifest unknown":                                       runpod.PodErrorImageMissing,
		"container killed: out of memory":                        runpod.PodErrorOOM,
		"Exited by Runpod: Fri Jul 10 2026":                      runpod.PodErrorUnknown,
		"":                                                       runpod.PodErrorUnknown,
	}
	for msg, want := range cases {
		if got := runpod.ClassifyPodErrorMessage(msg); got != want {
			t.Fatalf("%q => %q want %q", msg, got, want)
		}
	}
}
