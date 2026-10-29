package runpod

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
)

var podCUDAVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)

// CreatePod sends one native REST v2 create. Use CreatePodWithFallback to
// explicitly try several GPU types; each attempt is a separate purchase.
func (c *Client) CreatePod(ctx context.Context, req *CreatePodRequest) (*Pod, error) {
	return c.createPod(ctx, req)
}

func (c *Client) createPod(ctx context.Context, req *CreatePodRequest) (*Pod, error) {
	body, err := c.PrepareCreatePod(req)
	if err != nil {
		return nil, err
	}
	return c.ExecuteCreatePod(ctx, body)
}

// PrepareCreatePod returns the exact native v2 body to durably record before
// purchase. Executing those bytes does not marshal or translate them again.
func (c *Client) PrepareCreatePod(req *CreatePodRequest) ([]byte, error) {
	if err := c.validateCreatePodRequest(req); err != nil {
		return nil, err
	}
	return json.Marshal(req)
}

// InspectPreparedCreatePod refuses corrupt, unknown-field, or obsolete bodies.
// Durable records must be explicitly migrated by their owner, never rewritten
// while executing an already-authorized purchase.
func (c *Client) InspectPreparedCreatePod(body []byte) (*CreatePodRequest, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var req CreatePodRequest
	if err := decoder.Decode(&req); err != nil {
		return nil, NewValidationError("prepared", "must be a closed REST v2 create object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, NewValidationError("prepared", "must contain exactly one create object")
	}
	if err := c.validateCreatePodRequest(&req); err != nil {
		return nil, err
	}
	return &req, nil
}

// ExecuteCreatePod sends the recorded body unchanged. POST is never replayed
// after an ambiguous transport or server failure.
func (c *Client) ExecuteCreatePod(ctx context.Context, prepared []byte) (*Pod, error) {
	body := append([]byte(nil), prepared...)
	req, err := c.InspectPreparedCreatePod(body)
	if err != nil {
		return nil, err
	}
	var pod Pod
	if err := c.postBytes(ctx, "/pods", body, &pod); err != nil {
		return nil, fmt.Errorf("create pod: %w", classifyCreatePodError(err, req))
	}
	if err := validatePodIdentity(&pod, ""); err != nil {
		return nil, fmt.Errorf("create pod: %w", err)
	}
	return &pod, nil
}

// GetPod fetches the full native v2 resource, including live runtime metrics.
func (c *Client) GetPod(ctx context.Context, podID string) (*Pod, error) {
	if err := validatePodID(podID); err != nil {
		return nil, err
	}
	var pod Pod
	if err := c.Get(ctx, "/pods/"+url.PathEscape(podID), &pod); err != nil {
		return nil, fmt.Errorf("get pod %s: %w", podID, err)
	}
	if err := validatePodIdentity(&pod, podID); err != nil {
		return nil, err
	}
	return &pod, nil
}

func (c *Client) UpdatePod(ctx context.Context, podID string, req *UpdatePodRequest) (*Pod, error) {
	if err := validatePodID(podID); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, NewValidationError("request", "cannot be nil")
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		return nil, NewValidationError("name", "cannot be empty")
	}
	if req.Disk != nil && *req.Disk < 1 {
		return nil, NewValidationError("disk", "must be positive")
	}
	var pod Pod
	if err := c.Patch(ctx, "/pods/"+url.PathEscape(podID), req, &pod); err != nil {
		return nil, err
	}
	if err := validatePodIdentity(&pod, podID); err != nil {
		return nil, err
	}
	return &pod, nil
}

