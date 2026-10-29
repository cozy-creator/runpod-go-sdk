package runpod

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
)

// CatalogListPrice identifies a catalog rate rather than an exact host quote.
const CatalogListPrice = "catalog_list_price"

// GPUOffer combines scoped catalog availability with a cloud's catalog list
// rate. Its price is not datacenter-specific and does not certify host RAM,
// vCPUs, disk or bandwidth. The pod's cost is its actual assigned rate.
type GPUOffer struct {
	GPUTypeID                     string
	DisplayName                   string
	MemoryInGB                    int
	CloudType                     string
	GPUCount                      int
	StockStatus                   string
	OnDemandPriceUSDMicrosPerHour USDMicrosPerHour
	PriceSource                   string
}

// GPUOfferFilter contains only constraints represented by REST v2 catalog
// availability. DataCenterID selects a returned datacenter's stock locally;
// it never changes the cloud-wide catalog price.
type GPUOfferFilter struct {
	CloudType           string
	GPUCount            int
	MinCudaVersion      string
	AllowedCudaVersions []string
	CountryCodes        []string
	DataCenterID        string
	IDs                 []string
	InStockOnly         bool
}

func validateGPUOfferFilter(f *GPUOfferFilter) error {
	if f == nil {
		return nil
	}
	if f.DataCenterID != strings.TrimSpace(f.DataCenterID) {
		return NewValidationError("dataCenterID", "must be an exact id")
	}
	_, err := gpuCatalogQuery(&GPUTypeFilter{IDs: f.IDs, GPUCount: f.GPUCount, CloudType: f.CloudType, MinCudaVersion: f.MinCudaVersion, AllowedCudaVersions: f.AllowedCudaVersions, CountryCodes: f.CountryCodes})
	return err
}
func checkedCatalogRate(rate USDMicrosPerHour, count int) (USDMicrosPerHour, error) {
	if count <= 0 || rate < 0 || int64(rate) > math.MaxInt64/int64(count) {
		return 0, fmt.Errorf("catalog aggregate hourly price overflows or has invalid count")
	}
	return rate * USDMicrosPerHour(count), nil
}
func (c *Client) listGPUOffers(ctx context.Context, f *GPUOfferFilter, read catalogReader) ([]GPUOffer, error) {
	if f == nil {
		f = &GPUOfferFilter{}
	}
	if err := validateGPUOfferFilter(f); err != nil {
		return nil, err
	}
	clouds := []string{f.CloudType}
	if f.CloudType == "" {
		clouds = []string{"SECURE", "COMMUNITY"}
	}
	offers := make([]GPUOffer, 0)
	for _, cloud := range clouds {
		gpus, err := c.listGPUTypes(ctx, &GPUTypeFilter{IDs: f.IDs, GPUCount: f.GPUCount, CloudType: cloud, MinCudaVersion: f.MinCudaVersion, AllowedCudaVersions: f.AllowedCudaVersions, CountryCodes: f.CountryCodes}, read)
		if err != nil {
			return nil, err
		}
		for _, gpu := range gpus {
			rate := gpu.Price.Secure
			supported := gpu.Secure
			if cloud == "COMMUNITY" {
				rate = gpu.Price.Community
				supported = gpu.Community
			}
			if !supported || rate == nil {
				continue
			}
			stock := gpu.Availability
			if f.DataCenterID != "" {
				stock = "NONE"
				for _, dc := range gpu.DataCenters {
					if dc.ID == f.DataCenterID {
						stock = dc.Availability
						break
					}
				}
				if len(gpu.DataCenters) == 0 && gpu.Availability != "NONE" {
					return nil, fmt.Errorf("RunPod omitted datacenters for available GPU %s", gpu.ID)
				}
			}
			if f.InStockOnly && !isAvailableStockStatus(stock) {
				continue
			}
			count := max(1, f.GPUCount)
			aggregate, err := checkedCatalogRate(*rate, count)
			if err != nil {
				return nil, err
			}
			offers = append(offers, GPUOffer{GPUTypeID: gpu.ID, DisplayName: gpu.Name, MemoryInGB: gpu.Memory, CloudType: cloud, GPUCount: count, StockStatus: stock, OnDemandPriceUSDMicrosPerHour: aggregate, PriceSource: CatalogListPrice})
		}
	}
	sort.SliceStable(offers, func(i, j int) bool {
		if offers[i].OnDemandPriceUSDMicrosPerHour == offers[j].OnDemandPriceUSDMicrosPerHour {
			return offers[i].GPUTypeID < offers[j].GPUTypeID
		}
		return offers[i].OnDemandPriceUSDMicrosPerHour < offers[j].OnDemandPriceUSDMicrosPerHour
	})
	return offers, nil
}
func (c *Client) ListGPUOffers(ctx context.Context, f *GPUOfferFilter) ([]GPUOffer, error) {
	return c.listGPUOffers(ctx, f, c.Get)
}
