package runpod_test

import (
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func TestGPUCatalogSelection(t *testing.T) {
	catalog := runpod.GPUCatalog()
	if len(catalog) == 0 {
		t.Fatal("catalog is empty")
	}

	// Hardware facts for the CUDA 13 placement set match RunPod's live IDs.
	wantSpecs := []runpod.GPUSpec{
		{ID: "NVIDIA B200", DisplayName: "B200", VRAMGB: 180, SMCapability: 100},
		{ID: "NVIDIA A100-SXM4-40GB", DisplayName: "A100 SXM 40GB", VRAMGB: 40, SMCapability: 80},
		{ID: "NVIDIA GeForce RTX 3080 Ti", DisplayName: "RTX 3080 Ti", VRAMGB: 12, SMCapability: 86, Consumer: true},
		{ID: "NVIDIA GeForce RTX 3090 Ti", DisplayName: "RTX 3090 Ti", VRAMGB: 24, SMCapability: 86, Consumer: true},
		{ID: "NVIDIA GeForce RTX 5080", DisplayName: "RTX 5080", VRAMGB: 16, SMCapability: 120, Consumer: true},
		{ID: "NVIDIA GeForce RTX 5090", DisplayName: "RTX 5090", VRAMGB: 32, SMCapability: 120, Consumer: true},
		{ID: "NVIDIA RTX 4000 Ada Generation", DisplayName: "RTX 4000 Ada", VRAMGB: 20, SMCapability: 89},
		{ID: "NVIDIA RTX 5000 Ada Generation", DisplayName: "RTX 5000 Ada", VRAMGB: 32, SMCapability: 89},
		{ID: "NVIDIA RTX A4500", DisplayName: "RTX A4500", VRAMGB: 20, SMCapability: 86},
		{ID: "NVIDIA RTX PRO 4000 Blackwell", DisplayName: "RTX PRO 4000", VRAMGB: 24, SMCapability: 120},
		{ID: "NVIDIA RTX PRO 4500 Blackwell", DisplayName: "RTX PRO 4500", VRAMGB: 32, SMCapability: 120},
		{ID: "NVIDIA RTX PRO 5000 Blackwell", DisplayName: "RTX PRO 5000", VRAMGB: 48, SMCapability: 120},
		{ID: "NVIDIA RTX PRO 6000 Blackwell Max-Q Workstation Edition", DisplayName: "RTX PRO 6000 MaxQ", VRAMGB: 96, SMCapability: 120},
		{ID: "NVIDIA RTX PRO 6000 Blackwell Server Edition", DisplayName: "RTX PRO 6000", VRAMGB: 96, SMCapability: 120},
	}
	for _, want := range wantSpecs {
		got, ok := runpod.GPUSpecByID(want.ID)
		if !ok {
			t.Errorf("catalog missing %q", want.ID)
		} else if got != want {
			t.Errorf("catalog spec for %q = %+v, want %+v", want.ID, got, want)
		}
	}

	// VRAM floor filters and preserves fallback order.
	specs := runpod.GPUsWithAtLeast(24, 0)
	if len(specs) == 0 {
		t.Fatal("no GPUs with >=24GB VRAM")
	}
	for _, s := range specs {
		if s.VRAMGB < 24 {
			t.Errorf("%s has %dGB, want >=24", s.ID, s.VRAMGB)
		}
	}
	if specs[0].ID != "NVIDIA GeForce RTX 3090" {
		t.Errorf("fallback order: first >=24GB SKU = %q, want RTX 3090", specs[0].ID)
	}

	// SM floor: SM120 leaves only Blackwell.
	for _, s := range runpod.GPUsWithAtLeast(0, 120) {
		if s.SMCapability < 120 {
			t.Errorf("%s has SM %d, want >=120", s.ID, s.SMCapability)
		}
	}
	if len(runpod.GPUsWithAtLeast(0, 120)) == 0 {
		t.Error("expected Blackwell SKUs at SM>=120")
	}

	// IDs helper preserves order.
	ids := runpod.GPUTypeIDs(specs)
	if len(ids) != len(specs) || ids[0] != specs[0].ID {
		t.Errorf("GPUTypeIDs mismatch: %v", ids)
	}

	// Case-insensitive lookup.
	if spec, ok := runpod.GPUSpecByID("nvidia geforce rtx 4090"); !ok || spec.SMCapability != 89 {
		t.Errorf("case-insensitive lookup failed: %+v ok=%v", spec, ok)
	}
	if _, ok := runpod.GPUSpecByID("no such gpu"); ok {
		t.Error("lookup of unknown GPU should fail")
	}
}
