package runpod

import (
	"context"
	"fmt"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ObservationWindow records the local interval occupied by one provider read.
// It is evidence of observation time, not a provider generation fence.
type ObservationWindow struct{ StartedAt, CompletedAt time.Time }

// PodReadback preserves one independently timed REST v2 snapshot.
type PodReadback struct {
	PodID        string
	REST         *Pod
	RESTObserved ObservationWindow
}

type PodReadbackStage string

const (
	PodReadbackStageREST     PodReadbackStage = "rest"
	PodReadbackStageIdentity PodReadbackStage = "identity"
)

type PodReadbackError struct {
	PodID   string
	Stage   PodReadbackStage
	Partial *PodReadback
	Cause   error
}

func (e *PodReadbackError) Error() string {
	return fmt.Sprintf("pod %s readback %s: %v", e.PodID, e.Stage, e.Cause)
}
func (e *PodReadbackError) Unwrap() error { return e.Cause }
func (e *PodReadbackError) Is(target error) bool {
	return target == ErrIncompleteReadback && e.Stage == PodReadbackStageIdentity
}

// GetPodReadback preserves invalid primary evidence without misclassifying a
// malformed or foreign response as a missing pod. Only the primary HTTP 404
// supplies absence evidence.
func (c *Client) GetPodReadback(ctx context.Context, podID string) (*PodReadback, error) {
	if err := validatePodID(podID); err != nil {
		return nil, err
	}
	result := &PodReadback{PodID: podID, RESTObserved: ObservationWindow{StartedAt: time.Now().UTC()}}
	var pod Pod
	err := c.Get(ctx, "/pods/"+url.PathEscape(podID), &pod)
	result.RESTObserved.CompletedAt = time.Now().UTC()
	if err != nil {
		return nil, &PodReadbackError{PodID: podID, Stage: PodReadbackStageREST, Partial: result, Cause: err}
	}
	result.REST = &pod
	if err := validatePodIdentity(&pod, podID); err != nil {
		return nil, &PodReadbackError{PodID: podID, Stage: PodReadbackStageIdentity, Partial: result, Cause: err}
	}
	return result, nil
}

// ValidatedPublicTCPEndpoints returns published TCP mappings only when the
// snapshot belongs to this pod and the mapping agrees with an exposed port.
// Omitted public mappings are unknown/unavailable, never fabricated endpoints.
func (r *PodReadback) ValidatedPublicTCPEndpoints() ([]PodRuntimePort, error) {
	if r == nil || r.REST == nil {
		return nil, fmt.Errorf("pod readback has no REST snapshot")
	}
	if err := validatePodIdentity(r.REST, r.PodID); err != nil {
		return nil, err
	}
	pod := r.REST
	if pod.Status != "RUNNING" || pod.Runtime == nil {
		return nil, nil
	}
	configured := map[int]bool{}
	for _, value := range pod.Ports {
		var port int
		if _, err := fmt.Sscanf(value, "%d/tcp", &port); err == nil && value == fmt.Sprintf("%d/tcp", port) {
			configured[port] = true
		}
	}
	endpoints := make([]PodRuntimePort, 0)
	seen := map[int]PodRuntimePort{}
	var publicIP string
	for _, mapping := range pod.Runtime.Ports {
		if strings.ToLower(mapping.Type) != "tcp" || mapping.Public == nil || mapping.IP == nil {
			continue
		}
		if mapping.Private < 1 || mapping.Private > 65535 || *mapping.Public < 1 || *mapping.Public > 65535 {
			return nil, fmt.Errorf("pod %s has invalid TCP port mapping", pod.ID)
		}
		if !configured[mapping.Private] {
			return nil, fmt.Errorf("pod %s published unconfigured private TCP port %d", pod.ID, mapping.Private)
		}
		ip, err := netip.ParseAddr(*mapping.IP)
		if err != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
			return nil, fmt.Errorf("pod %s has invalid public TCP address %q", pod.ID, *mapping.IP)
		}
		normalizedIP := ip.Unmap().String()
		if publicIP != "" && publicIP != normalizedIP {
			return nil, fmt.Errorf("pod %s has conflicting public TCP addresses", pod.ID)
		}
		publicIP = normalizedIP
		if previous, ok := seen[mapping.Private]; ok {
			if *previous.Public != *mapping.Public || *previous.IP != *mapping.IP {
				return nil, fmt.Errorf("pod %s has conflicting mappings for private TCP port %d", pod.ID, mapping.Private)
			}
			continue
		}
		seen[mapping.Private] = mapping
		endpoints = append(endpoints, mapping)
	}
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].Private < endpoints[j].Private })
	return endpoints, nil
}
