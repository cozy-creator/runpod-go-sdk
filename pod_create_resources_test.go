package runpod_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func TestCreatePodGPUResourceMinimaWireFormat(t *testing.T) {
	requestNumber := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber++
		if r.Method != http.MethodPost || r.URL.Path != "/pods" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch requestNumber {
		case 1:
			if body["minRAMPerGPU"] != float64(32) {
				t.Errorf("minRAMPerGPU = %v, want 32", body["minRAMPerGPU"])
			}
			if body["minVCPUPerGPU"] != float64(8) {
				t.Errorf("minVCPUPerGPU = %v, want 8", body["minVCPUPerGPU"])
			}
		case 2:
			if _, ok := body["minRAMPerGPU"]; ok {
				t.Errorf("zero minRAMPerGPU must be omitted: %v", body)
			}
			if _, ok := body["minVCPUPerGPU"]; ok {
				t.Errorf("zero minVCPUPerGPU must be omitted: %v", body)
			}
		default:
			t.Fatalf("unexpected request number %d", requestNumber)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"pod-1","desiredStatus":"RUNNING"}`))
	}))
	defer server.Close()

	client := mustClient(t, "test_key", runpod.WithBaseURL(server.URL))
	base := runpod.CreatePodRequest{
		Name:              "n",
		ImageName:         "img",
		GPUTypeIDs:        []string{"NVIDIA GeForce RTX 4090"},
		GPUCount:          1,
		ContainerDiskInGB: 10,
	}

	withMinima := base
	withMinima.MinRAMPerGPU = 32
	withMinima.MinVCPUPerGPU = 8
	if _, err := client.CreatePod(context.Background(), &withMinima); err != nil {
		t.Fatalf("CreatePod with minima: %v", err)
	}
	if _, err := client.CreatePod(context.Background(), &base); err != nil {
		t.Fatalf("CreatePod without minima: %v", err)
	}
	if requestNumber != 2 {
		t.Fatalf("got %d requests, want 2", requestNumber)
	}
}
