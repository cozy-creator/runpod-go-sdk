package runpod

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PodRuntimePort is one runtime port row returned by RunPod's GraphQL pod
// projection. It is provider evidence, not proof that the mapping still
// belongs to the container answering at the end of the read.
type PodRuntimePort struct {
	IP          string `json:"ip"`
	IsIPPublic  bool   `json:"isIpPublic"`
	PrivatePort int    `json:"privatePort"`
	PublicPort  int    `json:"publicPort"`
	Type        string `json:"type"`
}

// PodRuntimeContainer is the container utilization block exposed by GraphQL.
type PodRuntimeContainer struct {
	CPUPercent    *int `json:"cpuPercent"`
	MemoryPercent *int `json:"memoryPercent"`
}

// PodRuntimeGPU is one GPU utilization row exposed by GraphQL.
type PodRuntimeGPU struct {
	ID                string `json:"id"`
	GPUUtilPercent    *int   `json:"gpuUtilPercent"`
	MemoryUtilPercent *int   `json:"memoryUtilPercent"`
}

// PodGraphQLRuntime preserves the complete documented GraphQL runtime block.
// RunPod may replace or clear this block independently of REST desiredStatus.
type PodGraphQLRuntime struct {
	UptimeInSeconds *int                 `json:"uptimeInSeconds"`
	Container       *PodRuntimeContainer `json:"container"`
	GPUs            []PodRuntimeGPU      `json:"gpus"`
	Ports           []PodRuntimePort     `json:"ports"`
}

// PodGraphQLMachine preserves placement facts useful to provider
// reconciliation. REST's independently observed machine is retained beside it.
type PodGraphQLMachine struct {
	ID                   string `json:"id"`
	GPUTypeID            string `json:"gpuTypeId"`
	GPUDisplayName       string `json:"gpuDisplayName"`
	DataCenterID         string `json:"dataCenterId"`
	Location             string `json:"location"`
	MachineType          string `json:"machineType"`
	PodHostID            string `json:"podHostId"`
	RunPodIP             string `json:"runpodIp"`
	SupportPublicIP      *bool  `json:"supportPublicIp"`
	SecureCloud          *bool  `json:"secureCloud"`
	DiskMBps             *int   `json:"diskMBps"`
	MaxDownloadSpeedMbps *int   `json:"maxDownloadSpeedMbps"`
	MaxUploadSpeedMbps   *int   `json:"maxUploadSpeedMbps"`
}

// PodGraphQLSnapshot is the detailed provider snapshot returned by GraphQL.
// It intentionally does not collapse into Pod: the two APIs are separate
// observations and can disagree during a restart or provider transition.
type PodGraphQLSnapshot struct {
	ID                           string                 `json:"id"`
	Name                         string                 `json:"name"`
	DesiredStatus                string                 `json:"desiredStatus"`
	LastStatusChange             string                 `json:"lastStatusChange"`
	CreatedAt                    *JSONTime              `json:"createdAt"`
	LastStartedAt                *JSONTime              `json:"lastStartedAt"`
	ImageName                    string                 `json:"imageName"`
	GPUCount                     int                    `json:"gpuCount"`
	MachineID                    string                 `json:"machineId"`
	MemoryInGB                   float64                `json:"memoryInGb"`
	VCPUCount                    float64                `json:"vcpuCount"`
	ContainerDiskInGB            int                    `json:"containerDiskInGb"`
	VolumeInGB                   float64                `json:"volumeInGb"`
	VolumeMountPath              string                 `json:"volumeMountPath"`
	NetworkVolumeID              string                 `json:"networkVolumeId"`
	RequestedPorts               string                 `json:"ports"`
	PodType                      string                 `json:"podType"`
	CostUSDMicrosPerHour         *USDMicrosPerHour      `json:"costPerHr"`
	AdjustedCostUSDMicrosPerHour *USDMicrosPerHour      `json:"adjustedCostPerHr"`
	LatestTelemetry              *PodLifecycleTelemetry `json:"latestTelemetry"`
	Runtime                      *PodGraphQLRuntime     `json:"runtime"`
	Machine                      *PodGraphQLMachine     `json:"machine"`
	NetworkVolume                *NetworkVolume         `json:"networkVolume"`
}

