package runpod

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

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
	if err := c.validateCreatePodRequest(req); err != nil {
		return nil, err
	}

	var pod Pod
	err := c.Post(ctx, "/pods", req, &pod)
	if err != nil {
		return nil, fmt.Errorf("failed to create pod: %w", classifyCreatePodError(err, req))
	}

	pod.normalize()
	return &pod, nil
}

// CreateSpotPod creates a spot/interruptible pod. The caller's request is
// not mutated. Set req.BidPerGPU to bid above the market floor (see
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
	endpoint := c.buildListURL("/pods", opts)

	// RunPod has returned multiple shapes for this endpoint over time:
	// - {"pods":[...]}
	// - [...]
	//
	// Be permissive so higher-level schedulers can reliably enforce max_workers / pod counts.
	var raw json.RawMessage
	if err := c.Get(ctx, endpoint, &raw); err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}

	// Prefer object wrapper (documented shape).
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
	if req.BidPerGPU != 0 {
		if err := c.validatePositiveFloat("bidPerGpu", req.BidPerGPU); err != nil {
			return err
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
