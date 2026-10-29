package runpod

import (
	"context"
	"errors"
	"strings"
	"time"
)

// PodErrorClass is a cause supported by typed evidence. REST v2 status alone
// proves a terminal state; it does not identify a host, image, or container fault.
type PodErrorClass string

const (
	PodErrorImageAuth    PodErrorClass = "image_auth"
	PodErrorImageMissing PodErrorClass = "image_missing"
	PodErrorUnknown      PodErrorClass = "unknown"
)

type PodTerminalError struct {
	PodID             string
	Class             PodErrorClass
	Reason            string
	Message           string
	ImageRef          string
	GPUTypeID         string
	DataCenterID      string
	UptimeSeconds     *int
	ContainerObserved bool
	ProbeOutcome      string
	ObservedAt        time.Time
}

const (
	RegistryProbeOK           = "ok"
	RegistryProbeUnauthorized = "unauthorized"
	RegistryProbeNotFound     = "not_found"
	RegistryProbeInvalidRef   = "invalid_ref"
	RegistryProbeUnreachable  = "unreachable"
)

type RegistryProbeFunc func(ctx context.Context, imageRef string) (outcome, detail string)
type PodTerminalErrorOptions struct{ RegistryProbe RegistryProbeFunc }

// GetPodTerminalError reports EXITED, ERROR, TERMINATED, or a primary REST 404.
// PROVISIONING and STARTING remain pending. No deleted telemetry or log prose
// is substituted for an unexposed exit code, host ID, or failure cause.
func (c *Client) GetPodTerminalError(ctx context.Context, podID string, opts *PodTerminalErrorOptions) (*PodTerminalError, bool, error) {
	pod, err := c.GetPod(ctx, podID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return &PodTerminalError{PodID: podID, Class: PodErrorUnknown, Reason: "pod_missing", Message: "pod record missing at provider", ObservedAt: time.Now().UTC()}, true, nil
		}
		return nil, false, err
	}
	if !isTerminalPodStatus(pod.Status) {
		return nil, false, nil
	}
	result := &PodTerminalError{PodID: pod.ID, Class: PodErrorUnknown, Reason: "pod_exited", Message: pod.Status, ImageRef: pod.Image, ObservedAt: time.Now().UTC()}
	if pod.GPU != nil {
		result.GPUTypeID = pod.GPU.ID
	}
	if pod.DataCenterID != nil {
		result.DataCenterID = *pod.DataCenterID
	}
	if pod.Runtime != nil && pod.Runtime.Uptime != nil {
		result.UptimeSeconds = pod.Runtime.Uptime
	}
	result.ContainerObserved = result.UptimeSeconds != nil && *result.UptimeSeconds > 0
	if opts != nil && opts.RegistryProbe != nil && result.ImageRef != "" {
		outcome, detail := opts.RegistryProbe(ctx, result.ImageRef)
		result.ProbeOutcome = outcome
		if detail != "" {
			result.Message += "; registry probe: " + strings.TrimSpace(detail)
		}
		switch outcome {
		case RegistryProbeUnauthorized:
			result.Class = PodErrorImageAuth
		case RegistryProbeNotFound, RegistryProbeInvalidRef:
			result.Class = PodErrorImageMissing
		}
	}
	return result, true, nil
}
