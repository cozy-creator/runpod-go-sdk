package runpod_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func newVolumeServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test_key" {
			t.Fatalf("missing auth header, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == "GET" && r.URL.Path == "/networkvolumes":
			// RunPod returns a bare array.
			w.Write([]byte(`[{"id":"vol1","name":"models","size":100,"dataCenterId":"EU-RO-1"}]`))
		case r.Method == "POST" && r.URL.Path == "/networkvolumes":
			var req runpod.CreateNetworkVolumeRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if req.DataCenterID != "EU-RO-1" {
				t.Fatalf("dataCenterId not marshalled correctly: %+v", req)
			}
			json.NewEncoder(w).Encode(runpod.NetworkVolume{ID: "vol2", Name: req.Name, Size: req.Size, DataCenterID: req.DataCenterID})
		case r.Method == "GET" && r.URL.Path == "/networkvolumes/vol1":
			w.Write([]byte(`{"id":"vol1","name":"models","size":100,"dataCenterId":"EU-RO-1"}`))
		case r.Method == "PATCH" && r.URL.Path == "/networkvolumes/vol1":
			var req runpod.UpdateNetworkVolumeRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode: %v", err)
			}
			json.NewEncoder(w).Encode(runpod.NetworkVolume{ID: "vol1", Name: "models", Size: req.Size, DataCenterID: "EU-RO-1"})
		case r.Method == "DELETE" && r.URL.Path == "/networkvolumes/vol1":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
}

func TestNetworkVolumeCRUD(t *testing.T) {
	server := newVolumeServer(t)
	defer server.Close()

	client := mustClient(t, "test_key", runpod.WithBaseURL(server.URL))
	ctx := context.Background()

	volumes, err := client.ListNetworkVolumes(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(volumes) != 1 || volumes[0].ID != "vol1" || volumes[0].DataCenterID != "EU-RO-1" {
		t.Fatalf("unexpected list result %+v", volumes)
	}

	created, err := client.CreateNetworkVolume(ctx, &runpod.CreateNetworkVolumeRequest{
		Name: "cache", Size: 50, DataCenterID: "EU-RO-1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID != "vol2" || created.Size != 50 {
		t.Fatalf("unexpected create result %+v", created)
	}

	got, err := client.GetNetworkVolume(ctx, "vol1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != "vol1" {
		t.Fatalf("unexpected get result %+v", got)
	}

	resized, err := client.UpdateNetworkVolume(ctx, "vol1", &runpod.UpdateNetworkVolumeRequest{Size: 200})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if resized.Size != 200 {
		t.Fatalf("unexpected update result %+v", resized)
	}

	if err := client.DeleteNetworkVolume(ctx, "vol1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestNetworkVolumeValidation(t *testing.T) {
	client := mustClient(t, "test_key")
	ctx := context.Background()

	if _, err := client.CreateNetworkVolume(ctx, nil); err == nil {
		t.Fatal("nil request must fail validation")
	}
	if _, err := client.CreateNetworkVolume(ctx, &runpod.CreateNetworkVolumeRequest{Name: "x", Size: 10}); err == nil {
		t.Fatal("missing dataCenterId must fail validation")
	}
	if _, err := client.UpdateNetworkVolume(ctx, "vol1", &runpod.UpdateNetworkVolumeRequest{}); err == nil {
		t.Fatal("empty update must fail validation")
	}
	if _, err := client.UpdateNetworkVolume(ctx, "", &runpod.UpdateNetworkVolumeRequest{Size: 10}); err == nil {
		t.Fatal("empty volumeID must fail validation")
	}
}
