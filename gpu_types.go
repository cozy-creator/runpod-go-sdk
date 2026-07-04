package runpod

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

type graphQLGPUTypePayload struct {
	GPUTypes []GPUType `json:"gpuTypes"`
}

// ListGPUTypes lists GPU types with optional filtering.
// This uses RunPod's GraphQL API because CUDA-aware availability is exposed there.
func (c *Client) ListGPUTypes(ctx context.Context, filter *GPUTypeFilter) ([]GPUType, error) {
	gpuCount := 1
	minCUDA := ""
	var allowedCUDA []string

	if filter != nil {
		if filter.GPUCount > 0 {
			gpuCount = filter.GPUCount
		}
		minCUDA = strings.TrimSpace(filter.MinCudaVersion)
		allowedCUDA = filter.AllowedCudaVersions
	}

	query := `
query($gpuCount: Int!, $minCudaVersion: String, $allowedCudaVersions: [String!]) {
  gpuTypes {
    id
    displayName
    memoryInGb
    secureCloud
    communityCloud
    lowestPrice(input: { gpuCount: $gpuCount, minCudaVersion: $minCudaVersion, allowedCudaVersions: $allowedCudaVersions }) {
      minimumBidPrice
      uninterruptablePrice
      stockStatus
    }
  }
}`

	variables := map[string]interface{}{
		"gpuCount":            gpuCount,
		"minCudaVersion":      minCUDA,
		"allowedCudaVersions": allowedCUDA,
	}

	var payload graphQLGPUTypePayload
	if err := c.GraphQL(ctx, query, variables, &payload); err != nil {
		return nil, fmt.Errorf("failed to list GPU types: %w", err)
	}

	return filterGPUTypeResults(payload.GPUTypes, filter), nil
}

// ListAvailableGPUs returns GPU types with currently available stock for the requested CUDA/runtime constraints.
func (c *Client) ListAvailableGPUs(ctx context.Context, minCudaVersion string, gpuCount int) ([]GPUTypeWithAvailability, error) {
	filter := &GPUTypeFilter{
		MinCudaVersion: strings.TrimSpace(minCudaVersion),
		GPUCount:       gpuCount,
	}

	gpus, err := c.ListGPUTypes(ctx, filter)
	if err != nil {
		return nil, err
	}

	out := make([]GPUTypeWithAvailability, 0, len(gpus))
	for _, gpu := range gpus {
		if gpu.LowestPrice == nil {
			continue
		}
		status := strings.TrimSpace(gpu.LowestPrice.StockStatus)
		if !isAvailableStockStatus(status) {
			continue
		}
		out = append(out, GPUTypeWithAvailability{
			GPUType:     gpu,
			StockStatus: status,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		left := out[i].LowestPrice
		right := out[j].LowestPrice
		if left == nil && right == nil {
			return out[i].DisplayName < out[j].DisplayName
		}
		if left == nil {
			return false
		}
		if right == nil {
			return true
		}
		if left.UninterruptablePrice == right.UninterruptablePrice {
			return out[i].DisplayName < out[j].DisplayName
		}
		return left.UninterruptablePrice < right.UninterruptablePrice
	})

	return out, nil
}

// GetGPUType returns details for a single GPU type by ID.
func (c *Client) GetGPUType(ctx context.Context, gpuTypeID string) (*GPUType, error) {
	id := strings.TrimSpace(gpuTypeID)
	if id == "" {
		return nil, NewValidationError("gpuTypeID", "cannot be empty")
	}

	gpus, err := c.ListGPUTypes(ctx, &GPUTypeFilter{IDs: []string{id}})
	if err != nil {
		return nil, err
	}
	if len(gpus) == 0 {
		return nil, NewAPIErrorWithDetails(404, "gpu type not found", id)
	}
	return &gpus[0], nil
}

func filterGPUTypeResults(items []GPUType, filter *GPUTypeFilter) []GPUType {
	if filter == nil {
		return items
	}

	idSet := make(map[string]struct{}, len(filter.IDs))
	for _, id := range filter.IDs {
		id = strings.TrimSpace(id)
		if id != "" {
			idSet[id] = struct{}{}
		}
	}

	out := make([]GPUType, 0, len(items))
	for _, item := range items {
		if len(idSet) > 0 {
			if _, ok := idSet[item.ID]; !ok {
				continue
			}
		}
		if filter.SecureCloud != nil && item.SecureCloud != *filter.SecureCloud {
			continue
		}
		if filter.CommunityCloud != nil && item.CommunityCloud != *filter.CommunityCloud {
			continue
		}
		if (strings.TrimSpace(filter.MinCudaVersion) != "" || len(filter.AllowedCudaVersions) > 0) && item.LowestPrice == nil {
			// CUDA-constrained query returning no price usually means unsupported/unavailable for that constraint.
			continue
		}
		out = append(out, item)
	}
	return out
}

func isAvailableStockStatus(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "AVAILABLE", "LOW", "LOW_STOCK", "IN_STOCK", "HIGH":
		return true
	default:
		return false
	}
}
