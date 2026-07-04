package runpod_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

// newFallbackServer returns a POST /pods handler that responds per GPU type:
// map value is the status code; 0 means success. Records the order of
// attempted GPU types.
func newFallbackServer(t *testing.T, responses map[string]int, body string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var attempted []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/pods" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var req struct {
			GPUTypeIDs []string `json:"gpuTypeIds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(req.GPUTypeIDs) != 1 {
			t.Fatalf("fan-out must send exactly one gpuTypeId per request, got %v", req.GPUTypeIDs)
		}
		gpuType := req.GPUTypeIDs[0]
		mu.Lock()
		attempted = append(attempted, gpuType)
		mu.Unlock()

		status := responses[gpuType]
		w.Header().Set("Content-Type", "application/json")
		if status == 0 {
			fmt.Fprintf(w, `{"id":"pod-%s","name":"n","desiredStatus":"RUNNING"}`, gpuType)
			return
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))

	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := append([]string(nil), attempted...)
		return out
	}
}

func fallbackClient(url string) *runpod.Client {
	return runpod.NewClient("test_key",
		runpod.WithBaseURL(url),
		runpod.WithMaxRetryAttempts(0),
		runpod.WithRetryDelay(time.Millisecond),
	)
}

func baseGPURequest(types ...string) *runpod.CreatePodRequest {
	return &runpod.CreatePodRequest{
		Name:              "n",
		ImageName:         "img",
		GPUTypeIDs:        types,
		GPUCount:          1,
		ContainerDiskInGB: 10,
	}
}

func TestFallback_FirstStockOutSecondSucceeds(t *testing.T) {
	server, attempted := newFallbackServer(t,
		map[string]int{"A": 500, "B": 0},
		`{"error":"no instances available"}`)
	defer server.Close()

	pod, err := fallbackClient(server.URL).CreatePod(context.Background(), baseGPURequest("A", "B"))
	if err != nil {
		t.Fatalf("expected success on second candidate, got %v", err)
	}
	if pod.ID != "pod-B" {
		t.Fatalf("expected pod-B, got %q", pod.ID)
	}
	if got := attempted(); len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Fatalf("expected attempts [A B], got %v", got)
	}
}

func TestFallback_AllStockOut(t *testing.T) {
	server, attempted := newFallbackServer(t,
		map[string]int{"A": 500, "B": 500},
		`{"error":"no instances available"}`)
	defer server.Close()

	_, err := fallbackClient(server.URL).CreatePod(context.Background(), baseGPURequest("A", "B"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, runpod.ErrNoCapacity) {
		t.Fatalf("aggregate error should match ErrNoCapacity, got %v", err)
	}
	var exhausted *runpod.FallbackExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("expected FallbackExhaustedError, got %T: %v", err, err)
	}
	if len(exhausted.Attempts) != 2 {
		t.Fatalf("expected 2 recorded attempts, got %+v", exhausted.Attempts)
	}
	var noCap *runpod.NoCapacityError
	if !errors.As(exhausted.Attempts[0].Err, &noCap) || noCap.GPUTypeID != "A" {
		t.Fatalf("attempt error should be NoCapacityError with GPUTypeID, got %v", exhausted.Attempts[0].Err)
	}
	if got := attempted(); len(got) != 2 {
		t.Fatalf("expected 2 attempts, got %v", got)
	}
}

func TestFallback_AbortsOnClientError(t *testing.T) {
	server, attempted := newFallbackServer(t,
		map[string]int{"A": 400, "B": 0},
		`{"error":"invalid request"}`)
	defer server.Close()

	_, err := fallbackClient(server.URL).CreatePod(context.Background(), baseGPURequest("A", "B"))
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, runpod.ErrNoCapacity) {
		t.Fatalf("4xx must not classify as capacity error: %v", err)
	}
	if got := attempted(); len(got) != 1 {
		t.Fatalf("4xx should abort the fan-out after 1 attempt, got %v", got)
	}
}

func TestFallback_SingleTypeNoCapacityTyped(t *testing.T) {
	server, _ := newFallbackServer(t,
		map[string]int{"A": 500},
		`{"error":"There are no longer any instances available with the requested specifications."}`)
	defer server.Close()

	req := baseGPURequest("A")
	req.DataCenterIDs = []string{"EU-RO-1"}
	_, err := fallbackClient(server.URL).CreatePod(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, runpod.ErrNoCapacity) {
		t.Fatalf("expected ErrNoCapacity, got %v", err)
	}
	var noCap *runpod.NoCapacityError
	if !errors.As(err, &noCap) {
		t.Fatalf("expected NoCapacityError, got %T", err)
	}
	if noCap.GPUTypeID != "A" || len(noCap.DataCenterIDs) != 1 || noCap.DataCenterIDs[0] != "EU-RO-1" {
		t.Fatalf("NoCapacityError fields not populated: %+v", noCap)
	}
}

func TestFallback_PlainServerErrorNotCapacityButContinues(t *testing.T) {
	server, attempted := newFallbackServer(t,
		map[string]int{"A": 502, "B": 0},
		`{"error":"bad gateway"}`)
	defer server.Close()

	pod, err := fallbackClient(server.URL).CreatePod(context.Background(), baseGPURequest("A", "B"))
	if err != nil {
		t.Fatalf("5xx on first candidate should continue fan-out, got %v", err)
	}
	if pod.ID != "pod-B" {
		t.Fatalf("expected pod-B, got %q", pod.ID)
	}
	if got := attempted(); len(got) != 2 {
		t.Fatalf("expected 2 attempts, got %v", got)
	}
}

func TestFallback_CandidateFilterAndFailureHook(t *testing.T) {
	server, attempted := newFallbackServer(t,
		map[string]int{"A": 500, "B": 500, "C": 0},
		`{"error":"no instances available"}`)
	defer server.Close()

	var hookCalls []string
	opts := &runpod.CreatePodFallbackOptions{
		CandidateFilter: func(candidates []string) []string {
			// Simulate a failure tracker that already knows A is bad.
			out := make([]string, 0, len(candidates))
			for _, c := range candidates {
				if c != "A" {
					out = append(out, c)
				}
			}
			return out
		},
		OnAttemptFailure: func(gpuTypeID string, err error) {
			hookCalls = append(hookCalls, gpuTypeID)
		},
	}

	client := fallbackClient(server.URL)
	pod, err := client.CreatePodWithFallback(context.Background(), baseGPURequest("A", "B", "C"), nil, opts)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if pod.ID != "pod-C" {
		t.Fatalf("expected pod-C, got %q", pod.ID)
	}
	if got := attempted(); len(got) != 2 || got[0] != "B" || got[1] != "C" {
		t.Fatalf("filter should skip A; attempts=%v", got)
	}
	if len(hookCalls) != 1 || hookCalls[0] != "B" {
		t.Fatalf("failure hook should record B only, got %v", hookCalls)
	}
}

func TestFallback_ContextCancelledEarlyExit(t *testing.T) {
	server, attempted := newFallbackServer(t,
		map[string]int{"A": 500, "B": 0},
		`{"error":"no instances available"}`)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	client := runpod.NewClient("test_key",
		runpod.WithBaseURL(server.URL),
		runpod.WithMaxRetryAttempts(0),
		runpod.WithHTTPClient(&http.Client{
			Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				resp, err := http.DefaultTransport.RoundTrip(r)
				cancel() // cancel once the first attempt has completed
				return resp, err
			}),
		}),
	)

	_, err := client.CreatePod(ctx, baseGPURequest("A", "B"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if got := attempted(); len(got) != 1 {
		t.Fatalf("cancelled context must stop the fan-out, got attempts %v", got)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
