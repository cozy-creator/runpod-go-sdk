package runpod

import "strings"

// GPUSpec describes a RunPod GPU SKU: the API type ID plus the hardware
// facts (VRAM, SM compute capability) that placement decisions need but the
// live API does not return. Static data — refresh stock/pricing with
// ListAvailableGPUs / ListGPUOffers.
type GPUSpec struct {
	// ID is the RunPod GPU type ID as accepted by CreatePodRequest.GPUTypeIDs
	// (e.g. "NVIDIA GeForce RTX 4090").
	ID string
	// DisplayName is the short human-readable name (e.g. "RTX 4090").
	DisplayName string
	// VRAMGB is the GPU memory in GB.
	VRAMGB int
	// SMCapability is the CUDA compute capability x10 (86 = 8.6, 120 = 12.0).
	SMCapability int
	// Consumer is true for consumer (GeForce) SKUs, false for
	// datacenter/workstation SKUs.
	Consumer bool
}

// gpuCatalog is the SDK's static SKU table, ordered by fallback preference:
// cheaper/more-available SKUs first, so the order is directly usable as a
// CreatePodWithFallback candidate chain. Verified against the live gpuTypes
// query by TestGPUCatalogIDsLive.
var gpuCatalog = []GPUSpec{
	// Consumer (GeForce) — cheapest first
	{ID: "NVIDIA GeForce RTX 3070", DisplayName: "RTX 3070", VRAMGB: 8, SMCapability: 86, Consumer: true},
	{ID: "NVIDIA GeForce RTX 3080", DisplayName: "RTX 3080", VRAMGB: 10, SMCapability: 86, Consumer: true},
	{ID: "NVIDIA GeForce RTX 4070 Ti", DisplayName: "RTX 4070 Ti", VRAMGB: 12, SMCapability: 89, Consumer: true},
	{ID: "NVIDIA GeForce RTX 4080", DisplayName: "RTX 4080", VRAMGB: 16, SMCapability: 89, Consumer: true},
	{ID: "NVIDIA GeForce RTX 3090", DisplayName: "RTX 3090", VRAMGB: 24, SMCapability: 86, Consumer: true},
	{ID: "NVIDIA GeForce RTX 4090", DisplayName: "RTX 4090", VRAMGB: 24, SMCapability: 89, Consumer: true},
	{ID: "NVIDIA GeForce RTX 5090", DisplayName: "RTX 5090", VRAMGB: 32, SMCapability: 120, Consumer: true},

	// Workstation / datacenter — Ampere and Ada
	{ID: "NVIDIA RTX A4000", DisplayName: "RTX A4000", VRAMGB: 16, SMCapability: 86},
	{ID: "NVIDIA RTX A5000", DisplayName: "RTX A5000", VRAMGB: 24, SMCapability: 86},
	{ID: "NVIDIA L4", DisplayName: "L4", VRAMGB: 24, SMCapability: 89},
	{ID: "NVIDIA RTX A6000", DisplayName: "RTX A6000", VRAMGB: 48, SMCapability: 86},
	{ID: "NVIDIA A40", DisplayName: "A40", VRAMGB: 48, SMCapability: 86},
	{ID: "NVIDIA L40", DisplayName: "L40", VRAMGB: 48, SMCapability: 89},
	{ID: "NVIDIA L40S", DisplayName: "L40S", VRAMGB: 48, SMCapability: 89},
	{ID: "NVIDIA RTX 6000 Ada Generation", DisplayName: "RTX 6000 Ada", VRAMGB: 48, SMCapability: 89},

	// Blackwell workstation
	{ID: "NVIDIA RTX PRO 6000 Blackwell Workstation Edition", DisplayName: "RTX PRO 6000 Blackwell", VRAMGB: 96, SMCapability: 120},

	// Datacenter accelerators
	{ID: "NVIDIA A100 80GB PCIe", DisplayName: "A100 PCIe 80GB", VRAMGB: 80, SMCapability: 80},
	{ID: "NVIDIA A100-SXM4-80GB", DisplayName: "A100 SXM4 80GB", VRAMGB: 80, SMCapability: 80},
	{ID: "NVIDIA H100 PCIe", DisplayName: "H100 PCIe", VRAMGB: 80, SMCapability: 90},
	{ID: "NVIDIA H100 80GB HBM3", DisplayName: "H100 SXM5 80GB", VRAMGB: 80, SMCapability: 90},
	{ID: "NVIDIA H100 NVL", DisplayName: "H100 NVL", VRAMGB: 94, SMCapability: 90},
	{ID: "NVIDIA H200", DisplayName: "H200", VRAMGB: 141, SMCapability: 90},
	{ID: "NVIDIA B200", DisplayName: "B200", VRAMGB: 180, SMCapability: 100},
}

// GPUCatalog returns a copy of the static GPU SKU catalog in fallback
// preference order (cheaper/more-available SKUs first).
func GPUCatalog() []GPUSpec {
	out := make([]GPUSpec, len(gpuCatalog))
	copy(out, gpuCatalog)
	return out
}

// GPUSpecByID looks up a catalog entry by RunPod GPU type ID
// (case-insensitive).
func GPUSpecByID(id string) (GPUSpec, bool) {
	id = strings.TrimSpace(id)
	for _, spec := range gpuCatalog {
		if strings.EqualFold(spec.ID, id) {
			return spec, true
		}
	}
	return GPUSpec{}, false
}

// GPUsWithAtLeast returns the catalog entries with at least vramGB of VRAM
// and at least smMin SM compute capability (either may be 0 for "any"), in
// fallback preference order. The result's IDs are directly usable as a
// CreatePodRequest.GPUTypeIDs / CreatePodWithFallback candidate chain.
func GPUsWithAtLeast(vramGB, smMin int) []GPUSpec {
	out := make([]GPUSpec, 0, len(gpuCatalog))
	for _, spec := range gpuCatalog {
		if vramGB > 0 && spec.VRAMGB < vramGB {
			continue
		}
		if smMin > 0 && spec.SMCapability < smMin {
			continue
		}
		out = append(out, spec)
	}
	return out
}

// GPUTypeIDs extracts the RunPod type IDs from a spec list, preserving
// order — a convenience for feeding selection results into pod creation.
func GPUTypeIDs(specs []GPUSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.ID
	}
	return out
}
