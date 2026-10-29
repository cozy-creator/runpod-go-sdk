// Package runpodtest provides a REST v2 control plane and separate Serverless
// inference fixture. It serves no legacy resource paths or query transport.
package runpodtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

type fault struct {
	status     int
	body       string
	retryAfter string
}
type Server struct {
	httpServer *httptest.Server
	mu         sync.Mutex
	nextID     int
	pods       map[string]*runpod.Pod
	podLogs    map[string][]runpod.PodLogEntry
	volumes    map[string]*runpod.NetworkVolume
	auths      map[string]*runpod.ContainerRegistryAuth
	secrets    map[string]*runpod.Secret
	jobs       map[string]*fakeJob
	stockOut   map[string]bool
	gpuTypes   []runpod.GPUType
	authz      []string
	faults     []fault
}

type fakeJob struct {
	ID         string          `json:"id"`
	Status     string          `json:"status"`
	Input      json.RawMessage `json:"input,omitempty"`
	Output     json.RawMessage `json:"output,omitempty"`
	Error      string          `json:"error,omitempty"`
	EndpointID string          `json:"endpointId,omitempty"`
}

func New() *Server {
	s := &Server{pods: map[string]*runpod.Pod{}, podLogs: map[string][]runpod.PodLogEntry{}, volumes: map[string]*runpod.NetworkVolume{}, auths: map[string]*runpod.ContainerRegistryAuth{}, secrets: map[string]*runpod.Secret{}, jobs: map[string]*fakeJob{}, stockOut: map[string]bool{}}
	price := runpod.USDMicrosPerHour(300000)
	for _, spec := range runpod.GPUCatalog() {
		s.gpuTypes = append(s.gpuTypes, runpod.GPUType{ID: spec.ID, Name: spec.DisplayName, Memory: spec.VRAMGB, Manufacturer: "NVIDIA", Secure: true, Community: spec.Consumer, Price: runpod.GPUPrices{Secure: &price, Community: &price}, MaxCount: runpod.GPUCounts{Secure: 8, Community: 4}, Availability: "HIGH"})
	}
	s.httpServer = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *Server) URL() string { return s.httpServer.URL }
func (s *Server) Close()      { s.httpServer.Close() }
func (s *Server) Client(opts ...runpod.ClientOption) (*runpod.Client, error) {
	return s.ClientWithAPIKey("runpodtest-key", opts...)
}
func (s *Server) ClientWithAPIKey(key string, opts ...runpod.ClientOption) (*runpod.Client, error) {
	base := []runpod.ClientOption{runpod.WithBaseURL(s.URL() + "/v2"), runpod.WithServerlessBaseURL(s.URL())}
	return runpod.NewClient(key, append(base, opts...)...)
}
func (s *Server) MustClient(opts ...runpod.ClientOption) *runpod.Client {
	c, err := s.Client(opts...)
	if err != nil {
		panic(err)
	}
	return c
}
func (s *Server) SetGPUStockOut(id string, out bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stockOut[id] = out
}
func (s *Server) FailNext(status int, body, retryAfter string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = append(s.faults, fault{status, body, retryAfter})
}
func (s *Server) SetGPUTypes(types []runpod.GPUType) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gpuTypes = append([]runpod.GPUType(nil), types...)
}

