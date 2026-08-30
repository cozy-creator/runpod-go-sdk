package runpod

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// validateCreatePodRequest is unexported, so reach it through CreatePod with a
// nil transport — validation runs before any HTTP call. A nil-client setup
// works for validation-only checks because validateCreatePodRequest doesn't
// touch the network. We construct a minimal client instead.

func newValidationClient() *Client {
	c, err := NewClient("test-key")
	if err != nil {
		panic(err)
	}
	return c
}

func TestValidateCreatePodRequest_GPU_RequiresGPUFields(t *testing.T) {
	c := newValidationClient()
	cases := []struct {
		name string
		req  *CreatePodRequest
		want string // substring expected in error message
	}{
		{
			name: "missing gpuTypeIds",
			req: &CreatePodRequest{
				Name:              "n",
				ImageName:         "img",
				ContainerDiskInGB: 10,
				GPUCount:          1,
			},
			want: "gpuTypeId",
		},
		{
			name: "missing gpuCount",
			req: &CreatePodRequest{
				Name:              "n",
				ImageName:         "img",
				ContainerDiskInGB: 10,
				GPUTypeIDs:        []string{"NVIDIA GeForce RTX 4090"},
			},
			want: "gpuCount",
		},
		{
			name: "explicit GPU with cpuFlavorIds rejected",
			req: &CreatePodRequest{
				Name:              "n",
				ImageName:         "img",
				ContainerDiskInGB: 10,
				ComputeType:       "GPU",
				GPUTypeIDs:        []string{"NVIDIA GeForce RTX 4090"},
				GPUCount:          1,
				CPUFlavorIDs:      []string{"cpu5c"},
			},
			want: "cpuFlavorIds",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := c.validateCreatePodRequest(tc.req)
			if err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("expected error to mention %q, got %q", tc.want, err.Error())
			}
		})
	}
}

func TestValidateCreatePodRequest_CPU_AllowsMinimal(t *testing.T) {
	c := newValidationClient()
	req := &CreatePodRequest{
		Name:              "n",
		ImageName:         "img",
		ContainerDiskInGB: 10,
		ComputeType:       "CPU",
	}
	if err := c.validateCreatePodRequest(req); err != nil {
		t.Fatalf("CPU pod without selector should pass validation, got: %v", err)
	}
}

func TestValidateCreatePodRequest_CPU_AllowsFlavorIDs(t *testing.T) {
	c := newValidationClient()
	req := &CreatePodRequest{
		Name:              "n",
		ImageName:         "img",
		ContainerDiskInGB: 10,
		ComputeType:       "CPU",
		CPUFlavorIDs:      []string{"cpu5c", "cpu3c"},
		CPUFlavorPriority: CPUFlavorPriorityCustom,
	}
	if err := c.validateCreatePodRequest(req); err != nil {
		t.Fatalf("CPU pod with cpuFlavorIds should pass, got: %v", err)
	}
}

func TestPrepareCreatePod_CPUFlavorPriorityRoundTrips(t *testing.T) {
	c := newValidationClient()
	prepared, err := c.PrepareCreatePod(&CreatePodRequest{
		Name: "n", ImageName: "img", ContainerDiskInGB: 10, ComputeType: "CPU",
		CPUFlavorIDs: []string{"cpu5c", "cpu3c"}, CPUFlavorPriority: CPUFlavorPriorityCustom,
		VCPUCount: 2,
	})
	if err != nil {
		t.Fatalf("PrepareCreatePod: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(prepared, &wire); err != nil {
		t.Fatalf("decode prepared create: %v", err)
	}
	if wire["cpuFlavorPriority"] != CPUFlavorPriorityCustom {
		t.Fatalf("cpuFlavorPriority = %#v", wire["cpuFlavorPriority"])
	}
	if _, present := wire["gpuCount"]; present {
		t.Fatalf("CPU create carries gpuCount: %s", prepared)
	}
	inspected, err := c.InspectPreparedCreatePod(prepared)
	if err != nil || inspected.CPUFlavorPriority != CPUFlavorPriorityCustom || inspected.VCPUCount != 2 {
		t.Fatalf("InspectPreparedCreatePod = %+v, %v", inspected, err)
	}
}

func TestValidateCreatePodRequest_CPUFlavorPriority(t *testing.T) {
	c := newValidationClient()
	for name, req := range map[string]*CreatePodRequest{
		"unknown": {
			Name: "n", ImageName: "img", ContainerDiskInGB: 10, ComputeType: "CPU",
			CPUFlavorPriority: "cheapest",
		},
		"custom without flavors": {
			Name: "n", ImageName: "img", ContainerDiskInGB: 10, ComputeType: "CPU",
			CPUFlavorPriority: CPUFlavorPriorityCustom,
		},
		"priority on GPU": {
			Name: "n", ImageName: "img", ContainerDiskInGB: 10, ComputeType: "GPU",
			GPUTypeIDs: []string{"gpu"}, GPUCount: 1,
			CPUFlavorPriority: CPUFlavorPriorityAvailability,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := c.validateCreatePodRequest(req); err == nil {
				t.Fatal("expected CPU flavor priority refusal")
			}
		})
	}
}

