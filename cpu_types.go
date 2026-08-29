package runpod

import (
	"strings"
)

// CPUFamily identifies a RunPod CPU instance family. Within a family, RunPod
// auto-selects the cheapest instance size that has stock for the requested
// `vcpuCount` / `memoryInGb`. The REST API's `cpuFlavorIds` field accepts only
// these family-level IDs — the granular `cpu5c-2-2` form used by the older
// GraphQL CPU mutation is not accepted by REST.
//
// Source: probed against the live REST API on 2026-05-15 via
// `POST /v1/pods` with `cpuFlavorIds: [...]`. Catalog grows when RunPod adds
// generations.
type CPUFamily struct {
	ID          string // "cpu5c", "cpu5g", "cpu5m", "cpu3c", "cpu3g", "cpu3m"
	Family      string // "compute" | "general" | "memory"
	Description string // short human-readable note about the family
}

// runpodCPUFamilyCatalog is the static catalog of known CPU families in curated
// fallback order. It carries no price: authoritative price must come from a
// current provider observation.
var runpodCPUFamilyCatalog = []CPUFamily{
	{ID: "cpu5c", Family: "compute", Description: "Intel Xeon Gold, 5th-gen compute-optimized"},
	{ID: "cpu5g", Family: "general", Description: "5th-gen general-purpose"},
	{ID: "cpu5m", Family: "memory", Description: "5th-gen memory-optimized"},
	{ID: "cpu3c", Family: "compute", Description: "older compute-optimized; 2GB RAM per vCPU"},
	{ID: "cpu3g", Family: "general", Description: "general-purpose; 4GB RAM per vCPU"},
	{ID: "cpu3m", Family: "memory", Description: "3rd-gen memory-optimized"},
}

// CPUFamilies returns a copy of the known CPU family catalog. Stable ordering.
// Use this when the caller needs to render family choices or build a custom
// fallback chain.
func CPUFamilies() []CPUFamily {
	out := make([]CPUFamily, len(runpodCPUFamilyCatalog))
	copy(out, runpodCPUFamilyCatalog)
	return out
}

// CPUFamilyByID returns one exact known REST CPU family.
func CPUFamilyByID(id string) (CPUFamily, bool) {
	id = strings.TrimSpace(id)
	for _, family := range runpodCPUFamilyCatalog {
		if family.ID == id {
			return family, true
		}
	}
	return CPUFamily{}, false
}

// SelectCPUFamilies returns a fallback-ordered list of family IDs suitable
// for the REST `cpuFlavorIds` field. The list preserves curated catalog order;
// it makes no price claim.
//
// `preferred` (optional) lets the caller push a specific family to the front
// of the chain — useful when a workload has a known good family but should
// degrade gracefully when that family is out of stock. Pass an empty string
// for no preference.
//
// `familyFilter` (optional) restricts the result to only families matching
// "compute", "general", or "memory"; pass an empty string for no filter.
//
// Returns an empty slice when no family in the catalog matches the filter.
// RunPod then auto-picks any available CPU when the caller passes `nil` /
// empty for `CPUFlavorIDs`, so an empty result is a valid "let RunPod choose"
// signal.
func SelectCPUFamilies(preferred, familyFilter string) []string {
	families := make([]CPUFamily, 0, len(runpodCPUFamilyCatalog))
	for _, f := range runpodCPUFamilyCatalog {
		if familyFilter != "" && !strings.EqualFold(f.Family, familyFilter) {
			continue
		}
		families = append(families, f)
	}
	out := make([]string, 0, len(families))
	seen := map[string]struct{}{}
	push := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	// Honor the preference first (when present and matches a family that
	// passed the filter).
	if pref := strings.TrimSpace(preferred); pref != "" {
		for _, f := range families {
			if strings.EqualFold(f.ID, pref) {
				push(f.ID)
				break
			}
		}
	}
	for _, f := range families {
		push(f.ID)
	}
	return out
}

// DefaultCPUFlavorIDs returns the full fallback-ordered family chain. The
// result is suitable for direct use as `CreatePodRequest.CPUFlavorIDs`.
func DefaultCPUFlavorIDs() []string {
	return SelectCPUFamilies("", "")
}
