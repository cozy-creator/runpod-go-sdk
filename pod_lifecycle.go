package runpod

import (
	"context"
	"strings"
)

// PodLifecycleObservation reports only the lifecycle facts REST v2 exposes.
// StartedAt and runtime uptime are diagnostic timestamps, not an incarnation ID.
type PodLifecycleObservation struct {
	PodID                  string
	Status                 string
	StartedAt              *JSONTime
	RuntimeUptimeInSeconds *int
}

func (c *Client) GetPodLifecycleObservation(ctx context.Context, podID string) (*PodLifecycleObservation, error) {
	pod, err := c.GetPod(ctx, podID)
	if err != nil {
		return nil, err
	}
	out := &PodLifecycleObservation{PodID: pod.ID, Status: pod.Status, StartedAt: pod.StartedAt}
	if pod.Runtime != nil {
		out.RuntimeUptimeInSeconds = pod.Runtime.Uptime
	}
	return out, nil
}

func isTerminalPodStatus(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "EXITED", "ERROR", "TERMINATED":
		return true
	default:
		return false
	}
}
