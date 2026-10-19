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

// InventoryRequest is one observation of all configured widths, prices and CPU shapes.
type InventoryRequest struct {
	Availability []GPUAvailabilityFilter
	GPUOffers    []GPUOfferFilter
	CPUOffers    []CPUOfferRequest
}

// InventoryResult follows the input order independently within each root.
type InventoryResult struct {
	DataCenters []DataCentersResult
	GPUOffers   []GPUOffersResult
	CPUOffers   []CPUOfferResult
}

type inventorySelection struct {
	root, fields string
	definitions  []string
	variables    map[string]interface{}
	aliases      map[string]int
	projections  []map[string]string
}

// ObserveInventory sends one GraphQL HTTP attempt for all requested roots.
// It never retries an inventory request, including429; the shared client cooldown
// still retains that refusal. The caller owns refresh cadence and next retry time.
// A wholly empty request sends no HTTP.
func (c *Client) ObserveInventory(ctx context.Context, request InventoryRequest) (InventoryResult, error) {
	result := InventoryResult{DataCenters: make([]DataCentersResult, len(request.Availability)), GPUOffers: make([]GPUOffersResult, len(request.GPUOffers)), CPUOffers: make([]CPUOfferResult, len(request.CPUOffers))}
	var selections []inventorySelection
	var err error
	if len(request.Availability) > 0 {
		var selection inventorySelection
		selection, err = availabilitySelection(request.Availability)
		if err != nil {
			return InventoryResult{}, err
		}
		selections = append(selections, selection)
	}
	if len(request.GPUOffers) > 0 {
		var selection inventorySelection
		selection, err = gpuOfferSelection(request.GPUOffers)
		if err != nil {
			return InventoryResult{}, err
		}
		selections = append(selections, selection)
	}
	if len(request.CPUOffers) > 0 {
		var selection inventorySelection
		selection, err = cpuOfferSelection(request.CPUOffers)
		if err != nil {
			return InventoryResult{}, err
		}
		selections = append(selections, selection)
	}
	if len(selections) == 0 {
		return result, nil
	}
	var definitions, roots []string
	variables := make(map[string]interface{})
	failuresByRoot := make(map[string][]GraphQLError, len(selections))
	for _, selection := range selections {
		definitions = append(definitions, selection.definitions...)
		roots = append(roots, selection.root+"{"+selection.fields+"}")
		for name, value := range selection.variables {
			variables[name] = value
		}
		failuresByRoot[selection.root] = nil
	}
	query := "query(" + strings.Join(definitions, ",") + "){" + strings.Join(roots, " ") + "}"
	envelope, err := c.graphQLWithRetryLimit(ctx, query, variables, 0)
	if err != nil {
		return InventoryResult{}, err
	}
	for _, failure := range envelope.Errors {
		if len(failure.Path) == 0 {
			return InventoryResult{}, &GraphQLResponseError{Errors: envelope.Errors}
		}
		root, ok := failure.Path[0].(string)
		if _, known := failuresByRoot[root]; !ok || !known {
			return InventoryResult{}, &GraphQLResponseError{Errors: envelope.Errors}
		}
		failuresByRoot[root] = append(failuresByRoot[root], failure)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		return InventoryResult{}, fmt.Errorf("decode batch data: %w", err)
	}
	for _, selection := range selections {
		count := len(request.Availability)
		switch selection.root {
		case "gpuTypes":
			count = len(request.GPUOffers)
		case "cpuFlavors":
			count = len(request.CPUOffers)
		}
		rows, failures, err := inventoryBatchData(data, failuresByRoot[selection.root], selection.root, selection.aliases, count)
		if err != nil {
			return InventoryResult{}, err
		}
		switch selection.root {
		case "dataCenters":
			result.DataCenters = decodeDataCentersBatch(rows, failures)
		case "gpuTypes":
			result.GPUOffers = decodeGPUOffersBatch(rows, failures, request.GPUOffers, selection.projections)
		case "cpuFlavors":
			result.CPUOffers = decodeCPUOffersBatch(rows, failures, request.CPUOffers)
		}
	}
	return result, nil
}

// ListDataCentersBatch quotes widths alone. Use ObserveInventory to combine roots.
func (c *Client) ListDataCentersBatch(ctx context.Context, filters []GPUAvailabilityFilter) ([]DataCentersResult, error) {
	result, err := c.ObserveInventory(ctx, InventoryRequest{Availability: filters})
	if err != nil {
		return nil, err
	}
	return result.DataCenters, nil
}

// ListGPUOffersBatch quotes prices alone. Use ObserveInventory to combine roots.
func (c *Client) ListGPUOffersBatch(ctx context.Context, filters []GPUOfferFilter) ([]GPUOffersResult, error) {
	result, err := c.ObserveInventory(ctx, InventoryRequest{GPUOffers: filters})
	if err != nil {
		return nil, err
	}
	return result.GPUOffers, nil
}

