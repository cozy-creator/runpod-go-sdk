// Package runpodtest provides an in-process fake RunPod API server for
// consumer tests. It implements the endpoints the SDK covers — pods CRUD
// with per-GPU-type stock-out injection, network volumes, container registry
// auths, the serverless job lifecycle, and GraphQL gpuTypes/pod lifecycle
// queries — plus
// one-shot fault injection (429/500) for retry-path testing.
//
// Usage:
//
//	srv := runpodtest.New()
//	defer srv.Close()
//	client := srv.MustClient()
//	srv.SetGPUStockOut("NVIDIA GeForce RTX 4090", true)
//	_, err := client.CreatePod(ctx, req) // 500 "no instances available"
package runpodtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

// removedLowestPriceField matches LowestPrice selections RunPod deleted from
// its live schema (word-bounded so uninterruptablePrice / minCudaVersion
// don't match).
var removedLowestPriceField = regexp.MustCompile(`\b(interruptablePrice|cudaVersion)\b`)

type fault struct {
	status     int
	body       string
	retryAfter string
}

// Server is a fake RunPod API (REST + serverless + GraphQL) backed by
// in-memory state. Safe for concurrent use.
type Server struct {
	httpServer *httptest.Server

	mu        sync.Mutex
	nextID    int
	pods      map[string]*runpod.Pod
	volumes   map[string]*runpod.NetworkVolume
	auths     map[string]*runpod.ContainerRegistryAuth
	jobs      map[string]*fakeJob // key: endpointID + "/" + jobID
	stockOut  map[string]bool     // GPU type ID -> out of stock
	gpuTypes  []runpod.GPUType
	lifecycle map[string]*runpod.PodLifecycleObservation
	accountID string
	faults    []fault // queued one-shot injected responses
}

type fakeJob struct {
	ID         string          `json:"id"`
	Status     string          `json:"status"`
	Input      json.RawMessage `json:"input,omitempty"`
	Output     json.RawMessage `json:"output,omitempty"`
	Error      string          `json:"error,omitempty"`
	EndpointID string          `json:"endpointId,omitempty"`
}

// New starts a fake RunPod server. Call Close when done.
func New() *Server {
	s := &Server{
		pods:      map[string]*runpod.Pod{},
		volumes:   map[string]*runpod.NetworkVolume{},
		auths:     map[string]*runpod.ContainerRegistryAuth{},
		jobs:      map[string]*fakeJob{},
		stockOut:  map[string]bool{},
		lifecycle: map[string]*runpod.PodLifecycleObservation{},
		accountID: "runpodtest-account",
	}
	// Default GPU catalog for gpuTypes queries; override with SetGPUTypes.
	for _, spec := range runpod.GPUCatalog() {
		s.gpuTypes = append(s.gpuTypes, runpod.GPUType{
			ID:             spec.ID,
			DisplayName:    spec.DisplayName,
			MemoryInGB:     spec.VRAMGB,
			SecureCloud:    true,
			CommunityCloud: spec.Consumer,
			LowestPrice: &runpod.Price{
				MinimumBidPrice:      0.10,
				UninterruptablePrice: 0.30,
				StockStatus:          "High",
			},
		})
	}
	s.httpServer = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// URL returns the server's base URL (REST, serverless and GraphQL share it).
func (s *Server) URL() string { return s.httpServer.URL }

// Close shuts the server down.
func (s *Server) Close() { s.httpServer.Close() }

// Client returns an SDK client pointed at this fake server.
func (s *Server) Client(opts ...runpod.ClientOption) (*runpod.Client, error) {
	return s.ClientWithAPIKey("runpodtest-key", opts...)
}

// ClientWithAPIKey returns an SDK client authenticated with apiKey. The fake
// account identity is independent of the credential so consumers can exercise
// API-key rotation without changing provider resource ownership.
func (s *Server) ClientWithAPIKey(apiKey string, opts ...runpod.ClientOption) (*runpod.Client, error) {
	base := []runpod.ClientOption{
		runpod.WithBaseURL(s.httpServer.URL),
		runpod.WithServerlessBaseURL(s.httpServer.URL),
		runpod.WithGraphQLBaseURL(s.httpServer.URL + "/graphql"),
	}
	return runpod.NewClient(apiKey, append(base, opts...)...)
}

// MustClient is Client but panics on error (construction only fails on
// programmer error).
func (s *Server) MustClient(opts ...runpod.ClientOption) *runpod.Client {
	client, err := s.Client(opts...)
	if err != nil {
		panic(err)
	}
	return client
}

// SetGPUStockOut marks a GPU type in or out of stock. Pod creation for an
// out-of-stock type returns RunPod's real-world 500 "no instances
// available" response.
func (s *Server) SetGPUStockOut(gpuTypeID string, out bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stockOut[gpuTypeID] = out
}

// FailNext queues a one-shot injected response (e.g. 429 or 500) served for
// the next API request before normal handling resumes. Call repeatedly to
// queue several. retryAfter, when non-empty, is sent as the Retry-After
// header.
func (s *Server) FailNext(status int, body string, retryAfter string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = append(s.faults, fault{status: status, body: body, retryAfter: retryAfter})
}

// SetGPUTypes replaces the catalog served to GraphQL gpuTypes queries.
func (s *Server) SetGPUTypes(types []runpod.GPUType) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gpuTypes = append([]runpod.GPUType(nil), types...)
}

