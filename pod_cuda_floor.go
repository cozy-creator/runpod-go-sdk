package runpod

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

// GraphQL exposes both the CUDA floor and the network/public-IP placement
// filters. REST v1 does not document a Pod CUDA floor; REST v2 does not expose
// those network filters. Persist the provider's complete GraphQL request, not
// an SDK envelope: replay selects transport from these recorded bytes and sends
// them unchanged. Existing REST obligations keep their original route.
const cudaFloorPodMutation = `mutation CreatePodWithCUDAFloor($input: PodFindAndDeployOnDemandInput!) { podFindAndDeployOnDemand(input: $input) { id } }`

var cudaFloorPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)

type podEnvironmentVariable struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type cudaFloorPodInput struct {
	Name                    string                   `json:"name"`
	ImageName               string                   `json:"imageName"`
	CloudType               string                   `json:"cloudType"`
	GPUTypeID               string                   `json:"gpuTypeId"`
	GPUCount                int                      `json:"gpuCount"`
	ContainerDiskInGB       int                      `json:"containerDiskInGb"`
	VolumeInGB              int                      `json:"volumeInGb"`
	VolumeMountPath         string                   `json:"volumeMountPath,omitempty"`
	DataCenterID            string                   `json:"dataCenterId,omitempty"`
	Env                     []podEnvironmentVariable `json:"env,omitempty"`
	Ports                   string                   `json:"ports,omitempty"`
	DockerArgs              string                   `json:"dockerArgs,omitempty"`
	NetworkVolumeID         string                   `json:"networkVolumeId,omitempty"`
	ContainerRegistryAuthID string                   `json:"containerRegistryAuthId,omitempty"`
	SupportPublicIP         bool                     `json:"supportPublicIp,omitempty"`
	MinMemoryInGB           int                      `json:"minMemoryInGb,omitempty"`
	MinVCPUCount            int                      `json:"minVcpuCount,omitempty"`
	MinDownload             int                      `json:"minDownload,omitempty"`
	MinUpload               int                      `json:"minUpload,omitempty"`
	MinCudaVersion          string                   `json:"minCudaVersion"`
}

type cudaFloorPodRequest struct {
	Query     string `json:"query"`
	Variables struct {
		Input cudaFloorPodInput `json:"input"`
	} `json:"variables"`
}

func isGraphQLPodCreate(raw []byte) bool {
	var keys map[string]json.RawMessage
	if json.Unmarshal(raw, &keys) != nil {
		return false
	}
	_, found := keys["query"]
	return found
}

func prepareCUDAFloorPod(req *CreatePodRequest) ([]byte, error) {
	// This is a narrow on-demand GPU lane. Never silently discard a REST-only
	// selector or container override during translation.
	if req.ComputeType != "" && req.ComputeType != "GPU" || req.CloudType == "" ||
		req.Interruptible || req.BidPerGPUUSDMicrosPerHour != 0 ||
		len(req.CPUFlavorIDs) != 0 || req.CPUFlavorPriority != "" || req.VCPUCount != 0 ||
		len(req.DataCenterIDs) > 1 || req.TemplateID != "" ||
		len(req.DockerEntrypoint) != 0 || len(req.DockerStartCmd) != 0 ||
		req.GPUTypePriority != "" || req.DataCenterPriority != "" ||
		len(req.AllowedCudaVersions) != 0 || !cudaFloorPattern.MatchString(req.MinCudaVersion) {
		return nil, NewValidationError("minCudaVersion", "requires an on-demand GPU request with an explicit cloud, one datacenter at most, no template or REST-only overrides, and a major.minor floor")
	}
	for _, port := range req.Ports {
		if port == "" || strings.Contains(port, ",") {
			return nil, NewValidationError("ports", "CUDA floor creation requires individual nonempty port entries")
		}
	}
	input := cudaFloorPodInput{
		Name: req.Name, ImageName: req.ImageName, CloudType: req.CloudType,
		GPUTypeID: req.GPUTypeIDs[0], GPUCount: req.GPUCount,
		ContainerDiskInGB: req.ContainerDiskInGB, VolumeInGB: req.VolumeInGB,
		VolumeMountPath: req.VolumeMountPath, Ports: strings.Join(req.Ports, ","),
		DockerArgs: req.DockerArgs, NetworkVolumeID: req.NetworkVolumeID,
		ContainerRegistryAuthID: req.ContainerRegistryAuthId, SupportPublicIP: req.SupportPublicIP,
		MinMemoryInGB: req.MinRAMPerGPU, MinVCPUCount: req.MinVCPUPerGPU,
		MinDownload: req.MinDownloadMbps, MinUpload: req.MinUploadMbps, MinCudaVersion: req.MinCudaVersion,
	}
	if len(req.DataCenterIDs) == 1 {
		input.DataCenterID = req.DataCenterIDs[0]
	}
	keys := make([]string, 0, len(req.Env))
	for key := range req.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		input.Env = append(input.Env, podEnvironmentVariable{key, req.Env[key]})
	}
	body := cudaFloorPodRequest{Query: cudaFloorPodMutation}
	body.Variables.Input = input
	return json.Marshal(body)
}