// GetCPUOffersBatch quotes CPU shapes alone. Use ObserveInventory to combine roots.
func (c *Client) GetCPUOffersBatch(ctx context.Context, requests []CPUOfferRequest) ([]CPUOfferResult, error) {
	result, err := c.ObserveInventory(ctx, InventoryRequest{CPUOffers: requests})
	if err != nil {
		return nil, err
	}
	return result.CPUOffers, nil
}

func availabilitySelection(filters []GPUAvailabilityFilter) (inventorySelection, error) {
	var definitions, fields []string
	variables := make(map[string]interface{}, len(filters))
	aliases := make(map[string]int, len(filters))
	for i, filter := range filters {
		if err := validateGPUAvailabilityFilter(&filter); err != nil {
			return inventorySelection{}, err
		}
		variable, alias := fmt.Sprintf("f%d", i), fmt.Sprintf("q%d", i)
		definitions = append(definitions, "$"+variable+": GpuAvailabilityInput")
		fields = append(fields, alias+": gpuAvailability(input:$"+variable+"){gpuTypeId displayName stockStatus available}")
		variables[variable], aliases[alias] = filter, i
	}
	return inventorySelection{root: "dataCenters", fields: "id name location storageSupport " + strings.Join(fields, " "), definitions: definitions, variables: variables, aliases: aliases}, nil
}
func gpuOfferSelection(filters []GPUOfferFilter) (inventorySelection, error) {
	var definitions, fields []string
	variables := make(map[string]interface{})
	aliases := make(map[string]int)
	projections := make([]map[string]string, len(filters))
	for i, filter := range filters {
		if err := validateGPUOfferFilter(&filter); err != nil {
			return inventorySelection{}, err
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
	return inventorySelection{root: "gpuTypes", fields: "id displayName memoryInGb secureCloud communityCloud " + strings.Join(fields, " "), definitions: definitions, variables: variables, aliases: aliases, projections: projections}, nil
}
func cpuOfferSelection(requests []CPUOfferRequest) (inventorySelection, error) {
	var definitions, fields []string
	variables := make(map[string]interface{}, 2*len(requests))
	aliases := make(map[string]int, len(requests))
	for i, request := range requests {
		if err := validateCPUOfferRequest(request); err != nil {
			return inventorySelection{}, err
		}
		instance, center, alias := fmt.Sprintf("instanceId%d", i), fmt.Sprintf("dataCenterId%d", i), fmt.Sprintf("q%d", i)
		definitions = append(definitions, "$"+instance+":String!", "$"+center+":String!")
		fields = append(fields, alias+":specifics(input:{instanceId:$"+instance+",dataCenterId:$"+center+"}){stockStatus securePrice}")
		variables[instance], variables[center], aliases[alias] = strings.TrimSpace(request.InstanceID), strings.TrimSpace(request.DataCenterID), i
	}
	return inventorySelection{root: "cpuFlavors", fields: "id displayName minVcpu maxVcpu ramMultiplier diskLimitPerVcpu " + strings.Join(fields, " "), definitions: definitions, variables: variables, aliases: aliases}, nil
}
func decodeDataCentersBatch(rows []map[string]json.RawMessage, failures []error) []DataCentersResult {
	results := make([]DataCentersResult, len(failures))
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
	return results
}
func decodeGPUOffersBatch(rows []map[string]json.RawMessage, failures []error, filters []GPUOfferFilter, projections []map[string]string) []GPUOffersResult {
	results := make([]GPUOffersResult, len(filters))
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
	return results
}
func decodeCPUOffersBatch(rows []map[string]json.RawMessage, failures []error, requests []CPUOfferRequest) []CPUOfferResult {
	results := make([]CPUOfferResult, len(requests))
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
	return results
}

// inventoryBatchData receives only errors for this root: q0 under census and
// q0 under CPU are distinct fields, even though their alias spellings coincide.
func inventoryBatchData(data map[string]json.RawMessage, providerErrors []GraphQLError, root string, aliases map[string]int, count int) ([]map[string]json.RawMessage, []error, error) {
	grouped := make([][]GraphQLError, count)
	for _, failure := range providerErrors {
		index := -1
		for _, part := range failure.Path[1:] {
			if name, ok := part.(string); ok {
				if found, known := aliases[name]; known {
					index = found
					break
				}
			}
		}
		if index < 0 {
			return nil, nil, &GraphQLResponseError{Errors: providerErrors}
		}
		grouped[index] = append(grouped[index], failure)
	}
	failures := make([]error, count)
	for i, items := range grouped {
		if len(items) > 0 {
			failures[i] = &GraphQLResponseError{Errors: items}
		}
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
