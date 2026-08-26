package ledger

import "testing"

// TestFitsSilenceIsNotZero pins down the asymmetry that makes resource routing
// deployable on an existing network: a card that never wrote a resource_profile
// block is unknown, not empty. Every card shipped before v0.0.6 looks like that,
// so reading silence as "no hardware" would decline every GPU task in the
// network — a regression dressed up as a safety check.
func TestFitsSilenceIsNotZero(t *testing.T) {
	undeclared := Node{ID: "legacy"}
	if undeclared.ResourceProfile.Declared() {
		t.Fatalf("an all-zero profile must read as undeclared, not as a claim of zero")
	}
	if !undeclared.Fits(ResourceProfile{GPUVRAMGB: 8}) {
		t.Errorf("an undeclared node must pass: unknown hardware is not zero hardware")
	}

	pi := Node{ID: "pi", ResourceProfile: ResourceProfile{CPU: 4, RAMGB: 4, GPUVRAMGB: 0}}
	if !pi.Fits(ResourceProfile{}) {
		t.Errorf("a task that asks for nothing fits everywhere")
	}
	if pi.Fits(ResourceProfile{GPUVRAMGB: 8}) {
		t.Errorf("R2: a node declaring 0 VRAM must not fit an 8 GiB requirement")
	}
	if pi.Fits(ResourceProfile{RAMGB: 32}) {
		t.Errorf("4 GiB of RAM must not fit a 32 GiB requirement")
	}
	if !pi.Fits(ResourceProfile{CPU: 4}) {
		t.Errorf("4 cores must fit a 4-core requirement (>= not >)")
	}
	if pi.Fits(ResourceProfile{CPU: 8}) {
		t.Errorf("4 cores must not fit an 8-core requirement")
	}

	// A declared node with an unstated dimension is only excluded on the one
	// dimension that is decisive: VRAM. RAM and CPU are graded on what the card
	// actually says, because a card that lists a GPU and omits its RAM is common
	// and treating that as 0 GiB would exclude it from everything.
	gpuOnly := Node{ID: "win", ResourceProfile: ResourceProfile{GPUVRAMGB: 24}}
	if !gpuOnly.Fits(ResourceProfile{RAMGB: 64, GPUVRAMGB: 8}) {
		t.Errorf("an unstated RAM figure must not exclude a node that declares the GPU")
	}
}
