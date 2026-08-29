package runpod

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
)

const podNameLookupPageSize = 100

// CreatePod creates a new RunPod instance.
//
// When req.GPUTypeIDs contains more than one entry, CreatePod fans out via
// CreatePodWithFallback: RunPod's REST API does not walk the list itself and
// returns 500 "no instances available" when the first type has no stock.
// Stock-outs surface as errors matching errors.Is(err, ErrNoCapacity).
func (c *Client) CreatePod(ctx context.Context, req *CreatePodRequest) (*Pod, error) {
	if req != nil && len(req.GPUTypeIDs) > 1 {
		return c.CreatePodWithFallback(ctx, req, req.GPUTypeIDs, nil)
	}
	return c.createPod(ctx, req)
}

// createPod performs a single POST /pods with no fan-out.
func (c *Client) createPod(ctx context.Context, req *CreatePodRequest) (*Pod, error) {
	prepared, err := c.PrepareCreatePod(req)
	if err != nil {
		return nil, err
	}
	return c.ExecuteCreatePod(ctx, prepared)
}

// PrepareCreatePod validates a single provider create and returns the exact
// JSON bytes to record before making the call. A GPU request must name exactly
// one type: fallback attempts are separate purchase obligations and must each
// be prepared and recorded separately.
func (c *Client) PrepareCreatePod(req *CreatePodRequest) ([]byte, error) {
	if err := c.validateCreatePodRequest(req); err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(req.ComputeType), "CPU") && len(req.GPUTypeIDs) != 1 {
		return nil, NewValidationError("gpuTypeIds", "must contain exactly one type for a prepared create")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("prepare pod create: %w", err)
	}
	return body, nil
}

// InspectPreparedCreatePod decodes and validates exact bytes previously
// returned by PrepareCreatePod without changing them. Durable controllers use
// the returned provider-shaped request to prove that a recorded obligation
// still names the SKU, count, datacenter, and volume they authorized.
func (c *Client) InspectPreparedCreatePod(prepared []byte) (*CreatePodRequest, error) {
	if len(prepared) == 0 {
		return nil, NewValidationError("prepared", "cannot be empty")
	}
	dec := json.NewDecoder(bytes.NewReader(prepared))
	dec.DisallowUnknownFields()
	var req CreatePodRequest
	if err := dec.Decode(&req); err != nil {
		return nil, NewValidationError("prepared", "must be a valid closed pod-create JSON object")
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, NewValidationError("prepared", "must contain exactly one pod-create JSON object")
	}
	if err := c.validateCreatePodRequest(&req); err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(req.ComputeType), "CPU") && len(req.GPUTypeIDs) != 1 {
		return nil, NewValidationError("gpuTypeIds", "must contain exactly one type for a prepared create")
	}
	return &req, nil
}

// ExecuteCreatePod sends a body returned by PrepareCreatePod without
// re-marshaling it. The bytes are validated again so corrupt or incompatible
// durable records are refused before the wire; validation never changes the
// bytes sent.
func (c *Client) ExecuteCreatePod(ctx context.Context, prepared []byte) (*Pod, error) {
	body := append([]byte(nil), prepared...)
	req, err := c.InspectPreparedCreatePod(body)
	if err != nil {
		return nil, err
	}

	var pod Pod
	err = c.postBytes(ctx, "/pods", body, &pod)
	if err != nil {
		return nil, fmt.Errorf("failed to create pod: %w", classifyCreatePodError(err, req))
	}

	pod.normalize()
	return &pod, nil
}

// CreateSpotPod creates a spot/interruptible pod. The caller's request is
// not mutated. Set req.BidPerGPUUSDMicrosPerHour above the market floor (see
// ListGPUOffers for current minimum bids); when zero RunPod bids the
// current minimum.
//
// Reclaim semantics: when RunPod preempts a spot pod (outbid or capacity
// reclaimed) the pod is stopped, not deleted — GetPod reports
// desiredStatus="EXITED" with the runtime block cleared, exactly like a
// container exit. There is no dedicated preemption signal or notice period
// in the public API; poll pod status and treat an unexpected EXITED on an
// interruptible pod as a probable reclaim.
func (c *Client) CreateSpotPod(ctx context.Context, req *CreatePodRequest) (*Pod, error) {
	if req == nil {
		return nil, NewValidationError("request", "cannot be nil")
	}
	spotReq := *req
	spotReq.Interruptible = true
	return c.CreatePod(ctx, &spotReq)
}

