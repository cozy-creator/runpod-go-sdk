package runpod

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// GetProviderFeatureSupport reports support for provider-specific optional features.
func (c *Client) GetProviderFeatureSupport(ctx context.Context) ProviderFeatureSupport {
	_ = ctx
	return ProviderFeatureSupport{
		PodLogsAPI: false,
		Reason:     "RunPod public REST/GraphQL APIs do not expose a supported pod logs endpoint",
	}
}

// GetPodDiagnostics returns a normalized pod diagnostics snapshot suitable for scheduler bootstrap diagnostics.
func (c *Client) GetPodDiagnostics(ctx context.Context, podID string) (*PodDiagnostics, error) {
	if err := c.validateRequired("podID", podID); err != nil {
		return nil, err
	}

	pod, err := c.GetPodWithOptions(ctx, podID, &GetPodOptions{
		IncludeMachine:       true,
		IncludeNetworkVolume: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get pod diagnostics for %s: %w", podID, err)
	}

	diag := &PodDiagnostics{
		PodID:            strings.TrimSpace(pod.ID),
		DesiredStatus:    strings.TrimSpace(pod.DesiredStatus),
		LastStatusChange: firstNonEmpty(strings.TrimSpace(pod.LastStatusChange), runtimeLastStatus(pod.Runtime)),
		Runtime:          pod.Runtime,
		Machine:          pod.Machine,
		PublicIP:         firstNonEmpty(strings.TrimSpace(pod.PublicIP), runtimePublicIP(pod.Runtime)),
		PortMappings:     derivePortMappings(pod),
		NetworkVolumeID:  firstNonEmpty(strings.TrimSpace(pod.NetworkVolumeID), networkVolumeID(pod.NetworkVolume)),
		ProviderReason:   deriveProviderReason(pod),
	}
	diag.RuntimeReady = isRuntimeReady(diag)
	diag.DataCenterID = deriveDatacenterID(pod)
	return diag, nil
}

func deriveDatacenterID(pod *Pod) string {
	if pod == nil {
		return ""
	}
	if pod.Machine != nil && strings.TrimSpace(pod.Machine.DataCenterID) != "" {
		return strings.TrimSpace(pod.Machine.DataCenterID)
	}
	if pod.NetworkVolume != nil && strings.TrimSpace(pod.NetworkVolume.DatacenterID) != "" {
		return strings.TrimSpace(pod.NetworkVolume.DatacenterID)
	}
	return ""
}

func networkVolumeID(v *NetworkVolume) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(v.ID)
}

func deriveProviderReason(pod *Pod) string {
	if pod == nil {
		return ""
	}
	if pod.Runtime != nil {
		for _, cand := range []string{
			strings.TrimSpace(pod.Runtime.Reason),
			strings.TrimSpace(pod.Runtime.Error),
			strings.TrimSpace(pod.Runtime.Status),
		} {
			if cand != "" {
				return cand
			}
		}
	}
	if strings.EqualFold(strings.TrimSpace(pod.DesiredStatus), "RUNNING") && pod.Runtime == nil {
		return "runtime_unavailable"
	}
	return ""
}

func runtimePublicIP(runtime *PodRuntime) string {
	if runtime == nil {
		return ""
	}
	return strings.TrimSpace(runtime.PublicIP)
}

func runtimeLastStatus(runtime *PodRuntime) string {
	if runtime == nil {
		return ""
	}
	return firstNonEmpty(strings.TrimSpace(runtime.LastStatusChange), strings.TrimSpace(runtime.LastStatusCharge))
}

func isRuntimeReady(diag *PodDiagnostics) bool {
	if diag == nil || diag.Runtime == nil {
		return false
	}
	if strings.TrimSpace(diag.PublicIP) != "" {
		return true
	}
	return len(diag.PortMappings) > 0
}

func derivePortMappings(pod *Pod) map[string]int {
	out := map[string]int{}
	if pod == nil || pod.Runtime == nil || pod.Runtime.Ports == nil {
		return out
	}

	for key, val := range pod.Runtime.Ports {
		port := coercePortValue(val)
		if port > 0 {
			out[key] = port
		}
	}
	return out
}

func coercePortValue(v interface{}) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(t))
		return n
	case map[string]interface{}:
		// Common shapes: {"publicPort": 1234} or {"port": 1234}
		for _, k := range []string{"publicPort", "port"} {
			if raw, ok := t[k]; ok {
				if n := coercePortValue(raw); n > 0 {
					return n
				}
			}
		}
	case []interface{}:
		// Common shapes: [{"publicPort":1234, ...}]
		for _, item := range t {
			if n := coercePortValue(item); n > 0 {
				return n
			}
		}
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