// ObservationWindow bounds one provider read. The API can change while an
// HTTP request is in flight, so a point timestamp would claim false precision.
type ObservationWindow struct {
	StartedAt   time.Time
	CompletedAt time.Time
}

// PodReadbackCheckStatus describes one cross-source comparison.
type PodReadbackCheckStatus string

const (
	PodReadbackCheckAgree         PodReadbackCheckStatus = "agree"
	PodReadbackCheckDisagree      PodReadbackCheckStatus = "disagree"
	PodReadbackCheckNotComparable PodReadbackCheckStatus = "not_comparable"
	PodReadbackCheckInvalid       PodReadbackCheckStatus = "invalid"
)

// PodReadbackCheck preserves both values behind a coherence verdict.
type PodReadbackCheck struct {
	Field        string
	Status       PodReadbackCheckStatus
	RESTValue    string
	GraphQLValue string
	Detail       string
}

// PodReadbackCoherence summarizes only the checks in PodReadback.Checks. It
// is not a readiness verdict and never proves a container-generation join.
type PodReadbackCoherence string

const (
	PodReadbackCoherent    PodReadbackCoherence = "coherent"
	PodReadbackIncomplete  PodReadbackCoherence = "incomplete"
	PodReadbackConflicting PodReadbackCoherence = "conflicting"
)

// PodReadback retains two independently timed source observations plus every
// comparison used to summarize them. RunPod does not expose a container
// incarnation ID: equal lastStartedAt values are agreement on that timestamp
// only. Live crash-restart evidence shows they are not a generation fence.
type PodReadback struct {
	RequestedPodID  string
	RESTObserved    ObservationWindow
	REST            *Pod
	GraphQLObserved ObservationWindow
	GraphQL         *PodGraphQLSnapshot
	Checks          []PodReadbackCheck
	Coherence       PodReadbackCoherence
}

// PodReadbackStage identifies exactly which step failed.
type PodReadbackStage string

const (
	PodReadbackStageValidate PodReadbackStage = "validate"
	PodReadbackStageREST     PodReadbackStage = "rest"
	PodReadbackStageGraphQL  PodReadbackStage = "graphql"
	PodReadbackStageJoin     PodReadbackStage = "join"
)

// PodReadbackError is a typed stage failure. Partial retains any successful
// earlier source read, including its observation window.
type PodReadbackError struct {
	PodID   string
	Stage   PodReadbackStage
	Partial *PodReadback
	Cause   error
}

func (e *PodReadbackError) Error() string {
	return fmt.Sprintf("runpod: pod %q readback failed at %s stage: %v", e.PodID, e.Stage, e.Cause)
}

func (e *PodReadbackError) Unwrap() error { return e.Cause }

// GetPodReadback observes REST first and GraphQL second. It requests detailed
// REST machine and network-volume projections regardless of opts; opts can add
// other REST expansions. Cross-source disagreements are returned as facts in
// Checks rather than discarded as generic errors. Only an invalid/mismatched
// pod identity makes the join unusable.
func (c *Client) GetPodReadback(ctx context.Context, podID string, opts *GetPodOptions) (*PodReadback, error) {
	out := &PodReadback{RequestedPodID: podID}
	if err := c.validateRequired("podID", podID); err != nil {
		return nil, readbackError(podID, PodReadbackStageValidate, nil, err)
	}

	detailed := GetPodOptions{IncludeMachine: true, IncludeNetworkVolume: true}
	if opts != nil {
		detailed.IncludeSavingsPlans = opts.IncludeSavingsPlans
		detailed.IncludeTemplate = opts.IncludeTemplate
		detailed.IncludeWorkers = opts.IncludeWorkers
	}
	out.RESTObserved.StartedAt = time.Now().UTC()
	pod, err := c.GetPodWithOptions(ctx, podID, &detailed)
	out.RESTObserved.CompletedAt = time.Now().UTC()
	if err != nil {
		return nil, readbackError(podID, PodReadbackStageREST, out, err)
	}
	out.REST = pod

	out.GraphQLObserved.StartedAt = time.Now().UTC()
	graphQL, err := c.getPodGraphQLSnapshot(ctx, podID)
	out.GraphQLObserved.CompletedAt = time.Now().UTC()
	if err != nil {
		return nil, readbackError(podID, PodReadbackStageGraphQL, out, err)
	}
	out.GraphQL = graphQL

	if strings.TrimSpace(pod.ID) != podID || strings.TrimSpace(graphQL.ID) != podID {
		out.Checks = append(out.Checks, PodReadbackCheck{
			Field:        "pod_id",
			Status:       PodReadbackCheckInvalid,
			RESTValue:    pod.ID,
			GraphQLValue: graphQL.ID,
			Detail:       fmt.Sprintf("requested %q", podID),
		})
		out.Coherence = PodReadbackConflicting
		return nil, readbackError(podID, PodReadbackStageJoin, out, fmt.Errorf("provider response identity did not match the requested pod"))
	}

	out.Checks = buildPodReadbackChecks(pod, graphQL)
	out.Coherence = summarizePodReadbackChecks(out.Checks)
	return out, nil
}