// AuthorizationHeaders returns the credentials observed by the fake in
// request order. It is intended for tests that need to prove credential
// rotation reached the provider boundary.
func (s *Server) AuthorizationHeaders() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.authz...)
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
	writeJSON(w, status, map[string]any{"title": http.StatusText(status), "status": status, "detail": msg})
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if len(s.faults) > 0 {
		f := s.faults[0]
		s.faults = s.faults[1:]
		s.mu.Unlock()
		if f.retryAfter != "" {
			w.Header().Set("Retry-After", f.retryAfter)
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(f.status)
		fmt.Fprint(w, f.body)
		return
	}
	s.authz = append(s.authz, r.Header.Get("Authorization"))
	s.mu.Unlock()
	if r.Header.Get("Authorization") == "" {
		writeErr(w, 401, "unauthorized")
		return
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	if strings.HasPrefix(path, "/v2/pods/") && strings.HasSuffix(path, "/logs") {
		s.handlePodLogs(w, r, path)
		return
	}
	switch {
	case path == "/v2/pods" || strings.HasPrefix(path, "/v2/pods/"):
		s.handlePods(w, r, strings.TrimPrefix(path, "/v2"))
	case path == "/v2/network-volumes" || strings.HasPrefix(path, "/v2/network-volumes/"):
		s.handleVolumes(w, r, strings.TrimPrefix(path, "/v2"))
	case path == "/v2/registries" || strings.HasPrefix(path, "/v2/registries/"):
		s.handleRegistryAuths(w, r, strings.TrimPrefix(path, "/v2"))
	case path == "/v2/catalog/gpus" && r.Method == http.MethodGet:
		s.mu.Lock()
		defer s.mu.Unlock()
		types := append([]runpod.GPUType(nil), s.gpuTypes...)
		if r.URL.Query().Get("include") == "AVAILABILITY" {
			for i := range types {
				if s.stockOut[types[i].ID] {
					types[i].Availability = "NONE"
				}
			}
		}
		writeJSON(w, 200, map[string]any{"gpus": types})
	case strings.HasPrefix(path, "/v2/catalog/gpus/") && r.Method == http.MethodGet:
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, gpu := range s.gpuTypes {
			if gpu.ID == strings.TrimPrefix(path, "/v2/catalog/gpus/") {
				writeJSON(w, 200, gpu)
				return
			}
		}
		writeErr(w, 404, "GPU type not found")
	case path == "/v2/catalog/cpus" && r.Method == http.MethodGet:
		writeJSON(w, 200, map[string]any{"cpus": []any{}})
	case path == "/v2/catalog/datacenters" && r.Method == http.MethodGet:
		writeJSON(w, 200, map[string]any{"dataCenters": []any{}})
	case path == "/v2/account/secrets" || strings.HasPrefix(path, "/v2/account/secrets/"):
		s.handleSecrets(w, r, strings.TrimPrefix(path, "/v2/account/secrets"))
	case strings.HasPrefix(path, "/v2/"):
		s.handleServerless(w, r, path)
	default:
		writeErr(w, 404, "route not found")
	}
}

func fixturePage[T any](w http.ResponseWriter, r *http.Request, key string, rows []T) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 1000
	}
	if offset < 0 || offset > len(rows) {
		writeErr(w, 400, "invalid cursor")
		return
	}
	end := min(offset+limit, len(rows))
	var next *string
	if end < len(rows) {
		cursor := strconv.Itoa(end)
		next = &cursor
	}
	writeJSON(w, 200, map[string]any{key: rows[offset:end], "pagination": map[string]any{"nextCursor": next, "hasNextPage": next != nil}})
}

