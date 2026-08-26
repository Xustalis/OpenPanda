package ledger

import "testing"

// The whole point of the sentinel: a machine whose GPU size could not be read
// must stay a candidate, while a machine that honestly has no GPU must not.
// Getting this backwards is what kept the flagship training stage on the Orange
// Pi — the node that declares 0 VRAM truthfully — while the Windows box with the
// card sat idle because its VRAM probe returned nothing and was written as 0.
func TestFitsTreatsUnknownVRAMAsPermissiveAndZeroAsExcluding(t *testing.T) {
	need8 := ResourceProfile{GPUVRAMGB: 8, DurationHint: "long"}

	cases := []struct {
		name string
		node ResourceProfile
		want bool
	}{
		{"gpu present, size unreadable", ResourceProfile{CPU: 16, RAMGB: 32, GPUVRAMGB: GPUVRAMUnknown}, true},
		{"no gpu at all", ResourceProfile{CPU: 4, RAMGB: 2, GPUVRAMGB: 0}, false},
		{"gpu too small", ResourceProfile{CPU: 16, RAMGB: 32, GPUVRAMGB: 4}, false},
		{"gpu big enough", ResourceProfile{CPU: 16, RAMGB: 32, GPUVRAMGB: 24}, true},
		{"card predates resource_profile", ResourceProfile{}, true},
	}
	for _, tc := range cases {
		n := Node{Name: "n", ResourceProfile: tc.node}
		if got := n.Fits(need8); got != tc.want {
			t.Errorf("%s: Fits(8 GiB) = %v, want %v (node %+v)", tc.name, got, tc.want, tc.node)
		}
	}

	// A task that asks for no GPU is unaffected either way.
	for _, node := range []ResourceProfile{{GPUVRAMGB: GPUVRAMUnknown, CPU: 8}, {CPU: 8}} {
		if !(Node{ResourceProfile: node}).Fits(ResourceProfile{CPU: 4}) {
			t.Errorf("a CPU-only requirement was refused by node %+v", node)
		}
	}
}

// The sentinel has to survive the card loader, or a rescan on a machine with an
// unreadable GPU writes a card the daemon then refuses to start with.
func TestValidateAcceptsUnknownVRAMButNotOtherNegatives(t *testing.T) {
	if err := validateResourceProfile(ResourceProfile{CPU: 8, RAMGB: 16, GPUVRAMGB: GPUVRAMUnknown}); err != nil {
		t.Errorf("gpu_vram_gb %d rejected: %v", GPUVRAMUnknown, err)
	}
	if err := validateResourceProfile(ResourceProfile{GPUVRAMGB: -2}); err == nil {
		t.Error("gpu_vram_gb -2 accepted; only the documented sentinel is legal")
	}
	if err := validateResourceProfile(ResourceProfile{RAMGB: -1}); err == nil {
		t.Error("ram_gb -1 accepted; there is no unknown-RAM sentinel")
	}
}
