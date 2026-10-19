package runpod

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Batch results follow the input order. Err preserves an alias-scoped provider
// failure; a transport, unscoped GraphQL or invalid-input failure returns at the
// method level. Empty batches send no HTTP request.
type DataCentersResult struct {
	DataCenters []DataCenter
	Err         error
}
type GPUOffersResult struct {
	Offers []GPUOffer
	Err    error
}
type CPUOfferResult struct {
	Offer CPUOffer
	Err   error
}

// ListDataCentersBatch quotes every requested width in one GraphQL request.
// The shared datacenter fields are fetched once, with one availability alias
// per filter; no width or datacenter adds an HTTP request.
func (c *Client) ListDataCentersBatch(ctx context.Context, filters []GPUAvailabilityFilter) ([]DataCentersResult, error) {
	results := make([]DataCentersResult, len(filters))
	if len(filters) == 0 {
		return results, nil
	}
	var definitions, fields []string
	variables := make(map[string]interface{}, len(filters))
	aliases := make(map[string]int, len(filters))
	for i, filter := range filters {
		if err := validateGPUAvailabilityFilter(&filter); err != nil {
			return nil, err
		}
		variable, alias := fmt.Sprintf("f%d", i), fmt.Sprintf("q%d", i)
		definitions = append(definitions, "$"+variable+": GpuAvailabilityInput")
		fields = append(fields, alias+": gpuAvailability(input:$"+variable+"){gpuTypeId displayName stockStatus available}")
		variables[variable], aliases[alias] = filter, i
	}
	query := "query(" + strings.Join(definitions, ",") + "){dataCenters{id name location storageSupport " + strings.Join(fields, " ") + "}}"
	rows, failures, err := c.inventoryBatch(ctx, query, variables, "dataCenters", aliases, len(filters))
	if err != nil {
		return nil, err
	}
	for i := range results {
		if results[i].Err = failures[i]; results[i].Err != nil {
			continue
		}
		var payload graphQLDataCenterPayload
		if err := decodeBatchProjection(rows, "dataCenters", map[string]string{"gpuAvailability": fmt.Sprintf("q%d", i)}, &payload); err != nil {
			results[i].Err = err
			continue
		}
		results[i].DataCenters = payload.DataCenters
	}
	return results, nil
}

// ListGPUOffersBatch quotes width/datacenter/cloud filters in one request.
// One gpuTypes root shares the hardware fields; each requested cloud gets one
// lowestPrice alias. CloudType empty retains the legacy two-cloud result.
func (c *Client) ListGPUOffersBatch(ctx context.Context, filters []GPUOfferFilter) ([]GPUOffersResult, error) {
	results := make([]GPUOffersResult, len(filters))
	if len(filters) == 0 {
		return results, nil
	}
	var definitions, fields []string
	variables := make(map[string]interface{})
	aliases := make(map[string]int)
	projections := make([]map[string]string, len(filters))
	for i, filter := range filters {
		if err := validateGPUOfferFilter(&filter); err != nil {
			return nil, err
		}
		prefix := fmt.Sprintf("v%d_", i)
		count := filter.GPUCount
		if count == 0 {
			count = 1
		}
		inputs := []struct {
			name, kind string
			value      interface{}
		}{
			{"gpuCount", "Int!", count}, {"minCudaVersion", "String", strings.TrimSpace(filter.MinCudaVersion)},
			{"allowedCudaVersions", "[String]", filter.AllowedCudaVersions}, {"dataCenterId", "String", strings.TrimSpace(filter.DataCenterID)},
			{"minMemoryInGb", "Int", filter.MinMemoryInGB}, {"minVcpuCount", "Int", filter.MinVCPUCount},
			{"minDisk", "Int", filter.MinDisk}, {"totalDisk", "Int", filter.TotalDisk},
			{"minDownload", "Int", filter.MinDownloadMbps}, {"minUpload", "Int", filter.MinUploadMbps}, {"supportPublicIp", "Boolean", filter.SupportPublicIP},
		}
		var arguments []string
		for _, input := range inputs {
			name := prefix + input.name
			definitions = append(definitions, "$"+name+":"+input.kind)
			arguments = append(arguments, input.name+":$"+name)
			variables[name] = input.value
		}
		projections[i] = make(map[string]string)
		for _, cloud := range []string{"SECURE", "COMMUNITY"} {
			if filter.CloudType != "" && filter.CloudType != cloud {
				continue
			}
			field := strings.ToLower(cloud)
			alias := fmt.Sprintf("q%d_%s", i, field)
			fields = append(fields, alias+":lowestPrice(input:{"+strings.Join(arguments, ",")+fmt.Sprintf(",secureCloud:%t", cloud == "SECURE")+"}){minimumBidPrice uninterruptablePrice stockStatus availableGpuCounts}")
			projections[i][field], aliases[alias] = alias, i
		}
	}
	query := "query(" + strings.Join(definitions, ",") + "){gpuTypes{id displayName memoryInGb secureCloud communityCloud " + strings.Join(fields, " ") + "}}"
	rows, failures, err := c.inventoryBatch(ctx, query, variables, "gpuTypes", aliases, len(filters))
	if err != nil {
		return nil, err
	}
	for i, filter := range filters {
		if results[i].Err = failures[i]; results[i].Err != nil {
			continue
		}
		var payload graphQLGPUOfferPayload
		if err := decodeBatchProjection(rows, "gpuTypes", projections[i], &payload); err != nil {
			results[i].Err = err
			continue
		}
		ids := make(map[string]struct{}, len(filter.IDs))
		for _, id := range filter.IDs {
			if id = strings.TrimSpace(id); id != "" {
				ids[id] = struct{}{}
			}
		}
		count := filter.GPUCount
		if count == 0 {
			count = 1
		}
		results[i].Offers, results[i].Err = decodeGPUOffers(payload, count, ids, filter.InStockOnly, filter)
	}
	return results, nil
}

