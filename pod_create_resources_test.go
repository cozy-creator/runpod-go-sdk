package runpod_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func TestExecuteCreatePodSendsPreparedBytesUnchanged(t *testing.T) {
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		received, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"pod-1","desiredStatus":"RUNNING"}`))
	}))
	defer server.Close()

	client := mustClient(t, "test_key", runpod.WithBaseURL(server.URL))
	prepared, err := client.PrepareCreatePod(&runpod.CreatePodRequest{
		Name: "obligation-1", ImageName: "img", GPUTypeIDs: []string{"NVIDIA H200"},
		GPUCount: 1, ContainerDiskInGB: 100,
	})
	if err != nil {
		t.Fatalf("PrepareCreatePod: %v", err)
	}

	// Whitespace is semantically irrelevant JSON but byte-significant durable
	// intent. If ExecuteCreatePod re-marshals, this exact comparison goes red.
	prepared = append(append([]byte("\n  "), prepared...), '\n')
	if _, err := client.ExecuteCreatePod(context.Background(), prepared); err != nil {
		t.Fatalf("ExecuteCreatePod: %v", err)
	}
	if !bytes.Equal(received, prepared) {
		t.Fatalf("provider received %q, want exact prepared bytes %q", received, prepared)
	}
}

func TestInspectPreparedCreatePodReturnsRecordedPlacement(t *testing.T) {
	client := mustClient(t, "test_key")
	prepared, err := client.PrepareCreatePod(&runpod.CreatePodRequest{
		Name: "obligation-1", ImageName: "img", GPUTypeIDs: []string{"NVIDIA H200"},
		GPUCount: 2, DataCenterIDs: []string{"EU-RO-1"}, ContainerDiskInGB: 100,
		NetworkVolumeID: "volume-1", VolumeMountPath: "/workspace",
	})
	if err != nil {
		t.Fatalf("PrepareCreatePod: %v", err)
	}
	got, err := client.InspectPreparedCreatePod(prepared)
	if err != nil {
		t.Fatalf("InspectPreparedCreatePod: %v", err)
	}
	if len(got.GPUTypeIDs) != 1 || got.GPUTypeIDs[0] != "NVIDIA H200" ||
		got.GPUCount != 2 || len(got.DataCenterIDs) != 1 || got.DataCenterIDs[0] != "EU-RO-1" ||
		got.NetworkVolumeID != "volume-1" || got.VolumeMountPath != "/workspace" {
		t.Fatalf("inspected placement = %#v", got)
	}
}

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

func TestCreatePodBandwidthFloorsWireFormat(t *testing.T) {
	requestNumber := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if requestNumber == 1 && body["minDownloadMbps"] != float64(2000) {
			t.Errorf("minDownloadMbps = %v, want 2000", body["minDownloadMbps"])
		}
		if requestNumber == 1 && body["minUploadMbps"] != float64(1000) {
			t.Errorf("minUploadMbps = %v, want 1000", body["minUploadMbps"])
		}
		if requestNumber == 2 {
			if _, ok := body["minDownloadMbps"]; ok {
				t.Errorf("zero minDownloadMbps must be omitted: %v", body)
			}
			if _, ok := body["minUploadMbps"]; ok {
				t.Errorf("zero minUploadMbps must be omitted: %v", body)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"pod-1","desiredStatus":"RUNNING"}`))
	}))
	defer server.Close()

	client := mustClient(t, "test_key", runpod.WithBaseURL(server.URL))
	base := runpod.CreatePodRequest{
		Name: "n", ImageName: "img", GPUTypeIDs: []string{"NVIDIA GeForce RTX 4090"},
		GPUCount: 1, ContainerDiskInGB: 10,
	}
	withFloor := base
	withFloor.MinDownloadMbps = 2000
	withFloor.MinUploadMbps = 1000
	if _, err := client.CreatePod(context.Background(), &withFloor); err != nil {
		t.Fatalf("CreatePod with floor: %v", err)
	}
	if _, err := client.CreatePod(context.Background(), &base); err != nil {
		t.Fatalf("CreatePod without floor: %v", err)
	}
}