// SetAccountID replaces the stable ID returned by the authenticated
// `myself` GraphQL query.
func (s *Server) SetAccountID(accountID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accountID = accountID
}

// AddPod seeds a pod into the fake state.
func (s *Server) AddPod(pod *runpod.Pod) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pods[pod.ID] = pod
}

// AddNetworkVolume seeds a provider-owned network volume. The fake enforces
// the same create-time Secure Cloud and datacenter attachment constraints as
// RunPod, so consumer tests exercise their real placement intent.
func (s *Server) AddNetworkVolume(volume *runpod.NetworkVolume) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := *volume
	s.volumes[copy.ID] = &copy
}

// NetworkVolume returns a copy of a seeded/created network volume, or nil.
func (s *Server) NetworkVolume(id string) *runpod.NetworkVolume {
	s.mu.Lock()
	defer s.mu.Unlock()
	volume := s.volumes[id]
	if volume == nil {
		return nil
	}
	copy := *volume
	return &copy
}

// SetPodLifecycleObservation sets the GraphQL lifecycle view for a pod.
// When unset, the fake derives desired status and last-start time from AddPod.
func (s *Server) SetPodLifecycleObservation(observation *runpod.PodLifecycleObservation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := *observation
	if observation.LatestTelemetry != nil {
		telemetry := *observation.LatestTelemetry
		copy.LatestTelemetry = &telemetry
	}
	s.lifecycle[observation.PodID] = &copy
}

// Pod returns a seeded/created pod by ID, or nil.
func (s *Server) Pod(id string) *runpod.Pod {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pods[id]
}

// CompleteJob marks a queued job COMPLETED with the given output.
func (s *Server) CompleteJob(endpointID, jobID string, output interface{}) error {
	raw, err := json.Marshal(output)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[endpointID+"/"+jobID]
	if !ok {
		return fmt.Errorf("runpodtest: no job %s on endpoint %s", jobID, endpointID)
	}
	job.Status = "COMPLETED"
	job.Output = raw
	return nil
}

// FailJob marks a queued job FAILED with the given error message.
func (s *Server) FailJob(endpointID, jobID, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[endpointID+"/"+jobID]
	if !ok {
		return fmt.Errorf("runpodtest: no job %s on endpoint %s", jobID, endpointID)
	}
	job.Status = "FAILED"
	job.Error = message
	return nil
}

