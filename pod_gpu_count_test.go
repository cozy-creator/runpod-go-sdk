package runpod_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func TestFindPodsByNamePagination(t *testing.T) {
	t.Run("short page does not hide later matches", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Query().Get("offset") {
			case "0":
				_, _ = fmt.Fprint(w, `[{"id":"pod-a","name":"obligation-1"}]`)
			case "1":
				_, _ = fmt.Fprint(w, `[{"id":"pod-b","name":"obligation-1"}]`)
			default:
				_, _ = fmt.Fprint(w, `[]`)
			}
		}))
		defer server.Close()

		client := mustClient(t, "test_key", runpod.WithBaseURL(server.URL))
		pods, err := client.FindPodsByName(context.Background(), "obligation-1")
		if err != nil || len(pods) != 2 {
			t.Fatalf("FindPodsByName = %+v, %v; want both short-page matches", pods, err)
		}
	})

	t.Run("non-progress is refused", func(t *testing.T) {
		page := make([]string, 100)
		for i := range page {
			page[i] = fmt.Sprintf(`{"id":"pod-%03d","name":"obligation-1"}`, i)
		}
		body := "[" + strings.Join(page, ",") + "]"
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, body) // deliberately ignores offset
		}))
		defer server.Close()

		client := mustClient(t, "test_key", runpod.WithBaseURL(server.URL))
		_, err := client.FindPodsByName(context.Background(), "obligation-1")
		if err == nil || !strings.Contains(err.Error(), "pagination did not advance") {
			t.Fatalf("non-progress error = %v", err)
		}
	})
}

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
		cost string
	}{
		{name: "legacy wrapper compatibility", body: `{"pods":[{"id":"pod-1","gpu":{"count":2}}]}`},
		{name: "documented bare array with exact price", body: `[{"id":"pod-1","gpu":{"count":2},"costPerHr":0.123456}]`, cost: "0.123456"},
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
			if test.cost == "" {
				if pods[0].CostPerHourUSD != nil {
					t.Fatalf("CostPerHourUSD = %q, want nil for %s", pods[0].CostPerHourUSD, test.body)
				}
			} else if pods[0].CostPerHourUSD == nil || pods[0].CostPerHourUSD.String() != test.cost {
				t.Fatalf("CostPerHourUSD = %v, want %s for %s", pods[0].CostPerHourUSD, test.cost, test.body)
			}
		})
	}
}
