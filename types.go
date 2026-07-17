package runpod

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ListOptions paginates list endpoints.
type ListOptions struct {
	Limit  int `json:"limit,omitempty"`
	Offset int `json:"offset,omitempty"`
}

// JSONTime wraps time.Time to tolerate RunPod's non-RFC-3339 timestamp
// formats on unmarshal; it always marshals as RFC-3339.
type JSONTime struct {
	time.Time
}

// UnmarshalJSON lets us parse either RFC-3339 or RunPod's broken format with flexible precision
func (jt *JSONTime) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}

	// Try RFC-3339 formats first (proper format)
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		jt.Time = t
		return nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		jt.Time = t
		return nil
	}

	// Handle RunPod's broken format: "2025-10-06 15:27:53.5 +0000 UTC"
	// Problem: fractional seconds can be 1-6 digits (.5, .53, .535, etc.)
	//
	// They also sometimes omit fractional seconds entirely:
	//   "2026-02-14 11:27:20 +0000 UTC"
	if strings.Contains(s, " +") && strings.Contains(s, " UTC") {
		// First try without fractional seconds.
		if t, err := time.Parse("2006-01-02 15:04:05 -0700 MST", s); err == nil {
			jt.Time = t
			return nil
		}

		// Find the fractional seconds part
		parts := strings.Split(s, ".")
		if len(parts) == 2 {
			// Split fractional part from timezone
			fracAndTZ := strings.SplitN(parts[1], " ", 2)
			if len(fracAndTZ) == 2 {
				frac := fracAndTZ[0]
				tz := fracAndTZ[1]

				// Normalize fractional seconds to exactly 3 digits
				if len(frac) < 3 {
					// Pad with zeros: "5" → "500"
					for len(frac) < 3 {
						frac += "0"
					}
				} else if len(frac) > 3 {
					// Truncate: "535678" → "535"
					frac = frac[:3]
				}

				// Reconstruct with normalized format
				normalized := parts[0] + "." + frac + " " + tz

				// Try parsing with fixed format
				if t, err := time.Parse("2006-01-02 15:04:05.000 -0700 MST", normalized); err == nil {
					jt.Time = t
					return nil
				}
			}
		}
	}

	return fmt.Errorf("runpod.JSONTime: cannot parse %q as JSONTime", s)
}

// MarshalJSON always emits proper RFC-3339 format
func (jt JSONTime) MarshalJSON() ([]byte, error) {
	if jt.Time.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(jt.Time.Format(time.RFC3339))
}

// Pod is a RunPod GPU or CPU pod.
type Pod struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	DesiredStatus    string `json:"desiredStatus"`
	LastStatusChange string `json:"lastStatusChange,omitempty"`
	// ImageName is the pod's container image ref. The live REST API sends
	// `imageName` on POST/GET/LIST responses (verified th#648) even though
	// the published OpenAPI schema names it `image` — accept both via
	// ImageAlt + normalizePod.
	ImageName         string            `json:"imageName"`
	ImageAlt          string            `json:"image,omitempty"`
	GPUCount          int               `json:"gpuCount"`
	GPU               *PodGPU           `json:"gpu,omitempty"`
	VCPUCount         int               `json:"vcpuCount"`
	MemoryInGB        int               `json:"memoryInGb"`
	ContainerDiskInGB int               `json:"containerDiskInGb"`
	VolumeInGB        int               `json:"volumeInGb"`
	VolumeMountPath   string            `json:"volumeMountPath"`
	CostPerHour       float64           `json:"costPerHr"`
	MachineID         string            `json:"machineId"`
	CreatedAt         *JSONTime         `json:"createdAt"`
	Env               map[string]string `json:"env"`
	Ports             []string          `json:"ports"`
	LastStartedAt     *JSONTime         `json:"lastStartedAt"`
	AdjustedCostPerHr float64           `json:"adjustedCostPerHr"`
	Locked            bool              `json:"locked"`
	Interruptible     bool              `json:"interruptible"`
	PublicIP          string            `json:"publicIp,omitempty"`
	Runtime           *PodRuntime       `json:"runtime,omitempty"`
	Machine           *Machine          `json:"machine,omitempty"`
	NetworkVolume     *NetworkVolume    `json:"networkVolume,omitempty"`
	NetworkVolumeID   string            `json:"networkVolumeId,omitempty"`

	// CPU-pod-specific response fields. RunPod's REST `POST /pods` returns
	// `cpuFlavorId` (the family the instance was placed on, e.g. "cpu3c")
	// when ComputeType="CPU". The vcpu/memory fields above are populated
	// for both compute classes. For CPU pods, Machine.GPUTypeID comes back
	// as the sentinel "unknown" — callers should treat that as "no GPU"
	// rather than as a real GPU type.
	CPUFlavorID string `json:"cpuFlavorId,omitempty"`
}

