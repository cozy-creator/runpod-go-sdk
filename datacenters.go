package runpod

import (
	"context"
	"fmt"
)

// DataCenter keeps native REST v2 location/storage facts and a normalized Pod
// availability view assembled from the scoped GPU catalog.
type DataCenter struct {
	ID                 string                        `json:"id"`
	Name               string                        `json:"name"`
	Region             string                        `json:"region"`
	NetworkVolumeTypes []string                      `json:"networkVolumeTypes"`
	Compliance         []string                      `json:"compliance"`
	GlobalNetwork      bool                          `json:"globalNetwork"`
	Location           string                        `json:"-"`
	StorageSupport     bool                          `json:"-"`
	GPUAvailability    []GPUAvailabilityInDataCenter `json:"-"`
}

// GPUAvailabilityInDataCenter preserves the cloud whose scoped request supplied
// its stock. A both-cloud census can contain two rows for the same GPU type.
type GPUAvailabilityInDataCenter struct {
	GPUTypeID   string
	DisplayName string
	CloudType   string
	StockStatus string
	Available   bool
}

// GPUAvailabilityFilter contains only REST v2 catalog placement filters.
// A nil SecureCloud reads both clouds; true selects SECURE, false COMMUNITY.
type GPUAvailabilityFilter struct {
	GPUCount            int
	SecureCloud         *bool
	AllowedCUDAVersions []string
	MinCUDAVersion      string
	CountryCodes        []string
}

func validateGPUAvailabilityFilter(f *GPUAvailabilityFilter) error {
	if f == nil {
		return nil
	}
	return validateGPUOfferFilter(&GPUOfferFilter{GPUCount: f.GPUCount, MinCudaVersion: f.MinCUDAVersion, AllowedCudaVersions: f.AllowedCUDAVersions, CountryCodes: f.CountryCodes})
}
func (c *Client) listDataCenters(ctx context.Context, f *GPUAvailabilityFilter, read catalogReader) ([]DataCenter, error) {
	if err := validateGPUAvailabilityFilter(f); err != nil {
		return nil, err
	}
	if f == nil {
		f = &GPUAvailabilityFilter{}
	}
	centers, err := catalogList[DataCenter](ctx, read, "/catalog/datacenters", "dataCenters")
	if err != nil {
		return nil, err
	}
	byID := make(map[string]int, len(centers))
	for i := range centers {
		dc := &centers[i]
		if dc.ID == "" || dc.Name == "" || dc.Region == "" || dc.NetworkVolumeTypes == nil {
			return nil, fmt.Errorf("RunPod returned incomplete datacenter row")
		}
		if _, exists := byID[dc.ID]; exists {
			return nil, fmt.Errorf("RunPod returned duplicate datacenter %q", dc.ID)
		}
		byID[dc.ID] = i
		dc.Location = dc.Region
		dc.StorageSupport = len(dc.NetworkVolumeTypes) > 0
		dc.GPUAvailability = make([]GPUAvailabilityInDataCenter, 0)
	}
	clouds := []string{"SECURE", "COMMUNITY"}
	if f.SecureCloud != nil {
		if *f.SecureCloud {
			clouds = []string{"SECURE"}
		} else {
			clouds = []string{"COMMUNITY"}
		}
	}
	for _, cloud := range clouds {
		gpus, err := c.listGPUTypes(ctx, &GPUTypeFilter{GPUCount: f.GPUCount, CloudType: cloud, MinCudaVersion: f.MinCUDAVersion, AllowedCudaVersions: f.AllowedCUDAVersions, CountryCodes: f.CountryCodes}, read)
		if err != nil {
			return nil, err
		}
		for _, gpu := range gpus {
			supported := gpu.Secure
			if cloud == "COMMUNITY" {
				supported = gpu.Community
			}
			if !supported {
				continue
			}
			if len(gpu.DataCenters) == 0 && gpu.Availability != "NONE" {
				return nil, fmt.Errorf("RunPod omitted datacenters for available GPU %s", gpu.ID)
			}
			for _, availability := range gpu.DataCenters {
				i, exists := byID[availability.ID]
				if !exists {
					return nil, fmt.Errorf("RunPod GPU availability names unknown datacenter %q", availability.ID)
				}
				centers[i].GPUAvailability = append(centers[i].GPUAvailability, GPUAvailabilityInDataCenter{GPUTypeID: gpu.ID, DisplayName: gpu.Name, CloudType: cloud, StockStatus: availability.Availability, Available: isAvailableStockStatus(availability.Availability)})
			}
		}
	}
	return centers, nil
}
func (c *Client) ListDataCenters(ctx context.Context, f *GPUAvailabilityFilter) ([]DataCenter, error) {
	return c.listDataCenters(ctx, f, c.Get)
}
