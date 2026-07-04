package runpod

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// GPUOffer is one purchasable combination of GPU type and cloud, with
// current stock and pricing. Datacenter granularity is not exposed by
// RunPod's lowestPrice query; constrain placement with
// CreatePodRequest.DataCenterIDs instead.
type GPUOffer struct {
	GPUTypeID   string
	DisplayName string
	MemoryInGB  int
	CloudType   string // "SECURE" or "COMMUNITY"
	StockStatus string
	// OnDemandPrice is the uninterruptible USD/hr price.
	OnDemandPrice float64
	// MinimumBidPrice is the current spot-market floor (USD/hr per GPU);
	// use it as CreatePodRequest.BidPerGPU guidance. RunPod no longer
	// reports a separate interruptible/spot price on LowestPrice — this
	// floor is the only spot pricing signal exposed.
	MinimumBidPrice float64
}

// GPUOfferFilter constrains ListGPUOffers. Zero value = all offers for one
// GPU.
type GPUOfferFilter struct {
	// GPUCount prices the offer for this many GPUs per pod (default 1).
	GPUCount int
	// MinCudaVersion filters to machines with at least this CUDA version.
	MinCudaVersion string
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
// GPUTypeIDs/CloudType (and BidPerGPU for spot) on the CreatePodRequest.
func (c *Client) ListGPUOffers(ctx context.Context, filter *GPUOfferFilter) ([]GPUOffer, error) {
	gpuCount := 1
	minCUDA := ""
	inStockOnly := false
	idSet := map[string]struct{}{}
	if filter != nil {
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
query($gpuCount: Int!, $minCudaVersion: String) {
  gpuTypes {
    id
    displayName
    memoryInGb
    secureCloud
    communityCloud
    secure: lowestPrice(input: { gpuCount: $gpuCount, minCudaVersion: $minCudaVersion, secureCloud: true }) {
      minimumBidPrice
      uninterruptablePrice
      stockStatus
    }
    community: lowestPrice(input: { gpuCount: $gpuCount, minCudaVersion: $minCudaVersion, secureCloud: false }) {
      minimumBidPrice
      uninterruptablePrice
      stockStatus
    }
  }
}`

	variables := map[string]interface{}{
		"gpuCount":       gpuCount,
		"minCudaVersion": minCUDA,
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
				GPUTypeID:       gpu.ID,
				DisplayName:     gpu.DisplayName,
				MemoryInGB:      gpu.MemoryInGB,
				CloudType:       cloud,
				StockStatus:     status,
				OnDemandPrice:   price.UninterruptablePrice,
				MinimumBidPrice: price.MinimumBidPrice,
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
