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

func TestGetPodTerminalError_TelemetryStartGeneration(t *testing.T) {
	start := lifecycleTime("2026-07-15T13:41:27Z")
	oldExit := lifecycleTime("2026-07-15T13:40:00Z")
	freshExit := lifecycleTime("2026-07-15T13:41:42Z")
	negativeUptime := -17

	tests := []struct {
		name        string
		observation *runpod.PodLifecycleObservation
		wantDead    bool
		wantMessage string
	}{
		{
			name: "fresh exited telemetry overrides running desired status",
			observation: &runpod.PodLifecycleObservation{
				DesiredStatus:   "RUNNING",
				LastStartedAt:   start,
				LatestTelemetry: &runpod.PodLifecycleTelemetry{State: "exited", Time: freshExit},
			},
			wantDead:    true,
			wantMessage: "provider telemetry state exited",
		},
		{
			name: "telemetry at generation boundary is current",
			observation: &runpod.PodLifecycleObservation{
				DesiredStatus:   "RUNNING",
				LastStartedAt:   start,
				LatestTelemetry: &runpod.PodLifecycleTelemetry{State: "EXITED", Time: start},
			},
			wantDead: true,
		},
		{
			name: "resumed generation ignores prior exit",
			observation: &runpod.PodLifecycleObservation{
				DesiredStatus:   "RUNNING",
				LastStartedAt:   start,
				LatestTelemetry: &runpod.PodLifecycleTelemetry{State: "exited", Time: oldExit},
			},
		},
		{
			name: "missing telemetry timestamp is nonterminal",
			observation: &runpod.PodLifecycleObservation{
				DesiredStatus:   "RUNNING",
				LastStartedAt:   start,
				LatestTelemetry: &runpod.PodLifecycleTelemetry{State: "exited"},
			},
		},
		{
			name: "missing start generation is nonterminal",
			observation: &runpod.PodLifecycleObservation{
				DesiredStatus:   "RUNNING",
				LatestTelemetry: &runpod.PodLifecycleTelemetry{State: "exited", Time: freshExit},
			},
		},
		{
			name: "negative uptime alone is nonterminal",
			observation: &runpod.PodLifecycleObservation{
				DesiredStatus:          "RUNNING",
				LastStartedAt:          start,
				LatestTelemetry:        &runpod.PodLifecycleTelemetry{State: "running", Time: freshExit},
				RuntimeUptimeInSeconds: &negativeUptime,
			},
		},
		{
			name: "unknown telemetry state is nonterminal",
			observation: &runpod.PodLifecycleObservation{
				DesiredStatus:   "RUNNING",
				LastStartedAt:   start,
				LatestTelemetry: &runpod.PodLifecycleTelemetry{State: "failed", Time: freshExit},
			},
		},
		{
			name: "graphql desired exit is terminal without telemetry",
			observation: &runpod.PodLifecycleObservation{
				DesiredStatus: "EXITED",
				LastStartedAt: start,
			},
			wantDead:    true,
			wantMessage: "GraphQL desired status EXITED",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := runpodtest.New()
			t.Cleanup(srv.Close)
			podID := strings.ReplaceAll(tc.name, " ", "-")
			srv.AddPod(&runpod.Pod{ID: podID, DesiredStatus: "RUNNING", LastStartedAt: start})
			observation := *tc.observation
			observation.PodID = podID
			srv.SetPodLifecycleObservation(&observation)

			verdict, dead, err := srv.MustClient().GetPodTerminalError(context.Background(), podID, nil)
			if err != nil {
				t.Fatalf("GetPodTerminalError: %v", err)
			}
			if dead != tc.wantDead {
				t.Fatalf("dead=%v want %v, verdict=%+v", dead, tc.wantDead, verdict)
			}
			if !tc.wantDead {
				if verdict != nil {
					t.Fatalf("nonterminal verdict: %+v", verdict)
				}
				return
			}
			if verdict == nil || verdict.Reason != "pod_exited" || verdict.Class != runpod.PodErrorUnknown {
				t.Fatalf("unexpected terminal verdict: %+v", verdict)
			}
			if tc.wantMessage != "" && !strings.Contains(verdict.Message, tc.wantMessage) {
				t.Fatalf("message %q does not contain %q", verdict.Message, tc.wantMessage)
			}
		})
	}
}
