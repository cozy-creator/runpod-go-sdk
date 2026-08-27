package runpod

import (
	"context"
	"fmt"
	"strings"
)

// PodLifecycleTelemetry is RunPod's latest observed container state. RunPod
// does not expose a container incarnation ID: Time and LastStartedAt are
// independent timestamps and must not be treated as a generation fence.
type PodLifecycleTelemetry struct {
	State                        string    `json:"state"`
	Time                         *JSONTime `json:"time,omitempty"`
	CPUUtilization               *float64  `json:"cpuUtilization,omitempty"`
	MemoryUtilization            *float64  `json:"memoryUtilization,omitempty"`
	LastStateTransitionTimestamp *int64    `json:"lastStateTransitionTimestamp,omitempty"`
}

// PodLifecycleObservation is the read-only lifecycle state exposed by
// RunPod's GraphQL pod query. DesiredStatus is the requested provider state;
// LatestTelemetry is the independently observed container state.
type PodLifecycleObservation struct {
	PodID                  string
	DesiredStatus          string
	LastStartedAt          *JSONTime
	LatestTelemetry        *PodLifecycleTelemetry
	RuntimeUptimeInSeconds *int
}

// GetPodLifecycleObservation returns RunPod's desired state, last-start
// timestamp, and latest telemetry sample for a pod. RuntimeUptimeInSeconds is
// diagnostic only; no timestamp or uptime value identifies an incarnation.
func (c *Client) GetPodLifecycleObservation(ctx context.Context, podID string) (*PodLifecycleObservation, error) {
	if err := c.validateRequired("podID", podID); err != nil {
		return nil, err
	}

	const query = `query PodLifecycle($input: PodFilter) {
  pod(input: $input) {
    id
    desiredStatus
    lastStartedAt
    runtime { uptimeInSeconds }
    latestTelemetry { state time }
  }
}`
	var payload struct {
		Pod *struct {
			ID            string    `json:"id"`
			DesiredStatus string    `json:"desiredStatus"`
			LastStartedAt *JSONTime `json:"lastStartedAt"`
			Runtime       *struct {
				UptimeInSeconds *int `json:"uptimeInSeconds"`
			} `json:"runtime"`
			LatestTelemetry *PodLifecycleTelemetry `json:"latestTelemetry"`
		} `json:"pod"`
	}
	if err := c.GraphQL(ctx, query, map[string]interface{}{
		"input": map[string]interface{}{"podId": podID},
	}, &payload); err != nil {
		return nil, fmt.Errorf("failed to get pod %s lifecycle: %w", podID, err)
	}
	if payload.Pod == nil {
		return nil, NewAPIErrorWithDetails(404, "pod not found", podID)
	}

	observation := &PodLifecycleObservation{
		PodID:           payload.Pod.ID,
		DesiredStatus:   payload.Pod.DesiredStatus,
		LastStartedAt:   payload.Pod.LastStartedAt,
		LatestTelemetry: payload.Pod.LatestTelemetry,
	}
	if payload.Pod.Runtime != nil {
		observation.RuntimeUptimeInSeconds = payload.Pod.Runtime.UptimeInSeconds
	}
	return observation, nil
}

func isTerminalPodStatus(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "EXITED", "TERMINATED", "DEAD":
		return true
	default:
		return false
	}
}
