package runpod

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type cooldownTransport func(*http.Request) (*http.Response, error)

func (f cooldownTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type cooldownBlockedBody struct {
	reader  io.Reader
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *cooldownBlockedBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered); <-b.release })
	return b.reader.Read(p)
}
func (*cooldownBlockedBody) Close() error { return nil }

func TestCooldownBeginsAtHeadersBeforeBlockedResponseBody(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC).UnixNano())
	var hits atomic.Int32
	body := &cooldownBlockedBody{reader: strings.NewReader(`{"message":"original provider limit","code":"FIRST"}`), entered: make(chan struct{}), release: make(chan struct{})}
	client, _ := NewClient("fixture", WithMaxRetryAttempts(0), WithHTTPClient(&http.Client{Transport: cooldownTransport(func(r *http.Request) (*http.Response, error) {
		var responseBody io.ReadCloser = http.NoBody
		if hits.Add(1) == 1 {
			responseBody = body
		}
		return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {"6000"}}, Body: responseBody, Request: r}, nil
	})}))
	client.cooldown.clock = func() time.Time { return time.Unix(0, now.Load()) }
	finished := make(chan error, 1)
	go func() { finished <- client.Get(t.Context(), "/pods", nil) }()
	<-body.entered
	now.Add(int64(100 * time.Second))
	second := client.Get(t.Context(), "/catalog/gpus", nil)
	close(body.release)
	first := <-finished
	var original *APIError
	if !errors.As(first, &original) || original.Code != "FIRST" {
		t.Fatalf("lost original response: %v", first)
	}
	var limited *APIError
	if !errors.As(second, &limited) || limited.RetryAfter != 5900*time.Second || hits.Load() != 1 {
		t.Fatalf("blocked body delayed suppression: hits=%d err=%+v", hits.Load(), limited)
	}
	if current := client.rateLimited(); current == nil || current.RetryAfter != 5900*time.Second || current.Code != "FIRST" {
		t.Fatalf("body read extended deadline/lost diagnostics: %+v", current)
	}
}

func TestCooldownClockExpiryAndConcurrentCallers(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC).UnixNano())
	var hits atomic.Int32
	client, _ := NewClient("fixture", WithMaxRetryAttempts(0), WithHTTPClient(&http.Client{Transport: cooldownTransport(func(r *http.Request) (*http.Response, error) {
		status, body, header := http.StatusOK, `{}`, make(http.Header)
		if hits.Add(1) == 1 {
			status, body = http.StatusTooManyRequests, `{"message":"slow down","code":"FIRST"}`
			header.Set("Retry-After", time.Unix(0, now.Load()).Add(6000*time.Second).UTC().Format(http.TimeFormat))
		}
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}))
	client.cooldown.clock = func() time.Time { return time.Unix(0, now.Load()) }
	if err := client.Get(t.Context(), "/pods", nil); !errors.Is(err, ErrRateLimited) {
		t.Fatal(err)
	}
	now.Add(int64(1800 * time.Second))
	var group sync.WaitGroup
	for range 32 {
		group.Add(1)
		go func() {
			defer group.Done()
			err := client.Get(t.Context(), "/catalog/gpus", nil)
			var limited *APIError
			if !errors.As(err, &limited) || limited.RetryAfter != 4200*time.Second || limited.Code != "FIRST" {
				t.Errorf("retained cooldown: %#v, %v", limited, err)
			}
		}()
	}
	group.Wait()
	if hits.Load() != 1 {
		t.Fatalf("cooldown reached HTTP %d times", hits.Load())
	}
	now.Add(int64(4201 * time.Second))
	if err := client.Get(t.Context(), "/pods", nil); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("expired cooldown did not recover: hits=%d", hits.Load())
	}
}

func TestCooldownKeepsLongestDeadlineAndOwnsErrorBytes(t *testing.T) {
	client, _ := NewClient("fixture")
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	client.cooldown.clock = func() time.Time { return now }
	first := &APIError{StatusCode: 429, Code: "LONG", ResponseBody: []byte(`{"message":"long"}`)}
	client.retainRateLimit(first, 6000*time.Second)
	first.ResponseBody[0] = 'x'
	now = now.Add(time.Second)
	client.retainRateLimit(&APIError{StatusCode: 429, Code: "SHORT"}, time.Second)
	limited := client.rateLimited()
	if limited.Code != "LONG" || limited.RetryAfter != 5999*time.Second || limited.ResponseBody[0] != '{' {
		t.Fatalf("short limit changed retained evidence: %+v", limited)
	}
	limited.ResponseBody[0] = 'y'
	if client.rateLimited().ResponseBody[0] != '{' {
		t.Fatal("caller mutated retained response")
	}
}

func TestCooldownCancellationPreservesTypedLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	client, _ := NewClient("fixture", WithHTTPClient(&http.Client{Transport: cooldownTransport(func(r *http.Request) (*http.Response, error) {
		cancel()
		return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {"1"}}, Body: http.NoBody, Request: r}, nil
	})}))
	err := client.Get(ctx, "/pods", nil)
	var limited *APIError
	if !errors.As(err, &limited) || limited.RetryAfter <= 0 {
		t.Fatalf("cancellation lost429: %v", err)
	}
	if !errors.Is(client.Post(t.Context(), "/pods", nil, nil), ErrRateLimited) {
		t.Fatal("POST escaped retained cooldown")
	}
}

func TestParseRetryAfterClock(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		header string
		want   time.Duration
	}{
		{"6000", 6000 * time.Second}, {now.Add(6000 * time.Second).Format(http.TimeFormat), 6000 * time.Second},
		{"", 0}, {"bad", 0}, {"-1", 0}, {now.Add(-time.Second).Format(http.TimeFormat), 0},
	} {
		if got := parseRetryAfterAt(tc.header, now); got != tc.want {
			t.Errorf("%q=%v, want %v", tc.header, got, tc.want)
		}
	}
}
