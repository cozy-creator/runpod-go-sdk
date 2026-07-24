package runpod_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func TestListPodCreateDatacenterIDs(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    []string
		wantErr string
	}{
		{
			name: "trimmed and deduplicated",
			body: `{"components":{"schemas":{"PodCreateInput":{"properties":{"dataCenterIds":{"enum":["EU-RO-1"," US-GA-1 ","EU-RO-1",""]}}}}}}`,
			want: []string{"EU-RO-1", "US-GA-1"},
		},
		{
			name:    "missing enum fails closed",
			body:    `{"components":{"schemas":{"PodCreateInput":{"properties":{"dataCenterIds":{}}}}}}`,
			wantErr: "dataCenterIds enum is missing or empty",
		},
		{
			name:    "malformed enum fails closed",
			body:    `{"components":{"schemas":{"PodCreateInput":{"properties":{"dataCenterIds":{"enum":"EU-RO-1"}}}}}}`,
			wantErr: "failed to decode RunPod OpenAPI schema",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/openapi.json" {
					t.Fatalf("request = %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			client := mustClient(t, "test_key", runpod.WithBaseURL(server.URL), runpod.WithMaxRetryAttempts(0))
			got, err := client.ListPodCreateDatacenterIDs(t.Context())
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ListPodCreateDatacenterIDs: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("ids = %#v, want %#v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("ids = %#v, want %#v", got, tt.want)
				}
			}
		})
	}
}
