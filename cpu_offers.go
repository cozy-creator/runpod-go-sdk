package runpod

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var cpuInstanceIDPattern = regexp.MustCompile(`^(cpu[0-9]+[cgm])-([1-9][0-9]*)-([1-9][0-9]*)$`)

type CPUOfferRequest struct {
	InstanceID   string
	DataCenterID string
}

// CPUOffer prices one exact shape using a catalog per-vCPU rate. REST v2 does
// not expose the old host-specific disk ceiling.
type CPUOffer struct {
	CPUFamilyID                   string
	CPUInstanceID                 string
	DataCenterID                  string
	DisplayName                   string
	VCPUCount                     int
	MemoryInGB                    int
	StockStatus                   string
	OnDemandPriceUSDMicrosPerHour USDMicrosPerHour
	PriceSource                   string
}
type CPUVCPURange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}
type CPUPrices struct {
	SecurePerVCPU     *USDMicrosPerHour `json:"securePerVcpu"`
	ServerlessPerVCPU *USDMicrosPerHour `json:"serverlessPerVcpu"`
}
type CPUType struct {
	ID           string                          `json:"id"`
	Name         string                          `json:"name"`
	Group        string                          `json:"group"`
	VCPU         CPUVCPURange                    `json:"vcpu"`
	RAMGBPerVCPU json.Number                     `json:"ramGbPerVcpu"`
	Price        CPUPrices                       `json:"price"`
	Availability string                          `json:"availability,omitempty"`
	DataCenters  []CatalogDataCenterAvailability `json:"dataCenters,omitempty"`
}

func validateCPUOfferRequest(r CPUOfferRequest) error {
	m := cpuInstanceIDPattern.FindStringSubmatch(r.InstanceID)
	if m == nil {
		return NewValidationError("instanceId", "must use family-vCPU-RAM format")
	}
	count, err := strconv.Atoi(m[2])
	if err != nil || count < 2 || count&(count-1) != 0 {
		return NewValidationError("vcpuCount", "must be a power of two of at least two")
	}
	if _, err := strconv.Atoi(m[3]); err != nil {
		return NewValidationError("instanceId", "memory overflows")
	}
	if r.DataCenterID == "" || r.DataCenterID != strings.TrimSpace(r.DataCenterID) {
		return NewValidationError("dataCenterId", "must be an exact non-empty id")
	}
	return nil
}
func (c *Client) getCPUOffer(ctx context.Context, r CPUOfferRequest, read catalogReader) (CPUOffer, error) {
	var out CPUOffer
	if err := validateCPUOfferRequest(r); err != nil {
		return out, err
	}
	m := cpuInstanceIDPattern.FindStringSubmatch(r.InstanceID)
	count, _ := strconv.Atoi(m[2])
	memory, _ := strconv.Atoi(m[3])
	family := m[1]
	q := url.Values{"include": {"AVAILABILITY"}, "product": {"POD"}, "vcpuCount": {strconv.Itoa(count)}}
	var flavor CPUType
	if err := read(ctx, "/catalog/cpus/"+url.PathEscape(family)+"?"+q.Encode(), &flavor); err != nil {
		return out, err
	}
	if flavor.ID != family || flavor.Name == "" {
		return out, fmt.Errorf("RunPod returned incomplete or foreign CPU flavor %q", flavor.ID)
	}
	if flavor.VCPU.Min <= 0 || flavor.VCPU.Max < flavor.VCPU.Min || count < flavor.VCPU.Min || count > flavor.VCPU.Max {
		return out, fmt.Errorf("RunPod CPU flavor %s does not admit %d vCPU", family, count)
	}
	ratio, ok := new(big.Rat).SetString(flavor.RAMGBPerVCPU.String())
	if !ok || ratio.Sign() <= 0 {
		return out, fmt.Errorf("RunPod omitted or malformed RAM ratio for CPU flavor %s", family)
	}
	expected := new(big.Rat).Mul(ratio, big.NewRat(int64(count), 1))
	if expected.Cmp(big.NewRat(int64(memory), 1)) != 0 {
		return out, fmt.Errorf("RunPod CPU instance %s disagrees with flavor RAM ratio", r.InstanceID)
	}
	if err := validateCatalogAvailability(flavor.Availability); err != nil {
		return out, err
	}
	if flavor.Price.SecurePerVCPU == nil {
		return out, fmt.Errorf("RunPod CPU flavor %s has no secure catalog price", family)
	}
	stock := "NONE"
	seen := map[string]bool{}
	for _, dc := range flavor.DataCenters {
		if dc.ID == "" || dc.Name == "" || seen[dc.ID] {
			return out, fmt.Errorf("RunPod returned incomplete or duplicate CPU datacenter")
		}
		seen[dc.ID] = true
		if err := validateCatalogAvailability(dc.Availability); err != nil {
			return out, err
		}
		if dc.ID == r.DataCenterID {
			stock = dc.Availability
		}
	}
	if len(flavor.DataCenters) == 0 && flavor.Availability != "NONE" {
		return out, fmt.Errorf("RunPod omitted datacenters for available CPU flavor %s", family)
	}
	rate, err := checkedCatalogRate(*flavor.Price.SecurePerVCPU, count)
	if err != nil {
		return out, err
	}
	return CPUOffer{CPUFamilyID: family, CPUInstanceID: r.InstanceID, DataCenterID: r.DataCenterID, DisplayName: flavor.Name, VCPUCount: count, MemoryInGB: memory, StockStatus: stock, OnDemandPriceUSDMicrosPerHour: rate, PriceSource: CatalogListPrice}, nil
}
func (c *Client) GetCPUOffer(ctx context.Context, r CPUOfferRequest) (CPUOffer, error) {
	return c.getCPUOffer(ctx, r, c.Get)
}