func TestValidateCreatePodRequest_CPU_ForbidsGPUFields(t *testing.T) {
	c := newValidationClient()
	cases := []struct {
		name string
		req  *CreatePodRequest
		want string
	}{
		{
			name: "GPUTypeIDs forbidden",
			req: &CreatePodRequest{
				Name: "n", ImageName: "img", ContainerDiskInGB: 10,
				ComputeType: "CPU",
				GPUTypeIDs:  []string{"NVIDIA GeForce RTX 4090"},
			},
			want: "gpuTypeIds",
		},
		{
			name: "GPUCount forbidden",
			req: &CreatePodRequest{
				Name: "n", ImageName: "img", ContainerDiskInGB: 10,
				ComputeType: "CPU",
				GPUCount:    1,
			},
			want: "gpuCount",
		},
		{
			name: "MinRAMPerGPU forbidden",
			req: &CreatePodRequest{
				Name: "n", ImageName: "img", ContainerDiskInGB: 10,
				ComputeType:  "CPU",
				MinRAMPerGPU: 32,
			},
			want: "minRAMPerGPU",
		},
		{
			name: "MinVCPUPerGPU forbidden",
			req: &CreatePodRequest{
				Name: "n", ImageName: "img", ContainerDiskInGB: 10,
				ComputeType:   "CPU",
				MinVCPUPerGPU: 8,
			},
			want: "minVCPUPerGPU",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := c.validateCreatePodRequest(tc.req)
			if err == nil {
				t.Fatalf("expected validation error for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("expected error to mention %q, got %q", tc.want, err.Error())
			}
		})
	}
}

func TestValidateCreatePodRequest_GPUResourceMinima(t *testing.T) {
	c := newValidationClient()
	base := CreatePodRequest{
		Name:              "n",
		ImageName:         "img",
		ContainerDiskInGB: 10,
		GPUTypeIDs:        []string{"NVIDIA GeForce RTX 4090"},
		GPUCount:          1,
	}

	valid := base
	valid.MinRAMPerGPU = 32
	valid.MinVCPUPerGPU = 8
	if err := c.validateCreatePodRequest(&valid); err != nil {
		t.Fatalf("positive GPU resource minima should pass validation: %v", err)
	}

	cases := []struct {
		name  string
		field string
		set   func(*CreatePodRequest)
	}{
		{
			name:  "negative RAM",
			field: "minRAMPerGPU",
			set:   func(req *CreatePodRequest) { req.MinRAMPerGPU = -1 },
		},
		{
			name:  "negative vCPU",
			field: "minVCPUPerGPU",
			set:   func(req *CreatePodRequest) { req.MinVCPUPerGPU = -1 },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			tc.set(&req)
			err := c.validateCreatePodRequest(&req)
			if err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("expected %s validation error, got %v", tc.field, err)
			}
		})
	}
}

func TestValidateCreatePodRequestDownloadFloor(t *testing.T) {
	c := newValidationClient()
	req := CreatePodRequest{
		Name: "n", ImageName: "img", ContainerDiskInGB: 10,
		GPUTypeIDs: []string{"NVIDIA GeForce RTX 4090"}, GPUCount: 1,
		MinDownloadMbps: 2000,
	}
	if err := c.validateCreatePodRequest(&req); err != nil {
		t.Fatalf("positive download floor should pass validation: %v", err)
	}
	req.MinDownloadMbps = -1
	if err := c.validateCreatePodRequest(&req); err == nil || !strings.Contains(err.Error(), "minDownloadMbps") {
		t.Fatalf("expected minDownloadMbps validation error, got %v", err)
	}
}

func TestSelectCPUFamilies_NoFilter(t *testing.T) {
	got := SelectCPUFamilies("", "")
	if len(got) == 0 {
		t.Fatalf("expected non-empty fallback chain")
	}
	if got[0] != "cpu5c" {
		t.Errorf("expected current-generation cpu5c first, got %q", got[0])
	}
}

func TestCPUFamilies_CurrentRESTVocabulary(t *testing.T) {
	want := []string{"cpu5c", "cpu5g", "cpu5m", "cpu3c", "cpu3g", "cpu3m"}
	families := CPUFamilies()
	got := make([]string, len(families))
	for i, family := range families {
		got[i] = family.ID
		if resolved, ok := CPUFamilyByID(family.ID); !ok || resolved != family {
			t.Fatalf("CPUFamilyByID(%q) = %+v, %t", family.ID, resolved, ok)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CPU family catalog = %v, want %v", got, want)
	}
}

func TestSelectCPUFamilies_FamilyFilter(t *testing.T) {
	got := SelectCPUFamilies("", "compute")
	for _, id := range got {
		if id == "cpu3g" {
			t.Errorf("compute filter should exclude general-family cpu3g; got %v", got)
		}
	}
	if len(got) == 0 {
		t.Errorf("expected at least one compute family")
	}
}

func TestSelectCPUFamilies_PreferenceMovesToFront(t *testing.T) {
	got := SelectCPUFamilies("cpu3c", "")
	if got[0] != "cpu3c" {
		t.Errorf("expected cpu3c first due to preference, got %v", got)
	}
	// The rest of the chain should still be present (no dups).
	seen := map[string]int{}
	for _, id := range got {
		seen[id]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("family %q appeared %d times; chain should be dedup'd", id, n)
		}
	}
}

func TestSelectCPUFamilies_ImpossibleFilterReturnsEmpty(t *testing.T) {
	got := SelectCPUFamilies("", "nonsense")
	if len(got) != 0 {
		t.Errorf("expected empty chain for unknown family filter, got %v", got)
	}
}

func TestDefaultCPUFlavorIDs_Stable(t *testing.T) {
	a := DefaultCPUFlavorIDs()
	b := DefaultCPUFlavorIDs()
	if !reflect.DeepEqual(a, b) {
		t.Errorf("DefaultCPUFlavorIDs non-deterministic: %v vs %v", a, b)
	}
}
