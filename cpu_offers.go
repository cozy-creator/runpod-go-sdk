package runpod

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var cpuInstanceIDPattern = regexp.MustCompile(`^(cpu[0-9]+[cgm])-([1-9][0-9]*)-([1-9][0-9]*)$`)

// CPUStockStatusUnknown means RunPod returned an exact secure price but
// omitted stockStatus. Callers may attempt a create; that create remains the
// capacity authority.
const CPUStockStatusUnknown = "Unknown"

// CPUOfferRequest identifies one exact RunPod CPU instance shape in one data center.
// InstanceID uses RunPod's family-vCPU-RAM spelling, for example cpu5c-2-4.
type CPUOfferRequest struct {
	InstanceID   string
	DataCenterID string
}

// CPUOffer is provider-authoritative stock and on-demand Pod pricing for one
// exact family, instance shape, and data center.
type CPUOffer struct {
	CPUFamilyID                   string
	CPUInstanceID                 string
	DataCenterID                  string
	DisplayName                   string
	VCPUCount                     int
	MemoryInGB                    int
	StockStatus                   string
	OnDemandPriceUSDMicrosPerHour USDMicrosPerHour
}

type graphQLCPUOfferPayload struct {
	CPUFlavors []struct {
		ID            string  `json:"id"`
		DisplayName   string  `json:"displayName"`
		MinVCPU       float64 `json:"minVcpu"`
		MaxVCPU       int     `json:"maxVcpu"`
		RAMMultiplier float64 `json:"ramMultiplier"`
		Specifics     *struct {
			StockStatus string            `json:"stockStatus"`
			SecurePrice *USDMicrosPerHour `json:"securePrice"`
		} `json:"specifics"`
	} `json:"cpuFlavors"`
}

// GetCPUOffer asks RunPod's cpuFlavors.specifics surface for one exact
// family/instance/data-center quote. The provider's USD decimal is decoded
// directly to integer micros; sub-micro and overflowing rates refuse.
func (c *Client) GetCPUOffer(ctx context.Context, request CPUOfferRequest) (CPUOffer, error) {
	var out CPUOffer
	instanceID := strings.TrimSpace(request.InstanceID)
	dataCenterID := strings.TrimSpace(request.DataCenterID)
	match := cpuInstanceIDPattern.FindStringSubmatch(instanceID)
	if len(match) != 4 {
		return out, NewValidationError("instanceId", "must use the family-vCPU-RAM spelling, for example cpu5c-2-4")
	}
	familyID := match[1]
	if _, ok := CPUFamilyByID(familyID); !ok {
		return out, NewValidationErrorWithValue("instanceId", "names an unknown CPU family", instanceID)
	}
	if dataCenterID == "" {
		return out, NewValidationError("dataCenterId", "cannot be empty")
	}
	vcpu, _ := strconv.Atoi(match[2])
	memory, _ := strconv.Atoi(match[3])

	query := `
query($instanceId: String!, $dataCenterId: String!) {
  cpuFlavors {
    id
    displayName
    minVcpu
    maxVcpu
    ramMultiplier
    specifics(input: { instanceId: $instanceId, dataCenterId: $dataCenterId }) {
      stockStatus
      securePrice
    }
  }
}`
	var payload graphQLCPUOfferPayload
	if err := c.GraphQL(ctx, query, map[string]interface{}{
		"instanceId": instanceID, "dataCenterId": dataCenterID,
	}, &payload); err != nil {
		return out, fmt.Errorf("failed to quote CPU offer: %w", err)
	}

	matches := 0
	for _, flavor := range payload.CPUFlavors {
		if strings.TrimSpace(flavor.ID) != familyID {
			continue
		}
		matches++
		if flavor.Specifics == nil || flavor.Specifics.SecurePrice == nil {
			return out, fmt.Errorf("RunPod CPU offer %s in %s has no exact secure Pod price", instanceID, dataCenterID)
		}
		stock := strings.TrimSpace(flavor.Specifics.StockStatus)
		if stock == "" {
			stock = CPUStockStatusUnknown
		}
		if (flavor.MinVCPU > 0 && float64(vcpu) < flavor.MinVCPU) ||
			(flavor.MaxVCPU > 0 && vcpu > flavor.MaxVCPU) {
			return out, fmt.Errorf("RunPod CPU flavor %s does not admit %d vCPU", familyID, vcpu)
		}
		if flavor.RAMMultiplier > 0 && float64(memory) != float64(vcpu)*flavor.RAMMultiplier {
			return out, fmt.Errorf("RunPod CPU instance %s disagrees with flavor RAM multiplier", instanceID)
		}
		out = CPUOffer{
			CPUFamilyID: familyID, CPUInstanceID: instanceID, DataCenterID: dataCenterID,
			DisplayName: strings.TrimSpace(flavor.DisplayName), VCPUCount: vcpu, MemoryInGB: memory,
			StockStatus: stock, OnDemandPriceUSDMicrosPerHour: *flavor.Specifics.SecurePrice,
		}
	}
	if matches != 1 {
		return CPUOffer{}, fmt.Errorf("RunPod returned %d CPU flavor rows for %s", matches, familyID)
	}
	return out, nil
}