func (c *Client) inspectCUDAFloorPod(raw []byte) (*CreatePodRequest, error) {
	var body cudaFloorPodRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		return nil, NewValidationError("prepared", "invalid closed CUDA-floor create object")
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF || body.Query != cudaFloorPodMutation {
		return nil, NewValidationError("prepared", "requires exactly the supported CUDA-floor create mutation")
	}
	i := body.Variables.Input
	req := &CreatePodRequest{
		Name: i.Name, ImageName: i.ImageName, CloudType: i.CloudType, ComputeType: "GPU",
		GPUTypeIDs: []string{i.GPUTypeID}, GPUCount: i.GPUCount,
		ContainerDiskInGB: i.ContainerDiskInGB, VolumeInGB: i.VolumeInGB,
		VolumeMountPath: i.VolumeMountPath, DockerArgs: i.DockerArgs,
		NetworkVolumeID: i.NetworkVolumeID, ContainerRegistryAuthId: i.ContainerRegistryAuthID,
		SupportPublicIP: i.SupportPublicIP, MinRAMPerGPU: i.MinMemoryInGB,
		MinVCPUPerGPU: i.MinVCPUCount, MinDownloadMbps: i.MinDownload, MinUploadMbps: i.MinUpload,
		MinCudaVersion: i.MinCudaVersion, Env: make(map[string]string, len(i.Env)),
	}
	if i.DataCenterID != "" {
		req.DataCenterIDs = []string{i.DataCenterID}
	}
	if i.Ports != "" {
		req.Ports = strings.Split(i.Ports, ",")
	}
	for _, variable := range i.Env {
		if _, exists := req.Env[variable.Key]; exists {
			return nil, NewValidationError("env", "duplicate environment key in prepared create")
		}
		req.Env[variable.Key] = variable.Value
	}
	if err := c.validateCreatePodRequest(req); err != nil {
		return nil, err
	}
	if _, err := prepareCUDAFloorPod(req); err != nil {
		return nil, err
	}
	return req, nil
}

func (c *Client) executeCUDAFloorPod(ctx context.Context, body []byte, req *CreatePodRequest) (*Pod, error) {
	endpoint := strings.TrimSpace(c.graphqlBaseURL)
	if endpoint == "" {
		endpoint = DefaultGraphQLBaseURL
	}
	// makeRequestBytes does not retry a create POST after an uncertain result.
	resp, err := c.makeRequestBytes(ctx, "POST", endpoint, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read CUDA-floor create response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, classifyCreatePodError(c.parseErrorResponse(resp.StatusCode, resp.Header, raw), req)
	}
	var envelope struct {
		Data struct {
			Pod *Pod `json:"podFindAndDeployOnDemand"`
		} `json:"data"`
		Errors []GraphQLError `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode CUDA-floor create response: %w", err)
	}
	// A returned identity proves creation even if other GraphQL fields errored.
	if envelope.Data.Pod != nil && envelope.Data.Pod.ID != "" {
		return envelope.Data.Pod, nil
	}
	if len(envelope.Errors) != 0 {
		failure := &GraphQLResponseError{Errors: envelope.Errors}
		noCapacity := true
		for _, item := range envelope.Errors {
			noCapacity = noCapacity && ClassifiesAsNoPodCreated(item.Message)
		}
		if noCapacity {
			return nil, &NoCapacityError{GPUTypeID: req.GPUTypeIDs[0], DataCenterIDs: req.DataCenterIDs, Cause: failure}
		}
		return nil, failure // unknown outcomes require the caller's absence/adoption readback
	}
	return nil, fmt.Errorf("CUDA-floor create response omitted pod identity")
}
