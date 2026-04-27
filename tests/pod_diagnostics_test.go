package runpod_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func TestGetPodWithOptions_QueryParams(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test_key" {
			t.Fatalf("unexpected auth header %q", got)
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/pods/pod-1") {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("includeMachine") != "true" || q.Get("includeNetworkVolume") != "true" || q.Get("includeWorkers") != "true" {
			t.Fatalf("missing include query params: %v", q)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":            "pod-1",
			"desiredStatus": "RUNNING",
		})
	}))
	defer server.Close()

	client := runpod.NewClient("test_key", runpod.WithBaseURL(server.URL+"/v1"))
	_, err := client.GetPodWithOptions(context.Background(), "pod-1", &runpod.GetPodOptions{
		IncludeMachine:       true,
		IncludeNetworkVolume: true,
		IncludeWorkers:       true,
	})
	if err != nil {
		t.Fatalf("GetPodWithOptions error: %v", err)
	}
}

func TestGetPodDiagnostics_StatusMatrix(t *testing.T) {
	tests := []struct {
		name             string
		response         map[string]interface{}
		wantRuntimeReady bool
		wantDataCenter   string
		wantReason       string
	}{
		{
			name: "created_runtime_nil",
			response: map[string]interface{}{
				"id":            "pod-created",
				"desiredStatus": "CREATED",
				"runtime":       nil,
			},
			wantRuntimeReady: false,
			wantDataCenter:   "",
			wantReason:       "",
		},
		{
			name: "running_runtime_ready",
			response: map[string]interface{}{
				"id":               "pod-running",
				"desiredStatus":    "RUNNING",
				"lastStatusChange": "2026-02-11T00:00:00Z",
				"runtime": map[string]interface{}{
					"publicIp": "1.2.3.4",
					"ports": map[string]interface{}{
						"http": map[string]interface{}{"publicPort": 443},
					},
				},
				"machine": map[string]interface{}{
					"id":           "machine-1",
					"dataCenterId": "US-CA-1",
				},
				"networkVolume": map[string]interface{}{
					"id":           "vol-1",
					"datacenterId": "US-CA-1",
				},
			},
			wantRuntimeReady: true,
			wantDataCenter:   "US-CA-1",
			wantReason:       "",
		},
		{
			name: "running_runtime_missing",
			response: map[string]interface{}{
				"id":            "pod-stuck",
				"desiredStatus": "RUNNING",
				"runtime":       nil,
			},
			wantRuntimeReady: false,
			wantDataCenter:   "",
			wantReason:       "runtime_unavailable",
		},
		{
			name: "exited_with_reason",
			response: map[string]interface{}{
				"id":            "pod-exited",
				"desiredStatus": "EXITED",
				"runtime": map[string]interface{}{
					"reason": "container_exit",
				},
			},
			wantRuntimeReady: false,
			wantDataCenter:   "",
			wantReason:       "container_exit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			client := runpod.NewClient("test_key", runpod.WithBaseURL(server.URL))
			diag, err := client.GetPodDiagnostics(context.Background(), "pod-any")
			if err != nil {
				t.Fatalf("GetPodDiagnostics error: %v", err)
			}

			if diag.RuntimeReady != tt.wantRuntimeReady {
				t.Fatalf("runtimeReady=%v want=%v", diag.RuntimeReady, tt.wantRuntimeReady)
			}
			if diag.DataCenterID != tt.wantDataCenter {
				t.Fatalf("dataCenter=%q want=%q", diag.DataCenterID, tt.wantDataCenter)
			}
			if diag.ProviderReason != tt.wantReason {
				t.Fatalf("providerReason=%q want=%q", diag.ProviderReason, tt.wantReason)
			}
		})
	}
}

func TestGetPodLogs_RouteNotFoundBecomesCapabilityError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"message": "route not found",
		})
	}))
	defer server.Close()

	client := runpod.NewClient("test_key", runpod.WithBaseURL(server.URL))
	_, err := client.GetPodLogs(context.Background(), "pod-1")
	if err == nil {
		t.Fatal("expected capability error")
	}
	if !runpod.IsCapabilityNotAvailable(err) {
		t.Fatalf("expected CapabilityNotAvailableError, got %T (%v)", err, err)
	}
}

func TestGetProviderFeatureSupport(t *testing.T) {
	client := runpod.NewClient("test_key")
	cap := client.GetProviderFeatureSupport(context.Background())
	if cap.PodLogsAPI {
		t.Fatal("expected pod logs capability to be false")
	}
	if strings.TrimSpace(cap.Reason) == "" {
		t.Fatal("expected non-empty reason")
	}
}
