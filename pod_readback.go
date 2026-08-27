package runpod

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// PodRuntimePort is one exact runtime port row returned by RunPod's GraphQL
// pod projection. REST identifies the requested private ports but does not
// currently return their public mappings.
type PodRuntimePort struct {
	IP          string `json:"ip"`
	IsIPPublic  bool   `json:"isIpPublic"`
	PrivatePort int    `json:"privatePort"`
	PublicPort  int    `json:"publicPort"`
	Type        string `json:"type"`
}

type podRuntimeGQL struct {
	UptimeInSeconds *int             `json:"uptimeInSeconds"`
	Ports           []PodRuntimePort `json:"ports"`
}

type podRuntimeReadbackGQL struct {
	ID              string                 `json:"id"`
	DesiredStatus   string                 `json:"desiredStatus"`
	LastStartedAt   *JSONTime              `json:"lastStartedAt"`
	LatestTelemetry *PodLifecycleTelemetry `json:"latestTelemetry"`
	Runtime         *podRuntimeGQL         `json:"runtime"`
}

// CurrentPodRuntime is a GraphQL runtime projection proven to belong to the
// same current container generation as PodReadback.Pod. It remains nil while
// runtime is absent, telemetry is absent/stale, or current telemetry is not
// running. No elapsed-time guess turns those states into readiness.
type CurrentPodRuntime struct {
	UptimeInSeconds *int
	LatestTelemetry PodLifecycleTelemetry
	PublicIP        string
	Ports           []PodRuntimePort
}

// PodReadback joins the provider facts that RunPod currently splits across
// two APIs. Pod is the REST authority for machine, price, volume and requested
// ports. Lifecycle preserves GraphQL's exact current-generation telemetry,
// including a fresh terminal observation while REST desiredStatus still says
// RUNNING. CurrentRuntime is the GraphQL authority for current public mappings;
// it is populated only after ID, desired-status and start-generation fences
// agree and current telemetry says running.
type PodReadback struct {
	Pod            *Pod
	Lifecycle      PodLifecycleObservation
	CurrentRuntime *CurrentPodRuntime
}