func (s *Server) newID(prefix string) string {
	s.nextID++
	return fmt.Sprintf("%s-%d", prefix, s.nextID)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	// Injected faults take priority (retry-path testing).
	s.mu.Lock()
	if len(s.faults) > 0 {
		f := s.faults[0]
		s.faults = s.faults[1:]
		s.mu.Unlock()
		if f.retryAfter != "" {
			w.Header().Set("Retry-After", f.retryAfter)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		fmt.Fprint(w, f.body)
		return
	}
	s.mu.Unlock()

	if r.Header.Get("Authorization") == "" {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	path := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case path == "/graphql":
		s.handleGraphQL(w, r)
	case strings.HasPrefix(path, "/v2/"):
		s.handleServerless(w, r, path)
	case strings.HasPrefix(path, "/pods"):
		s.handlePods(w, r, path)
	case strings.HasPrefix(path, "/networkvolumes"):
		s.handleVolumes(w, r, path)
	case strings.HasPrefix(path, "/containerregistryauth"):
		s.handleRegistryAuths(w, r, path)
	default:
		writeErr(w, http.StatusNotFound, "route not found")
	}
}

// --- pods ---

func (s *Server) handlePods(w http.ResponseWriter, r *http.Request, path string) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/") // pods[, id[, action]]

	switch {
	case r.Method == http.MethodPost && len(parts) == 1:
		var req runpod.CreatePodRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		s.mu.Lock()
		for _, gpuType := range req.GPUTypeIDs {
			if s.stockOut[gpuType] {
				s.mu.Unlock()
				// RunPod's real stock-out shape: 500, not 4xx.
				writeErr(w, http.StatusInternalServerError, "no instances available")
				return
			}
		}
		var attached *runpod.NetworkVolume
		if volumeID := strings.TrimSpace(req.NetworkVolumeID); volumeID != "" {
			volume := s.volumes[volumeID]
			if volume == nil {
				s.mu.Unlock()
				writeErr(w, http.StatusBadRequest, "network volume not found")
				return
			}
			if !strings.EqualFold(strings.TrimSpace(req.CloudType), "SECURE") {
				s.mu.Unlock()
				writeErr(w, http.StatusBadRequest, "network volumes require Secure Cloud")
				return
			}
			if len(req.DataCenterIDs) != 1 || strings.TrimSpace(req.DataCenterIDs[0]) != strings.TrimSpace(volume.DataCenterID) {
				s.mu.Unlock()
				writeErr(w, http.StatusBadRequest, "network volume datacenter mismatch")
				return
			}
			copy := *volume
			attached = &copy
		}
		pod := &runpod.Pod{
			ID:                s.newID("pod"),
			Name:              req.Name,
			DesiredStatus:     "RUNNING",
			ImageName:         req.ImageName,
			GPUCount:          req.GPUCount,
			ContainerDiskInGB: req.ContainerDiskInGB,
			Interruptible:     req.Interruptible,
			Env:               req.Env,
			VolumeMountPath:   req.VolumeMountPath,
			NetworkVolumeID:   req.NetworkVolumeID,
			NetworkVolume:     attached,
		}
		if len(req.GPUTypeIDs) > 0 {
			pod.Machine = &runpod.Machine{ID: s.newID("machine"), GPUTypeID: req.GPUTypeIDs[0]}
			if len(req.DataCenterIDs) > 0 {
				pod.Machine.DataCenterID = strings.TrimSpace(req.DataCenterIDs[0])
			}
		}
		s.pods[pod.ID] = pod
		s.mu.Unlock()
		writeJSON(w, http.StatusCreated, pod)

	case r.Method == http.MethodGet && len(parts) == 1:
		s.mu.Lock()
		pods := make([]*runpod.Pod, 0, len(s.pods))
		for _, p := range s.pods {
			pods = append(pods, p)
		}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]interface{}{"pods": pods})

	case len(parts) >= 2:
		podID := parts[1]
		s.mu.Lock()
		pod := s.pods[podID]
		s.mu.Unlock()
		if pod == nil {
			writeErr(w, http.StatusNotFound, "pod not found")
			return
		}
		switch {
		case r.Method == http.MethodGet && len(parts) == 2:
			writeJSON(w, http.StatusOK, pod)
		case r.Method == http.MethodDelete && len(parts) == 2:
			s.mu.Lock()
			delete(s.pods, podID)
			s.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && len(parts) == 3 && parts[2] == "stop":
			s.mu.Lock()
			pod.DesiredStatus = "EXITED"
			s.mu.Unlock()
			writeJSON(w, http.StatusOK, pod)
		case r.Method == http.MethodPost && len(parts) == 3 && parts[2] == "resume":
			s.mu.Lock()
			pod.DesiredStatus = "RUNNING"
			s.mu.Unlock()
			writeJSON(w, http.StatusOK, pod)
		default:
			writeErr(w, http.StatusNotFound, "route not found")
		}

	default:
		writeErr(w, http.StatusNotFound, "route not found")
	}
}

// --- network volumes ---

