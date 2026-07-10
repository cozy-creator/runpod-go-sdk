package runpod

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Pod terminal-error surface (tensorhub th#648).
//
// RunPod's pod create returns success synchronously; machine-side failures
// (image pull rejection, host faults, spot reclaims) surface MINUTES later as
// an async EXITED pod state. What the API key can see:
//
//   - REST GET /pods/{id}: desiredStatus (RUNNING|EXITED|TERMINATED) plus
//     lastStatusChange — free text that for failures reads just
//     "Exited by Runpod: <ts>" with NO error class.
//   - GraphQL pod{...}: the same fields; no event/error fields exist
//     (probed exhaustively 2026-07; introspection disabled).
//   - The console/email texts ("Pod initialization error ...
//     IMAGE_AUTH_ERROR:unauthorized", "IMAGE_NOT_FOUND") come from RunPod's
//     session-authed system-log service (hapi.runpod.net) and CSR-scoped
//     notification queries — NOT reachable with an API key.
//
// So classification is derived: message markers when present (defensive —
// RunPod may add them to lastStatusChange someday), the interruptible flag
// for spot reclaims, and an optional caller-supplied registry probe that
// checks the image manifest with the same pull credentials pods receive:
// 401/403 → image_auth, 404 → image_missing, 200 → the image is fine so the
// failure was host-side.

// PodErrorClass is the job-relevant classification of a terminal pod error.
type PodErrorClass string

const (
	// PodErrorImageAuth — image pull rejected as unauthorized (RunPod's
	// IMAGE_AUTH_ERROR family). Docker Hub also answers this for refs under
	// a private namespace the credentials cannot see.
	PodErrorImageAuth PodErrorClass = "image_auth"
	// PodErrorImageMissing — image manifest does not exist (RunPod's
	// IMAGE_NOT_FOUND family; registry 404).
	PodErrorImageMissing PodErrorClass = "image_missing"
	// PodErrorOOM — out-of-memory kill.
	PodErrorOOM PodErrorClass = "oom"
	// PodErrorHostFault — machine-side failure while the image itself is
	// pullable; retry on a different host.
	PodErrorHostFault PodErrorClass = "host_fault"
	// PodErrorInterrupted — spot/interruptible pod reclaimed.
	PodErrorInterrupted PodErrorClass = "interrupted"
	// PodErrorUnknown — terminal, cause not classifiable from the available
	// surface.
	PodErrorUnknown PodErrorClass = "unknown"
)

// PodTerminalError is the classified terminal verdict for a pod.
type PodTerminalError struct {
	PodID string
	Class PodErrorClass
	// Reason is the mechanical shape: pod_exited | pod_missing.
	Reason string
	// Message carries the provider's own text (lastStatusChange) plus any
	// classification detail.
	Message string
	// ImageRef is the pod's image, when the pod record still exists.
	ImageRef string
	// Interruptible marks spot pods.
	Interruptible bool
	ObservedAt    time.Time
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
	// image_auth / image_missing / host_fault.
	RegistryProbe RegistryProbeFunc
}

// ClassifyPodErrorMessage maps RunPod's error-text vocabulary (console,
// notification emails, and any future lastStatusChange enrichment) to a
// class. Returns PodErrorUnknown when the text carries no marker — which is
// the norm for the API surface's bare "Exited by Runpod: <ts>".
func ClassifyPodErrorMessage(msg string) PodErrorClass {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "image_auth_error") || strings.Contains(m, "pull access denied") || strings.Contains(m, "unauthorized"):
		return PodErrorImageAuth
	case strings.Contains(m, "image_not_found") || strings.Contains(m, "manifest unknown") || strings.Contains(m, "not found: manifest"):
		return PodErrorImageMissing
	case strings.Contains(m, "out of memory") || strings.Contains(m, "oom kill") || strings.Contains(m, "oom_kill") || strings.Contains(m, "oomkilled"):
		return PodErrorOOM
	default:
		return PodErrorUnknown
	}
}

// GetPodTerminalError reports whether the pod is terminally dead without
// having served, and classifies why. Returns (nil, false, nil) while the pod
// is alive or still initializing; (verdict, true, nil) for a terminal state;
// error only for API failures other than the pod-missing 404.
func (c *Client) GetPodTerminalError(ctx context.Context, podID string, opts *PodTerminalErrorOptions) (*PodTerminalError, bool, error) {
	if err := c.validateRequired("podID", podID); err != nil {
		return nil, false, err
	}
	pod, err := c.GetPod(ctx, podID)
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
	switch strings.ToUpper(strings.TrimSpace(pod.DesiredStatus)) {
	case "EXITED", "TERMINATED", "DEAD":
	default:
		return nil, false, nil
	}

	verdict := &PodTerminalError{
		PodID:         podID,
		Reason:        "pod_exited",
		Message:       strings.TrimSpace(pod.LastStatusChange),
		ImageRef:      strings.TrimSpace(pod.ImageName),
		Interruptible: pod.Interruptible,
		ObservedAt:    time.Now().UTC(),
	}

	// 1. Message markers (rare on the API surface, definitive when present).
	if class := ClassifyPodErrorMessage(verdict.Message); class != PodErrorUnknown {
		verdict.Class = class
		return verdict, true, nil
	}
	// 2. Spot reclaim.
	if pod.Interruptible {
		verdict.Class = PodErrorInterrupted
		verdict.Message = strings.TrimSpace(verdict.Message + "; interruptible pod reclaimed by provider")
		return verdict, true, nil
	}
	// 3. Registry probe with the caller's pull credentials.
	if opts != nil && opts.RegistryProbe != nil && verdict.ImageRef != "" {
		outcome, detail := opts.RegistryProbe(ctx, verdict.ImageRef)
		if detail != "" {
			verdict.Message = strings.TrimSpace(verdict.Message + "; registry probe: " + detail)
		}
		switch outcome {
		case RegistryProbeUnauthorized:
			verdict.Class = PodErrorImageAuth
		case RegistryProbeNotFound, RegistryProbeInvalidRef:
			verdict.Class = PodErrorImageMissing
		case RegistryProbeOK:
			verdict.Class = PodErrorHostFault
		default:
			verdict.Class = PodErrorUnknown
		}
		return verdict, true, nil
	}
	verdict.Class = PodErrorUnknown
	return verdict, true, nil
}
