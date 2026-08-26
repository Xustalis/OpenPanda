package main

import (
	"testing"

	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// The point of the merge: hardware the machine reports wins, decisions the user
// wrote down survive. A rescan that fails either half is useless — one throws
// away the operator's tuning, the other keeps routing GPU work to a machine
// whose GPU is gone.
func TestMergeCardOverwritesHardwareAndKeepsUserFields(t *testing.T) {
	old := ledger.Card{
		Device:        "renamed-workstation",
		ResourceClass: "Micro", // deliberately downgraded to keep work away
		NodeKind:      "physical",
		NodeIdentity:  "machine-abc123",
		Chip:          "old chip",
		Native: []ledger.NativeAbility{
			{ID: "sys:info", Command: "uname", Args: []string{"-a"}, Tier: 1},
		},
		Manual:   []ledger.ManualAbility{{ID: "gpio:wire", Notify: "接线"}},
		Capacity: ledger.Capacity{CPUCores: 4, RAMGB: 16, MaxConcurrent: 1},
		ResourceProfile: ledger.ResourceProfile{
			CPU: 4, RAMGB: 16, GPUVRAMGB: 0, DurationHint: "short",
		},
	}
	scanned := ledger.Card{
		Device:        "probed-hostname",
		ResourceClass: "Full",
		Chip:          "AMD Ryzen 9",
		Capacity:      ledger.Capacity{CPUCores: 16, RAMGB: 64, MaxConcurrent: 4},
		ResourceProfile: ledger.ResourceProfile{
			CPU: 16, RAMGB: 64, GPUVRAMGB: 24, DurationHint: "long",
		},
	}

	merged, diffs := mergeCard(old, scanned)

	// Machine-owned fields moved.
	if merged.Chip != "AMD Ryzen 9" {
		t.Errorf("chip = %q, want the probed one", merged.Chip)
	}
	if merged.Capacity.CPUCores != 16 || merged.Capacity.RAMGB != 64 {
		t.Errorf("capacity = %+v, want the probed cores/ram", merged.Capacity)
	}
	if merged.ResourceProfile.GPUVRAMGB != 24 || merged.ResourceProfile.DurationHint != "long" {
		t.Errorf("resource_profile = %+v, want the probed GPU and hint", merged.ResourceProfile)
	}
	// User-owned fields stayed. max_concurrent_tasks is the throttle: a rescan
	// raising 1 → 4 would silently undo the operator's decision to keep this
	// machine lightly loaded.
	if merged.Device != "renamed-workstation" {
		t.Errorf("device = %q, want the hand-set name preserved", merged.Device)
	}
	if merged.ResourceClass != "Micro" {
		t.Errorf("resource_class = %q, want the hand-set class preserved", merged.ResourceClass)
	}
	if merged.Capacity.MaxConcurrent != 1 {
		t.Errorf("max_concurrent_tasks = %d, want the operator's 1", merged.Capacity.MaxConcurrent)
	}
	if len(merged.Native) != 1 || merged.Native[0].ID != "sys:info" {
		t.Errorf("native = %+v, want it untouched", merged.Native)
	}
	if len(merged.Manual) != 1 || merged.NodeIdentity != "machine-abc123" || merged.NodeKind != "physical" {
		t.Errorf("manual/identity mangled: %+v", merged)
	}
	// The diff has to name every applied change and nothing else, because
	// --write is approved on the strength of what was printed.
	want := map[string]string{
		"chip":                           "AMD Ryzen 9",
		"capacity.cpu_cores":             "16",
		"capacity.ram_gb":                "64",
		"resource_profile.cpu":           "16",
		"resource_profile.ram_gb":        "64",
		"resource_profile.gpu_vram_gb":   "24",
		"resource_profile.duration_hint": "long",
	}
	if len(diffs) != len(want) {
		t.Fatalf("diff = %+v, want exactly %d entries", diffs, len(want))
	}
	for _, d := range diffs {
		if want[d.Field] != d.New {
			t.Errorf("diff %s → %q, want %q", d.Field, d.New, want[d.Field])
		}
	}
}

// A removed GPU has to be written back as 0. This is the one field where "only
// fill in blanks" would be actively harmful: Fits is a hard filter, so a card
// still claiming VRAM keeps winning tasks that now fail at run time.
func TestMergeCardWritesGPURemovalAndUnknownVRAM(t *testing.T) {
	old := ledger.Card{Device: "d", ResourceProfile: ledger.ResourceProfile{CPU: 8, RAMGB: 32, GPUVRAMGB: 24}}
	merged, diffs := mergeCard(old, ledger.Card{Device: "d",
		ResourceProfile: ledger.ResourceProfile{CPU: 8, RAMGB: 32, GPUVRAMGB: 0}})
	if merged.ResourceProfile.GPUVRAMGB != 0 {
		t.Fatalf("gpu_vram_gb = %d, want 0 after the card was pulled", merged.ResourceProfile.GPUVRAMGB)
	}
	if len(diffs) != 1 || diffs[0].Field != "resource_profile.gpu_vram_gb" {
		t.Fatalf("diff = %+v, want just the VRAM change", diffs)
	}

	// Unreadable size is its own value and must reach the card, where Fits
	// reads it as permissive rather than as "no GPU".
	merged, _ = mergeCard(old, ledger.Card{Device: "d",
		ResourceProfile: ledger.ResourceProfile{CPU: 8, RAMGB: 32, GPUVRAMGB: ledger.GPUVRAMUnknown}})
	if merged.ResourceProfile.GPUVRAMGB != ledger.GPUVRAMUnknown {
		t.Fatalf("gpu_vram_gb = %d, want %d (present, size unknown)",
			merged.ResourceProfile.GPUVRAMGB, ledger.GPUVRAMUnknown)
	}
}

// The agents map is the mixed case: its key set is a machine fact, each entry's
// body is a human judgement. Both halves are load-bearing — a stale key routes
// work to a CLI that is gone, and an overwritten body silently resets `tier`,
// which is the authorization gate.
func TestMergeAgentsSyncsInstalledSetAndPreservesTuning(t *testing.T) {
	old := map[string]ledger.Agent{
		"claude_code": {
			Adapter:      "claude_code.py",
			InstallCheck: "which claude",
			Capabilities: []string{"coding", "review"},
			BestAt:       []string{"code_search"},
			NotFor:       []string{"hardware_io"},
			CostTier:     "low",
			Tier:         1, // hand-declared read-only: must not silently become 2
		},
		"gemini": {Adapter: "gemini.py", Tier: 2}, // uninstalled since
	}
	scanned := map[string]ledger.Agent{
		"claude_code": {Adapter: "claude_code.py", CostTier: "high", Tier: 2,
			Capabilities: []string{"coding", "shell", "file_edit"}},
		"codex": {Adapter: "codex.py", CostTier: "high", Tier: 2},
	}

	merged, diffs := mergeAgents(old, scanned, nil)

	kept := merged["claude_code"]
	if kept.Tier != 1 || kept.CostTier != "low" || len(kept.Capabilities) != 2 {
		t.Errorf("claude_code = %+v, want the hand-tuned body untouched", kept)
	}
	if _, ok := merged["codex"]; !ok {
		t.Error("newly installed codex missing from the merged card")
	}
	if _, ok := merged["gemini"]; ok {
		t.Error("gemini is no longer installed but stayed on the card")
	}
	byField := map[string]string{}
	for _, d := range diffs {
		byField[d.Field] = d.New
	}
	if byField["agents.codex"] != "installed" {
		t.Errorf("diff missing the codex addition: %+v", diffs)
	}
	if byField["agents.gemini"] == "" {
		t.Errorf("diff missing the gemini removal: %+v", diffs)
	}
	if _, reported := byField["agents.claude_code"]; reported {
		t.Errorf("unchanged agent reported as a change: %+v", diffs)
	}
}

// An adapter rename is a fact, not a preference: the card must point at a script
// that exists under adapters/.
func TestMergeAgentsUpdatesAdapterOfAnExistingEntry(t *testing.T) {
	merged, diffs := mergeAgents(
		map[string]ledger.Agent{"codex": {Adapter: "codex_old.py", Tier: 1, CostTier: "low"}},
		map[string]ledger.Agent{"codex": {Adapter: "codex.py", Tier: 2, CostTier: "high"}},
		nil)
	got := merged["codex"]
	if got.Adapter != "codex.py" {
		t.Errorf("adapter = %q, want the registry's current script", got.Adapter)
	}
	if got.Tier != 1 || got.CostTier != "low" {
		t.Errorf("agent = %+v, want tier/cost_tier preserved", got)
	}
	if len(diffs) != 1 || diffs[0].Field != "agents.codex.adapter" {
		t.Fatalf("diff = %+v, want only the adapter change", diffs)
	}
}

// A card that never declared hardware (every card shipped before v0.0.6) must
// come out of a rescan fully populated, and a scan that could probe nothing must
// not blank out a card that has real numbers in it.
func TestMergeCardFillsAnEmptyCardAndIgnoresAnEmptyScan(t *testing.T) {
	merged, diffs := mergeCard(ledger.Card{}, ledger.Card{
		Device: "probed", ResourceClass: "Standard", Chip: "M1",
		Capacity:        ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 4},
		ResourceProfile: ledger.ResourceProfile{CPU: 8, RAMGB: 16, DurationHint: "long"},
	})
	if merged.Device != "probed" || merged.ResourceClass != "Standard" ||
		merged.Capacity.MaxConcurrent != 4 || merged.ResourceProfile.CPU != 8 {
		t.Fatalf("empty card not filled in: %+v", merged)
	}
	if len(diffs) == 0 {
		t.Fatal("no diffs reported for a card built from nothing")
	}

	full := ledger.Card{Device: "d", Chip: "M1",
		Capacity:        ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 2},
		ResourceProfile: ledger.ResourceProfile{CPU: 8, RAMGB: 16, GPUVRAMGB: 0, DurationHint: "long"}}
	merged, diffs = mergeCard(full, ledger.Card{})
	if len(diffs) != 0 {
		t.Fatalf("a scan that probed nothing proposed changes: %+v", diffs)
	}
	if merged.Chip != "M1" || merged.Capacity.RAMGB != 16 || merged.ResourceProfile.CPU != 8 {
		t.Fatalf("a failed scan blanked real values: %+v", merged)
	}
}
