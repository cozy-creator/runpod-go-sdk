package runpod_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func newCountingServer(status int, succeedAfter int32, body string) (*httptest.Server, *int32) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if succeedAfter > 0 && n > succeedAfter {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"id":"pod1","name":"n"}`)
			return
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	return server, &hits
}

// POST is not idempotent: a 500 must not be retried (duplicate pod/job risk,
// and RunPod uses 500 for ordinary "no instances available" stock-outs).
func TestPost500NotRetried(t *testing.T) {
	server, hits := newCountingServer(http.StatusInternalServerError, 0, `{"error":"no instances available"}`)
	defer server.Close()

	client := mustClient(t, "test_key",
		runpod.WithBaseURL(server.URL),
		runpod.WithMaxRetryAttempts(3),
		runpod.WithRetryDelay(time.Millisecond),
	)

	err := client.Post(context.Background(), "/pods", map[string]string{"x": "y"}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Fatalf("POST on 500 should hit the server exactly once, got %d", got)
	}
}

// GET is idempotent: transient 5xx should be retried until success.
func TestGet500Retried(t *testing.T) {
	server, hits := newCountingServer(http.StatusInternalServerError, 2, `{"error":"transient"}`)
	defer server.Close()

	client := mustClient(t, "test_key",
		runpod.WithBaseURL(server.URL),
		runpod.WithMaxRetryAttempts(3),
		runpod.WithRetryDelay(time.Millisecond),
	)

	pod, err := client.GetPod(context.Background(), "pod1")
	if err != nil {
		t.Fatalf("expected retries to succeed, got %v", err)
	}
	if pod.ID != "pod1" {
		t.Fatalf("unexpected pod %+v", pod)
	}
	if got := atomic.LoadInt32(hits); got != 3 {
		t.Fatalf("expected 3 hits (2 failures + 1 success), got %d", got)
	}
}

// The Is* helpers must see through fmt.Errorf("%w") wrapping, which every
// SDK method applies before returning.
func TestErrorHelpersSeeThroughWrapping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"pod not found"}`)
	}))
	defer server.Close()

	client := mustClient(t, "test_key", runpod.WithBaseURL(server.URL))

	_, err := client.GetPod(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *runpod.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("errors.As should find wrapped APIError, err=%v", err)
	}
	if !errors.Is(err, runpod.ErrNotFound) {
		t.Fatalf("404 should match ErrNotFound, err=%v", err)
	}
}

// 429 responses must surface the Retry-After header instead of "unknown".
func TestRateLimitRetryAfterSurfaced(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := mustClient(t, "test_key",
		runpod.WithBaseURL(server.URL),
		runpod.WithMaxRetryAttempts(0),
	)

	err := client.Get(context.Background(), "/pods", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, runpod.ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited, got %v", err)
	}
	var apiErr *runpod.APIError
	if !errors.As(err, &apiErr) || apiErr.RetryAfter != 7*time.Second {
		t.Fatalf("expected RetryAfter=7s, got %+v", apiErr)
	}
}

// NewClient must reject an empty API key instead of panicking.
func TestNewClientEmptyKey(t *testing.T) {
	if _, err := runpod.NewClient(""); err == nil {
		t.Fatal("expected error for empty API key")
	}
	if _, err := runpod.NewClient("   "); err == nil {
		t.Fatal("expected error for blank API key")
	}
}

// A Retry-After header on 429 must be honored as the retry wait.
func TestRetryAfterHonoredOn429(t *testing.T) {
	var hits int32
	var firstRetryDelay time.Duration
	var lastHit time.Time
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		now := time.Now()
		if n == 2 {
			firstRetryDelay = now.Sub(lastHit)
		}
		lastHit = now
		if n <= 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"id":"pod1","name":"n"}`)
	}))
	defer server.Close()

	client := mustClient(t, "test_key",
		runpod.WithBaseURL(server.URL),
		runpod.WithMaxRetryAttempts(2),
		runpod.WithRetryDelay(time.Millisecond), // backoff would be ~1ms without Retry-After
	)

	if _, err := client.GetPod(context.Background(), "pod1"); err != nil {
		t.Fatalf("expected retry to succeed, got %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("expected 2 hits, got %d", got)
	}
	if firstRetryDelay < 900*time.Millisecond {
		t.Fatalf("Retry-After=1s not honored; retried after %v", firstRetryDelay)
	}
}

// Backoff must grow between attempts (exponential, jittered).
func TestExponentialBackoffGrows(t *testing.T) {
	var hits int32
	var stamps []time.Time
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		stamps = append(stamps, time.Now())
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":"unavailable"}`)
	}))
	defer server.Close()

	client := mustClient(t, "test_key",
		runpod.WithBaseURL(server.URL),
		runpod.WithMaxRetryAttempts(3),
		runpod.WithRetryDelay(80*time.Millisecond),
	)

	if err := client.Get(context.Background(), "/pods", nil); err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if len(stamps) != 4 {
		t.Fatalf("expected 4 attempts, got %d", len(stamps))
	}
	// delays: attempt1 in [40,80]ms, attempt2 in [80,160]ms, attempt3 in [160,320]ms
	d1 := stamps[1].Sub(stamps[0])
	d3 := stamps[3].Sub(stamps[2])
	if d3 <= d1 {
		t.Fatalf("backoff did not grow: d1=%v d3=%v", d1, d3)
	}
	if d1 < 40*time.Millisecond || d3 > 500*time.Millisecond {
		t.Fatalf("backoff outside expected window: d1=%v d3=%v", d1, d3)
	}
}
