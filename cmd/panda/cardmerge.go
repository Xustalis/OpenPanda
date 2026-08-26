package main

// mergeCard is the whole of `panda card rescan`'s policy, kept pure so it can be
// tested without a machine attached.
//
// A rescan exists because hardware and installed agents change — a GPU goes in, a
// second CLI gets installed, RAM doubles — but the card is also a document the
// user edits by hand: which capabilities an agent is trusted with, what it is
// best at, its cost tier, the native whitelist, the manual abilities. Those two
// facts pull in opposite directions, and the naive implementations both lose
// something real: overwriting the file with a fresh `panda detect` throws away
// every hand edit, while only filling in blanks means a card written when the
// machine had 16 GB still claims 16 GB after the upgrade and keeps losing the
// training stage it should now win.
//
// So the split is by who owns the field. Probed hardware is owned by the machine
// and always overwritten. Everything a human decides is owned by the file and
// never touched. The agents map is the one genuinely mixed structure: its *set*
// of keys is a machine fact (the CLI is installed or it is not), while each
// entry's routing metadata is a human judgement, so keys are synced and bodies
// are preserved.

import (
	"fmt"
	"sort"

	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// cardDiff is one field-level change a rescan would make, rendered for review.
type cardDiff struct {
	Field string `json:"field"`
	Old   string `json:"old"`
	New   string `json:"new"`
}

// mergeCard folds a freshly probed card into the card on disk and returns the
// merged card plus the diff. old is not mutated.
//
// Owned by the machine (overwritten): chip, capacity.cpu_cores, capacity.ram_gb,
// resource_profile.{cpu,ram_gb,gpu_vram_gb,duration_hint}, and the agents map's
// key set.
//
// Owned by the file (preserved): device, node_kind, node_identity,
// resource_class, native, manual, capacity.max_concurrent_tasks, and every field
// inside an agent entry that already exists.
func mergeCard(old, scanned ledger.Card) (ledger.Card, []cardDiff) {
	merged := old
	var diffs []cardDiff
	set := func(field string, oldv, newv any, apply func()) {
		if fmt.Sprint(oldv) == fmt.Sprint(newv) {
			return
		}
		diffs = append(diffs, cardDiff{Field: field, Old: fmt.Sprint(oldv), New: fmt.Sprint(newv)})
		apply()
	}

	// device: a hostname the user renamed is the node's identity across the
	// network — peers, task history and the ledger all key off it. Only an empty
	// one gets filled in.
	if merged.Device == "" && scanned.Device != "" {
		set("device", merged.Device, scanned.Device, func() { merged.Device = scanned.Device })
	}
	// resource_class is a scheduling policy knob, not a measurement: an operator
	// who downgraded a workstation to Micro to keep work off it means it. Only a
	// card that never declared one takes the probe's guess.
	if merged.ResourceClass == "" && scanned.ResourceClass != "" {
		set("resource_class", merged.ResourceClass, scanned.ResourceClass, func() { merged.ResourceClass = scanned.ResourceClass })
	}
	if scanned.Chip != "" {
		set("chip", merged.Chip, scanned.Chip, func() { merged.Chip = scanned.Chip })
	}
	if scanned.Capacity.CPUCores > 0 {
		set("capacity.cpu_cores", merged.Capacity.CPUCores, scanned.Capacity.CPUCores,
			func() { merged.Capacity.CPUCores = scanned.Capacity.CPUCores })
	}
	if scanned.Capacity.RAMGB > 0 {
		set("capacity.ram_gb", merged.Capacity.RAMGB, scanned.Capacity.RAMGB,
			func() { merged.Capacity.RAMGB = scanned.Capacity.RAMGB })
	}
	// max_concurrent_tasks is the operator's throttle — the one number that says
	// "do not put more than this much work on my laptop". A rescan proposing 4
	// because the CPU is wide would quietly undo that, so it is only ever filled
	// in when absent.
	if merged.Capacity.MaxConcurrent == 0 && scanned.Capacity.MaxConcurrent > 0 {
		set("capacity.max_concurrent_tasks", merged.Capacity.MaxConcurrent, scanned.Capacity.MaxConcurrent,
			func() { merged.Capacity.MaxConcurrent = scanned.Capacity.MaxConcurrent })
	}
	merged.ResourceProfile = mergeResourceProfile(merged.ResourceProfile, scanned.ResourceProfile, set)
	merged.Agents, diffs = mergeAgents(merged.Agents, scanned.Agents, diffs)
	return merged, diffs
}

// mergeResourceProfile overwrites the probed hardware fields. This is the half
// that must not be conservative: resource_profile is the router's hard filter,
// so a stale figure here does not merely look wrong, it sends the GPU stage to
// the wrong machine.
func mergeResourceProfile(old, scanned ledger.ResourceProfile, set func(string, any, any, func())) ledger.ResourceProfile {
	merged := old
	if scanned.CPU > 0 {
		set("resource_profile.cpu", merged.CPU, scanned.CPU, func() { merged.CPU = scanned.CPU })
	}
	if scanned.RAMGB > 0 {
		set("resource_profile.ram_gb", merged.RAMGB, scanned.RAMGB, func() { merged.RAMGB = scanned.RAMGB })
	}
	// gpu_vram_gb is written even when the probe says 0 — "the GPU was removed"
	// is exactly as much a hardware fact as "a GPU appeared", and a card that
	// keeps claiming 24 GB after the card left will keep winning training tasks
	// it cannot run. GPUVRAMUnknown (-1) is also written: it means "present,
	// size unreadable", which Fits treats as permissive.
	set("resource_profile.gpu_vram_gb", merged.GPUVRAMGB, scanned.GPUVRAMGB, func() { merged.GPUVRAMGB = scanned.GPUVRAMGB })
	if scanned.DurationHint != "" {
		set("resource_profile.duration_hint", merged.DurationHint, scanned.DurationHint,
			func() { merged.DurationHint = scanned.DurationHint })
	}
	return merged
}

// mergeAgents syncs the installed-agent set while keeping each entry's
// hand-tuned body. A newly installed CLI arrives with the scan's defaults; an
// uninstalled one is removed, because an agent on the card is an advertised
// ability and advertising a CLI that is gone is how a delegated stage lands on a
// node that cannot start it. Everything else about an existing entry — the
// capability list, best_at/not_for, cost_tier, and above all tier (the
// authorization gate) — is the user's and survives untouched.
func mergeAgents(old, scanned map[string]ledger.Agent, diffs []cardDiff) (map[string]ledger.Agent, []cardDiff) {
	merged := make(map[string]ledger.Agent, len(old)+len(scanned))
	for name, ag := range old {
		merged[name] = ag
	}
	for _, name := range sortedKeys(scanned) {
		if _, exists := merged[name]; exists {
			// Adapter is the one field inside an existing entry that is a fact,
			// not a preference: it names the adapters/*.py that must exist.
			cur := merged[name]
			if scanned[name].Adapter != "" && cur.Adapter != scanned[name].Adapter {
				diffs = append(diffs, cardDiff{
					Field: "agents." + name + ".adapter", Old: cur.Adapter, New: scanned[name].Adapter,
				})
				cur.Adapter = scanned[name].Adapter
				merged[name] = cur
			}
			continue
		}
		merged[name] = scanned[name]
		diffs = append(diffs, cardDiff{Field: "agents." + name, Old: "-", New: "installed"})
	}
	for _, name := range sortedKeys(old) {
		if _, still := scanned[name]; still {
			continue
		}
		delete(merged, name)
		diffs = append(diffs, cardDiff{Field: "agents." + name, Old: "declared", New: "removed (CLI not found)"})
	}
	if len(merged) == 0 {
		merged = nil
	}
	return merged, diffs
}

// sortedKeys returns a map's keys in a stable order, so a diff reads the same
// way twice and `--json` output is comparable between runs.
func sortedKeys(m map[string]ledger.Agent) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