func (s *Server) handleVolumes(w http.ResponseWriter, r *http.Request, path string) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")

	switch {
	case r.Method == http.MethodGet && len(parts) == 1:
		s.mu.Lock()
		vols := make([]*runpod.NetworkVolume, 0, len(s.volumes))
		for _, v := range s.volumes {
			vols = append(vols, v)
		}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, vols)

	case r.Method == http.MethodPost && len(parts) == 1:
		var req runpod.CreateNetworkVolumeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		vol := &runpod.NetworkVolume{
			ID:           s.newID("vol"),
			Name:         req.Name,
			Size:         req.Size,
			DataCenterID: req.DataCenterID,
		}
		s.mu.Lock()
		s.volumes[vol.ID] = vol
		s.mu.Unlock()
		writeJSON(w, http.StatusCreated, vol)

	case len(parts) == 2:
		volID := parts[1]
		s.mu.Lock()
		vol := s.volumes[volID]
		s.mu.Unlock()
		if vol == nil {
			writeErr(w, http.StatusNotFound, "network volume not found")
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, vol)
		case http.MethodPatch:
			var req runpod.UpdateNetworkVolumeRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeErr(w, http.StatusBadRequest, "invalid body")
				return
			}
			s.mu.Lock()
			if req.Size < vol.Size && req.Size != 0 {
				s.mu.Unlock()
				writeErr(w, http.StatusBadRequest, "network volumes can only grow")
				return
			}
			if req.Size != 0 {
				vol.Size = req.Size
			}
			if req.Name != "" {
				vol.Name = req.Name
			}
			s.mu.Unlock()
			writeJSON(w, http.StatusOK, vol)
		case http.MethodDelete:
			s.mu.Lock()
			for _, pod := range s.pods {
				if strings.TrimSpace(pod.NetworkVolumeID) == volID {
					s.mu.Unlock()
					writeErr(w, http.StatusConflict, "network volume is attached to a pod")
					return
				}
			}
			delete(s.volumes, volID)
			s.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			writeErr(w, http.StatusNotFound, "route not found")
		}

	default:
		writeErr(w, http.StatusNotFound, "route not found")
	}
}

// --- container registry auths ---

func (s *Server) handleRegistryAuths(w http.ResponseWriter, r *http.Request, path string) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")

	switch {
	case r.Method == http.MethodGet && len(parts) == 1:
		s.mu.Lock()
		auths := make([]*runpod.ContainerRegistryAuth, 0, len(s.auths))
		for _, a := range s.auths {
			auths = append(auths, a)
		}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, auths)

	case r.Method == http.MethodPost && len(parts) == 1:
		var req runpod.CreateContainerRegistryAuthRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		s.mu.Lock()
		for _, a := range s.auths {
			if a.Name == req.Name {
				s.mu.Unlock()
				writeErr(w, http.StatusBadRequest, "name already exists")
				return
			}
		}
		auth := &runpod.ContainerRegistryAuth{ID: s.newID("auth"), Name: req.Name}
		s.auths[auth.ID] = auth
		s.mu.Unlock()
		writeJSON(w, http.StatusCreated, auth)

	case r.Method == http.MethodDelete && len(parts) == 2:
		s.mu.Lock()
		_, ok := s.auths[parts[1]]
		delete(s.auths, parts[1])
		s.mu.Unlock()
		if !ok {
			writeErr(w, http.StatusNotFound, "registry auth not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		writeErr(w, http.StatusNotFound, "route not found")
	}
}

// --- serverless jobs ---

func (s *Server) handleServerless(w http.ResponseWriter, r *http.Request, path string) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/") // v2, endpoint, action[, jobID]
	if len(parts) < 3 {
		writeErr(w, http.StatusNotFound, "route not found")
		return
	}
	endpointID, action := parts[1], parts[2]
	jobKey := func(jobID string) string { return endpointID + "/" + jobID }

	switch {
	case r.Method == http.MethodPost && action == "run":
		var req struct {
			Input json.RawMessage `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		s.mu.Lock()
		job := &fakeJob{ID: s.newID("job"), Status: "IN_QUEUE", Input: req.Input, EndpointID: endpointID}
		s.jobs[jobKey(job.ID)] = job
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, job)

	case r.Method == http.MethodPost && action == "runsync":
		var req struct {
			Input json.RawMessage `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		s.mu.Lock()
		// runsync completes immediately, echoing the input as output.
		job := &fakeJob{ID: s.newID("job"), Status: "COMPLETED", Input: req.Input, Output: req.Input, EndpointID: endpointID}
		s.jobs[jobKey(job.ID)] = job
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, job)

	case r.Method == http.MethodGet && (action == "status" || action == "stream") && len(parts) == 4:
		s.mu.Lock()
		job := s.jobs[jobKey(parts[3])]
		s.mu.Unlock()
		if job == nil {
			writeErr(w, http.StatusNotFound, "job not found")
			return
		}
		writeJSON(w, http.StatusOK, job)

	case r.Method == http.MethodPost && action == "cancel" && len(parts) == 4:
		s.mu.Lock()
		if job := s.jobs[jobKey(parts[3])]; job != nil && job.Status == "IN_QUEUE" {
			job.Status = "CANCELLED"
		}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"message": "cancelled"})

	case r.Method == http.MethodPost && action == "retry" && len(parts) == 4:
		s.mu.Lock()
		job := s.jobs[jobKey(parts[3])]
		if job != nil {
			job.Status = "IN_QUEUE"
			job.Error = ""
			job.Output = nil
		}
		s.mu.Unlock()
		if job == nil {
			writeErr(w, http.StatusNotFound, "job not found")
			return
		}
		writeJSON(w, http.StatusOK, job)

	case r.Method == http.MethodPost && action == "purge-queue":
		s.mu.Lock()
		for key, job := range s.jobs {
			if strings.HasPrefix(key, endpointID+"/") && job.Status == "IN_QUEUE" {
				job.Status = "CANCELLED"
			}
		}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"message": "queue purged"})

	case r.Method == http.MethodGet && action == "health":
		s.mu.Lock()
		queued := 0
		for key, job := range s.jobs {
			if strings.HasPrefix(key, endpointID+"/") && job.Status == "IN_QUEUE" {
				queued++
			}
		}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, runpod.EndpointHealth{Status: "healthy", JobsInQueue: queued})

	default:
		writeErr(w, http.StatusNotFound, "route not found")
	}
}

