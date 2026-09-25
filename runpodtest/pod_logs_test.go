package runpodtest_test

import (
	"context"
	"testing"
	"time"

	runpod "github.com/cozy-creator/runpod-go-sdk"
	"github.com/cozy-creator/runpod-go-sdk/runpodtest"
)

func TestPodLogsPreserveSameTimestampDistinctEvents(t *testing.T) {
	server := runpodtest.New()
	defer server.Close()
	server.AddPod(&runpod.Pod{ID: "pod", DesiredStatus: "RUNNING"})
	stamp := time.Date(2026, 9, 25, 20, 45, 44, 0, time.UTC)
	server.SetPodLogs("pod", []runpod.PodLogEntry{
		{ID: stamp.Format(time.RFC3339), Source: runpod.PodLogSourceSystem, Timestamp: stamp, Line: "error creating volume"},
		{ID: stamp.Format(time.RFC3339), Source: runpod.PodLogSourceSystem, Timestamp: stamp, Line: "container needs volume"},
		{ID: stamp.Format(time.RFC3339), Source: runpod.PodLogSourceContainer, Timestamp: stamp, Line: "workload output"},
	})
	tail := 2
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := server.MustClient(runpod.WithTimeout(0)).StreamPodLogs(ctx, "pod", &runpod.PodLogsOptions{Source: runpod.PodLogSourceSystem, Tail: &tail})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	first, err := stream.Next()
	if err != nil {
		t.Fatal(err)
	}
	second, err := stream.Next()
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.Timestamp != second.Timestamp || first.Line == second.Line {
		t.Fatalf("lost distinct same-cursor events: %#v %#v", first, second)
	}
	if first.Line != "error creating volume" || second.Line != "container needs volume" {
		t.Fatal("source/tail/order changed")
	}
}
