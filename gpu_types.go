package runpod

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// GPUPrices are catalog list rates for one GPU per hour. Nil means unavailable,
// not free; negotiated discounts and the pod's actual cost are separate facts.
type GPUPrices struct {
	Secure     *USDMicrosPerHour `json:"secure"`
	Community  *USDMicrosPerHour `json:"community"`
	Serverless *USDMicrosPerHour `json:"serverless,omitempty"`
}

// GPUCounts contains machine-size ceilings, never current stock counts.
type GPUCounts struct {
	Secure    int `json:"secure"`
	Community int `json:"community"`
}
type CatalogDataCenterAvailability struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Availability string `json:"availability"`
}
type CUDAVersionAvailability struct {
	Version   string `json:"version"`
	Available bool   `json:"available"`
}

// GPUType is the REST v2 catalog representation. Availability is scoped to the
// product, GPU count, cloud and CUDA filters of its request.
type GPUType struct {
	ID           string                          `json:"id"`
	Name         string                          `json:"name"`
	Pool         *string                         `json:"pool"`
	Manufacturer string                          `json:"manufacturer"`
	Memory       int                             `json:"memory"`
	Secure       bool                            `json:"secure"`
	Community    bool                            `json:"community"`
	Price        GPUPrices                       `json:"price"`
	MaxCount     GPUCounts                       `json:"maxCount"`
	Availability string                          `json:"availability,omitempty"`
	DataCenters  []CatalogDataCenterAvailability `json:"dataCenters,omitempty"`
	CUDAVersions []CUDAVersionAvailability       `json:"cudaVersions,omitempty"`
}

// UnmarshalJSON requires cloud support flags rather than treating an omitted
// flag as evidence that the cloud cannot serve this GPU.
func (g *GPUType) UnmarshalJSON(raw []byte) error {
	type native GPUType
	var decoded native
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	var required struct {
		Secure    *bool `json:"secure"`
		Community *bool `json:"community"`
	}
	if err := json.Unmarshal(raw, &required); err != nil {
		return err
	}
	if required.Secure == nil || required.Community == nil {
		return fmt.Errorf("RunPod omitted GPU cloud support flags")
	}
	*g = GPUType(decoded)
	return nil
}

// GPUTypeFilter scopes Pod availability. CloudType defaults to SECURE, matching
// the provider; ListGPUOffers explicitly reads both clouds when it is empty.
type GPUTypeFilter struct {
	IDs                 []string
	GPUCount            int
	CloudType           string
	MinCudaVersion      string
	AllowedCudaVersions []string
	CountryCodes        []string
}

type GPUTypeWithAvailability struct {
	GPUType
	StockStatus string
}
type catalogReader func(context.Context, string, any) error

var cudaVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
var countryCodePattern = regexp.MustCompile(`^[A-Z]{2}$`)

func catalogList[T any](ctx context.Context, read catalogReader, endpoint, key string) ([]T, error) {
	var raw json.RawMessage
	if err := read(ctx, endpoint, &raw); err != nil {
		return nil, err
	}
	var wrapped map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, fmt.Errorf("decode catalog %s: %w", key, err)
	}
	list, ok := wrapped[key]
	if !ok || len(list) == 0 || string(list) == "null" {
		return nil, fmt.Errorf("RunPod omitted %s", key)
	}
	var out []T
	if err := json.Unmarshal(list, &out); err != nil {
		return nil, fmt.Errorf("decode catalog %s: %w", key, err)
	}
	if out == nil {
		return nil, fmt.Errorf("RunPod returned null %s", key)
	}
	return out, nil
}

