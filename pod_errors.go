package runpod

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Pod terminal-error surface (tensorhub th#648, reshaped by rp#16 / th#874).
//
// RunPod's pod create returns success synchronously; machine-side failures
// (image pull rejection, host faults, spot reclaims) surface MINUTES later as
// an async EXITED pod state. What the API key can see:
//
//   - REST GET /pods/{id}: desiredStatus (RUNNING|EXITED|TERMINATED) plus
//     lastStatusChange — free text that for failures reads just
//     "Exited by Runpod: <ts>" with NO error class. With includeMachine the
//     record carries the host machine id / GPU type / datacenter, and the
//     runtime block (when still present) the container exit code.
//   - GraphQL pod{...}: desiredStatus plus current lastStartedAt and
//     latestTelemetry{state,time}; telemetry can expose a current-generation
//     container exit while REST desiredStatus is still RUNNING.
//   - No pod-logs endpoint exists on the public API surface
//     (GetProviderFeatureSupport documents this), so a last-log tail cannot
//     be fetched.
//
// The SDK reports typed FACTS (exit code, machine, SKU, container-observed,
// probe outcome) and only claims a class where the evidence is itself typed:
// the registry probe (image_auth / image_missing) and the interruptible flag
// (interrupted). Everything else is `unknown` — semantic death classification
// (boot crash vs transient vs SKU-bound) is the consumer's job, aggregated
// across pods (tensorhub th#874 layer 2). The former prose-marker matching
// (ClassifyPodErrorMessage) and its probe-OK → host_fault default are gone.

// PodErrorClass is the typed-evidence classification of a terminal pod error.
type PodErrorClass string

const (
	// PodErrorImageAuth — image pull rejected as unauthorized (registry
	// answered 401/403 to the same pull credentials pods receive). Docker Hub
	// also answers this for refs under a private namespace the credentials
	// cannot see.
	PodErrorImageAuth PodErrorClass = "image_auth"
	// PodErrorImageMissing — image manifest does not exist (registry 404 /
	// unparseable ref).
	PodErrorImageMissing PodErrorClass = "image_missing"
	// PodErrorInterrupted — spot/interruptible pod reclaimed.
	PodErrorInterrupted PodErrorClass = "interrupted"
	// PodErrorUnknown — terminal, cause not classifiable from typed evidence
	// on the API surface. Consumers classify from the observation fields.
	PodErrorUnknown PodErrorClass = "unknown"
)

// PodTerminalError is the terminal verdict for a pod: a typed class where the
// evidence allows one, plus raw observation facts for consumer-side
// classification.
type PodTerminalError struct {
	PodID string
	Class PodErrorClass
	// Reason is the mechanical shape: pod_exited | pod_missing.
	Reason string
	// Message carries the provider's own text (lastStatusChange) plus any
	// probe/telemetry detail.
	Message string
	// ImageRef is the pod's image, when the pod record still exists.
	ImageRef string
	// Interruptible marks spot pods.
	Interruptible bool

	// ExitCode is the container exit code when the pod record still carries
	// it (runtime.containerExitCode). Nil = not exposed.
	ExitCode *int
	// MachineID / GPUTypeID / DataCenterID identify the host placement, when
	// the record exposes them (GPUTypeID empty for CPU pods).
	MachineID    string
	GPUTypeID    string
	DataCenterID string
	// UptimeSeconds is the runtime block's uptime at observation, when present.
	UptimeSeconds int
	// ContainerObserved reports that a container generation demonstrably ran
	// (exit code present, positive uptime, or fresh terminal telemetry for
	// the current start generation). False = the pod died before any
	// container start the API surface can prove (placement / image pull).
	ContainerObserved bool
	// ProbeOutcome is the raw registry-probe outcome (RegistryProbe*
	// constants; empty = no probe ran).
	ProbeOutcome string

	ObservedAt time.Time
}

// Registry-probe outcomes for PodTerminalErrorOptions.RegistryProbe.
const (
	RegistryProbeOK           = "ok"
	RegistryProbeUnauthorized = "unauthorized"
	RegistryProbeNotFound     = "not_found"
	RegistryProbeInvalidRef   = "invalid_ref"
	RegistryProbeUnreachable  = "unreachable"
)

// RegistryProbeFunc probes an image manifest with the same pull credentials
// pods receive and reports one of the RegistryProbe* outcomes plus a
// human-readable detail. Callers own credential handling; the SDK stays
// dependency-free.
type RegistryProbeFunc func(ctx context.Context, imageRef string) (outcome string, detail string)

// PodTerminalErrorOptions tunes GetPodTerminalError.
type PodTerminalErrorOptions struct {
	// RegistryProbe, when set, refines an otherwise-unclassifiable exit into
	// image_auth / image_missing and stamps ProbeOutcome either way.
	RegistryProbe RegistryProbeFunc
}

// GetPodTerminalError reports whether the pod is terminally dead without
// having served, with typed observation facts. Returns (nil, false, nil)
// while the pod is alive or still initializing; (verdict, true, nil) for a
// terminal state; error for provider API failures other than a REST
// pod-missing 404.
func (c *Client) GetPodTerminalError(ctx context.Context, podID string, opts *PodTerminalErrorOptions) (*PodTerminalError, bool, error) {
	if err := c.validateRequired("podID", podID); err != nil {
		return nil, false, err
	}
	pod, err := c.GetPodWithOptions(ctx, podID, &GetPodOptions{IncludeMachine: true})
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			return &PodTerminalError{
				PodID:      podID,
				Class:      PodErrorUnknown,
				Reason:     "pod_missing",
				Message:    "pod record missing at provider (reaped)",
				ObservedAt: time.Now().UTC(),
			}, true, nil
		}
		return nil, false, err
	}
	providerDetail := ""
	freshTerminalTelemetry := false
	if !isTerminalPodStatus(pod.DesiredStatus) {
		observation, err := c.GetPodLifecycleObservation(ctx, podID)
		if err != nil {
			return nil, false, err
		}
		switch {
		case isTerminalPodStatus(observation.DesiredStatus):
			providerDetail = "GraphQL desired status " + strings.TrimSpace(observation.DesiredStatus)
		case hasFreshTerminalTelemetry(observation):
			freshTerminalTelemetry = true
			providerDetail = "provider telemetry state exited at " + observation.LatestTelemetry.Time.Time.UTC().Format(time.RFC3339Nano) +
				" for start generation " + observation.LastStartedAt.Time.UTC().Format(time.RFC3339Nano)
		default:
			return nil, false, nil
		}
	}
	message := strings.TrimSpace(pod.LastStatusChange)
	if providerDetail != "" {
		message = strings.TrimSpace(message + "; " + providerDetail)
	}

	verdict := &PodTerminalError{
		PodID:         podID,
		Class:         PodErrorUnknown,
		Reason:        "pod_exited",
		Message:       message,
		ImageRef:      strings.TrimSpace(pod.ImageName),
		Interruptible: pod.Interruptible,
		MachineID:     strings.TrimSpace(pod.MachineID),
		ObservedAt:    time.Now().UTC(),
	}
	if pod.Machine != nil {
		if verdict.MachineID == "" {
			verdict.MachineID = strings.TrimSpace(pod.Machine.ID)
		}
		verdict.DataCenterID = strings.TrimSpace(pod.Machine.DataCenterID)
		// CPU pods report the sentinel "unknown" GPU type — normalize to none.
		if gt := strings.TrimSpace(pod.Machine.GPUTypeID); !strings.EqualFold(gt, "unknown") {
			verdict.GPUTypeID = gt
		}
	}
	if verdict.GPUTypeID == "" && pod.GPU != nil {
		verdict.GPUTypeID = strings.TrimSpace(pod.GPU.ID)
	}
	if pod.Runtime != nil {
		verdict.ExitCode = pod.Runtime.ContainerExitCode
		verdict.UptimeSeconds = pod.Runtime.UptimeSeconds
	}
	verdict.ContainerObserved = verdict.ExitCode != nil || verdict.UptimeSeconds > 0 || freshTerminalTelemetry

	// Spot reclaim: typed provider flag.
	if pod.Interruptible {
		verdict.Class = PodErrorInterrupted
		verdict.Message = strings.TrimSpace(verdict.Message + "; interruptible pod reclaimed by provider")
		return verdict, true, nil
	}
	// Registry probe with the caller's pull credentials.
	if opts != nil && opts.RegistryProbe != nil && verdict.ImageRef != "" {
		outcome, detail := opts.RegistryProbe(ctx, verdict.ImageRef)
		verdict.ProbeOutcome = outcome
		if detail != "" {
			verdict.Message = strings.TrimSpace(verdict.Message + "; registry probe: " + detail)
		}
		switch outcome {
		case RegistryProbeUnauthorized:
			verdict.Class = PodErrorImageAuth
		case RegistryProbeNotFound, RegistryProbeInvalidRef:
			verdict.Class = PodErrorImageMissing
		}
	}
	return verdict, true, nil
}
