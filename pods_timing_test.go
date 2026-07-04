package runpod

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubPodScript scripts a sequence of REST /pods/:id responses for tests.
// Each call to GetPod consumes one entry; the last entry repeats forever.
type stubPodScript struct {
	mu      sync.Mutex
	bodies  []string
	cursor  int
	gqlBody string
}

func (s *stubPodScript) next() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cursor >= len(s.bodies) {
		return s.bodies[len(s.bodies)-1]
	}
	body := s.bodies[s.cursor]
	s.cursor++
	return body
}

func newStubServer(t *testing.T, script *stubPodScript) (*httptest.Server, *Client) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/pods/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(script.next()))
	})
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := script.gqlBody
		if body == "" {
			body = `{"data":{}}`
		}
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)

	c, _ := NewClient("test-key",
		WithBaseURL(srv.URL),
		WithGraphQLBaseURL(srv.URL+"/graphql"),
		WithMaxRetryAttempts(0),
		WithRetryDelay(time.Millisecond),
	)
	return srv, c
}

func podJSON(t *testing.T, id, status, createdAt, lastStartedAt string, withRuntime bool) string {
	t.Helper()
	body := map[string]any{
		"id":            id,
		"desiredStatus": status,
	}
	if createdAt != "" {
		body["createdAt"] = createdAt
	}
	if lastStartedAt != "" {
		body["lastStartedAt"] = lastStartedAt
	}
	if withRuntime {
		body["runtime"] = map[string]any{
			"uptimeSeconds": 3,
			"ports": map[string]any{
				"22/tcp": map[string]any{"publicPort": 12345, "privatePort": 22},
			},
		}
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("podJSON marshal: %v", err)
	}
	return string(b)
}

func TestWaitForPodReady_RuntimeAlreadyUp(t *testing.T) {
	created := time.Now().Add(-90 * time.Second).UTC().Format(time.RFC3339)
	started := time.Now().Add(-60 * time.Second).UTC().Format(time.RFC3339)
	script := &stubPodScript{bodies: []string{
		podJSON(t, "pod-1", "RUNNING", created, started, true),
	}}
	srv, c := newStubServer(t, script)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	timing, state, err := c.WaitForPodReady(ctx, "pod-1", &WaitForPodReadyOptions{
		Interval: 50 * time.Millisecond,
		Timeout:  1 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != PodReadyStateRuntimeReady {
		t.Errorf("got state=%v, want runtime_ready", state)
	}
	if timing == nil {
		t.Fatal("timing is nil")
	}
	// provisionDuration should be ~30s based on our scripted timestamps.
	if timing.ProvisionDuration < 25*time.Second || timing.ProvisionDuration > 35*time.Second {
		t.Errorf("ProvisionDuration=%s, want ~30s", timing.ProvisionDuration)
	}
	// pullAndStartDuration is anchored to wall-clock from when we observed
	// runtime; with the runtime already up on first poll and lastStartedAt
	// set 60s ago, the duration should be ~60s.
	if timing.PullAndStartDuration < 50*time.Second || timing.PullAndStartDuration > 70*time.Second {
		t.Errorf("PullAndStartDuration=%s, want ~60s", timing.PullAndStartDuration)
	}
}

func TestWaitForPodReady_PollsUntilRuntimeAppears(t *testing.T) {
	created := time.Now().Add(-30 * time.Second).UTC().Format(time.RFC3339)
	started := time.Now().Add(-10 * time.Second).UTC().Format(time.RFC3339)
	// Three polls: no-runtime, no-runtime, then runtime up.
	script := &stubPodScript{bodies: []string{
		podJSON(t, "pod-2", "RUNNING", created, started, false),
		podJSON(t, "pod-2", "RUNNING", created, started, false),
		podJSON(t, "pod-2", "RUNNING", created, started, true),
	}}
	srv, c := newStubServer(t, script)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	timing, state, err := c.WaitForPodReady(ctx, "pod-2", &WaitForPodReadyOptions{
		Interval: 50 * time.Millisecond,
		Timeout:  1 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != PodReadyStateRuntimeReady {
		t.Errorf("got state=%v, want runtime_ready", state)
	}
	if timing.PodID != "pod-2" {
		t.Errorf("got pod=%s, want pod-2", timing.PodID)
	}
	if script.cursor != 3 {
		t.Errorf("script cursor=%d, expected 3 polls", script.cursor)
	}
}

func TestWaitForPodReady_TimeoutWithoutRuntime(t *testing.T) {
	created := time.Now().Add(-30 * time.Second).UTC().Format(time.RFC3339)
	started := time.Now().Add(-10 * time.Second).UTC().Format(time.RFC3339)
	script := &stubPodScript{bodies: []string{
		podJSON(t, "pod-3", "RUNNING", created, started, false),
	}}
	srv, c := newStubServer(t, script)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	timing, state, err := c.WaitForPodReady(ctx, "pod-3", &WaitForPodReadyOptions{
		Interval: 50 * time.Millisecond,
		Timeout:  300 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if state != PodReadyStateTimeout {
		t.Errorf("got state=%v, want timeout", state)
	}
	if timing == nil {
		t.Fatal("timing should be populated even on timeout")
	}
}

func TestWaitForPodReady_TerminalAbort(t *testing.T) {
	created := time.Now().Add(-30 * time.Second).UTC().Format(time.RFC3339)
	started := time.Now().Add(-10 * time.Second).UTC().Format(time.RFC3339)
	script := &stubPodScript{bodies: []string{
		podJSON(t, "pod-4", "EXITED", created, started, false),
	}}
	srv, c := newStubServer(t, script)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, state, err := c.WaitForPodReady(ctx, "pod-4", &WaitForPodReadyOptions{
		Interval: 50 * time.Millisecond,
		Timeout:  1 * time.Second,
	})
	if err == nil {
		t.Fatal("expected terminal-state error")
	}
	if state != PodReadyStateTerminal {
		t.Errorf("got state=%v, want terminal", state)
	}
}

func TestPodTimingSnapshot_OneShot(t *testing.T) {
	created := time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339)
	started := time.Now().Add(-90 * time.Second).UTC().Format(time.RFC3339)
	script := &stubPodScript{bodies: []string{
		podJSON(t, "pod-5", "RUNNING", created, started, true),
	}}
	srv, c := newStubServer(t, script)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	timing, err := c.PodTimingSnapshot(ctx, "pod-5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if timing.PodID != "pod-5" {
		t.Errorf("got pod=%s, want pod-5", timing.PodID)
	}
	if timing.ProvisionDuration < 25*time.Second {
		t.Errorf("ProvisionDuration=%s, want ~30s", timing.ProvisionDuration)
	}
}

// Sanity check that the stub URL routing matches what the client actually
// does — guards against pod path drift in the SDK.
func TestStubServerHonorsPodPath(t *testing.T) {
	hit := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"x","desiredStatus":"RUNNING"}`)
	}))
	defer srv.Close()
	c, _ := NewClient("k", WithBaseURL(srv.URL), WithMaxRetryAttempts(0))
	_, err := c.GetPod(context.Background(), "abc-123")
	if err != nil {
		t.Fatalf("GetPod error: %v", err)
	}
	if !strings.HasSuffix(hit, "/pods/abc-123") {
		t.Errorf("hit=%q, expected suffix /pods/abc-123", hit)
	}
}
