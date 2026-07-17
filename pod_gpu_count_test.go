package runpod_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func TestCreatePodNormalizesOfficialNestedGPUCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/pods" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"id":"pod-official-shape",
			"desiredStatus":"RUNNING",
			"gpu":{"id":"NVIDIA GeForce RTX 4090","count":2,"displayName":"RTX 4090"}
		}`)
	}))
	defer server.Close()

	client := mustClient(t, "test_key", runpod.WithBaseURL(server.URL))
	pod, err := client.CreatePod(context.Background(), &runpod.CreatePodRequest{
		Name: "worker", ImageName: "worker:latest", GPUTypeIDs: []string{"NVIDIA GeForce RTX 4090"},
		GPUCount: 2, ContainerDiskInGB: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if pod.GPUCount != 2 || pod.GPU == nil || pod.GPU.Count != 2 {
		t.Fatalf("normalized pod GPU shape = %+v, want count 2", pod)
	}
}

func TestGetPodGPUCountNormalization(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{
			name: "nested documented shape",
			body: `{"id":"pod-1","gpu":{"count":4}}`,
			want: 4,
		},
		{name: "matching top-level compatibility", body: `{"id":"pod-1","gpuCount":2,"gpu":{"count":2}}`, want: 2},
		{name: "conflicting positive counts fail closed", body: `{"id":"pod-1","gpuCount":2,"gpu":{"count":4}}`, want: 0},
		{
			name: "missing count remains unknown",
			body: `{"id":"pod-1","gpu":{"id":"NVIDIA A40"}}`,
			want: 0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/pods/pod-1" {
					t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, test.body)
			}))
			defer server.Close()

			client := mustClient(t, "test_key", runpod.WithBaseURL(server.URL))
			pod, err := client.GetPod(context.Background(), "pod-1")
			if err != nil {
				t.Fatal(err)
			}
			if pod.GPUCount != test.want {
				t.Fatalf("GPUCount = %d, want %d: %+v", pod.GPUCount, test.want, pod)
			}
		})
	}
}

func TestListPodsNormalizesNestedGPUCountInBothWireShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "legacy wrapper compatibility", body: `{"pods":[{"id":"pod-1","gpu":{"count":2}}]}`},
		{name: "documented bare array", body: `[{"id":"pod-1","gpu":{"count":2}}]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/pods" {
					t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, test.body)
			}))
			defer server.Close()

			client := mustClient(t, "test_key", runpod.WithBaseURL(server.URL))
			pods, err := client.ListPods(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(pods) != 1 || pods[0].GPUCount != 2 {
				t.Fatalf("normalized pods = %+v, want one pod with count 2", pods)
			}
		})
	}
}