// GetPodReadback returns one coherent pod observation. The REST read happens
// first; a restart between the REST and GraphQL reads is detected by the exact
// lastStartedAt fence and returned as an error for the caller to retry.
func (c *Client) GetPodReadback(ctx context.Context, podID string, opts *GetPodOptions) (*PodReadback, error) {
	pod, err := c.GetPodWithOptions(ctx, podID, opts)
	if err != nil {
		return nil, err
	}
	runtime, err := c.getPodRuntimeReadback(ctx, podID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(pod.ID) != podID || strings.TrimSpace(runtime.ID) != podID {
		return nil, fmt.Errorf("pod readback identity changed: requested %q, REST returned %q, GraphQL returned %q",
			podID, pod.ID, runtime.ID)
	}
	if !strings.EqualFold(strings.TrimSpace(pod.DesiredStatus), strings.TrimSpace(runtime.DesiredStatus)) {
		return nil, fmt.Errorf("pod %s readback desired status changed between REST %q and GraphQL %q",
			podID, pod.DesiredStatus, runtime.DesiredStatus)
	}
	if !sameGeneration(pod.LastStartedAt, runtime.LastStartedAt) {
		return nil, fmt.Errorf("pod %s readback generation changed between REST and GraphQL", podID)
	}

	out := &PodReadback{
		Pod: pod,
		Lifecycle: PodLifecycleObservation{
			PodID:                  runtime.ID,
			DesiredStatus:          runtime.DesiredStatus,
			LastStartedAt:          runtime.LastStartedAt,
			LatestTelemetry:        runtime.LatestTelemetry,
			RuntimeUptimeInSeconds: runtimeUptime(runtime.Runtime),
		},
	}
	if runtime.Runtime == nil || runtime.LatestTelemetry == nil || runtime.LastStartedAt == nil ||
		runtime.LatestTelemetry.Time == nil ||
		runtime.LatestTelemetry.Time.Time.Before(runtime.LastStartedAt.Time) ||
		!strings.EqualFold(strings.TrimSpace(runtime.LatestTelemetry.State), "running") {
		return out, nil
	}
	publicIP, err := validateRuntimePorts(pod, runtime.Runtime.Ports)
	if err != nil {
		return nil, fmt.Errorf("pod %s runtime port readback: %w", podID, err)
	}
	out.CurrentRuntime = &CurrentPodRuntime{
		UptimeInSeconds: runtime.Runtime.UptimeInSeconds,
		LatestTelemetry: *runtime.LatestTelemetry,
		PublicIP:        publicIP,
		Ports:           append([]PodRuntimePort(nil), runtime.Runtime.Ports...),
	}
	return out, nil
}

func runtimeUptime(runtime *podRuntimeGQL) *int {
	if runtime == nil {
		return nil
	}
	return runtime.UptimeInSeconds
}

func (c *Client) getPodRuntimeReadback(ctx context.Context, podID string) (*podRuntimeReadbackGQL, error) {
	if err := c.validateRequired("podID", podID); err != nil {
		return nil, err
	}
	query := `query($input: PodFilter!) {
  pod(input: $input) {
    id
    desiredStatus
    lastStartedAt
    latestTelemetry { state time }
    runtime {
      uptimeInSeconds
      ports { ip isIpPublic privatePort publicPort type }
    }
  }
}`
	var response struct {
		Pod *podRuntimeReadbackGQL `json:"pod"`
	}
	if err := c.GraphQL(ctx, query, map[string]interface{}{
		"input": map[string]interface{}{"podId": podID},
	}, &response); err != nil {
		return nil, fmt.Errorf("get pod %s runtime readback: %w", podID, err)
	}
	if response.Pod == nil {
		return nil, NewAPIErrorWithDetails(404, "pod not found", podID)
	}
	return response.Pod, nil
}

func sameGeneration(rest, graphql *JSONTime) bool {
	if rest == nil || graphql == nil {
		return rest == nil && graphql == nil
	}
	return rest.Time.Equal(graphql.Time)
}

func validateRuntimePorts(pod *Pod, ports []PodRuntimePort) (string, error) {
	requested := make(map[int]struct{}, len(pod.Ports))
	for _, port := range pod.Ports {
		value := strings.ToLower(strings.TrimSpace(port))
		privateText, protocol, found := strings.Cut(value, "/")
		if !found || protocol != "tcp" {
			continue
		}
		privatePort, err := strconv.Atoi(privateText)
		if err != nil || privatePort <= 0 || privatePort > 65535 || strconv.Itoa(privatePort) != privateText {
			return "", fmt.Errorf("invalid REST-requested TCP port %q", port)
		}
		requested[privatePort] = struct{}{}
	}
	seen := map[int]PodRuntimePort{}
	restPublicIP := strings.TrimSpace(pod.PublicIP)
	graphQLPublicIP := ""
	for _, port := range ports {
		if !port.IsIPPublic {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(port.Type))
		if kind != "tcp" {
			continue
		}
		if net.ParseIP(strings.TrimSpace(port.IP)) == nil || port.PrivatePort <= 0 || port.PrivatePort > 65535 ||
			port.PublicPort <= 0 || port.PublicPort > 65535 {
			return "", fmt.Errorf("invalid public TCP mapping %+v", port)
		}
		mappingIP := strings.TrimSpace(port.IP)
		if graphQLPublicIP == "" {
			graphQLPublicIP = mappingIP
		} else if mappingIP != graphQLPublicIP {
			return "", fmt.Errorf("public TCP mappings disagree on IP: %q and %q", graphQLPublicIP, mappingIP)
		}
		if restPublicIP != "" && mappingIP != restPublicIP {
			return "", fmt.Errorf("public mapping IP %q disagrees with REST publicIp %q", port.IP, pod.PublicIP)
		}
		if _, ok := requested[port.PrivatePort]; !ok {
			return "", fmt.Errorf("public mapping %d/tcp was not one of the REST-requested ports", port.PrivatePort)
		}
		if _, ok := seen[port.PrivatePort]; ok {
			return "", fmt.Errorf("multiple public mappings for private port %d", port.PrivatePort)
		}
		seen[port.PrivatePort] = port
	}
	for privatePort := range requested {
		if _, ok := seen[privatePort]; !ok {
			return "", fmt.Errorf("REST-requested port %d/tcp has no public mapping", privatePort)
		}
	}
	return graphQLPublicIP, nil
}

// PublicTCPPortMappings returns the current public TCP mappings keyed by
// private container port. The returned map is a copy.
func (r *PodReadback) PublicTCPPortMappings() map[int]int {
	out := map[int]int{}
	if r == nil || r.CurrentRuntime == nil {
		return out
	}
	for _, port := range r.CurrentRuntime.Ports {
		if port.IsIPPublic && strings.EqualFold(strings.TrimSpace(port.Type), "tcp") {
			out[port.PrivatePort] = port.PublicPort
		}
	}
	return out
}