// GetPod retrieves a pod by ID
func (c *Client) GetPod(ctx context.Context, podID string) (*Pod, error) {
	return c.GetPodWithOptions(ctx, podID, nil)
}

// GetPodWithOptions retrieves a pod by ID with include* query options.
func (c *Client) GetPodWithOptions(ctx context.Context, podID string, opts *GetPodOptions) (*Pod, error) {
	if err := c.validateRequired("podID", podID); err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("/pods/%s", podID)
	if opts != nil {
		q := url.Values{}
		if opts.IncludeMachine {
			q.Set("includeMachine", "true")
		}
		if opts.IncludeNetworkVolume {
			q.Set("includeNetworkVolume", "true")
		}
		if opts.IncludeSavingsPlans {
			q.Set("includeSavingsPlans", "true")
		}
		if opts.IncludeTemplate {
			q.Set("includeTemplate", "true")
		}
		if opts.IncludeWorkers {
			q.Set("includeWorkers", "true")
		}
		if encoded := q.Encode(); encoded != "" {
			endpoint += "?" + encoded
		}
	}

	var pod Pod
	err := c.Get(ctx, endpoint, &pod)
	if err != nil {
		return nil, fmt.Errorf("failed to get pod %s: %w", podID, err)
	}

	pod.normalize()
	return &pod, nil
}

// ListPods lists all pods with optional filtering
func (c *Client) ListPods(ctx context.Context, opts *ListOptions) ([]*Pod, error) {
	return c.listPods(ctx, c.buildListURL("/pods", opts))
}

// FindPodsByName returns every exact provider-side match for name. RunPod pod
// names are not unique, so this method deliberately returns all records and
// makes no adoption, health, or readiness decision for the caller.
//
// The lookup walks the provider's offset pagination and refuses a page that
// adds no new pod IDs; silently looping or treating an incomplete lookup as
// absence could lead a recovery path to buy a duplicate pod.
func (c *Client) FindPodsByName(ctx context.Context, name string) ([]*Pod, error) {
	if strings.TrimSpace(name) == "" {
		return nil, NewValidationError("name", "cannot be empty")
	}

	seen := make(map[string]struct{})
	var matches []*Pod
	for offset := 0; ; {
		endpoint := c.buildURLWithParams("/pods", map[string]string{
			"name":   name,
			"limit":  strconv.Itoa(podNameLookupPageSize),
			"offset": strconv.Itoa(offset),
		})
		page, err := c.listPods(ctx, endpoint)
		if err != nil {
			return nil, fmt.Errorf("find pods by name %q: %w", name, err)
		}
		if len(page) == 0 {
			break
		}

		advanced := false
		for _, pod := range page {
			if pod == nil || strings.TrimSpace(pod.ID) == "" {
				return nil, fmt.Errorf("find pods by name %q: provider response omitted a pod id", name)
			}
			if _, ok := seen[pod.ID]; ok {
				continue
			}
			seen[pod.ID] = struct{}{}
			advanced = true
			if pod.Name == name {
				matches = append(matches, pod)
			}
		}
		if len(page) < podNameLookupPageSize {
			break
		}
		if !advanced {
			return nil, fmt.Errorf("find pods by name %q: pagination did not advance at offset %d", name, offset)
		}
		offset += len(page)
	}

	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	return matches, nil
}

