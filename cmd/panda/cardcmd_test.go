package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// `panda card set` is the headless editing path (ssh, provisioning scripts), so
// it carries the whole validation burden alone: a bad value accepted here becomes
// a card the daemon refuses at start, on a machine with no editor open.
func TestSetCardFieldRejectsInvalidValuesAndKeepsTheCardIntact(t *testing.T) {
	base := ledger.Card{Device: "node", ResourceClass: "Standard",
		Capacity:        ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 2},
		ResourceProfile: ledger.ResourceProfile{CPU: 8, RAMGB: 16, DurationHint: "long"}}

	for _, tc := range []struct{ field, value, wants string }{
		{"resource_class", "Enormous", "invalid"},
		{"capacity.ram_gb", "lots", "not a number"},
		{"capacity.max_concurrent_tasks", "0", "at least 1"},
		{"resource_profile.duration_hint", "medium", "duration_hint"},
		{"resource_profile.ram_gb", "-4", "non-negative"},
		{"device", "", "must not be empty"},
		{"turbo", "yes", "unknown field"},
	} {
		c := base
		err := setCardField(&c, tc.field, tc.value)
		if err == nil {
			t.Errorf("%s=%q accepted, want a rejection", tc.field, tc.value)
			continue
		}
		if !strings.Contains(err.Error(), tc.wants) {
			t.Errorf("%s=%q error = %q, want it to mention %q", tc.field, tc.value, err, tc.wants)
		}
	}

	// The accepted path still has to work, including the two negatives that are
	// legal: gpu_vram_gb -1 means "GPU present, size unreadable".
	c := base
	for _, ok := range [][2]string{
		{"device", "renamed"}, {"resource_class", "Full"}, {"chip", "AMD Ryzen 9"},
		{"capacity.max_concurrent_tasks", "6"}, {"resource_profile.gpu_vram_gb", "-1"},
		{"resource_profile.duration_hint", "short"},
	} {
		if err := setCardField(&c, ok[0], ok[1]); err != nil {
			t.Fatalf("%s=%s rejected: %v", ok[0], ok[1], err)
		}
	}
	if c.Device != "renamed" || c.ResourceClass != "Full" || c.Capacity.MaxConcurrent != 6 ||
		c.ResourceProfile.GPUVRAMGB != ledger.GPUVRAMUnknown || c.ResourceProfile.DurationHint != "short" {
		t.Fatalf("card = %+v, want every assignment applied", c)
	}
}

// writeCard is what every mutating subcommand funnels through, so it owns two
// guarantees: the file it leaves behind parses, and the previous content is
// recoverable — a rescan overwrites numbers a user may have tuned by hand.
func TestWriteCardValidatesBacksUpAndRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "capabilities.yaml")

	first := ledger.Card{Device: "node-a", ResourceClass: "Standard",
		Capacity:        ledger.Capacity{CPUCores: 4, RAMGB: 8, MaxConcurrent: 2},
		ResourceProfile: ledger.ResourceProfile{CPU: 4, RAMGB: 8, DurationHint: "short"}}
	if err := writeCard(path, first, true); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Error("a .bak was created for a card that did not exist yet")
	}
	got, err := ledger.LoadCard(path)
	if err != nil {
		t.Fatalf("written card does not load: %v", err)
	}
	if got.Device != "node-a" || got.ResourceProfile.CPU != 4 {
		t.Fatalf("round trip lost fields: %+v", got)
	}
	// The provenance header survives yaml.Marshal's comment stripping only
	// because writeCard prepends it; without it the file looks hand-written.
	body, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(body), "# capabilities.yaml — written by `panda card`") {
		t.Errorf("missing provenance header: %q", firstLine(string(body)))
	}

	second := first
	second.Device = "node-b"
	if err := writeCard(path, second, true); err != nil {
		t.Fatalf("second write: %v", err)
	}
	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("no backup of the overwritten card: %v", err)
	}
	if !strings.Contains(string(bak), "node-a") {
		t.Errorf("backup does not hold the previous card: %q", string(bak))
	}

	// An invalid card must not reach disk at all — not even partially, which is
	// why the write goes through a temp file and a rename.
	bad := first
	bad.ResourceProfile.DurationHint = "sometimes"
	if err := writeCard(path, bad, true); err == nil {
		t.Fatal("an invalid card was written")
	}
	after, err := ledger.LoadCard(path)
	if err != nil || after.Device != "node-b" {
		t.Fatalf("the rejected write damaged the card on disk: %+v, %v", after, err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".capabilities-") {
			t.Errorf("temp file %s left behind after a failed write", e.Name())
		}
	}
}