func readbackError(podID string, stage PodReadbackStage, partial *PodReadback, cause error) *PodReadbackError {
	return &PodReadbackError{PodID: podID, Stage: stage, Partial: partial, Cause: cause}
}

func (c *Client) getPodGraphQLSnapshot(ctx context.Context, podID string) (*PodGraphQLSnapshot, error) {
	query := `query($input: PodFilter!) {
  pod(input: $input) {
    id
    name
    desiredStatus
    lastStatusChange
    createdAt
    lastStartedAt
    imageName
    gpuCount
    machineId
    memoryInGb
    vcpuCount
    containerDiskInGb
    volumeInGb
    volumeMountPath
    networkVolumeId
    ports
    podType
    costPerHr
    adjustedCostPerHr
    latestTelemetry { state time cpuUtilization memoryUtilization lastStateTransitionTimestamp }
    runtime {
      uptimeInSeconds
      container { cpuPercent memoryPercent }
      gpus { id gpuUtilPercent memoryUtilPercent }
      ports { ip isIpPublic privatePort publicPort type }
    }
    machine {
      id
      gpuTypeId
      gpuDisplayName
      dataCenterId
      location
      machineType
      podHostId
      runpodIp
      supportPublicIp
      secureCloud
      diskMBps
      maxDownloadSpeedMbps
      maxUploadSpeedMbps
    }
    networkVolume { id name size dataCenterId }
  }
}`
	var response struct {
		Pod *PodGraphQLSnapshot `json:"pod"`
	}
	if err := c.GraphQL(ctx, query, map[string]interface{}{
		"input": map[string]interface{}{"podId": podID},
	}, &response); err != nil {
		return nil, err
	}
	if response.Pod == nil {
		return nil, NewAPIErrorWithDetails(404, "pod not found", podID)
	}
	return response.Pod, nil
}