// ListPods follows every remaining cursor page. A malformed or stalled page
// is an error: an incomplete list cannot establish resource absence.
func (c *Client) ListPods(ctx context.Context, opts *ListOptions) ([]*Pod, error) {
	options := ListOptions{}
	if opts != nil {
		options = *opts
	}
	if options.Limit < 0 || options.Limit > 1000 {
		return nil, NewValidationError("limit", "must be between 1 and 1000, or zero for the provider default")
	}
	seenCursors := map[string]bool{options.Cursor: true}
	seenIDs := map[string]bool{}
	pods := make([]*Pod, 0)
	for {
		var result struct {
			Pods       []*Pod `json:"pods"`
			Pagination *struct {
				NextCursor  json.RawMessage `json:"nextCursor"`
				HasNextPage *bool           `json:"hasNextPage"`
			} `json:"pagination"`
		}
		if err := c.Get(ctx, c.buildListURL("/pods", &options), &result); err != nil {
			return nil, fmt.Errorf("list pods: %w", err)
		}
		if result.Pods == nil || result.Pagination == nil || result.Pagination.HasNextPage == nil || len(result.Pagination.NextCursor) == 0 {
			return nil, fmt.Errorf("list pods: response omitted pods or pagination")
		}
		added := 0
		for _, pod := range result.Pods {
			if err := validatePodIdentity(pod, ""); err != nil {
				return nil, fmt.Errorf("list pods: %w", err)
			}
			if strings.TrimSpace(pod.Name) == "" {
				return nil, fmt.Errorf("list pods: pod %s omitted its name", pod.ID)
			}
			if seenIDs[pod.ID] {
				continue
			}
			seenIDs[pod.ID] = true
			added++
			pods = append(pods, pod)
		}
		var cursor *string
		if err := json.Unmarshal(result.Pagination.NextCursor, &cursor); err != nil {
			return nil, fmt.Errorf("list pods: invalid next cursor: %w", err)
		}
		if !*result.Pagination.HasNextPage {
			return pods, nil
		}
		if cursor == nil || strings.TrimSpace(*cursor) == "" || seenCursors[*cursor] || added == 0 {
			return nil, fmt.Errorf("list pods: pagination did not advance")
		}
		seenCursors[*cursor] = true
		options.Cursor = *cursor
	}
}

