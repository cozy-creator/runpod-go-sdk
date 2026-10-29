package runpod

import (
	"context"
	"fmt"
)

type ProviderFeatureSupport struct {
	PodLogsAPI bool
	Reason     string
}

func (c *Client) GetProviderFeatureSupport(ctx context.Context) ProviderFeatureSupport {
	return ProviderFeatureSupport{PodLogsAPI: true, Reason: "RunPod REST v2 exposes container and system pod log streams"}
}

// PodDiagnostics does not infer application readiness or a failure cause.
type PodDiagnostics struct {
	PodID            string
	Status           string
	RuntimeReady     bool
	Runtime          *PodRuntime
	DataCenterID     string
	PublicTCP        []PodRuntimePort
	NetworkVolumeIDs []string
}

func (c *Client) GetPodDiagnostics(ctx context.Context, podID string) (*PodDiagnostics, error) {
	readback, err := c.GetPodReadback(ctx, podID)
	if err != nil {
		return nil, err
	}
	pod := readback.REST
	ports, err := readback.ValidatedPublicTCPEndpoints()
	if err != nil {
		return nil, err
	}
	result := &PodDiagnostics{PodID: pod.ID, Status: pod.Status, Runtime: pod.Runtime, RuntimeReady: pod.Status == "RUNNING" && pod.Runtime != nil, PublicTCP: ports}
	if pod.DataCenterID != nil {
		result.DataCenterID = *pod.DataCenterID
	}
	if pod.Mounts != nil {
		for _, mount := range pod.Mounts.Network {
			result.NetworkVolumeIDs = append(result.NetworkVolumeIDs, mount.VolumeID)
		}
	}
	return result, nil
}

// PodPortMappings returns valid published TCP ports keyed by private port.
// It omits malformed mappings; callers that need evidence validation should
// use PodReadback.ValidatedPublicTCPEndpoints and inspect its error.
func PodPortMappings(pod *Pod) map[string]int {
	result := map[string]int{}
	if pod == nil {
		return result
	}
	ports, err := (&PodReadback{PodID: pod.ID, REST: pod}).ValidatedPublicTCPEndpoints()
	if err != nil {
		return result
	}
	for _, port := range ports {
		result[fmt.Sprintf("%d/tcp", port.Private)] = *port.Public
	}
	return result
}
