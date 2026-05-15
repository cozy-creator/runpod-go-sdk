package runpod

import (
	"sort"
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
	ID           string  // "cpu5c", "cpu3c", "cpu3g"
	Family       string  // "compute" | "general"
	IndicativeHr float64 // illustrative USD/hr at the cheapest size, ordering hint only
	Description  string  // short human-readable note about the family
}

// runpodCPUFamilyCatalog is the static catalog of known CPU families, ordered
// roughly cheapest-first at the smallest instance size. Prices are illustrative
// (live prices move); treat as ordering hints only.
var runpodCPUFamilyCatalog = []CPUFamily{
	{ID: "cpu5c", Family: "compute", IndicativeHr: 0.04, Description: "Intel Xeon Gold, 5th-gen compute-optimized"},
	{ID: "cpu3c", Family: "compute", IndicativeHr: 0.06, Description: "older compute-optimized; 2GB RAM per vCPU"},
	{ID: "cpu3g", Family: "general", IndicativeHr: 0.10, Description: "general-purpose; 4GB RAM per vCPU"},
}

// CPUFamilies returns a copy of the known CPU family catalog. Stable ordering.
// Use this when the caller needs to render family choices or build a custom
// fallback chain.
func CPUFamilies() []CPUFamily {
	out := make([]CPUFamily, len(runpodCPUFamilyCatalog))
	copy(out, runpodCPUFamilyCatalog)
	return out
}

// SelectCPUFamilies returns a fallback-ordered list of family IDs suitable
// for the REST `cpuFlavorIds` field. The list is sorted cheapest-first by the
// catalog's indicative price.
//
// `preferred` (optional) lets the caller push a specific family to the front
// of the chain — useful when a workload has a known good family but should
// degrade gracefully when that family is out of stock. Pass an empty string
// for no preference.
//
// `familyFilter` (optional) restricts the result to only families matching
// "compute" or "general"; pass an empty string for no filter.
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
	sort.SliceStable(families, func(i, j int) bool {
		return families[i].IndicativeHr < families[j].IndicativeHr
	})

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