func (s *Server) handlePods(w http.ResponseWriter, r *http.Request, path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodPost:
			var req runpod.CreatePodRequest
			dec := json.NewDecoder(r.Body)
			dec.DisallowUnknownFields()
			if err := dec.Decode(&req); err != nil {
				writeErr(w, 400, "invalid body")
				return
			}
			if req.GPU != nil && s.stockOut[req.GPU.ID] {
				writeErr(w, 400, "There are no longer any instances available")
				return
			}
			if req.Mounts != nil {
				for _, mount := range req.Mounts.Network {
					volume := s.volumes[mount.VolumeID]
					if volume == nil {
						writeErr(w, 400, "network volume not found")
						return
					}
					if req.Cloud != "" && req.Cloud != "SECURE" {
						writeErr(w, 400, "network volumes require Secure Cloud")
						return
					}
					if len(req.DataCenterIDs) > 0 && (len(req.DataCenterIDs) != 1 || req.DataCenterIDs[0] != volume.DataCenter) {
						writeErr(w, 400, "network volume datacenter mismatch")
						return
					}
				}
			}
			now := &runpod.JSONTime{Time: time.Now().UTC()}
			pod := &runpod.Pod{ID: s.newID("pod"), Name: req.Name, Status: "RUNNING", Actions: []string{"stop", "restart", "terminate"}, Image: req.Image, Args: req.Args, Entrypoint: req.Entrypoint, Cmd: req.Cmd, Disk: req.Disk, Ports: req.Ports, Env: req.Env, Mounts: req.Mounts, Cloud: req.Cloud, CreatedAt: now, StartedAt: now}
			if pod.Cloud == "" {
				pod.Cloud = "SECURE"
			}
			if req.Registry != "" {
				pod.Registry = &req.Registry
			}
			if req.GPU != nil {
				count := req.GPU.Count
				if count == 0 {
					count = 1
				}
				pod.GPU = &runpod.PodGPU{ID: req.GPU.ID, Count: count}
			}
			if req.CPU != nil {
				pod.CPU = &runpod.PodCPU{ID: req.CPU.ID, VCPUCount: req.CPU.VCPUCount}
			}
			if len(req.DataCenterIDs) > 0 {
				id := req.DataCenterIDs[0]
				pod.DataCenterID = &id
			}
			if pod.Env == nil {
				pod.Env = map[string]string{}
			}
			s.pods[pod.ID] = pod
			writeJSON(w, 201, pod)
		case http.MethodGet:
			rows := make([]*runpod.Pod, 0, len(s.pods))
			for _, p := range s.pods {
				copy := *p
				copy.Runtime = nil
				rows = append(rows, &copy)
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
			fixturePage(w, r, "pods", rows)
		default:
			writeErr(w, 404, "route not found")
		}
		return
	}
	pod := s.pods[parts[1]]
	if pod == nil {
		writeErr(w, 404, "pod not found")
		return
	}
	switch {
	case len(parts) == 2 && r.Method == http.MethodGet:
		writeJSON(w, 200, pod)
	case len(parts) == 2 && r.Method == http.MethodDelete:
		delete(s.pods, pod.ID)
		w.WriteHeader(204)
	case len(parts) == 2 && r.Method == http.MethodPatch:
		var patch runpod.UpdatePodRequest
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			writeErr(w, 400, "invalid body")
			return
		}
		if patch.Name != nil {
			pod.Name = *patch.Name
		}
		if patch.Image != nil {
			pod.Image = *patch.Image
		}
		if patch.Disk != nil {
			pod.Disk = *patch.Disk
		}
		if patch.Env != nil {
			pod.Env = *patch.Env
		}
		if patch.Ports != nil {
			pod.Ports = *patch.Ports
		}
		if patch.Registry != nil {
			pod.Registry = patch.Registry
		}
		if patch.Locked != nil {
			pod.Locked = *patch.Locked
		}
		writeJSON(w, 200, pod)
	case len(parts) == 3 && parts[2] == "action" && r.Method == http.MethodPost:
		var action struct {
			Action string `json:"action"`
		}
		if err := json.NewDecoder(r.Body).Decode(&action); err != nil {
			writeErr(w, 400, "invalid action")
			return
		}
		switch action.Action {
		case "stop":
			pod.Status = "EXITED"
			pod.Runtime = nil
		case "start", "restart":
			pod.Status = "RUNNING"
		case "terminate":
			delete(s.pods, pod.ID)
		default:
			writeErr(w, 400, "invalid action")
			return
		}
		writeJSON(w, 200, pod)
	default:
		writeErr(w, 404, "route not found")
	}
}