// PodGPU is the allocated GPU shape embedded in current RunPod REST pod
// responses. The public API documents the pod allocation count at gpu.count;
// older responses also exposed the same value as top-level gpuCount.
type PodGPU struct {
	ID          string `json:"id,omitempty"`
	Count       int    `json:"count,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
}

func (p *Pod) Status() string {
	return p.DesiredStatus
}

// PodRuntime is the pod's live runtime block (present once the container
// is up).
type PodRuntime struct {
	UptimeSeconds     int                    `json:"uptimeSeconds"`
	LastStartedAt     string                 `json:"lastStartedAt"`
	LastStatusChange  string                 `json:"lastStatusChange,omitempty"`
	LastStatusCharge  string                 `json:"lastStatusCharge,omitempty"`
	PublicIP          string                 `json:"publicIp,omitempty"`
	Ports             map[string]interface{} `json:"ports,omitempty"`
	Status            string                 `json:"status,omitempty"`
	Reason            string                 `json:"reason,omitempty"`
	Error             string                 `json:"error,omitempty"`
	ContainerExitCode int                    `json:"containerExitCode,omitempty"`
}

// Machine describes the host a pod landed on.
type Machine struct {
	ID           string `json:"id"`
	Name         string `json:"name,omitempty"`
	GPUTypeID    string `json:"gpuTypeId,omitempty"`
	DataCenterID string `json:"dataCenterId,omitempty"`
	Region       string `json:"region,omitempty"`
	CountryCode  string `json:"countryCode,omitempty"`
}

// CreatePodRequest configures pod creation (REST POST /pods).
type CreatePodRequest struct {
	Name                    string `json:"name"`
	ImageName               string `json:"imageName"`
	ContainerRegistryAuthId string `json:"containerRegistryAuthId,omitempty"`

	// GPU placement (ComputeType="GPU" or empty). GPUTypeIDs is a
	// fallback-ordered list of acceptable GPU type IDs (e.g.,
	// "NVIDIA GeForce RTX 4090"). CAUTION: RunPod's REST API does NOT
	// reliably walk this list when the first type is out of stock — it can
	// return 500 "no instances available" instead. Callers that need
	// fallback behavior should submit one type per request. Omit
	// these on CPU requests — `omitempty` is required because RunPod's REST
	// validator rejects unknown/unexpected fields with `Extra input keys
	// provided in request body`, and an explicit `gpuTypeIds: null` /
	// `gpuCount: 0` would be sent on a CPU request without it.
	GPUTypeIDs []string `json:"gpuTypeIds,omitempty"`
	GPUCount   int      `json:"gpuCount,omitempty"`

	// MinRAMPerGPU and MinVCPUPerGPU constrain the minimum host resources
	// allocated per attached GPU. They are GPU-only; zero leaves RunPod's
	// defaults in effect.
	MinRAMPerGPU  int `json:"minRAMPerGPU,omitempty"`  // GB
	MinVCPUPerGPU int `json:"minVCPUPerGPU,omitempty"` // virtual CPUs

	// CPU placement (ComputeType="CPU"). CPUFlavorIDs is a fallback-ordered
	// list of acceptable CPU family IDs (e.g., "cpu5c", "cpu3c", "cpu3g");
	// RunPod picks the first available. Optional — if omitted, RunPod
	// auto-selects the cheapest available CPU flavor. Family IDs are the
	// REST API's vocabulary; the older GraphQL CPU mutation used granular
	// IDs (`cpu5c-2-2`) but REST accepts only family-level IDs.
	//
	// Note: REST does not accept `minVCpuCount` / `minMemoryInGb` as inputs.
	// To target a specific vCPU/memory size, pick families from the static
	// catalog (see cpu_types.go) — but be aware RunPod within a family will
	// still pick the cheapest instance size it has stock for.
	CPUFlavorIDs []string `json:"cpuFlavorIds,omitempty"`

	VCPUCount         int               `json:"vcpuCount,omitempty"`
	ContainerDiskInGB int               `json:"containerDiskInGb"`
	VolumeInGB        int               `json:"volumeInGb,omitempty"`
	VolumeMountPath   string            `json:"volumeMountPath,omitempty"`
	DataCenterIDs     []string          `json:"dataCenterIds,omitempty"`
	Env               map[string]string `json:"env,omitempty"`
	Ports             []string          `json:"ports,omitempty"`
	DockerArgs        string            `json:"dockerArgs,omitempty"`
	NetworkVolumeID   string            `json:"networkVolumeId,omitempty"`
	CloudType         string            `json:"cloudType,omitempty"`     // "SECURE" or "COMMUNITY"
	Interruptible     bool              `json:"interruptible,omitempty"` // spot/interruptible instance

	// BidPerGPU is the per-GPU bid price (USD/hr) for interruptible pods.
	// Only valid when Interruptible is true. When omitted RunPod uses the
	// current minimum bid. Get the market floor from ListGPUOffers /
	// ListAvailableGPUs (Price.MinimumBidPrice).
	BidPerGPU       float64 `json:"bidPerGpu,omitempty"`
	SupportPublicIP bool    `json:"supportPublicIp,omitempty"`
	TemplateID      string  `json:"templateId,omitempty"`

	AllowedCudaVersions []string `json:"allowedCudaVersions,omitempty"`
	MinCudaVersion      string   `json:"minCudaVersion,omitempty"`

	// Additional REST API fields
	ComputeType        string   `json:"computeType,omitempty"` // "GPU" or "CPU"
	DockerEntrypoint   []string `json:"dockerEntrypoint,omitempty"`
	DockerStartCmd     []string `json:"dockerStartCmd,omitempty"`
	GPUTypePriority    string   `json:"gpuTypePriority,omitempty"`
	DataCenterPriority string   `json:"dataCenterPriority,omitempty"`
}

