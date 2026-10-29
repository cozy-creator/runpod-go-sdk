package runpod_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func podV2Client(t *testing.T, handler http.HandlerFunc) *runpod.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return mustClient(t, "key", runpod.WithBaseURL(server.URL+"/v2"), runpod.WithMaxRetryAttempts(0))
}

func TestV2PreparedCreatePreservesExactNativeBytes(t *testing.T) {
	request := &runpod.CreatePodRequest{Name: "worker", Image: "image:tag", Disk: 80, Cloud: "SECURE",
		GPU:           &runpod.CreateGPUConfig{ID: "NVIDIA GeForce RTX 4090", Count: 2, MinCUDAVersion: "12.8", MinRAMPerGPU: 16, MinVCPUCountPerGPU: 4},
		DataCenterIDs: []string{"US-KS-2"}, Registry: "registry-a", Entrypoint: []string{"/bin/sh"}, Cmd: []string{"-c", "serve"},
		Mounts: &runpod.Mounts{Network: []runpod.VolumeMount{{VolumeID: "volume-a", Path: "/models"}}}}
	var sent []byte
	client := podV2Client(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v2/pods" {
			t.Errorf("request %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer key" {
			t.Error("missing bearer auth")
		}
		sent, _ = io.ReadAll(r.Body)
		fmt.Fprint(w, `{"id":"pod-1","status":"PROVISIONING","image":"image:tag","gpu":{"id":"NVIDIA GeForce RTX 4090","count":2},"cost":0.44}`)
	})
	prepared, err := client.PrepareCreatePod(request)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(prepared, &wire); err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []string{"gpuTypeIds", "gpuCount", "imageName", "containerDiskInGb", "networkVolumeId", "minCudaVersion"} {
		if _, exists := wire[legacy]; exists {
			t.Fatalf("obsolete field %s in %s", legacy, prepared)
		}
	}
	if !bytes.Contains(prepared, []byte(`"gpu":{"id":"NVIDIA GeForce RTX 4090","count":2,"minCudaVersion":"12.8","minRamPerGpu":16,"minVcpuCountPerGpu":4}`)) {
		t.Fatalf("nested GPU contract %s", prepared)
	}
	// Formatting must survive a prepare/record/execute boundary unchanged.
	recorded := append([]byte(" \n"), prepared...)
	inspected, err := client.InspectPreparedCreatePod(recorded)
	if err != nil || inspected.GPU.Count != 2 || inspected.Mounts.Network[0].VolumeID != "volume-a" {
		t.Fatalf("inspect %+v %v", inspected, err)
	}
	pod, err := client.ExecuteCreatePod(t.Context(), recorded)
	if err != nil || pod.Status != "PROVISIONING" || pod.Cost == nil || *pod.Cost != 440000 {
		t.Fatalf("pod %+v %v", pod, err)
	}
	if !bytes.Equal(sent, recorded) {
		t.Fatalf("recorded bytes changed: %q != %q", recorded, sent)
	}
}