func (s *Server) handleVolumes(w http.ResponseWriter, r *http.Request, path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			rows := make([]*runpod.NetworkVolume, 0, len(s.volumes))
			for _, v := range s.volumes {
				rows = append(rows, v)
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
			fixturePage(w, r, "networkVolumes", rows)
		case http.MethodPost:
			var req runpod.CreateNetworkVolumeRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeErr(w, 400, "invalid body")
				return
			}
			tier := req.Type
			if tier == "" {
				tier = "STANDARD"
			}
			v := &runpod.NetworkVolume{ID: s.newID("vol"), Name: req.Name, Size: req.Size, DataCenter: req.DataCenter, Type: tier}
			s.volumes[v.ID] = v
			writeJSON(w, 201, v)
		default:
			writeErr(w, 404, "route not found")
		}
		return
	}
	v := s.volumes[parts[1]]
	if v == nil {
		writeErr(w, 404, "network volume not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, 200, v)
	case http.MethodPatch:
		var req runpod.UpdateNetworkVolumeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid body")
			return
		}
		if req.Size != 0 && req.Size < v.Size {
			writeErr(w, 400, "network volumes can only grow")
			return
		}
		if req.Size > 0 {
			v.Size = req.Size
		}
		if req.Name != "" {
			v.Name = req.Name
		}
		writeJSON(w, 200, v)
	case http.MethodDelete:
		for _, p := range s.pods {
			if p.Mounts != nil {
				for _, m := range p.Mounts.Network {
					if m.VolumeID == v.ID {
						writeErr(w, 409, "network volume is attached to a pod")
						return
					}
				}
			}
		}
		delete(s.volumes, v.ID)
		w.WriteHeader(204)
	default:
		writeErr(w, 404, "route not found")
	}
}

func (s *Server) handleRegistryAuths(w http.ResponseWriter, r *http.Request, path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		rows := make([]*runpod.ContainerRegistryAuth, 0, len(s.auths))
		for _, a := range s.auths {
			rows = append(rows, a)
		}
		writeJSON(w, 200, map[string]any{"registries": rows})
	case len(parts) == 1 && r.Method == http.MethodPost:
		var req runpod.CreateContainerRegistryAuthRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid body")
			return
		}
		for _, a := range s.auths {
			if a.Name == req.Name {
				writeErr(w, 400, "name already exists")
				return
			}
		}
		a := &runpod.ContainerRegistryAuth{ID: s.newID("auth"), Name: req.Name}
		s.auths[a.ID] = a
		writeJSON(w, 201, a)
	case len(parts) == 2 && r.Method == http.MethodGet:
		a := s.auths[parts[1]]
		if a == nil {
			writeErr(w, 404, "registry not found")
			return
		}
		writeJSON(w, 200, a)
	case len(parts) == 2 && r.Method == http.MethodDelete:
		if s.auths[parts[1]] == nil {
			writeErr(w, 404, "registry not found")
			return
		}
		delete(s.auths, parts[1])
		w.WriteHeader(204)
	default:
		writeErr(w, 404, "route not found")
	}
}

func (s *Server) handleSecrets(w http.ResponseWriter, r *http.Request, path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := strings.TrimPrefix(path, "/")
	if id == "" {
		switch r.Method {
		case http.MethodGet:
			rows := make([]*runpod.Secret, 0, len(s.secrets))
			for _, v := range s.secrets {
				if name := r.URL.Query().Get("name"); name == "" || strings.EqualFold(name, v.Name) {
					rows = append(rows, v)
				}
			}
			writeJSON(w, 200, map[string]any{"secrets": rows})
		case http.MethodPost:
			var req runpod.CreateSecretRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeErr(w, 400, "invalid body")
				return
			}
			now := &runpod.JSONTime{Time: time.Now().UTC()}
			v := &runpod.Secret{ID: s.newID("secret"), Name: req.Name, CreatedAt: now, ValueLastUpdatedAt: now}
			if req.Description != "" {
				description := req.Description
				v.Description = &description
			}
			s.secrets[v.ID] = v
			writeJSON(w, 201, v)
		default:
			writeErr(w, 404, "route not found")
		}
		return
	}
	v := s.secrets[id]
	if v == nil {
		writeErr(w, 404, "secret not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, 200, v)
	case http.MethodPatch:
		var req runpod.UpdateSecretRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid body")
			return
		}
		if req.Description != nil {
			v.Description = req.Description
		}
		if req.Value != "" {
			v.ValueLastUpdatedAt = &runpod.JSONTime{Time: time.Now().UTC()}
		}
		writeJSON(w, 200, v)
	case http.MethodDelete:
		delete(s.secrets, id)
		w.WriteHeader(204)
	default:
		writeErr(w, 404, "route not found")
	}
}

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