func buildPodReadbackChecks(rest *Pod, graphQL *PodGraphQLSnapshot) []PodReadbackCheck {
	checks := []PodReadbackCheck{
		compareText("pod_id", rest.ID, graphQL.ID, true),
		compareText("desired_status", rest.DesiredStatus, graphQL.DesiredStatus, true),
		compareTimes("last_started_at", rest.LastStartedAt, graphQL.LastStartedAt),
		compareText("image", rest.ImageName, graphQL.ImageName, false),
		compareText("machine_id", restMachineID(rest), graphQLMachineID(graphQL), false),
		compareText("gpu_type_id", restGPUTypeID(rest), graphQLGPUTypeID(graphQL), false),
		compareText("datacenter_id", restDataCenterID(rest), graphQLDataCenterID(graphQL), false),
		compareText("network_volume_id", restNetworkVolumeID(rest), graphQLNetworkVolumeID(graphQL), true),
		compareText("requested_ports", normalizeRESTPorts(rest.Ports), normalizeGraphQLPorts(graphQL.RequestedPorts), false),
		compareInt("gpu_count", rest.GPUCount, graphQL.GPUCount),
		compareRate("list_price_usd_micros_per_hour", rest.ListPriceUSDMicrosPerHour, graphQL.CostUSDMicrosPerHour),
	}

	if graphQL.Runtime == nil {
		checks = append(checks,
			PodReadbackCheck{Field: "runtime", Status: PodReadbackCheckNotComparable, Detail: "GraphQL runtime block absent"},
			PodReadbackCheck{Field: "public_ip", Status: PodReadbackCheckNotComparable, RESTValue: strings.TrimSpace(rest.PublicIP), Detail: "GraphQL runtime block absent"},
			PodReadbackCheck{Field: "public_tcp_ports", Status: PodReadbackCheckNotComparable, RESTValue: formatRESTPortMappings(rest.PortMappings), Detail: "GraphQL runtime block absent"},
		)
		return checks
	}

	checks = append(checks, PodReadbackCheck{Field: "runtime", Status: PodReadbackCheckAgree, GraphQLValue: "present"})
	publicIP, err := validateRuntimePorts(rest, graphQL.Runtime.Ports)
	if err != nil {
		checks = append(checks, PodReadbackCheck{
			Field:        "public_tcp_ports",
			Status:       PodReadbackCheckInvalid,
			RESTValue:    formatRESTPortMappings(rest.PortMappings),
			GraphQLValue: formatRuntimePorts(graphQL.Runtime.Ports),
			Detail:       err.Error(),
		})
	} else if len(rest.PortMappings) == 0 {
		checks = append(checks, PodReadbackCheck{
			Field:        "public_tcp_ports",
			Status:       PodReadbackCheckNotComparable,
			GraphQLValue: formatRuntimePorts(graphQL.Runtime.Ports),
			Detail:       "REST omitted portMappings; GraphQL mappings were validated against REST-requested ports",
		})
	} else {
		checks = append(checks, PodReadbackCheck{
			Field:        "public_tcp_ports",
			Status:       PodReadbackCheckAgree,
			RESTValue:    formatRESTPortMappings(rest.PortMappings),
			GraphQLValue: formatRuntimePorts(graphQL.Runtime.Ports),
		})
	}
	checks = append(checks, compareText("public_ip", rest.PublicIP, publicIP, false))
	return checks
}

func summarizePodReadbackChecks(checks []PodReadbackCheck) PodReadbackCoherence {
	coherence := PodReadbackCoherent
	for _, check := range checks {
		switch check.Status {
		case PodReadbackCheckDisagree, PodReadbackCheckInvalid:
			return PodReadbackConflicting
		case PodReadbackCheckNotComparable:
			coherence = PodReadbackIncomplete
		}
	}
	return coherence
}

func compareText(field, rest, graphQL string, emptyIsValue bool) PodReadbackCheck {
	rest = strings.TrimSpace(rest)
	graphQL = strings.TrimSpace(graphQL)
	check := PodReadbackCheck{Field: field, RESTValue: rest, GraphQLValue: graphQL}
	if !emptyIsValue && (rest == "" || graphQL == "") {
		check.Status = PodReadbackCheckNotComparable
		check.Detail = "one or both sources omitted the field"
		return check
	}
	if strings.EqualFold(rest, graphQL) {
		check.Status = PodReadbackCheckAgree
	} else {
		check.Status = PodReadbackCheckDisagree
	}
	return check
}

func compareTimes(field string, rest, graphQL *JSONTime) PodReadbackCheck {
	check := PodReadbackCheck{Field: field}
	if rest != nil {
		check.RESTValue = rest.Time.UTC().Format(time.RFC3339Nano)
	}
	if graphQL != nil {
		check.GraphQLValue = graphQL.Time.UTC().Format(time.RFC3339Nano)
	}
	if rest == nil || graphQL == nil {
		check.Status = PodReadbackCheckNotComparable
		check.Detail = "one or both sources omitted the timestamp"
	} else if rest.Time.Equal(graphQL.Time) {
		check.Status = PodReadbackCheckAgree
		check.Detail = "timestamp equality is not a container-generation fence"
	} else {
		check.Status = PodReadbackCheckDisagree
		check.Detail = "timestamps differ; neither value identifies a container incarnation"
	}
	return check
}

func compareInt(field string, rest, graphQL int) PodReadbackCheck {
	return compareText(field, strconv.Itoa(rest), strconv.Itoa(graphQL), true)
}

