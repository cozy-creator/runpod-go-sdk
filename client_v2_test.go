package runpod_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

// Management and per-endpoint inference quotas are independent provider budgets.
func TestV2QuotaScopes(t *testing.T) {
	for _, limitedPath := range []string{"/v2/pods", "/v2/limited/run"} {
		t.Run(limitedPath, func(t *testing.T) {
			var mu sync.Mutex
			hits := map[string]int{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				hits[r.URL.Path]++
				mu.Unlock()
				if r.URL.Path == limitedPath {
					w.Header().Set("Retry-After", "6000")
					w.WriteHeader(429)
					fmt.Fprint(w, `{"title":"Too Many Requests","status":429,"detail":"quota exhausted"}`)
					return
				}
				if r.URL.Path == "/v2/pods/pod" {
					fmt.Fprint(w, `{"id":"pod","name":"worker","status":"RUNNING"}`)
					return
				}
				fmt.Fprint(w, `{"id":"job","status":"IN_QUEUE"}`)
			}))
			defer server.Close()
			client := mustClient(t, "fixture", runpod.WithBaseURL(server.URL+"/v2/"), runpod.WithServerlessBaseURL(server.URL), runpod.WithMaxRetryAttempts(0))
			if limitedPath == "/v2/pods" {
				if err := client.Get(t.Context(), "/pods", nil); !errors.Is(err, runpod.ErrRateLimited) {
					t.Fatal(err)
				}
				if client.Cooldown() == 0 {
					t.Fatal("missing management cooldown")
				}
			} else {
				if _, err := client.RunAsync(t.Context(), "limited", nil); !errors.Is(err, runpod.ErrRateLimited) {
					t.Fatal(err)
				}
				if _, err := client.RunAsync(t.Context(), "limited", nil); !errors.Is(err, runpod.ErrRateLimited) {
					t.Fatal(err)
				}
				if client.ServerlessCooldown("limited") == 0 || client.Cooldown() != 0 {
					t.Fatal("inference budget leaked into management")
				}
				if _, err := client.GetPod(t.Context(), "pod"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := client.RunAsync(t.Context(), "healthy", nil); err != nil {
				t.Fatalf("independent endpoint blocked: %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if hits[limitedPath] != 1 {
				t.Fatalf("limited resource retried %d times", hits[limitedPath])
			}
		})
	}
}

func TestOnlyV2ControlPlaneDefault(t *testing.T) {
	if runpod.DefaultBaseURL != "https://api.runpod.io/v2" {
		t.Fatal(runpod.DefaultBaseURL)
	}
}
