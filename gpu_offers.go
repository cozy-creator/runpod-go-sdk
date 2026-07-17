package runpod

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// GPUOffer is one purchasable combination of GPU type and cloud, with
// current stock and pricing. When GPUOfferFilter.DataCenterID is set, the
// quote is scoped to that exact data center.
type GPUOffer struct {
	GPUTypeID   string
	DisplayName string
	MemoryInGB  int
	CloudType   string // "SECURE" or "COMMUNITY"
	// GPUCount is the whole-pod count used to obtain both prices below.
	GPUCount    int
	StockStatus string
	// OnDemandPrice is the uninterruptible USD/hr price.
	OnDemandPrice float64
	// MinimumBidPrice is the aggregate spot-market floor for GPUCount GPUs.
	// REST CreatePodRequest.BidPerGPU expects a per-GPU number, so callers
	// must divide this value by GPUCount before using it there. RunPod no longer
	// reports a separate interruptible/spot price on LowestPrice — this
	// floor is the only spot pricing signal exposed.
	MinimumBidPrice float64
	// AvailableGPUCounts is the set of whole-pod GPU counts currently
	// reported for this exact GPU type/cloud price surface.
	AvailableGPUCounts []int
}

// GPUOfferFilter constrains ListGPUOffers. Zero value = all offers for one
// GPU.
type GPUOfferFilter struct {
	// GPUCount prices the offer for this many GPUs per pod (default 1).
	GPUCount int
	// MinCudaVersion filters to machines with at least this CUDA version.
	MinCudaVersion      string
	AllowedCudaVersions []string
	// DataCenterID restricts lowestPrice to one exact RunPod data center.
	// The API also accepts comma-separated IDs, but exact-placement callers
	// should pass one.
	DataCenterID  string
	MinMemoryInGB int
	MinVCPUCount  int
	MinDisk       int
	TotalDisk     int
	// IDs filters to specific GPU type IDs.
	IDs []string
	// InStockOnly drops offers whose stock status is unavailable.
	InStockOnly bool
}

type graphQLGPUOfferPayload struct {
	GPUTypes []struct {
		ID             string `json:"id"`
		DisplayName    string `json:"displayName"`
		MemoryInGB     int    `json:"memoryInGb"`
		SecureCloud    bool   `json:"secureCloud"`
		CommunityCloud bool   `json:"communityCloud"`
		Secure         *Price `json:"secure"`
		Community      *Price `json:"community"`
	} `json:"gpuTypes"`
}

// ListGPUOffers returns per-(GPU type x cloud) stock and pricing in one
// call, sorted by on-demand price ascending. This is the placement-decision
// view that connects the catalog to pod creation: pick an offer, then set
// GPUTypeIDs/CloudType/DataCenterIDs (and a per-GPU BidPerGPU for spot) on
// the CreatePodRequest.
func (c *Client) ListGPUOffers(ctx context.Context, filter *GPUOfferFilter) ([]GPUOffer, error) {
	gpuCount := 1
	minCUDA := ""
	inStockOnly := false
	idSet := map[string]struct{}{}
	if filter != nil {
		switch {
		case filter.GPUCount < 0:
			return nil, NewValidationError("gpuCount", "cannot be negative")
		case filter.MinMemoryInGB < 0:
			return nil, NewValidationError("minMemoryInGb", "cannot be negative")
		case filter.MinVCPUCount < 0:
			return nil, NewValidationError("minVcpuCount", "cannot be negative")
		case filter.MinDisk < 0:
			return nil, NewValidationError("minDisk", "cannot be negative")
		case filter.TotalDisk < 0:
			return nil, NewValidationError("totalDisk", "cannot be negative")
		}
		if filter.GPUCount > 0 {
			gpuCount = filter.GPUCount
		}
		minCUDA = strings.TrimSpace(filter.MinCudaVersion)
		inStockOnly = filter.InStockOnly
		for _, id := range filter.IDs {
			if id = strings.TrimSpace(id); id != "" {
				idSet[id] = struct{}{}
			}
		}
	}

	query := `
query($gpuCount: Int!, $minCudaVersion: String, $allowedCudaVersions: [String], $dataCenterId: String, $minMemoryInGb: Int, $minVcpuCount: Int, $minDisk: Int, $totalDisk: Int) {
  gpuTypes {
    id
    displayName
    memoryInGb
    secureCloud
    communityCloud
    secure: lowestPrice(input: { gpuCount: $gpuCount, minCudaVersion: $minCudaVersion, allowedCudaVersions: $allowedCudaVersions, dataCenterId: $dataCenterId, minMemoryInGb: $minMemoryInGb, minVcpuCount: $minVcpuCount, minDisk: $minDisk, totalDisk: $totalDisk, secureCloud: true }) {
      minimumBidPrice
      uninterruptablePrice
      stockStatus
      availableGpuCounts
    }
    community: lowestPrice(input: { gpuCount: $gpuCount, minCudaVersion: $minCudaVersion, allowedCudaVersions: $allowedCudaVersions, dataCenterId: $dataCenterId, minMemoryInGb: $minMemoryInGb, minVcpuCount: $minVcpuCount, minDisk: $minDisk, totalDisk: $totalDisk, secureCloud: false }) {
      minimumBidPrice
      uninterruptablePrice
      stockStatus
      availableGpuCounts
    }
  }
}`

	variables := map[string]interface{}{
		"gpuCount":       gpuCount,
		"minCudaVersion": minCUDA,
	}
	if filter != nil {
		variables["allowedCudaVersions"] = filter.AllowedCudaVersions
		variables["dataCenterId"] = strings.TrimSpace(filter.DataCenterID)
		variables["minMemoryInGb"] = filter.MinMemoryInGB
		variables["minVcpuCount"] = filter.MinVCPUCount
		variables["minDisk"] = filter.MinDisk
		variables["totalDisk"] = filter.TotalDisk
	}

	var payload graphQLGPUOfferPayload
	if err := c.GraphQL(ctx, query, variables, &payload); err != nil {
		return nil, fmt.Errorf("failed to list GPU offers: %w", err)
	}

	var offers []GPUOffer
	for _, gpu := range payload.GPUTypes {
		if len(idSet) > 0 {
			if _, ok := idSet[gpu.ID]; !ok {
				continue
			}
		}
		add := func(cloud string, price *Price) {
			if price == nil {
				return
			}
			status := strings.TrimSpace(price.StockStatus)
			if inStockOnly && !isAvailableStockStatus(status) {
				return
			}
			offers = append(offers, GPUOffer{
				GPUTypeID:          gpu.ID,
				DisplayName:        gpu.DisplayName,
				MemoryInGB:         gpu.MemoryInGB,
				CloudType:          cloud,
				GPUCount:           gpuCount,
				StockStatus:        status,
				OnDemandPrice:      price.UninterruptablePrice,
				MinimumBidPrice:    price.MinimumBidPrice,
				AvailableGPUCounts: append([]int(nil), price.AvailableGPUCounts...),
			})
		}
		if gpu.SecureCloud {
			add("SECURE", gpu.Secure)
		}
		if gpu.CommunityCloud {
			add("COMMUNITY", gpu.Community)
		}
	}

	sort.SliceStable(offers, func(i, j int) bool {
		if offers[i].OnDemandPrice == offers[j].OnDemandPrice {
			return offers[i].DisplayName < offers[j].DisplayName
		}
		return offers[i].OnDemandPrice < offers[j].OnDemandPrice
	})

	return offers, nil
}