func compareRate(field string, rest, graphQL *USDMicrosPerHour) PodReadbackCheck {
	check := PodReadbackCheck{Field: field}
	if rest != nil {
		check.RESTValue = strconv.FormatInt(int64(*rest), 10)
	}
	if graphQL != nil {
		check.GraphQLValue = strconv.FormatInt(int64(*graphQL), 10)
	}
	if rest == nil || graphQL == nil {
		check.Status = PodReadbackCheckNotComparable
		check.Detail = "one or both sources omitted the exact price"
	} else if *rest == *graphQL {
		check.Status = PodReadbackCheckAgree
	} else {
		check.Status = PodReadbackCheckDisagree
	}
	return check
}

func restMachineID(pod *Pod) string {
	if strings.TrimSpace(pod.MachineID) != "" {
		return pod.MachineID
	}
	if pod.Machine != nil {
		return pod.Machine.ID
	}
	return ""
}

func graphQLMachineID(pod *PodGraphQLSnapshot) string {
	if strings.TrimSpace(pod.MachineID) != "" {
		return pod.MachineID
	}
	if pod.Machine != nil {
		return pod.Machine.ID
	}
	return ""
}

func restGPUTypeID(pod *Pod) string {
	if pod.Machine != nil && !strings.EqualFold(strings.TrimSpace(pod.Machine.GPUTypeID), "unknown") {
		return pod.Machine.GPUTypeID
	}
	if pod.GPU != nil {
		return pod.GPU.ID
	}
	return ""
}

func graphQLGPUTypeID(pod *PodGraphQLSnapshot) string {
	if pod.Machine == nil || strings.EqualFold(strings.TrimSpace(pod.Machine.GPUTypeID), "unknown") {
		return ""
	}
	return pod.Machine.GPUTypeID
}

func restDataCenterID(pod *Pod) string {
	if pod.Machine == nil {
		return ""
	}
	return pod.Machine.DataCenterID
}

func graphQLDataCenterID(pod *PodGraphQLSnapshot) string {
	if pod.Machine == nil {
		return ""
	}
	return pod.Machine.DataCenterID
}

func restNetworkVolumeID(pod *Pod) string {
	if strings.TrimSpace(pod.NetworkVolumeID) != "" {
		return pod.NetworkVolumeID
	}
	if pod.NetworkVolume != nil {
		return pod.NetworkVolume.ID
	}
	return ""
}

func graphQLNetworkVolumeID(pod *PodGraphQLSnapshot) string {
	if strings.TrimSpace(pod.NetworkVolumeID) != "" {
		return pod.NetworkVolumeID
	}
	if pod.NetworkVolume != nil {
		return pod.NetworkVolume.ID
	}
	return ""
}

func normalizeRESTPorts(ports []string) string {
	normalized := make([]string, 0, len(ports))
	for _, port := range ports {
		if value := strings.ToLower(strings.TrimSpace(port)); value != "" {
			normalized = append(normalized, value)
		}
	}
	sort.Strings(normalized)
	return strings.Join(normalized, ",")
}

func normalizeGraphQLPorts(ports string) string {
	return normalizeRESTPorts(strings.Split(ports, ","))
}

func formatRuntimePorts(ports []PodRuntimePort) string {
	rows := make([]string, 0, len(ports))
	for _, port := range ports {
		rows = append(rows, fmt.Sprintf("%s:%d->%d/%s(public=%t)", strings.TrimSpace(port.IP), port.PublicPort,
			port.PrivatePort, strings.ToLower(strings.TrimSpace(port.Type)), port.IsIPPublic))
	}
	sort.Strings(rows)
	return strings.Join(rows, ",")
}

func formatRESTPortMappings(ports map[string]int) string {
	rows := make([]string, 0, len(ports))
	for privatePort, publicPort := range ports {
		rows = append(rows, fmt.Sprintf("%s->%d", strings.TrimSpace(privatePort), publicPort))
	}
	sort.Strings(rows)
	return strings.Join(rows, ",")
}