// FindPodsByName scans all pages and returns every exact match. Names are not
// unique and v2 does not offer a server-side name filter.
func (c *Client) FindPodsByName(ctx context.Context, name string) ([]*Pod, error) {
	if strings.TrimSpace(name) == "" {
		return nil, NewValidationError("name", "cannot be empty")
	}
	pods, err := c.ListPods(ctx, &ListOptions{Limit: 100})
	if err != nil {
		return nil, err
	}
	matches := make([]*Pod, 0)
	for _, pod := range pods {
		if pod.Name == name {
			matches = append(matches, pod)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	return matches, nil
}

func (c *Client) podAction(ctx context.Context, podID, action string) (*Pod, error) {
	if err := validatePodID(podID); err != nil {
		return nil, err
	}
	var pod Pod
	if err := c.Post(ctx, "/pods/"+url.PathEscape(podID)+"/action", map[string]string{"action": action}, &pod); err != nil {
		return nil, err
	}
	if err := validatePodIdentity(&pod, podID); err != nil {
		return nil, err
	}
	return &pod, nil
}

func (c *Client) StopPod(ctx context.Context, podID string) error {
	_, err := c.podAction(ctx, podID, "stop")
	return err
}
func (c *Client) ResumePod(ctx context.Context, podID string) (*Pod, error) {
	return c.podAction(ctx, podID, "start")
}
func (c *Client) RestartPod(ctx context.Context, podID string) (*Pod, error) {
	return c.podAction(ctx, podID, "restart")
}
func (c *Client) TerminatePod(ctx context.Context, podID string) error {
	if err := validatePodID(podID); err != nil {
		return err
	}
	return c.Delete(ctx, "/pods/"+url.PathEscape(podID))
}

func validatePodID(id string) error {
	if id == "" || id != strings.TrimSpace(id) {
		return NewValidationError("podID", "must be a nonempty exact pod id")
	}
	return nil
}

func validatePodIdentity(pod *Pod, requested string) error {
	if pod == nil || pod.ID == "" || pod.ID != strings.TrimSpace(pod.ID) {
		return fmt.Errorf("pod response omitted an exact id")
	}
	if requested != "" && pod.ID != requested {
		return fmt.Errorf("pod identity mismatch: requested %q, received %q", requested, pod.ID)
	}
	switch pod.Status {
	case "PROVISIONING", "STARTING", "RUNNING", "EXITED", "ERROR", "TERMINATED":
	default:
		return fmt.Errorf("pod %s has missing or unknown status %q", pod.ID, pod.Status)
	}
	return nil
}

func (c *Client) validateCreatePodRequest(req *CreatePodRequest) error {
	if req == nil {
		return NewValidationError("request", "cannot be nil")
	}
	if strings.TrimSpace(req.Name) == "" {
		return NewValidationError("name", "cannot be empty")
	}
	if strings.TrimSpace(req.Image) == "" && strings.TrimSpace(req.TemplateID) == "" {
		return NewValidationError("image", "image or templateId is required")
	}
	if (req.GPU == nil) == (req.CPU == nil) {
		return NewValidationError("gpu/cpu", "exactly one compute configuration is required")
	}
	if req.Disk < 0 {
		return NewValidationError("disk", "cannot be negative")
	}
	if req.Cloud != "" && req.Cloud != "SECURE" && req.Cloud != "COMMUNITY" {
		return NewValidationError("cloud", "must be SECURE or COMMUNITY")
	}
	if req.GPU != nil {
		gpu := req.GPU
		if strings.TrimSpace(gpu.ID) == "" {
			return NewValidationError("gpu.id", "cannot be empty")
		}
		if gpu.Count < 0 {
			return NewValidationError("gpu.count", "must be positive or omitted")
		}
		if gpu.MinRAMPerGPU < 0 || gpu.MinVCPUCountPerGPU < 0 {
			return NewValidationError("gpu", "resource floors cannot be negative")
		}
		if gpu.MinCUDAVersion != "" && !podCUDAVersionPattern.MatchString(gpu.MinCUDAVersion) {
			return NewValidationError("gpu.minCudaVersion", "must use major.minor")
		}
		if gpu.MinCUDAVersion != "" && len(gpu.AllowedCUDAVersions) > 0 {
			return NewValidationError("gpu", "allowedCudaVersions and minCudaVersion are mutually exclusive")
		}
		for _, version := range gpu.AllowedCUDAVersions {
			if !podCUDAVersionPattern.MatchString(version) {
				return NewValidationError("gpu.allowedCudaVersions", "must use major.minor")
			}
		}
	}
	if req.CPU != nil {
		if strings.TrimSpace(req.CPU.ID) == "" {
			return NewValidationError("cpu.id", "cannot be empty")
		}
		n := req.CPU.VCPUCount
		if n < 2 || n&(n-1) != 0 {
			return NewValidationError("cpu.vcpuCount", "must be a power of two of at least 2")
		}
		if req.Cloud == "COMMUNITY" {
			return NewValidationError("cloud", "CPU pods require SECURE cloud")
		}
	}
	if req.Mounts != nil {
		mounts := req.Mounts
		if mounts.Persistent != nil {
			if req.CPU != nil || len(mounts.Network)+len(mounts.Global) > 0 {
				return NewValidationError("mounts.persistent", "cannot be combined with CPU, network, or global mounts")
			}
			if mounts.Persistent.Size < 10 || !path.IsAbs(mounts.Persistent.Path) {
				return NewValidationError("mounts.persistent", "requires at least 10 GB and an absolute path")
			}
		}
		if len(mounts.Network) > 1 || len(mounts.Global) > 1 {
			return NewValidationError("mounts", "at most one network and one global volume are supported")
		}
		for _, mount := range append(append([]VolumeMount(nil), mounts.Network...), mounts.Global...) {
			if strings.TrimSpace(mount.VolumeID) == "" || !path.IsAbs(mount.Path) {
				return NewValidationError("mounts", "volume mounts require an id and absolute path")
			}
		}
		if len(mounts.Network) > 0 && req.Cloud == "COMMUNITY" {
			return NewValidationError("cloud", "network volumes require SECURE cloud")
		}
	}
	return nil
}

func (c *Client) isPodInErrorState(status string) bool { return isTerminalPodStatus(status) }
