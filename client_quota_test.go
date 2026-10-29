package runpod

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// quotaClient answers every request with status, headers and body, on a fake clock.
func quotaClient(t *testing.T, hits *atomic.Int32, status int, header http.Header, body string, opts ...ClientOption) (*Client, *atomic.Int64) {
	t.Helper()
	var now atomic.Int64
	now.Store(time.Date(2026, 10, 20, 3, 30, 0, 0, time.UTC).UnixNano())
	transport := cooldownTransport(func(r *http.Request) (*http.Response, error) {
		hits.Add(1)
		return &http.Response{StatusCode: status, Header: header.Clone(), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	client, err := NewClient("fixture", append([]ClientOption{WithHTTPClient(&http.Client{Transport: transport}), WithRetryDelay(time.Millisecond)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	client.cooldown.clock = func() time.Time { return time.Unix(0, now.Load()) }
	return client, &now
}

// Every REST reads and mutation call is refused, without HTTP, for what remains of the
// longest exhausted window.
func assertQuotaRefuses(t *testing.T, client *Client, hits *atomic.Int32, remaining time.Duration) {
	t.Helper()
	for _, call := range []func() error{
		func() error { return client.Get(t.Context(), "/billing/pods", nil) },
		func() error { return client.Get(t.Context(), "/catalog/gpus", nil) },
		func() error { return client.Post(t.Context(), "/pods", nil, nil) },
		func() error { return client.Delete(t.Context(), "/pods/id") },
	} {
		var limited *APIError
		if err := call(); !errors.Is(err, ErrRateLimited) || !errors.As(err, &limited) || limited.RetryAfter != remaining {
			t.Errorf("want a typed refusal for %s, got %v", remaining, err)
		}
	}
	if got := client.Cooldown(); got != remaining {
		t.Errorf("Cooldown() = %s, want %s", got, remaining)
	}
	if hits.Load() != 1 {
		t.Fatalf("an exhausted quota window sent %d HTTP requests, want 1", hits.Load())
	}
}

func TestQuotaWindowOutlastsRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		name, retryAfter string
		windows          []string
	}{
		{"short_retry_after", "2", []string{`"minute";r=0;t=44, "hour";r=0;t=6000, "day";r=12;t=20000`}},
		{"no_retry_after", "", []string{`"hour";r=0;t=6000`}},
		{"retry_after_longest", "6000", []string{`"hour";r=0;t=1724`}},
		{"separate_header_values", "2", []string{`"minute";r=0;t=44`, `"hour";r=0;t=6000`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			header := http.Header{}
			if tc.retryAfter != "" {
				header.Set("Retry-After", tc.retryAfter)
			}
			for _, window := range tc.windows {
				header.Add("RateLimit", window)
			}
			var hits atomic.Int32
			client, now := quotaClient(t, &hits, http.StatusTooManyRequests, header, `{"message":"rate limit exceeded","code":"HOUR"}`, WithMaxRetryAttempts(0))
			var limited *APIError
			if err := client.Get(t.Context(), "/pods", nil); !errors.As(err, &limited) || limited.RetryAfter != 6000*time.Second || limited.Code != "HOUR" {
				t.Fatalf("first 429 must carry the longest window: %v", err)
			}
			now.Add(int64(45 * time.Second))
			assertQuotaRefuses(t, client, &hits, 5955*time.Second)
		})
	}
}

// A response that spends a window's last request is returned unchanged, then later calls wait.
func TestExhaustedQuotaOnOtherResponses(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			header := http.Header{"Ratelimit": {`"minute";r=10;t=44, "hour";r=0;t=6000`}}
			var hits atomic.Int32
			client, now := quotaClient(t, &hits, status, header, `{"message":"served","code":"ORIGINAL"}`, WithMaxRetryAttempts(2))
			var data struct{ Message string }
			err := client.Get(t.Context(), "/pods", &data)
			var original *APIError
			switch {
			case status == http.StatusOK && (err != nil || data.Message != "served"):
				t.Fatalf("successful answer lost: %+v %v", data, err)
			case status != http.StatusOK && (!errors.As(err, &original) || original.StatusCode != status || original.Code != "ORIGINAL"):
				t.Fatalf("original error replaced: %v", err)
			}
			now.Add(int64(45 * time.Second))
			assertQuotaRefuses(t, client, &hits, 5955*time.Second)
			now.Add(int64(5955 * time.Second))
			_ = client.Get(t.Context(), "/pods", nil)
			if hits.Load() != 2 {
				t.Fatalf("quota did not reopen when its window reset: %d HTTP requests", hits.Load())
			}
		})
	}
}

func TestRemainingQuotaDoesNotBlock(t *testing.T) {
	header := http.Header{"Ratelimit": {`"hour";r=1;t=6000`}, "Retry-After": {"6000"}}
	var hits atomic.Int32
	client, _ := quotaClient(t, &hits, http.StatusOK, header, `{}`)
	for range 2 {
		if err := client.Get(t.Context(), "/pods", nil); err != nil {
			t.Fatal(err)
		}
	}
	if hits.Load() != 2 || client.Cooldown() != 0 {
		t.Fatalf("remaining quota suppressed HTTP: hits=%d cooldown=%s", hits.Load(), client.Cooldown())
	}
}

func TestQuotaDelayParsing(t *testing.T) {
	for value, want := range map[string]time.Duration{
		`"hour";r=1;t=6000`:                    0,
		`"hour";t=6000`:                        0,
		`"hour";r=0`:                           0,
		`"hour";r=0;t=-1`:                      0,
		`"hour";r=0;t=soon`:                    0,
		`bad, "hour";policy=x;t=6000;r=0`:      6000 * time.Second,
		` "hour" ; r=0 ; t=6000 `:              6000 * time.Second,
		`"hour";r=0;t=18446744073709551616`:    (1<<32 - 1) * time.Second,
		`"minute";r=0;t=44, "hour";r=0;t=6000`: 6000 * time.Second,
	} {
		if got := quotaDelay(http.Header{"Ratelimit": {value}}); got != want {
			t.Errorf("quotaDelay(%s) = %s, want %s", value, got, want)
		}
	}
}