func gpuCatalogQuery(filter *GPUTypeFilter) (string, error) {
	if filter == nil {
		filter = &GPUTypeFilter{}
	}
	for _, id := range filter.IDs {
		if id == "" || id != strings.TrimSpace(id) {
			return "", NewValidationError("ids", "must contain exact non-empty GPU type ids")
		}
	}
	if filter.GPUCount < 0 {
		return "", NewValidationError("gpuCount", "cannot be negative")
	}
	if filter.CloudType != "" && filter.CloudType != "SECURE" && filter.CloudType != "COMMUNITY" {
		return "", NewValidationError("cloudType", "must be SECURE or COMMUNITY")
	}
	if err := validateCUDAFilters(filter.MinCudaVersion, filter.AllowedCudaVersions); err != nil {
		return "", err
	}
	q := url.Values{"include": {"AVAILABILITY"}, "product": {"POD"}, "count": {strconv.Itoa(max(1, filter.GPUCount))}, "cloud": {"SECURE"}}
	if filter.CloudType != "" {
		q.Set("cloud", filter.CloudType)
	}
	if filter.MinCudaVersion != "" {
		q.Set("minCudaVersion", filter.MinCudaVersion)
	}
	if len(filter.AllowedCudaVersions) > 0 {
		q.Set("cudaVersions", strings.Join(filter.AllowedCudaVersions, ","))
	}
	for _, country := range filter.CountryCodes {
		if !countryCodePattern.MatchString(country) {
			return "", NewValidationError("countryCodes", "must be uppercase ISO country codes")
		}
	}
	if len(filter.CountryCodes) > 0 {
		q.Set("countryCodes", strings.Join(filter.CountryCodes, ","))
	}
	return q.Encode(), nil
}
func validateCUDAFilters(floor string, allowed []string) error {
	if floor != "" && !cudaVersionPattern.MatchString(floor) {
		return NewValidationError("minCudaVersion", "must have major.minor format")
	}
	if floor != "" && len(allowed) > 0 {
		return NewValidationError("cudaVersions", "cannot combine allowed versions and a minimum version")
	}
	for _, v := range allowed {
		if !cudaVersionPattern.MatchString(v) {
			return NewValidationError("cudaVersions", "must have major.minor format")
		}
	}
	return nil
}
func validateCatalogAvailability(status string) error {
	switch status {
	case "NONE", "LOW", "MEDIUM", "HIGH":
		return nil
	default:
		return fmt.Errorf("RunPod returned missing or unknown availability %q", status)
	}
}
func validateGPUCatalog(items []GPUType) error {
	seen := map[string]bool{}
	for _, item := range items {
		if item.ID == "" || item.Name == "" || item.Memory <= 0 {
			return fmt.Errorf("RunPod returned incomplete GPU catalog row")
		}
		if seen[item.ID] {
			return fmt.Errorf("RunPod returned duplicate GPU catalog id %q", item.ID)
		}
		seen[item.ID] = true
		if err := validateCatalogAvailability(item.Availability); err != nil {
			return err
		}
		dcSeen := map[string]bool{}
		for _, dc := range item.DataCenters {
			if dc.ID == "" || dc.Name == "" || dcSeen[dc.ID] {
				return fmt.Errorf("RunPod returned incomplete or duplicate GPU datacenter")
			}
			dcSeen[dc.ID] = true
			if err := validateCatalogAvailability(dc.Availability); err != nil {
				return err
			}
		}
	}
	return nil
}
func (c *Client) listGPUTypes(ctx context.Context, filter *GPUTypeFilter, read catalogReader) ([]GPUType, error) {
	query, err := gpuCatalogQuery(filter)
	if err != nil {
		return nil, err
	}
	items, err := catalogList[GPUType](ctx, read, "/catalog/gpus?"+query, "gpus")
	if err != nil {
		return nil, fmt.Errorf("list GPU types: %w", err)
	}
	if err := validateGPUCatalog(items); err != nil {
		return nil, err
	}
	if filter == nil || len(filter.IDs) == 0 {
		return items, nil
	}
	ids := map[string]bool{}
	for _, id := range filter.IDs {
		ids[id] = true
	}
	out := make([]GPUType, 0, len(items))
	for _, item := range items {
		if ids[item.ID] {
			out = append(out, item)
		}
	}
	return out, nil
}
func (c *Client) ListGPUTypes(ctx context.Context, filter *GPUTypeFilter) ([]GPUType, error) {
	return c.listGPUTypes(ctx, filter, c.Get)
}
func (c *Client) GetGPUType(ctx context.Context, id string) (*GPUType, error) {
	if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) {
		return nil, NewValidationError("gpuTypeID", "must be an exact non-empty id")
	}
	query, _ := gpuCatalogQuery(nil)
	var item GPUType
	if err := c.Get(ctx, "/catalog/gpus/"+url.PathEscape(id)+"?"+query, &item); err != nil {
		return nil, err
	}
	if item.ID != id {
		return nil, fmt.Errorf("RunPod returned a foreign GPU catalog id %q", item.ID)
	}
	if err := validateGPUCatalog([]GPUType{item}); err != nil {
		return nil, err
	}
	return &item, nil
}
func (c *Client) ListAvailableGPUs(ctx context.Context, minCUDA string, count int) ([]GPUTypeWithAvailability, error) {
	items, err := c.ListGPUTypes(ctx, &GPUTypeFilter{MinCudaVersion: minCUDA, GPUCount: count})
	if err != nil {
		return nil, err
	}
	out := make([]GPUTypeWithAvailability, 0, len(items))
	for _, gpu := range items {
		if isAvailableStockStatus(gpu.Availability) && gpu.Price.Secure != nil {
			out = append(out, GPUTypeWithAvailability{GPUType: gpu, StockStatus: gpu.Availability})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return *out[i].Price.Secure < *out[j].Price.Secure })
	return out, nil
}
func isAvailableStockStatus(status string) bool {
	return status == "LOW" || status == "MEDIUM" || status == "HIGH"
}
