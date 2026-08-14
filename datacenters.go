package runpod

import (
	"context"
	"fmt"
)

// DataCenter is one RunPod placement location together with its current
// per-GPU inventory. StockStatus is provider-owned telemetry (for example
// High, Medium, Low); callers decide freshness and placement policy.
//
// StorageSupport is the provider's own answer to whether this location can
// host a network volume at all. It is a property of the location, not of the
// account or the request: where it is false, CreateNetworkVolume is refused
// and no pod placed here can ever mount one. 30 of RunPod's 49 locations
// answer false, so a caller that cannot see this field cannot distinguish a
// datacenter whose cache is merely absent from one where a cache is
// impossible.
type DataCenter struct {
	ID              string                        `json:"id"`
	Name            string                        `json:"name"`
	Location        string                        `json:"location"`
	StorageSupport  bool                          `json:"storageSupport"`
	GPUAvailability []GPUAvailabilityInDataCenter `json:"gpuAvailability"`
}

// GPUAvailabilityInDataCenter is RunPod's current stock observation for one
// exact GPU type in one data center.
type GPUAvailabilityInDataCenter struct {
	GPUTypeID   string `json:"gpuTypeId"`
	DisplayName string `json:"displayName"`
	StockStatus string `json:"stockStatus"`
	Available   bool   `json:"available"`
}

// GPUAvailabilityFilter asks RunPod whether a data center can satisfy the
// complete machine shape, rather than returning the API's default one-GPU
// view. Zero values are omitted.
type GPUAvailabilityFilter struct {
	GPUCount            int      `json:"gpuCount,omitempty"`
	MinDisk             int      `json:"minDisk,omitempty"`
	MinMemoryInGB       int      `json:"minMemoryInGb,omitempty"`
	MinVCPUCount        int      `json:"minVcpuCount,omitempty"`
	SecureCloud         *bool    `json:"secureCloud,omitempty"`
	AllowedCUDAVersions []string `json:"allowedCudaVersions,omitempty"`
	MinCUDAVersion      string   `json:"minCudaVersion,omitempty"`
	IncludeAIAPI        *bool    `json:"includeAiApi,omitempty"`
}

type graphQLDataCenterPayload struct {
	DataCenters []DataCenter `json:"dataCenters"`
}

// ListDataCenters returns RunPod's authoritative per-datacenter GPU stock
// snapshot. It performs one bounded GraphQL request; caching, refresh cadence,
// and placement-failure policy belong to the orchestrator, not the SDK.
func (c *Client) ListDataCenters(ctx context.Context, filter *GPUAvailabilityFilter) ([]DataCenter, error) {
	if filter != nil {
		switch {
		case filter.GPUCount < 0:
			return nil, NewValidationError("gpuCount", "cannot be negative")
		case filter.MinDisk < 0:
			return nil, NewValidationError("minDisk", "cannot be negative")
		case filter.MinMemoryInGB < 0:
			return nil, NewValidationError("minMemoryInGb", "cannot be negative")
		case filter.MinVCPUCount < 0:
			return nil, NewValidationError("minVcpuCount", "cannot be negative")
		}
	}
	const query = `
query($input: GpuAvailabilityInput) {
  dataCenters {
    id
    name
    location
    storageSupport
    gpuAvailability(input: $input) {
      gpuTypeId
      displayName
      stockStatus
      available
    }
  }
}`

	var payload graphQLDataCenterPayload
	variables := map[string]interface{}{}
	if filter != nil {
		variables["input"] = filter
	}
	if err := c.GraphQL(ctx, query, variables, &payload); err != nil {
		return nil, fmt.Errorf("failed to list data centers: %w", err)
	}
	return payload.DataCenters, nil
}
