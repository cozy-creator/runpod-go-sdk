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

	client := runpod.NewClient("test_key",
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

	client := runpod.NewClient("test_key",
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

	client := runpod.NewClient("test_key", runpod.WithBaseURL(server.URL))

	_, err := client.GetPod(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error")
	}
	if !runpod.IsAPIError(err) {
		t.Fatalf("IsAPIError should be true for wrapped APIError, err=%v", err)
	}
}

// 429 responses must surface the Retry-After header instead of "unknown".
func TestRateLimitRetryAfterSurfaced(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := runpod.NewClient("test_key",
		runpod.WithBaseURL(server.URL),
		runpod.WithMaxRetryAttempts(0),
	)

	err := client.Get(context.Background(), "/pods", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !runpod.IsRateLimitError(err) {
		t.Fatalf("expected rate limit error, got %v", err)
	}
	var rle *runpod.RateLimitError
	if !errors.As(err, &rle) || rle.RetryAfter != "7 seconds" {
		t.Fatalf("expected RetryAfter=%q, got %+v", "7 seconds", rle)
	}
}