func TestV2CreateValidationRefusesObsoleteAndConflictingBodies(t *testing.T) {
	requests := 0
	client := podV2Client(t, func(w http.ResponseWriter, r *http.Request) { requests++ })
	for _, body := range []string{
		`{"name":"w","imageName":"img","gpuTypeIds":["A"],"gpuCount":1}`,
		`{"query":"mutation {createPod}"}`,
		`{"name":"w","image":"img","gpu":{"id":"A","bid":0.1}}`,
		`{"name":"w","image":"img","gpu":{"id":"A"},"cpu":{"id":"cpu5c","vcpuCount":2}}`,
		`{"name":"w","image":"img","gpu":{"id":"A","minCudaVersion":"12.8","allowedCudaVersions":["12.9"]}}`,
		`{"name":"w","image":"img","cpu":{"id":"cpu5c","vcpuCount":3}}`,
		`{"name":"w","image":"img","cpu":{"id":"cpu5c","vcpuCount":2},"mounts":{"persistent":{"size":10,"path":"/work"}}}`,
		`{"name":"w","image":"img","gpu":{"id":"A"},"mounts":{"persistent":{"size":9,"path":"/work"}}}`,
		`{"name":"w","image":"img","gpu":{"id":"A"},"mounts":{"network":[{"volumeId":"v","path":"relative"}]}}`,
		`{"name":"w","image":"img","gpu":{"id":"A"}} {}`,
	} {
		if _, err := client.ExecuteCreatePod(t.Context(), []byte(body)); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
	if requests != 0 {
		t.Fatalf("invalid purchase sent %d requests", requests)
	}
	body, err := client.PrepareCreatePod(&runpod.CreatePodRequest{Name: "cpu", Image: "img", CPU: &runpod.CreateCPUConfig{ID: "cpu5c", VCPUCount: 4}})
	if err != nil || bytes.Contains(body, []byte(`"gpu"`)) || !bytes.Contains(body, []byte(`"cpu":{"id":"cpu5c","vcpuCount":4}`)) {
		t.Fatalf("CPU body %s %v", body, err)
	}
}

func TestV2PodLifecycleUsesOneActionEndpoint(t *testing.T) {
	actions := []string{}
	client := podV2Client(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/pods/pod-1/action" || r.Method != "POST" {
			t.Errorf("obsolete lifecycle route %s %s", r.Method, r.URL)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		actions = append(actions, body["action"])
		fmt.Fprint(w, `{"id":"pod-1","status":"STARTING"}`)
	})
	if err := client.StopPod(t.Context(), "pod-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ResumePod(t.Context(), "pod-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RestartPod(t.Context(), "pod-1"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(actions, ",") != "stop,start,restart" {
		t.Fatalf("actions %v", actions)
	}
}

func TestV2FindPodsByNameFollowsCursorAndFiltersLocally(t *testing.T) {
	requests := 0
	client := podV2Client(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Get("name") != "" || r.URL.Query().Get("offset") != "" {
			t.Error("unsupported query filter")
		}
		if requests == 1 {
			fmt.Fprint(w, `{"pods":[{"id":"pod-z","name":"target","status":"PROVISIONING"},{"id":"other","name":"else","status":"RUNNING"}],"pagination":{"nextCursor":"opaque","hasNextPage":true}}`)
		} else {
			if r.URL.Query().Get("cursor") != "opaque" {
				t.Error("lost cursor")
			}
			fmt.Fprint(w, `{"pods":[{"id":"pod-a","name":"target","status":"ERROR"}],"pagination":{"nextCursor":null,"hasNextPage":false}}`)
		}
	})
	pods, err := client.FindPodsByName(t.Context(), "target")
	if err != nil || len(pods) != 2 || pods[0].ID != "pod-a" || pods[1].ID != "pod-z" || requests != 2 {
		t.Fatalf("pods %+v, %v, requests %d", pods, err, requests)
	}
}

func TestV2ListPodsRefusesIncompleteOrStalledEvidence(t *testing.T) {
	for _, body := range []string{
		`[]`,
		`{"pods":null,"pagination":{"nextCursor":null,"hasNextPage":false}}`,
		`{"pods":[],"pagination":{"hasNextPage":false}}`,
		`{"pods":[],"pagination":{"nextCursor":null}}`,
		`{"pods":[{"id":"pod-1","status":"RUNNING"}],"pagination":{"nextCursor":null,"hasNextPage":false}}`,
		`{"pods":[],"pagination":{"nextCursor":"next","hasNextPage":true}}`,
		`{"pods":[{"id":"pod-1","name":"worker","status":"RUNNING"}],"pagination":{"nextCursor":"repeat","hasNextPage":true}}`,
		`{"pods":[{"id":"pod-1","desiredStatus":"RUNNING"}],"pagination":{"nextCursor":null,"hasNextPage":false}}`,
	} {
		t.Run(body, func(t *testing.T) {
			calls := 0
			client := podV2Client(t, func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, body) })
			if _, err := client.ListPods(t.Context(), nil); err == nil {
				t.Errorf("accepted %s", body)
			}
			if calls > 2 {
				t.Fatalf("stalled list made %d requests", calls)
			}
		})
	}
}

func TestV2ReadbackAbsenceAndIdentityBoundaries(t *testing.T) {
	for _, tc := range []struct {
		status  int
		body    string
		missing bool
	}{
		{404, `{"title":"Not Found","status":404,"detail":"pod not found"}`, true},
		{200, `{"id":"wrong","status":"RUNNING"}`, false},
		{200, `{"id":"pod-1","desiredStatus":"RUNNING"}`, false},
		{200, `{`, false},
	} {
		client := podV2Client(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) })
		_, err := client.GetPodReadback(t.Context(), "pod-1")
		if err == nil || errors.Is(err, runpod.ErrNotFound) != tc.missing {
			t.Fatalf("readback %s classified %v", tc.body, err)
		}
		var typed *runpod.PodReadbackError
		if !errors.As(err, &typed) || typed.Partial == nil || typed.Partial.RESTObserved.CompletedAt.Before(typed.Partial.RESTObserved.StartedAt) {
			t.Fatalf("lost timing/typed error %v", err)
		}
		if tc.status == 200 && tc.body != `{` && (!errors.Is(err, runpod.ErrIncompleteReadback) || typed.Partial.REST == nil) {
			t.Fatalf("invalid snapshot lost: %+v", typed)
		}
	}
}

