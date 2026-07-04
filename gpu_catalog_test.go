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

	// Blackwell SKUs must be present.
	for _, id := range []string{"NVIDIA B200", "NVIDIA GeForce RTX 5090"} {
		if _, ok := runpod.GPUSpecByID(id); !ok {
			t.Errorf("catalog missing %q", id)
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