func (c *Client) listPods(ctx context.Context, endpoint string) ([]*Pod, error) {

	// RunPod has returned multiple shapes for this endpoint over time:
	// - [...] (current documented shape)
	// - {"pods":[...]} (legacy compatibility shape)
	//
	// Be permissive so higher-level schedulers can reliably enforce max_workers / pod counts.
	var raw json.RawMessage
	if err := c.Get(ctx, endpoint, &raw); err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}

	// Retain the legacy object wrapper before decoding the current bare array.
	var wrapped struct {
		Pods []*Pod `json:"pods"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.Pods != nil {
		normalizePods(wrapped.Pods)
		return wrapped.Pods, nil
	}

	// Fallback: bare array.
	var pods []*Pod
	if err := json.Unmarshal(raw, &pods); err == nil {
		normalizePods(pods)
		return pods, nil
	}

	return nil, fmt.Errorf("failed to list pods: unexpected response shape")
}

// StopPod stops a running pod
func (c *Client) StopPod(ctx context.Context, podID string) error {
	if err := c.validateRequired("podID", podID); err != nil {
		return err
	}

	endpoint := fmt.Sprintf("/pods/%s/stop", podID)
	err := c.Post(ctx, endpoint, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to stop pod %s: %w", podID, err)
	}

	return nil
}

// ResumePod resumes a stopped pod
func (c *Client) ResumePod(ctx context.Context, podID string) (*Pod, error) {
	if err := c.validateRequired("podID", podID); err != nil {
		return nil, err
	}

	var pod Pod
	endpoint := fmt.Sprintf("/pods/%s/resume", podID)
	err := c.Post(ctx, endpoint, nil, &pod)
	if err != nil {
		return nil, fmt.Errorf("failed to resume pod %s: %w", podID, err)
	}

	pod.normalize()
	return &pod, nil
}

// TerminatePod terminates/deletes a pod
func (c *Client) TerminatePod(ctx context.Context, podID string) error {
	if err := c.validateRequired("podID", podID); err != nil {
		return err
	}

	endpoint := fmt.Sprintf("/pods/%s", podID)
	err := c.Delete(ctx, endpoint)
	if err != nil {
		return fmt.Errorf("failed to terminate pod %s: %w", podID, err)
	}

	return nil
}

// validateCreatePodRequest validates a pod creation request. The GPU-only
// fields (gpuTypeIds, gpuCount) are required when ComputeType="GPU" or empty
// (the SDK historically defaulted to GPU); for CPU pods (ComputeType="CPU")
// they are forbidden — RunPod's REST API rejects unknown fields outright, so
// sending a zeroed gpuCount on a CPU request would fail. CPU placement allows
// an optional cpuFlavorIds list to constrain which CPU family to land on, but
// the list is not required — RunPod auto-picks the cheapest available flavor
// when omitted.
func (c *Client) validateCreatePodRequest(req *CreatePodRequest) error {
	if req == nil {
		return NewValidationError("request", "cannot be nil")
	}

	// Required fields
	if err := c.validateRequired("name", req.Name); err != nil {
		return err
	}
	if err := c.validateRequired("imageName", req.ImageName); err != nil {
		return err
	}

	// Compute-class-specific selector validation.
	isCPU := strings.EqualFold(strings.TrimSpace(req.ComputeType), "CPU")
	if isCPU {
		if len(req.GPUTypeIDs) > 0 {
			return NewValidationError("gpuTypeIds", "must not be set when computeType is CPU")
		}
		if req.GPUCount > 0 {
			return NewValidationError("gpuCount", "must not be set when computeType is CPU")
		}
		if req.MinRAMPerGPU != 0 {
			return NewValidationError("minRAMPerGPU", "must not be set when computeType is CPU")
		}
		if req.MinVCPUPerGPU != 0 {
			return NewValidationError("minVCPUPerGPU", "must not be set when computeType is CPU")
		}
		switch req.CPUFlavorPriority {
		case "", CPUFlavorPriorityAvailability:
		case CPUFlavorPriorityCustom:
			if len(req.CPUFlavorIDs) == 0 {
				return NewValidationError("cpuFlavorIds", "must not be empty when cpuFlavorPriority is custom")
			}
		default:
			return NewValidationErrorWithValue("cpuFlavorPriority", "must be either 'availability' or 'custom'", req.CPUFlavorPriority)
		}
	} else {
		// GPU is the historical default; require the GPU selector + count.
		if err := c.validateRequired("gpuTypeId", req.GPUTypeIDs); err != nil {
			return err
		}
		if err := c.validatePositive("gpuCount", req.GPUCount); err != nil {
			return err
		}
		if len(req.CPUFlavorIDs) > 0 {
			return NewValidationError("cpuFlavorIds", "must not be set unless computeType is CPU")
		}
		if req.CPUFlavorPriority != "" {
			return NewValidationError("cpuFlavorPriority", "must not be set unless computeType is CPU")
		}
		if req.MinRAMPerGPU != 0 {
			if err := c.validatePositive("minRAMPerGPU", req.MinRAMPerGPU); err != nil {
				return err
			}
		}
		if req.MinVCPUPerGPU != 0 {
			if err := c.validatePositive("minVCPUPerGPU", req.MinVCPUPerGPU); err != nil {
				return err
			}
		}
	}

	if err := c.validatePositive("containerDiskInGb", req.ContainerDiskInGB); err != nil {
		return err
	}

	// Optional positive values
	if req.VCPUCount > 0 {
		if err := c.validatePositive("vcpuCount", req.VCPUCount); err != nil {
			return err
		}
	}
	if req.VolumeInGB > 0 {
		if err := c.validatePositive("volumeInGb", req.VolumeInGB); err != nil {
			return err
		}
	}

	// Bid prices only make sense on interruptible (spot) pods.
	if req.BidPerGPUUSDMicrosPerHour != 0 {
		if req.BidPerGPUUSDMicrosPerHour < 0 {
			return NewValidationErrorWithValue("bidPerGpu", "must be positive", req.BidPerGPUUSDMicrosPerHour)
		}
		if !req.Interruptible {
			return NewValidationError("bidPerGpu", "requires interruptible=true (spot pods)")
		}
	}

	// Validate cloud type
	if req.CloudType != "" {
		validCloudTypes := []string{"SECURE", "COMMUNITY"}
		isValid := false
		for _, validType := range validCloudTypes {
			if req.CloudType == validType {
				isValid = true
				break
			}
		}
		if !isValid {
			return NewValidationErrorWithValue("cloudType", "must be either 'SECURE' or 'COMMUNITY'", req.CloudType)
		}
	}

	// Network volumes are a create-time, datacenter-local Secure Cloud
	// attachment. Keep the three provider constraints inseparable so callers
	// cannot accidentally ask RunPod to attach a volume while still allowing
	// placement in another datacenter or in Community Cloud.
	if strings.TrimSpace(req.NetworkVolumeID) != "" {
		if strings.EqualFold(strings.TrimSpace(req.CloudType), "COMMUNITY") {
			return NewValidationError("cloudType", "must be SECURE when networkVolumeId is set")
		}
		if len(req.DataCenterIDs) != 1 || strings.TrimSpace(req.DataCenterIDs[0]) == "" {
			return NewValidationError("dataCenterIds", "must contain exactly the network volume datacenter")
		}
		if req.VolumeInGB != 0 {
			return NewValidationError("volumeInGb", "must be omitted when networkVolumeId is set")
		}
		if mount := strings.TrimSpace(req.VolumeMountPath); mount != "" && !path.IsAbs(mount) {
			return NewValidationError("volumeMountPath", "must be an absolute POSIX container path")
		}
	}

	// Validate compute type
	if req.ComputeType != "" {
		validComputeTypes := []string{"GPU", "CPU"}
		isValid := false
		for _, validType := range validComputeTypes {
			if req.ComputeType == validType {
				isValid = true
				break
			}
		}
		if !isValid {
			return NewValidationErrorWithValue("computeType", "must be either 'GPU' or 'CPU'", req.ComputeType)
		}
	}

	if strings.TrimSpace(req.MinCudaVersion) != "" && len(req.AllowedCudaVersions) > 0 {
		return NewValidationError("minCudaVersion", "cannot be set together with allowedCudaVersions")
	}

	return nil
}

// isPodInErrorState checks if a pod is in a terminal error state
func (c *Client) isPodInErrorState(status string) bool {
	errorStates := []string{"EXITED", "DEAD", "TERMINATED", "FAILED"}
	upperStatus := strings.ToUpper(status)

	for _, errorState := range errorStates {
		if upperStatus == errorState {
			return true
		}
	}

	return false
}