func TestV2NativePodEvidenceAndPublicPorts(t *testing.T) {
	client := podV2Client(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"pod-1","status":"RUNNING","image":"img","ports":["22/tcp","8080/tcp"],"dataCenterId":"US-KS-2","startedAt":"2026-10-28T12:00:00Z","gpu":{"id":"A","count":2,"memory":64,"vcpuCount":8},"runtime":{"uptime":0,"ports":[{"private":8080,"public":30080,"type":"tcp","ip":"203.0.113.9"},{"private":22,"public":null,"type":"tcp","ip":null}]}}`)
	})
	readback, err := client.GetPodReadback(t.Context(), "pod-1")
	if err != nil {
		t.Fatal(err)
	}
	ports, err := readback.ValidatedPublicTCPEndpoints()
	if err != nil || len(ports) != 1 || ports[0].Private != 8080 || *ports[0].Public != 30080 {
		t.Fatalf("ports %+v %v", ports, err)
	}
	lifecycle, err := client.GetPodLifecycleObservation(t.Context(), "pod-1")
	if err != nil || lifecycle.RuntimeUptimeInSeconds == nil || *lifecycle.RuntimeUptimeInSeconds != 0 {
		t.Fatalf("zero uptime was lost %+v %v", lifecycle, err)
	}
	if readback.REST.Cost != nil {
		t.Fatal("missing price became a free price")
	}
	readback.REST.Runtime.Ports[0].Private = 9999
	if _, err := readback.ValidatedPublicTCPEndpoints(); err == nil {
		t.Fatal("accepted unconfigured public mapping")
	}
	readback.REST.Runtime.Ports[0].Private = 8080
	public, otherIP := 3022, "203.0.113.10"
	readback.REST.Runtime.Ports = append(readback.REST.Runtime.Ports, runpod.PodRuntimePort{Private: 22, Public: &public, Type: "tcp", IP: &otherIP})
	if _, err := readback.ValidatedPublicTCPEndpoints(); err == nil {
		t.Fatal("accepted contradictory public host projection")
	}
	readback.REST.Status = "EXITED"
	if ports, err := readback.ValidatedPublicTCPEndpoints(); err != nil || len(ports) != 0 {
		t.Fatalf("stale stopped ports exposed: %+v %v", ports, err)
	}
}

func TestV2TerminalStatesAndSSHUseNativeResource(t *testing.T) {
	state := "PROVISIONING"
	client := podV2Client(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"id":"pod-1","status":%q,"image":"img","ssh":{"direct":{"host":"203.0.113.9","port":3022,"username":"operator"}}}`, state)
	})
	for _, status := range []string{"PROVISIONING", "STARTING", "RUNNING", "ERROR", "EXITED", "TERMINATED"} {
		state = status
		verdict, terminal, err := client.GetPodTerminalError(t.Context(), "pod-1", nil)
		wantTerminal := status == "ERROR" || status == "EXITED" || status == "TERMINATED"
		if err != nil || terminal != wantTerminal {
			t.Fatalf("status %s => %+v %v %v", status, verdict, terminal, err)
		}
		if terminal && (verdict.Class != runpod.PodErrorUnknown || verdict.ContainerObserved) {
			t.Fatalf("invented failure cause %+v", verdict)
		}
		_, err = client.DiscoverSSHTarget(t.Context(), "pod-1")
		if (err == nil) != (status == "RUNNING") {
			t.Fatalf("SSH for %s: %v", status, err)
		}
	}
	state = "RUNNING"
	target, err := client.DiscoverSSHTarget(t.Context(), "pod-1")
	if err != nil || target.User != "operator" || target.Port != 3022 {
		t.Fatalf("native SSH %+v %v", target, err)
	}
	state = "ERROR"
	_, ready, err := client.WaitForPodReady(context.Background(), "pod-1", &runpod.WaitForPodReadyOptions{Timeout: time.Second})
	if err == nil || ready != runpod.PodReadyStateTerminal {
		t.Fatalf("ERROR was not terminal %v %v", ready, err)
	}
}