func validateRuntimePorts(pod *Pod, ports []PodRuntimePort) (string, error) {
	requested := make(map[int]struct{}, len(pod.Ports))
	for _, port := range pod.Ports {
		value := strings.ToLower(strings.TrimSpace(port))
		privateText, protocol, found := strings.Cut(value, "/")
		if !found || protocol != "tcp" {
			continue
		}
		privatePort, err := strconv.Atoi(privateText)
		if err != nil || privatePort <= 0 || privatePort > 65535 || strconv.Itoa(privatePort) != privateText {
			return "", fmt.Errorf("invalid REST-requested TCP port %q", port)
		}
		requested[privatePort] = struct{}{}
	}
	seen := map[int]PodRuntimePort{}
	restPublicIP := strings.TrimSpace(pod.PublicIP)
	graphQLPublicIP := ""
	for _, port := range ports {
		if !port.IsIPPublic || !strings.EqualFold(strings.TrimSpace(port.Type), "tcp") {
			continue
		}
		if net.ParseIP(strings.TrimSpace(port.IP)) == nil || port.PrivatePort <= 0 || port.PrivatePort > 65535 ||
			port.PublicPort <= 0 || port.PublicPort > 65535 {
			return "", fmt.Errorf("invalid GraphQL public TCP mapping %+v", port)
		}
		mappingIP := strings.TrimSpace(port.IP)
		if graphQLPublicIP == "" {
			graphQLPublicIP = mappingIP
		} else if mappingIP != graphQLPublicIP {
			return "", fmt.Errorf("GraphQL public TCP mappings disagree on IP: %q and %q", graphQLPublicIP, mappingIP)
		}
		if restPublicIP != "" && mappingIP != restPublicIP {
			return "", fmt.Errorf("GraphQL mapping IP %q disagrees with REST publicIp %q", mappingIP, restPublicIP)
		}
		if _, ok := requested[port.PrivatePort]; !ok {
			return "", fmt.Errorf("GraphQL public mapping %d/tcp was not one of the REST-requested ports", port.PrivatePort)
		}
		if restPublicPort, ok := pod.PortMappings[strconv.Itoa(port.PrivatePort)]; ok && restPublicPort != port.PublicPort {
			return "", fmt.Errorf("GraphQL public mapping %d->%d disagrees with REST portMappings value %d", port.PrivatePort, port.PublicPort, restPublicPort)
		}
		if _, ok := seen[port.PrivatePort]; ok {
			return "", fmt.Errorf("GraphQL returned multiple public mappings for private port %d", port.PrivatePort)
		}
		seen[port.PrivatePort] = port
	}
	for privatePort := range requested {
		if _, ok := seen[privatePort]; !ok {
			return "", fmt.Errorf("REST-requested port %d/tcp has no GraphQL public mapping", privatePort)
		}
	}
	for privateText, publicPort := range pod.PortMappings {
		privatePort, err := strconv.Atoi(strings.TrimSpace(privateText))
		if err != nil || privatePort <= 0 || privatePort > 65535 || publicPort <= 0 || publicPort > 65535 {
			return "", fmt.Errorf("invalid REST portMappings entry %q->%d", privateText, publicPort)
		}
		mapping, ok := seen[privatePort]
		if !ok {
			return "", fmt.Errorf("REST portMappings entry %d->%d has no GraphQL public TCP mapping", privatePort, publicPort)
		}
		if mapping.PublicPort != publicPort {
			return "", fmt.Errorf("REST portMappings entry %d->%d disagrees with GraphQL public port %d", privatePort, publicPort, mapping.PublicPort)
		}
	}
	return graphQLPublicIP, nil
}

// PublicTCPPortMappings returns GraphQL's observed public TCP mappings keyed
// by private container port. It does not claim the mappings belong to a
// particular container incarnation. The returned map is a copy.
func (r *PodReadback) PublicTCPPortMappings() map[int]int {
	out := map[int]int{}
	if r == nil || r.GraphQL == nil || r.GraphQL.Runtime == nil {
		return out
	}
	for _, port := range r.GraphQL.Runtime.Ports {
		if port.IsIPPublic && strings.EqualFold(strings.TrimSpace(port.Type), "tcp") &&
			port.PrivatePort > 0 && port.PrivatePort <= 65535 && port.PublicPort > 0 && port.PublicPort <= 65535 {
			out[port.PrivatePort] = port.PublicPort
		}
	}
	return out
}
