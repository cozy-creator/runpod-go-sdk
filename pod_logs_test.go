package runpod_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func TestPodLogsSSEReplayFieldsAndClose(t *testing.T) {
	done := make(chan struct{})
	cursor := "2026-09-25T20:44:04Z/000000000001"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		if r.Method != "GET" || r.URL.Path != "/v2/pods/pod-1/logs" {
			t.Errorf("request = %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer test_key" || r.Header.Get("Accept") != "text/event-stream" || r.Header.Get("Last-Event-ID") != cursor {
			t.Errorf("wrong stream headers")
		}
		q := r.URL.Query()
		if q.Get("source") != "system" || q.Get("tail") != "5000" || q.Get("since") != "2026-09-25T20:38:40Z" {
			t.Errorf("query = %v", q)
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		fmt.Fprintf(w, ": heartbeat\r\nid: %s\r\nevent: log\r\ndata: {\"source\":\"system\",\r\ndata: \"line\":\"create 20GB network volume\",\"ts\":\"2026-09-25T20:44:04Z\"}\r\n\r\n", cursor)
		fmt.Fprint(w, "data: {\"source\":\"container\",\"line\":\"\",\"ts\":\"2026-09-25T20:44:05.123456789Z\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	tail := 5000
	client := mustClient(t, "test_key", runpod.WithBaseURL("https://v1.invalid"), runpod.WithRESTV2BaseURL(server.URL+"/v2"), runpod.WithTimeout(0))
	stream, err := client.StreamPodLogs(context.Background(), "pod-1", &runpod.PodLogsOptions{Source: runpod.PodLogSourceSystem, Tail: &tail, Since: time.Date(2026, 9, 25, 20, 38, 40, 0, time.UTC), LastEventID: cursor})
	if err != nil {
		t.Fatal(err)
	}
	first, err := stream.Next()
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != cursor || first.Line != "create 20GB network volume" || first.Source != runpod.PodLogSourceSystem || first.Timestamp.Format(time.RFC3339) != "2026-09-25T20:44:04Z" {
		t.Fatalf("entry = %#v", first)
	}
	second, err := stream.Next()
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != cursor || second.Line != "" || second.Timestamp.Nanosecond() != 123456789 {
		t.Fatalf("second = %#v", second)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	<-done
}

func TestPodLogsCancellationBeforeHeadersAndDuringRead(t *testing.T) {
	for _, headers := range []bool{false, true} {
		t.Run(fmt.Sprint(headers), func(t *testing.T) {
			entered := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if headers {
					w.Header().Set("Content-Type", "text/event-stream")
					w.(http.Flusher).Flush()
				}
				close(entered)
				<-r.Context().Done()
			}))
			defer server.Close()
			client := mustClient(t, "test_key", runpod.WithRESTV2BaseURL(server.URL), runpod.WithTimeout(0))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			answer := make(chan error, 1)
			go func() {
				stream, err := client.StreamPodLogs(ctx, "pod", nil)
				if err == nil {
					defer stream.Close()
					_, err = stream.Next()
				}
				answer <- err
			}()
			<-entered
			cancel()
			if err := <-answer; !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation = %v", err)
			}
		})
	}
}

func TestPodLogsHTTPRetryAndProblemError(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Last-Event-ID") != "cursor/1" {
			t.Error("lost replay header")
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(429)
			fmt.Fprint(w, `{"title":"Too Many Requests","status":429,"detail":"slow down"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "id: cursor/2\ndata: {\"source\":\"system\",\"line\":\"start container\",\"ts\":\"2026-09-25T20:44:04Z\"}\n\n")
	}))
	defer server.Close()
	client := mustClient(t, "test_key", runpod.WithRESTV2BaseURL(server.URL), runpod.WithRetryDelay(time.Nanosecond))
	stream, err := client.StreamPodLogs(context.Background(), "pod", &runpod.PodLogsOptions{LastEventID: "cursor/1"})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err = stream.Next(); err != nil {
		t.Fatal(err)
	}
	if _, err = stream.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("EOF = %v", err)
	}
	if calls.Load() != 2 {
		t.Fatal("stream unexpectedly reconnected")
	}
	for _, status := range []int{401, 403, 404, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "3")
				w.WriteHeader(status)
				fmt.Fprint(w, `{"title":"Forbidden","status":403,"detail":"provider diagnostic"}`)
			}))
			defer s.Close()
			c := mustClient(t, "test_key", runpod.WithRESTV2BaseURL(s.URL), runpod.WithMaxRetryAttempts(0))
			_, err := c.StreamPodLogs(context.Background(), "pod", nil)
			var api *runpod.APIError
			if !errors.As(err, &api) || api.StatusCode != status || api.Message != "provider diagnostic" || len(api.ResponseBody) == 0 {
				t.Fatalf("error = %#v", err)
			}
			if status == 429 && api.RetryAfter != 3*time.Second {
				t.Fatal("lost Retry-After")
			}
		})
	}
}

func TestPodLogsFramingRefusalsAndLargeLine(t *testing.T) {
	cases := []struct {
		name, contentType, body string
		wantError               bool
	}{
		{"truncated", "text/event-stream", "data: {}", true},
		{"malformed", "text/event-stream", "data: invalid\n\n", true},
		{"missing", "text/event-stream", "data: {}\n\n", true},
		{"wrong-type", "application/json", "{}", true},
		{"large", "text/event-stream", "data: {\"source\":\"system\",\"ts\":\"2026-09-25T20:44:04Z\",\"line\":\"" + strings.Repeat("x", 128<<10) + "\"}\n\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			c := mustClient(t, "test_key", runpod.WithRESTV2BaseURL(server.URL))
			s, err := c.StreamPodLogs(context.Background(), "pod", nil)
			if err == nil {
				defer s.Close()
				var entry *runpod.PodLogEntry
				entry, err = s.Next()
				if !tc.wantError && (entry == nil || len(entry.Line) != 128<<10) {
					t.Fatal("large line lost")
				}
			}
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestPodLogsOptionsValidateWithoutRequest(t *testing.T) {
	negative, large := -1, 5001
	c := mustClient(t, "test_key", runpod.WithRESTV2BaseURL("http://unused.invalid"))
	for _, options := range []*runpod.PodLogsOptions{{Source: "other"}, {Tail: &negative}, {Tail: &large}, {LastEventID: "x\r\nInjected: y"}} {
		_, err := c.StreamPodLogs(context.Background(), "pod", options)
		var validation *runpod.ValidationError
		if !errors.As(err, &validation) {
			t.Fatalf("validation = %v", err)
		}
	}
}