const (
	CudaVersion118 = "11.8"
	CudaVersion120 = "12.0"
	CudaVersion121 = "12.1"
	CudaVersion122 = "12.2"
	CudaVersion123 = "12.3"
	CudaVersion124 = "12.4"
	CudaVersion125 = "12.5"
	CudaVersion126 = "12.6"
	CudaVersion128 = "12.8"
)

// Helper function to create common CUDA version slices
func AllCudaVersions() []string {
	return []string{
		CudaVersion118,
		CudaVersion120,
		CudaVersion121,
		CudaVersion122,
		CudaVersion123,
		CudaVersion124,
		CudaVersion125,
		CudaVersion126,
		CudaVersion128,
	}
}

// Job is a serverless job. Input/Output/Stream are raw JSON — unmarshal
// them into your own types.
type Job struct {
	ID            string          `json:"id"`
	Status        string          `json:"status"`
	Input         json.RawMessage `json:"input,omitempty"`
	Output        json.RawMessage `json:"output,omitempty"`
	Stream        json.RawMessage `json:"stream,omitempty"`
	Error         string          `json:"error,omitempty"`
	CreatedAt     *JSONTime       `json:"createdAt"`
	StartedAt     *JSONTime       `json:"startedAt,omitempty"`
	CompletedAt   *JSONTime       `json:"completedAt,omitempty"`
	ExecutionTime int             `json:"executionTimeMs,omitempty"`
	RetryCount    int             `json:"retryCount,omitempty"`
	EndpointID    string          `json:"endpointId,omitempty"`
}

// RunJobRequest wraps a serverless job input payload.
type RunJobRequest struct {
	Input interface{} `json:"input"`
}

// JobStatus enumerates serverless job states.
type JobStatus string

const (
	JobStatusInQueue    JobStatus = "IN_QUEUE"
	JobStatusInProgress JobStatus = "IN_PROGRESS"
	JobStatusCompleted  JobStatus = "COMPLETED"
	JobStatusFailed     JobStatus = "FAILED"
	JobStatusCancelled  JobStatus = "CANCELLED"
	JobStatusTimedOut   JobStatus = "TIMED_OUT"
)

// GPUType is a GPU SKU from RunPod's live catalog (GraphQL gpuTypes).
type GPUType struct {
	ID             string `json:"id"`
	DisplayName    string `json:"displayName"`
	MemoryInGB     int    `json:"memoryInGb"`
	CommunityCloud bool   `json:"communityCloud"`
	SecureCloud    bool   `json:"secureCloud"`
	LowestPrice    *Price `json:"lowestPrice,omitempty"`
}

