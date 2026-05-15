package runpod

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type ListOptions struct {
	Limit  int `json:"limit,omitempty"`
	Offset int `json:"offset,omitempty"`
}

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

type Pod struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	DesiredStatus     string            `json:"desiredStatus"`
	LastStatusChange  string            `json:"lastStatusChange,omitempty"`
	ImageName         string            `json:"image"`
	GPUCount          int               `json:"gpuCount"`
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

func (p *Pod) Status() string {
	return p.DesiredStatus
}

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

type Machine struct {
	ID           string `json:"id"`
	Name         string `json:"name,omitempty"`
	GPUTypeID    string `json:"gpuTypeId,omitempty"`
	DataCenterID string `json:"dataCenterId,omitempty"`
	Region       string `json:"region,omitempty"`
	CountryCode  string `json:"countryCode,omitempty"`
}

type CreatePodRequest struct {
	Name                    string `json:"name"`
	ImageName               string `json:"imageName"`
	ContainerRegistryAuthId string `json:"containerRegistryAuthId,omitempty"`

	// GPU placement (ComputeType="GPU" or empty). GPUTypeIDs is a
	// fallback-ordered list of acceptable GPU type IDs (e.g.,
	// "NVIDIA GeForce RTX 4090"); RunPod picks the first available. Omit
	// these on CPU requests — `omitempty` is required because RunPod's REST
	// validator rejects unknown/unexpected fields with `Extra input keys
	// provided in request body`, and an explicit `gpuTypeIds: null` /
	// `gpuCount: 0` would be sent on a CPU request without it.
	GPUTypeIDs []string `json:"gpuTypeIds,omitempty"`
	GPUCount   int      `json:"gpuCount,omitempty"`

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
	Interruptible     bool              `json:"interruptible,omitempty"` // For spot instances
	SupportPublicIP   bool              `json:"supportPublicIp,omitempty"`
	TemplateID        string            `json:"templateId,omitempty"`

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

type UpdatePodRequest struct {
	Name string            `json:"name,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
}

type Endpoint struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	TemplateID       string    `json:"templateId"`
	GPUTypeIDs       []string  `json:"gpuTypeIds"`
	ScalerType       string    `json:"scalerType"`
	ScalerValue      int       `json:"scalerValue"`
	WorkersMin       int       `json:"workersMin"`
	WorkersMax       int       `json:"workersMax"`
	IdleTimeout      int       `json:"idleTimeout"`
	ExecutionTimeout int       `json:"executionTimeoutMs"`
	CreatedAt        *JSONTime `json:"createdAt"`
	Status           string    `json:"status"`
	URL              string    `json:"url,omitempty"`
}

type CreateEndpointRequest struct {
	Name                string   `json:"name"`
	TemplateID          string   `json:"templateId"`
	GPUTypeIDs          []string `json:"gpuTypeIds"`
	ScalerType          string   `json:"scalerType"`
	ScalerValue         int      `json:"scalerValue"`
	WorkersMin          int      `json:"workersMin"`
	WorkersMax          int      `json:"workersMax"`
	IdleTimeout         int      `json:"idleTimeout"`
	ExecutionTimeout    int      `json:"executionTimeoutMs"`
	AllowedCudaVersions []string `json:"allowedCudaVersions,omitempty"`
}

type UpdateEndpointRequest struct {
	Name             string   `json:"name,omitempty"`
	GPUTypeIDs       []string `json:"gpuTypeIds,omitempty"`
	ScalerType       string   `json:"scalerType,omitempty"`
	ScalerValue      int      `json:"scalerValue,omitempty"`
	WorkersMin       int      `json:"workersMin,omitempty"`
	WorkersMax       int      `json:"workersMax,omitempty"`
	IdleTimeout      int      `json:"idleTimeout,omitempty"`
	ExecutionTimeout int      `json:"executionTimeoutMs,omitempty"`
}

type Job struct {
	ID            string      `json:"id"`
	Status        string      `json:"status"`
	Input         interface{} `json:"input"`
	Output        interface{} `json:"output,omitempty"`
	Stream        interface{} `json:"stream,omitempty"`
	Error         string      `json:"error,omitempty"`
	CreatedAt     *JSONTime   `json:"createdAt"`
	StartedAt     *JSONTime   `json:"startedAt,omitempty"`
	CompletedAt   *JSONTime   `json:"completedAt,omitempty"`
	ExecutionTime int         `json:"executionTimeMs,omitempty"`
	RetryCount    int         `json:"retryCount,omitempty"`
	EndpointID    string      `json:"endpointId,omitempty"`
}

type RunJobRequest struct {
	Input interface{} `json:"input"`
}

type JobStatus string

const (
	JobStatusInQueue    JobStatus = "IN_QUEUE"
	JobStatusInProgress JobStatus = "IN_PROGRESS"
	JobStatusCompleted  JobStatus = "COMPLETED"
	JobStatusFailed     JobStatus = "FAILED"
	JobStatusCancelled  JobStatus = "CANCELLED"
	JobStatusTimedOut   JobStatus = "TIMED_OUT"
)

type Template struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	ImageName         string            `json:"imageName"`
	IsServerless      bool              `json:"isServerless"`
	ContainerDiskInGB int               `json:"containerDiskInGb"`
	VolumeInGB        int               `json:"volumeInGb"`
	VolumeMountPath   string            `json:"volumeMountPath"`
	Env               map[string]string `json:"env"`
	Ports             string            `json:"ports"`
	DockerArgs        string            `json:"dockerArgs"`
	CreatedAt         *JSONTime         `json:"createdAt"`
	Runtime           *TemplateRuntime  `json:"runtime,omitempty"`
}

type TemplateRuntime struct {
	ContainerRegistryAuthID string `json:"containerRegistryAuthId,omitempty"`
	StartSSH                bool   `json:"startSsh,omitempty"`
}

type CreateTemplateRequest struct {
	Name              string            `json:"name"`
	ImageName         string            `json:"imageName"`
	IsServerless      bool              `json:"isServerless"`
	ContainerDiskInGB int               `json:"containerDiskInGb"`
	VolumeInGB        int               `json:"volumeInGb,omitempty"`
	VolumeMountPath   string            `json:"volumeMountPath,omitempty"`
	Env               map[string]string `json:"env,omitempty"`
	Ports             string            `json:"ports,omitempty"`
	DockerArgs        string            `json:"dockerArgs,omitempty"`
	Runtime           *TemplateRuntime  `json:"runtime,omitempty"`
}

type UpdateTemplateRequest struct {
	Name              string            `json:"name,omitempty"`
	ImageName         string            `json:"imageName,omitempty"`
	ContainerDiskInGB int               `json:"containerDiskInGb,omitempty"`
	VolumeInGB        int               `json:"volumeInGb,omitempty"`
	VolumeMountPath   string            `json:"volumeMountPath,omitempty"`
	Env               map[string]string `json:"env,omitempty"`
	Ports             string            `json:"ports,omitempty"`
	DockerArgs        string            `json:"dockerArgs,omitempty"`
	Runtime           *TemplateRuntime  `json:"runtime,omitempty"`
}

type GPUType struct {
	ID             string  `json:"id"`
	DisplayName    string  `json:"displayName"`
	MemoryInGB     int     `json:"memoryInGb"`
	CostPerHour    float64 `json:"costPerHr"`
	Available      bool    `json:"available"`
	CommunityCloud bool    `json:"communityCloud"`
	SecureCloud    bool    `json:"secureCloud"`
	LowestPrice    *Price  `json:"lowestPrice,omitempty"`
}

type Price struct {
	MinimumBidPrice      float64 `json:"minimumBidPrice"`
	UninterruptablePrice float64 `json:"uninterruptablePrice"`
	InterruptablePrice   float64 `json:"interruptablePrice,omitempty"`
	StockStatus          string  `json:"stockStatus,omitempty"`
	CudaVersion          string  `json:"cudaVersion,omitempty"`
}

type GPUTypeFilter struct {
	IDs                 []string
	MinCudaVersion      string
	AllowedCudaVersions []string
	SecureCloud         *bool
	CommunityCloud      *bool
	GPUCount            int
}

type GPUTypeWithAvailability struct {
	GPUType
	StockStatus    string
	AvailableCount int
}

type GetPodOptions struct {
	IncludeMachine       bool
	IncludeNetworkVolume bool
	IncludeSavingsPlans  bool
	IncludeTemplate      bool
	IncludeWorkers       bool
}

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

type ProviderFeatureSupport struct {
	PodLogsAPI bool
	Reason     string
}

type Datacenter struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Country string `json:"country"`
	Region  string `json:"region,omitempty"`
}

type AccountInfo struct {
	ID                string  `json:"id"`
	Email             string  `json:"email"`
	Balance           float64 `json:"balance"`
	SpendLimit        float64 `json:"spendLimit,omitempty"`
	CurrentSpendPerHr float64 `json:"currentSpendPerHr"`
	MachineQuota      int     `json:"machineQuota,omitempty"`
}

// NetworkVolume represents a network volume
type NetworkVolume struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Size         int       `json:"size"`
	DatacenterID string    `json:"datacenterId"`
	CreatedAt    *JSONTime `json:"createdAt"`
	PodIds       []string  `json:"podIds,omitempty"`
}

type CreateNetworkVolumeRequest struct {
	Name         string `json:"name"`
	Size         int    `json:"size"`
	DatacenterID string `json:"datacenterId"`
}

type WebhookConfig struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Secret  string            `json:"secret,omitempty"`
}

type EndpointHealth struct {
	Status        string `json:"status"`
	JobsInQueue   int    `json:"jobsInQueue"`
	WorkersIdle   int    `json:"workersIdle"`
	WorkersActive int    `json:"workersActive"`
	WorkersTotal  int    `json:"workersTotal"`
}

type Secret struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Value is not returned for security reasons
}

type CreateSecretRequest struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type UpdateSecretRequest struct {
	Value string `json:"value"`
}
