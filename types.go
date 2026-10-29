package runpod

import (
	"encoding/json"
	"fmt"
	"time"
)

// ListOptions controls cursor pagination. ListPods follows every remaining page.
type ListOptions struct {
	Limit              int
	Cursor             string
	IncludeClusterPods bool
}

// JSONTime wraps nullable RFC3339 provider timestamps.
type JSONTime struct{ time.Time }

func (jt *JSONTime) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*jt = JSONTime{}
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return fmt.Errorf("runpod timestamp: %w", err)
	}
	jt.Time = parsed
	return nil
}

func (jt JSONTime) MarshalJSON() ([]byte, error) {
	if jt.Time.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(jt.Time.Format(time.RFC3339Nano))
}

// Pod is the REST v2 pod representation. Status is observed provider state,
// not application readiness. Runtime is absent from list responses.
type Pod struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Status       string            `json:"status"`
	Actions      []string          `json:"actions"`
	Image        string            `json:"image"`
	Args         string            `json:"args"`
	Entrypoint   []string          `json:"entrypoint,omitempty"`
	Cmd          []string          `json:"cmd,omitempty"`
	Disk         int               `json:"disk"`
	Ports        []string          `json:"ports"`
	Env          map[string]string `json:"env"`
	Registry     *string           `json:"registry"`
	Mounts       *Mounts           `json:"mounts"`
	GPU          *PodGPU           `json:"gpu,omitempty"`
	CPU          *PodCPU           `json:"cpu,omitempty"`
	Cloud        string            `json:"cloud"`
	DataCenterID *string           `json:"dataCenterId"`
	CUDAVersion  *string           `json:"cudaVersion"`
	SSH          *PodSSH           `json:"ssh"`
	Template     *string           `json:"template"`
	Cost         *USDMicrosPerHour `json:"cost"`
	Locked       bool              `json:"locked"`
	Runtime      *PodRuntime       `json:"runtime"`
	CreatedAt    *JSONTime         `json:"createdAt"`
	StartedAt    *JSONTime         `json:"startedAt"`
}

type PodGPU struct {
	ID        string  `json:"id"`
	Count     int     `json:"count"`
	VCPUCount float64 `json:"vcpuCount"`
	Memory    float64 `json:"memory"`
}

type PodCPU struct {
	ID        string `json:"id"`
	VCPUCount int    `json:"vcpuCount"`
	Memory    int    `json:"memory"`
}

type PodRuntime struct {
	Uptime *int                `json:"uptime,omitempty"`
	Ports  []PodRuntimePort    `json:"ports,omitempty"`
	CPU    *Utilization        `json:"cpu,omitempty"`
	Memory *Utilization        `json:"memory,omitempty"`
	GPUs   []PodGPUUtilization `json:"gpus,omitempty"`
}

type Utilization struct {
	Util int `json:"util"`
}

type PodGPUUtilization struct {
	Util       int `json:"util"`
	MemoryUtil int `json:"memoryUtil"`
}

// Nil Public or IP means that the port has no published endpoint.
type PodRuntimePort struct {
	Private int     `json:"private"`
	Public  *int    `json:"public"`
	Type    string  `json:"type"`
	IP      *string `json:"ip"`
}

type PodSSH struct {
	Proxy  *SSHEndpoint `json:"proxy"`
	Direct *SSHEndpoint `json:"direct"`
}

type SSHEndpoint struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Command  string `json:"command,omitempty"`
}

type Mounts struct {
	Persistent *PersistentMount `json:"persistent,omitempty"`
	Network    []VolumeMount    `json:"network,omitempty"`
	Global     []VolumeMount    `json:"global,omitempty"`
}

type PersistentMount struct {
	Size int    `json:"size"`
	Path string `json:"path"`
}

type VolumeMount struct {
	VolumeID string `json:"volumeId"`
	Path     string `json:"path"`
}

// CreatePodRequest uses only REST v2 fields. Exactly one of GPU or CPU is required.
type CreatePodRequest struct {
	Name             string            `json:"name"`
	Image            string            `json:"image,omitempty"`
	Args             string            `json:"args,omitempty"`
	Entrypoint       []string          `json:"entrypoint,omitempty"`
	Cmd              []string          `json:"cmd,omitempty"`
	Disk             int               `json:"disk,omitempty"`
	Ports            []string          `json:"ports,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	Registry         string            `json:"registry,omitempty"`
	Cloud            string            `json:"cloud,omitempty"`
	GPU              *CreateGPUConfig  `json:"gpu,omitempty"`
	CPU              *CreateCPUConfig  `json:"cpu,omitempty"`
	DataCenterIDs    []string          `json:"dataCenterIds,omitempty"`
	Mounts           *Mounts           `json:"mounts,omitempty"`
	TemplateID       string            `json:"templateId,omitempty"`
	GlobalNetworking bool              `json:"globalNetworking,omitempty"`
	StartSSH         bool              `json:"startSsh,omitempty"`
	StartJupyter     bool              `json:"startJupyter,omitempty"`
}

type CreateGPUConfig struct {
	ID                  string   `json:"id"`
	Count               int      `json:"count,omitempty"`
	AllowedCUDAVersions []string `json:"allowedCudaVersions,omitempty"`
	MinCUDAVersion      string   `json:"minCudaVersion,omitempty"`
	MinRAMPerGPU        int      `json:"minRamPerGpu,omitempty"`
	MinVCPUCountPerGPU  int      `json:"minVcpuCountPerGpu,omitempty"`
}

type CreateCPUConfig struct {
	ID        string `json:"id"`
	VCPUCount int    `json:"vcpuCount"`
}

// UpdatePodRequest deliberately represents the v2 PATCH fields, not a create.
type UpdatePodRequest struct {
	Name             *string            `json:"name,omitempty"`
	Image            *string            `json:"image,omitempty"`
	Args             *string            `json:"args,omitempty"`
	Entrypoint       *[]string          `json:"entrypoint,omitempty"`
	Cmd              *[]string          `json:"cmd,omitempty"`
	Disk             *int               `json:"disk,omitempty"`
	Env              *map[string]string `json:"env,omitempty"`
	Ports            *[]string          `json:"ports,omitempty"`
	Registry         *string            `json:"registry,omitempty"`
	Mounts           *Mounts            `json:"mounts,omitempty"`
	Locked           *bool              `json:"locked,omitempty"`
	GlobalNetworking *bool              `json:"globalNetworking,omitempty"`
	TemplateID       *string            `json:"templateId,omitempty"`
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

// NetworkVolume is a datacenter-local REST v2 volume.
type NetworkVolume struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Size       int    `json:"size"`
	DataCenter string `json:"dataCenter"`
	Type       string `json:"type"`
}

type CreateNetworkVolumeRequest struct {
	Name       string `json:"name"`
	Size       int    `json:"size"`
	DataCenter string `json:"dataCenter"`
	Type       string `json:"type,omitempty"`
}

type UpdateNetworkVolumeRequest struct {
	Name string `json:"name,omitempty"`
	Size int    `json:"size,omitempty"`
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
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	Description        *string   `json:"description,omitempty"`
	CreatedAt          *JSONTime `json:"createdAt,omitempty"`
	ValueLastUpdatedAt *JSONTime `json:"valueLastUpdatedAt,omitempty"`
	// Value is not returned for security reasons
}

// CreateSecretRequest creates a named secret.
type CreateSecretRequest struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
}

// UpdateSecretRequest replaces a secret value.
type UpdateSecretRequest struct {
	Value       string  `json:"value,omitempty"`
	Description *string `json:"description,omitempty"`
}