// Price is the pricing/stock block returned by the gpuTypes lowestPrice
// query. Both prices are aggregate whole-pod prices for the GPU count supplied
// to that query. RunPod removed interruptablePrice and cudaVersion from
// LowestPrice; MinimumBidPrice is the only spot signal still exposed.
type Price struct {
	MinimumBidPrice      float64 `json:"minimumBidPrice"`
	UninterruptablePrice float64 `json:"uninterruptablePrice"`
	StockStatus          string  `json:"stockStatus,omitempty"`
	// AvailableGPUCounts is the set of whole-pod GPU counts RunPod currently
	// reports as rentable for this exact lowest-price query.
	AvailableGPUCounts []int `json:"availableGpuCounts,omitempty"`
}

// GPUTypeFilter constrains ListGPUTypes.
type GPUTypeFilter struct {
	IDs                 []string
	MinCudaVersion      string
	AllowedCudaVersions []string
	SecureCloud         *bool
	CommunityCloud      *bool
	GPUCount            int
}

// GPUTypeWithAvailability is a GPU type with its current stock status.
type GPUTypeWithAvailability struct {
	GPUType
	StockStatus string
}

// GetPodOptions toggles the include* query parameters on GetPodWithOptions.
type GetPodOptions struct {
	IncludeMachine       bool
	IncludeNetworkVolume bool
	IncludeSavingsPlans  bool
	IncludeTemplate      bool
	IncludeWorkers       bool
}

// PodDiagnostics is a normalized snapshot for scheduler/bootstrap
// troubleshooting.
type PodDiagnostics struct {
	PodID            string
	DesiredStatus    string
	LastStatusChange string
	RuntimeReady     bool
	Runtime          *PodRuntime
	Machine          *Machine
	DataCenterID     string
	PublicIP         string
	PortMappings     map[string]int
	NetworkVolumeID  string
	ProviderReason   string
}

// ProviderFeatureSupport reports the SDK's understanding of optional
// provider capabilities.
type ProviderFeatureSupport struct {
	PodLogsAPI bool
	Reason     string
}

// NetworkVolume represents a network volume.
type NetworkVolume struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Size         int       `json:"size"` // GB
	DataCenterID string    `json:"dataCenterId"`
	CreatedAt    *JSONTime `json:"createdAt"`
	PodIds       []string  `json:"podIds,omitempty"`
}

// CreateNetworkVolumeRequest configures network volume creation.
type CreateNetworkVolumeRequest struct {
	Name         string `json:"name"`
	Size         int    `json:"size"` // GB
	DataCenterID string `json:"dataCenterId"`
}

// UpdateNetworkVolumeRequest updates a network volume. Size can only grow.
type UpdateNetworkVolumeRequest struct {
	Name string `json:"name,omitempty"`
	Size int    `json:"size,omitempty"` // GB
}

// ContainerRegistryAuth is a stored Docker registry credential used by pods
// to pull private images. The password is never returned by the API.
type ContainerRegistryAuth struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// CreateContainerRegistryAuthRequest stores a new registry credential.
type CreateContainerRegistryAuthRequest struct {
	Name     string `json:"name"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// EndpointHealth is a serverless endpoint's queue/worker health.
type EndpointHealth struct {
	Status        string `json:"status"`
	JobsInQueue   int    `json:"jobsInQueue"`
	WorkersIdle   int    `json:"workersIdle"`
	WorkersActive int    `json:"workersActive"`
	WorkersTotal  int    `json:"workersTotal"`
}

// Secret is a stored secret (value never returned).
type Secret struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Value is not returned for security reasons
}

// CreateSecretRequest creates a named secret.
type CreateSecretRequest struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// UpdateSecretRequest replaces a secret value.
type UpdateSecretRequest struct {
	Value string `json:"value"`
}

// normalize reconciles wire-format drift on a decoded Pod. Preserve a valid
// legacy top-level count, fill it from the documented nested shape when
// absent, and fail closed on conflicting positive counts.
func (p *Pod) normalize() {
	if p == nil {
		return
	}
	if strings.TrimSpace(p.ImageName) == "" {
		p.ImageName = strings.TrimSpace(p.ImageAlt)
	}
	if p.GPU != nil && p.GPU.Count > 0 {
		switch {
		case p.GPUCount <= 0:
			p.GPUCount = p.GPU.Count
		case p.GPUCount != p.GPU.Count:
			// Conflicting positive allocation counts are not safely
			// reconcilable. Preserve the nested wire evidence but make the
			// normalized count unknown so exact-placement callers fail closed.
			p.GPUCount = 0
		}
	}
}

// normalizePods normalizes every pod in a decoded list.
func normalizePods(pods []*Pod) {
	for _, p := range pods {
		p.normalize()
	}
}
