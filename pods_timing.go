package runpod

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// PodReadyTiming decomposes the pod-startup timeline into the buckets that
// runpod's public API actually exposes. Use this to attribute slow cold-starts
// to (a) host placement, (b) image pull + container start, or (c) post-runtime
// worker registration (which the orchestrator must time itself).
//
// Timeline:
//
//	createdAt ─── provisionDuration ───► lastStartedAt ─── pullAndStartDuration ───► firstRuntimeAt
//
// Runpod does NOT separately expose "image pulling" vs "container starting"
// phases, so PullAndStartDuration is a combined bucket. If runpod ever adds
// pull-progress fields, the SDK can split it later.
type PodReadyTiming struct {
	PodID                string
	CreatedAt            time.Time
	LastStartedAt        time.Time
	FirstRuntimeAt       time.Time
	ProvisionDuration    time.Duration
	PullAndStartDuration time.Duration
	UptimeSecondsAtReady int
	Ports                map[string]int
	PublicIP             string
	DesiredStatus        string
}

// PodReadyState classifies what the helper observed.
type PodReadyState int

const (
	// PodReadyStateUnknown — never observed runtime.
	PodReadyStateUnknown PodReadyState = iota
	// PodReadyStateRuntimeReady — runtime block populated and ports/IP discovered.
	PodReadyStateRuntimeReady
	// PodReadyStateTerminal — pod entered a terminal state before reaching runtime.
	PodReadyStateTerminal
	// PodReadyStateTimeout — interval/timeout exceeded before runtime came up.
	PodReadyStateTimeout
)

func (s PodReadyState) String() string {
	switch s {
	case PodReadyStateRuntimeReady:
		return "runtime_ready"
	case PodReadyStateTerminal:
		return "terminal"
	case PodReadyStateTimeout:
		return "timeout"
	default:
		return "unknown"
	}
}

// WaitForPodReadyOptions controls the polling cadence and abort conditions.
//
// The defaults are tuned for monitoring an autoscaler-launched pod from
// creation through first runtime visibility — typical end-to-end is 30s-15min
// depending on whether the image is on the host or has to be pulled from
// docker.io.
type WaitForPodReadyOptions struct {
	// Interval between polls. Defaults to 5 seconds.
	Interval time.Duration
	// Timeout — overall budget across all polls. Defaults to 15 minutes.
	// Pass 0 to use the default; pass a negative value to disable the timeout
	// (rely on ctx cancellation instead).
	Timeout time.Duration
	// NoAbortOnTerminal — when true, keep polling even after the pod enters a
	// terminal state. By default the helper exits early on EXITED/TERMINATED
	// because runpod won't transition back to runtime-ready from there.
	NoAbortOnTerminal bool
}

// WaitForPodReady polls the pod every opts.Interval until either:
//   - runtime is observed populated (returns timing decomposition + StateRuntimeReady)
//   - pod enters a terminal state (returns whatever timing we have + StateTerminal)
//   - opts.Timeout or ctx is exceeded (returns last-observed timing + StateTimeout)
//
// Safe to call against a pod that's already running — it returns immediately
// with current timing on the first poll.
func (c *Client) WaitForPodReady(ctx context.Context, podID string, opts *WaitForPodReadyOptions) (*PodReadyTiming, PodReadyState, error) {
	if err := c.validateRequired("podID", podID); err != nil {
		return nil, PodReadyStateUnknown, err
	}

	interval := 5 * time.Second
	timeout := 15 * time.Minute
	abortOnTerminal := true
	if opts != nil {
		if opts.Interval > 0 {
			interval = opts.Interval
		}
		switch {
		case opts.Timeout > 0:
			timeout = opts.Timeout
		case opts.Timeout < 0:
			timeout = 0 // disabled
		}
		if opts.NoAbortOnTerminal {
			abortOnTerminal = false
		}
	}

	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}

	var lastTiming *PodReadyTiming
	for {
		pod, err := c.GetPod(ctx, podID)
		if err != nil {
			if lastTiming != nil {
				return lastTiming, PodReadyStateUnknown, err
			}
			return nil, PodReadyStateUnknown, err
		}

		timing := buildPodReadyTiming(pod)
		lastTiming = timing

		if pod.Runtime != nil {
			// Runtime first observation — populate FirstRuntimeAt as "now" if the
			// runtime has appeared since the previous poll. RunPod doesn't expose
			// the actual transition timestamp, so the orchestrator clock is the
			// best signal we have.
			if timing.FirstRuntimeAt.IsZero() {
				timing.FirstRuntimeAt = time.Now().UTC()
			}
			if !timing.LastStartedAt.IsZero() && timing.FirstRuntimeAt.After(timing.LastStartedAt) {
				timing.PullAndStartDuration = timing.FirstRuntimeAt.Sub(timing.LastStartedAt)
			}
			return timing, PodReadyStateRuntimeReady, nil
		}

		if abortOnTerminal && c.isPodInErrorState(pod.Status()) {
			return timing, PodReadyStateTerminal, fmt.Errorf("pod %s entered terminal state %q before runtime came up", podID, pod.Status())
		}

		if !deadline.IsZero() && time.Now().After(deadline) {
			return timing, PodReadyStateTimeout, fmt.Errorf("pod %s did not reach runtime-ready within %s", podID, timeout)
		}

		select {
		case <-ctx.Done():
			return timing, PodReadyStateUnknown, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// PodTimingSnapshot returns the decomposition derivable from a single GetPod
// call, with no polling. Useful for one-shot post-mortem logging.
func (c *Client) PodTimingSnapshot(ctx context.Context, podID string) (*PodReadyTiming, error) {
	if err := c.validateRequired("podID", podID); err != nil {
		return nil, err
	}
	pod, err := c.GetPod(ctx, podID)
	if err != nil {
		return nil, err
	}
	return buildPodReadyTiming(pod), nil
}

func buildPodReadyTiming(pod *Pod) *PodReadyTiming {
	t := &PodReadyTiming{
		PodID:         strings.TrimSpace(pod.ID),
		DesiredStatus: strings.TrimSpace(pod.DesiredStatus),
		Ports:         derivePortMappings(pod),
	}
	if pod.CreatedAt != nil {
		t.CreatedAt = pod.CreatedAt.Time.UTC()
	}
	if pod.LastStartedAt != nil {
		t.LastStartedAt = pod.LastStartedAt.Time.UTC()
	}
	if !t.LastStartedAt.IsZero() && !t.CreatedAt.IsZero() {
		t.ProvisionDuration = t.LastStartedAt.Sub(t.CreatedAt)
	}
	if pod.Runtime != nil {
		t.UptimeSecondsAtReady = pod.Runtime.UptimeSeconds
		t.PublicIP = strings.TrimSpace(pod.Runtime.PublicIP)
	}
	if t.PublicIP == "" {
		t.PublicIP = strings.TrimSpace(pod.PublicIP)
	}
	return t
}