// GetCPUOffersBatch quotes all configured shapes and locations in one request.
// A failed specifics alias leaves that input's Err populated while successful
// sibling quotes retain their exact provider prices and resource bounds.
func (c *Client) GetCPUOffersBatch(ctx context.Context, requests []CPUOfferRequest) ([]CPUOfferResult, error) {
	results := make([]CPUOfferResult, len(requests))
	if len(requests) == 0 {
		return results, nil
	}
	var definitions, fields []string
	variables := make(map[string]interface{}, 2*len(requests))
	aliases := make(map[string]int, len(requests))
	for i, request := range requests {
		if err := validateCPUOfferRequest(request); err != nil {
			return nil, err
		}
		instance, center, alias := fmt.Sprintf("instanceId%d", i), fmt.Sprintf("dataCenterId%d", i), fmt.Sprintf("q%d", i)
		definitions = append(definitions, "$"+instance+":String!", "$"+center+":String!")
		fields = append(fields, alias+":specifics(input:{instanceId:$"+instance+",dataCenterId:$"+center+"}){stockStatus securePrice}")
		variables[instance], variables[center], aliases[alias] = strings.TrimSpace(request.InstanceID), strings.TrimSpace(request.DataCenterID), i
	}
	query := "query(" + strings.Join(definitions, ",") + "){cpuFlavors{id displayName minVcpu maxVcpu ramMultiplier diskLimitPerVcpu " + strings.Join(fields, " ") + "}}"
	rows, failures, err := c.inventoryBatch(ctx, query, variables, "cpuFlavors", aliases, len(requests))
	if err != nil {
		return nil, err
	}
	for i, request := range requests {
		if results[i].Err = failures[i]; results[i].Err != nil {
			continue
		}
		var payload graphQLCPUOfferPayload
		if err := decodeBatchProjection(rows, "cpuFlavors", map[string]string{"specifics": fmt.Sprintf("q%d", i)}, &payload); err != nil {
			results[i].Err = err
			continue
		}
		results[i].Offer, results[i].Err = decodeCPUOffer(payload, request)
	}
	return results, nil
}

// inventoryBatch preserves partial errors only when their path identifies a
// requested alias. Errors outside those fields refuse the whole observation.
func (c *Client) inventoryBatch(ctx context.Context, query string, variables map[string]interface{}, root string, aliases map[string]int, count int) ([]map[string]json.RawMessage, []error, error) {
	envelope, err := c.graphQL(ctx, query, variables)
	if err != nil {
		return nil, nil, err
	}
	grouped := make([][]GraphQLError, count)
	for _, failure := range envelope.Errors {
		index := -1
		for _, part := range failure.Path {
			if name, ok := part.(string); ok {
				if found, known := aliases[name]; known {
					index = found
					break
				}
			}
		}
		if index < 0 {
			return nil, nil, &GraphQLResponseError{Errors: envelope.Errors}
		}
		grouped[index] = append(grouped[index], failure)
	}
	failures := make([]error, count)
	for i, items := range grouped {
		if len(items) > 0 {
			failures[i] = &GraphQLResponseError{Errors: items}
		}
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		return nil, nil, fmt.Errorf("decode batch data: %w", err)
	}
	raw, ok := data[root]
	if !ok || string(raw) == "null" {
		return nil, nil, fmt.Errorf("RunPod batch response omitted %s", root)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, nil, fmt.Errorf("decode batch %s: %w", root, err)
	}
	return rows, failures, nil
}

// Adapt aliased fields back to the existing single-quote typed decoders so all
// exact money, stock, shape and overflow checks are shared by both API forms.
func decodeBatchProjection(rows []map[string]json.RawMessage, root string, aliases map[string]string, result interface{}) error {
	projected := make([]map[string]json.RawMessage, len(rows))
	for i, row := range rows {
		item := make(map[string]json.RawMessage, 16)
		for name, value := range row {
			if len(name) > 1 && name[0] == 'q' && name[1] >= '0' && name[1] <= '9' {
				continue
			}
			item[name] = value
		}
		for name, alias := range aliases {
			raw, ok := row[alias]
			if !ok {
				return fmt.Errorf("RunPod %s row%d omitted batch alias %s", root, i, alias)
			}
			item[name] = raw
		}
		projected[i] = item
	}
	raw, err := json.Marshal(map[string][]map[string]json.RawMessage{root: projected})
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, result)
}