// --- GraphQL ---

func (s *Server) handleGraphQL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string                 `json:"query"`
		Variables map[string]interface{} `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid graphql request")
		return
	}
	if strings.Contains(req.Query, "myself") {
		s.mu.Lock()
		accountID := s.accountID
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{
				"myself": map[string]string{"id": accountID},
			},
		})
		return
	}
	if strings.Contains(req.Query, "pod(input:") {
		input, _ := req.Variables["input"].(map[string]interface{})
		podID, _ := input["podId"].(string)
		s.mu.Lock()
		observation := s.lifecycle[podID]
		pod := s.pods[podID]
		if observation != nil {
			copy := *observation
			if observation.LatestTelemetry != nil {
				telemetry := *observation.LatestTelemetry
				copy.LatestTelemetry = &telemetry
			}
			observation = &copy
		} else if pod != nil {
			observation = &runpod.PodLifecycleObservation{
				PodID:         pod.ID,
				DesiredStatus: pod.DesiredStatus,
				LastStartedAt: pod.LastStartedAt,
			}
		}
		s.mu.Unlock()

		if observation == nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"data": map[string]interface{}{"pod": nil}})
			return
		}
		result := map[string]interface{}{
			"id":              observation.PodID,
			"desiredStatus":   observation.DesiredStatus,
			"lastStartedAt":   observation.LastStartedAt,
			"latestTelemetry": observation.LatestTelemetry,
		}
		if observation.RuntimeUptimeInSeconds != nil {
			result["runtime"] = map[string]interface{}{"uptimeInSeconds": observation.RuntimeUptimeInSeconds}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"data": map[string]interface{}{"pod": result}})
		return
	}
	if !strings.Contains(req.Query, "gpuTypes") {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"errors": []map[string]string{{"message": "runpodtest: unsupported GraphQL query"}},
		})
		return
	}
	// RunPod removed these LowestPrice fields; reject them like the live API
	// so schema drift fails unit tests too.
	if m := removedLowestPriceField.FindString(req.Query); m != "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"errors": []map[string]interface{}{{
				"message":    fmt.Sprintf("Cannot query field %q on type \"LowestPrice\".", m),
				"extensions": map[string]string{"code": "GRAPHQL_VALIDATION_FAILED"},
			}},
		})
		return
	}

	s.mu.Lock()
	types := append([]runpod.GPUType(nil), s.gpuTypes...)
	s.mu.Unlock()

	// The offers query aliases lowestPrice per cloud; serve both shapes.
	out := make([]map[string]interface{}, 0, len(types))
	for _, g := range types {
		entry := map[string]interface{}{
			"id":             g.ID,
			"displayName":    g.DisplayName,
			"memoryInGb":     g.MemoryInGB,
			"secureCloud":    g.SecureCloud,
			"communityCloud": g.CommunityCloud,
		}
		if strings.Contains(req.Query, "secure:") {
			entry["secure"] = g.LowestPrice
			entry["community"] = g.LowestPrice
		} else {
			entry["lowestPrice"] = g.LowestPrice
		}
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data": map[string]interface{}{"gpuTypes": out},
	})
}
