package runpod_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	runpod "github.com/cozy-creator/runpod-go-sdk"
	"github.com/cozy-creator/runpod-go-sdk/runpodtest"
)

func lifecycleTime(value string) *runpod.JSONTime {
	return &runpod.JSONTime{Time: mustParseTime(value)}
}

func mustParseTime(value string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		panic(err)
	}
	return t
}

func TestGetPodLifecycleObservation(t *testing.T) {
	server := newGPUTypeGraphQLServer(t, func(req testGraphQLRequest) interface{} {
		for _, field := range []string{"desiredStatus", "lastStartedAt", "runtime", "uptimeInSeconds", "latestTelemetry", "state", "time"} {
			if !strings.Contains(req.Query, field) {
				t.Fatalf("query missing %s: %s", field, req.Query)
			}
		}
		input, ok := req.Variables["input"].(map[string]interface{})
		if !ok || input["podId"] != "pod-1" {
			t.Fatalf("unexpected variables: %#v", req.Variables)
		}
		return map[string]interface{}{
			"data": map[string]interface{}{
				"pod": map[string]interface{}{
					"id":            "pod-1",
					"desiredStatus": "RUNNING",
					"lastStartedAt": "2026-07-15T13:41:27.123Z",
					"runtime":       map[string]interface{}{"uptimeInSeconds": -17},
					"latestTelemetry": map[string]interface{}{
						"state": "exited",
						"time":  "2026-07-15T13:41:42.456Z",
					},
				},
			},
		}
	})
	defer server.Close()

	client := mustClient(t, "test_key", runpod.WithGraphQLBaseURL(server.URL))
	got, err := client.GetPodLifecycleObservation(context.Background(), "pod-1")
	if err != nil {
		t.Fatalf("GetPodLifecycleObservation: %v", err)
	}
	if got.PodID != "pod-1" || got.DesiredStatus != "RUNNING" {
		t.Fatalf("unexpected observation: %+v", got)
	}
	if got.LastStartedAt == nil || !got.LastStartedAt.Equal(mustParseTime("2026-07-15T13:41:27.123Z")) {
		t.Fatalf("unexpected lastStartedAt: %+v", got.LastStartedAt)
	}
	if got.LatestTelemetry == nil || got.LatestTelemetry.State != "exited" || got.LatestTelemetry.Time == nil ||
		!got.LatestTelemetry.Time.Equal(mustParseTime("2026-07-15T13:41:42.456Z")) {
		t.Fatalf("unexpected telemetry: %+v", got.LatestTelemetry)
	}
	if got.RuntimeUptimeInSeconds == nil || *got.RuntimeUptimeInSeconds != -17 {
		t.Fatalf("unexpected runtime uptime: %v", got.RuntimeUptimeInSeconds)
	}
}

func TestGetPodLifecycleObservation_NotFound(t *testing.T) {
	server := newGPUTypeGraphQLServer(t, func(testGraphQLRequest) interface{} {
		return map[string]interface{}{"data": map[string]interface{}{"pod": nil}}
	})
	defer server.Close()

	client := mustClient(t, "test_key", runpod.WithGraphQLBaseURL(server.URL))
	_, err := client.GetPodLifecycleObservation(context.Background(), "missing")
	if !errors.Is(err, runpod.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestGetPodTerminalError_TelemetryDoesNotFenceAContainer(t *testing.T) {
	start := lifecycleTime("2026-07-15T13:41:27Z")
	exit := lifecycleTime("2026-07-15T13:41:42Z")
	srv := runpodtest.New()
	t.Cleanup(srv.Close)
	srv.AddPod(&runpod.Pod{ID: "pod-1", DesiredStatus: "RUNNING", LastStartedAt: start})
	srv.SetPodLifecycleObservation(&runpod.PodLifecycleObservation{
		PodID:           "pod-1",
		DesiredStatus:   "RUNNING",
		LastStartedAt:   start,
		LatestTelemetry: &runpod.PodLifecycleTelemetry{State: "exited", Time: exit},
	})

	verdict, dead, err := srv.MustClient().GetPodTerminalError(context.Background(), "pod-1", nil)
	if err != nil || dead || verdict != nil {
		t.Fatalf("unfenced telemetry became a terminal verdict: verdict=%+v dead=%v err=%v", verdict, dead, err)
	}
}
