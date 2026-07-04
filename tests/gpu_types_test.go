package runpod_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

type testGraphQLRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables"`
}

func newGPUTypeGraphQLServer(t *testing.T, handler func(req testGraphQLRequest) interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("api_key"); got != "" {
			t.Fatalf("api key must not leak into the URL, got %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test_key" {
			t.Fatalf("expected authorization header, got %q", got)
		}

		var req testGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = r.Body.Close()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(handler(req))
	}))
}

func TestListGPUTypes_Filtering(t *testing.T) {
	server := newGPUTypeGraphQLServer(t, func(req testGraphQLRequest) interface{} {
		if !strings.Contains(req.Query, "gpuTypes") {
			t.Fatalf("expected gpuTypes query")
		}
		return map[string]interface{}{
			"data": map[string]interface{}{
				"gpuTypes": []map[string]interface{}{
					{
						"id":             "NVIDIA GeForce RTX 4090",
						"displayName":    "RTX 4090",
						"memoryInGb":     24,
						"secureCloud":    true,
						"communityCloud": true,
						"lowestPrice": map[string]interface{}{
							"minimumBidPrice":      0.10,
							"uninterruptablePrice": 0.49,
							"stockStatus":          "AVAILABLE",
							"cudaVersion":          "12.8",
						},
					},
					{
						"id":             "NVIDIA A100 SXM",
						"displayName":    "A100",
						"memoryInGb":     80,
						"secureCloud":    false,
						"communityCloud": true,
						"lowestPrice": map[string]interface{}{
							"minimumBidPrice":      1.10,
							"uninterruptablePrice": 2.49,
							"stockStatus":          "LOW",
							"cudaVersion":          "12.8",
						},
					},
					{
						"id":             "NVIDIA H100 SXM",
						"displayName":    "H100",
						"memoryInGb":     80,
						"secureCloud":    true,
						"communityCloud": true,
						"lowestPrice":    nil,
					},
				},
			},
		}
	})
	defer server.Close()

	client := mustClient(t, "test_key", runpod.WithGraphQLBaseURL(server.URL))
	got, err := client.ListGPUTypes(context.Background(), &runpod.GPUTypeFilter{
		IDs:            []string{"NVIDIA GeForce RTX 4090", "NVIDIA A100 SXM"},
		MinCudaVersion: "12.8",
		SecureCloud:    ptrBool(true),
	})
	if err != nil {
		t.Fatalf("ListGPUTypes error: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if got[0].ID != "NVIDIA GeForce RTX 4090" {
		t.Fatalf("unexpected gpu id %q", got[0].ID)
	}
}

func TestListAvailableGPUs_OnlyAvailable(t *testing.T) {
	server := newGPUTypeGraphQLServer(t, func(req testGraphQLRequest) interface{} {
		return map[string]interface{}{
			"data": map[string]interface{}{
				"gpuTypes": []map[string]interface{}{
					{
						"id":             "gpu-1",
						"displayName":    "GPU 1",
						"memoryInGb":     24,
						"secureCloud":    true,
						"communityCloud": false,
						"lowestPrice": map[string]interface{}{
							"uninterruptablePrice": 0.90,
							"stockStatus":          "AVAILABLE",
						},
					},
					{
						"id":             "gpu-2",
						"displayName":    "GPU 2",
						"memoryInGb":     24,
						"secureCloud":    true,
						"communityCloud": false,
						"lowestPrice": map[string]interface{}{
							"uninterruptablePrice": 0.80,
							"stockStatus":          "OUT_OF_STOCK",
						},
					},
					{
						"id":             "gpu-3",
						"displayName":    "GPU 3",
						"memoryInGb":     24,
						"secureCloud":    true,
						"communityCloud": false,
						"lowestPrice": map[string]interface{}{
							"uninterruptablePrice": 0.70,
							"stockStatus":          "LOW",
						},
					},
				},
			},
		}
	})
	defer server.Close()

	client := mustClient(t, "test_key", runpod.WithGraphQLBaseURL(server.URL))
	got, err := client.ListAvailableGPUs(context.Background(), "12.8", 1)
	if err != nil {
		t.Fatalf("ListAvailableGPUs error: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 available GPUs, got %d", len(got))
	}
	// sorted by uninterruptablePrice asc
	if got[0].ID != "gpu-3" || got[1].ID != "gpu-1" {
		t.Fatalf("unexpected order: %q then %q", got[0].ID, got[1].ID)
	}
}

func TestGetGPUType(t *testing.T) {
	server := newGPUTypeGraphQLServer(t, func(req testGraphQLRequest) interface{} {
		return map[string]interface{}{
			"data": map[string]interface{}{
				"gpuTypes": []map[string]interface{}{
					{
						"id":             "gpu-1",
						"displayName":    "GPU 1",
						"memoryInGb":     24,
						"secureCloud":    true,
						"communityCloud": false,
						"lowestPrice": map[string]interface{}{
							"uninterruptablePrice": 1.23,
							"stockStatus":          "AVAILABLE",
						},
					},
				},
			},
		}
	})
	defer server.Close()

	client := mustClient(t, "test_key", runpod.WithGraphQLBaseURL(server.URL))

	found, err := client.GetGPUType(context.Background(), "gpu-1")
	if err != nil {
		t.Fatalf("GetGPUType should succeed: %v", err)
	}
	if found.ID != "gpu-1" {
		t.Fatalf("unexpected id %q", found.ID)
	}

	_, err = client.GetGPUType(context.Background(), "missing-gpu")
	if err == nil {
		t.Fatal("expected not found error")
	}
	if !errors.Is(err, runpod.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func ptrBool(v bool) *bool { return &v }
