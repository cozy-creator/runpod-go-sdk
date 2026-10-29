package runpod_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func TestClientRetainsRateLimitAcrossRESTResources(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "6000")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"provider cooldown","code":"RATE_LIMIT"}`))
	}))
	defer server.Close()
	client := mustClient(t, "fixture", runpod.WithBaseURL(server.URL), runpod.WithMaxRetryAttempts(0))
	if err := client.Get(t.Context(), "/pods", nil); !errors.Is(err, runpod.ErrRateLimited) {
		t.Fatalf("initial limit: %v", err)
	}
	_, err := client.ListDataCenters(t.Context(), nil)
	var limited *runpod.APIError
	if !errors.As(err, &limited) || limited.RetryAfter <= 5999*time.Second || limited.Code != "RATE_LIMIT" {
		t.Fatalf("retained provider error: %#v (%v)", limited, err)
	}
	if hits.Load() != 1 {
		t.Fatalf("cooldown sent %d HTTP requests; want only the initial request", hits.Load())
	}
}

func TestLongRateLimitReturnsProviderErrorBeforeCallerDeadline(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "6000")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	client := mustClient(t, "fixture", runpod.WithBaseURL(server.URL))
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err := client.ListDataCenters(ctx, nil)
	var limited *runpod.APIError
	if !errors.As(err, &limited) || limited.RetryAfter < 5999*time.Second {
		t.Fatalf("lost original429 to caller deadline: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("waited for caller deadline instead of returning provider limit: %v", ctx.Err())
	}
	_, err = client.ListGPUOffers(t.Context(), nil)
	if !errors.Is(err, runpod.ErrRateLimited) || hits.Load() != 1 {
		t.Fatalf("second pass reached HTTP: hits=%d err=%v", hits.Load(), err)
	}
}
