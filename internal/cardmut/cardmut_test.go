// SPDX-License-Identifier: AGPL-3.0-or-later

package cardmut_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/cardmut"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

func TestMutateOnNonExistentCard(t *testing.T) {
	tmpDir := t.TempDir()
	cardPath := filepath.Join(tmpDir, "auto_card", "capabilities.yaml")

	// Ensure card does not exist yet
	if _, err := os.Stat(cardPath); !os.IsNotExist(err) {
		t.Fatalf("card should not exist yet")
	}

	// Adding an agent to non-existent card should auto-initialize and succeed
	ag := ledger.Agent{
		Adapter:      "custom_agent.py",
		Capabilities: []string{"coding", "testing"},
		Tier:         2,
	}
	if err := cardmut.AgentAdd(cardPath, "test_custom_agent", ag); err != nil {
		t.Fatalf("AgentAdd on non-existent card failed: %v", err)
	}

	// Verify the card file was created and contains the agent
	card, err := ledger.LoadCard(cardPath)
	if err != nil {
		t.Fatalf("LoadCard failed: %v", err)
	}
	if card.Device == "" {
		t.Errorf("expected auto-detected Device on card")
	}
	savedAg, ok := card.Agents["test_custom_agent"]
	if !ok {
		t.Fatalf("agent test_custom_agent not found in card agents")
	}
	if savedAg.Adapter != "custom_agent.py" {
		t.Errorf("got adapter %q, want custom_agent.py", savedAg.Adapter)
	}
}

// TestAgentCommandRoundTrip: the generic-adapter argv template survives the
// YAML write, updates via AgentSet, and clears on an empty value (an empty
// template must not linger as `command: ""` — generic.py errors on it).
func TestAgentCommandRoundTrip(t *testing.T) {
	cardPath := filepath.Join(t.TempDir(), "capabilities.yaml")
	ag := ledger.Agent{
		Adapter: "generic.py",
		Command: "zcode run --headless {prompt}",
		Tier:    1,
	}
	if err := cardmut.AgentAdd(cardPath, "zcode", ag); err != nil {
		t.Fatalf("AgentAdd: %v", err)
	}
	card, err := ledger.LoadCard(cardPath)
	if err != nil {
		t.Fatalf("LoadCard: %v", err)
	}
	if got := card.Agents["zcode"].Command; got != ag.Command {
		t.Fatalf("command = %q, want %q", got, ag.Command)
	}

	newCmd := "zcode exec {prompt}"
	if err := cardmut.AgentSet(cardPath, "zcode", cardmut.AgentUpdate{Command: &newCmd}); err != nil {
		t.Fatalf("AgentSet command: %v", err)
	}
	card, _ = ledger.LoadCard(cardPath)
	if got := card.Agents["zcode"].Command; got != newCmd {
		t.Fatalf("command after set = %q, want %q", got, newCmd)
	}

	empty := ""
	if err := cardmut.AgentSet(cardPath, "zcode", cardmut.AgentUpdate{Command: &empty}); err != nil {
		t.Fatalf("AgentSet clear: %v", err)
	}
	card, _ = ledger.LoadCard(cardPath)
	if got := card.Agents["zcode"].Command; got != "" {
		t.Fatalf("cleared command = %q, want empty", got)
	}
}

// TestNativeAddRemoveRoundTrip covers the native-ability write path: add
// appends to the sequence, a duplicate id refuses (a silent overwrite would
// hide a typo that meant to edit), and remove deletes by id while missing
// ids refuse rather than no-op.
func TestNativeAddRemoveRoundTrip(t *testing.T) {
	cardPath := filepath.Join(t.TempDir(), "capabilities.yaml")
	ab := ledger.NativeAbility{ID: "disk-usage", Command: "df", Args: []string{"-h"}, Tier: 1, Description: "disk free"}
	if err := cardmut.NativeAdd(cardPath, ab); err != nil {
		t.Fatalf("NativeAdd: %v", err)
	}
	card, err := ledger.LoadCard(cardPath)
	if err != nil {
		t.Fatalf("LoadCard: %v", err)
	}
	// A fresh card auto-initializes with a default native set, so assert
	// membership by id rather than owning the whole sequence.
	var found *ledger.NativeAbility
	for i := range card.Native {
		if card.Native[i].ID == "disk-usage" {
			found = &card.Native[i]
		}
	}
	if found == nil || found.Command != "df" || found.Description != "disk free" {
		t.Fatalf("added ability missing or wrong: %+v", card.Native)
	}

	if err := cardmut.NativeAdd(cardPath, ab); err == nil {
		t.Fatal("duplicate NativeAdd accepted")
	}
	if err := cardmut.NativeRemove(cardPath, "nope"); err == nil {
		t.Fatal("NativeRemove of unknown id accepted")
	}
	if err := cardmut.NativeRemove(cardPath, "disk-usage"); err != nil {
		t.Fatalf("NativeRemove: %v", err)
	}
	card, _ = ledger.LoadCard(cardPath)
	for _, n := range card.Native {
		if n.ID == "disk-usage" {
			t.Fatalf("removed ability still on card: %+v", card.Native)
		}
	}
}

// TestFailedEditLeavesCardUntouched pins the mutate contract: an edit that
// errors must not write anything — a failed add is not a partial add.
func TestFailedEditLeavesCardUntouched(t *testing.T) {
	cardPath := filepath.Join(t.TempDir(), "capabilities.yaml")
	ab := ledger.NativeAbility{ID: "a", Command: "true", Tier: 1}
	if err := cardmut.NativeAdd(cardPath, ab); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(cardPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cardmut.NativeAdd(cardPath, ab); err == nil {
		t.Fatal("duplicate NativeAdd accepted")
	}
	after, err := os.ReadFile(cardPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("failed edit mutated the card on disk")
	}
}

// TestManualAddRemoveRoundTrip mirrors the native path for manual abilities.
func TestManualAddRemoveRoundTrip(t *testing.T) {
	cardPath := filepath.Join(t.TempDir(), "capabilities.yaml")
	ab := ledger.ManualAbility{ID: "on-site-check", Notify: "call me"}
	if err := cardmut.ManualAdd(cardPath, ab); err != nil {
		t.Fatalf("ManualAdd: %v", err)
	}
	card, err := ledger.LoadCard(cardPath)
	if err != nil {
		t.Fatalf("LoadCard: %v", err)
	}
	var found *ledger.ManualAbility
	for i := range card.Manual {
		if card.Manual[i].ID == "on-site-check" {
			found = &card.Manual[i]
		}
	}
	if found == nil || found.Notify != "call me" {
		t.Fatalf("added ability missing or wrong: %+v", card.Manual)
	}
	if err := cardmut.ManualAdd(cardPath, ab); err == nil {
		t.Fatal("duplicate ManualAdd accepted")
	}
	if err := cardmut.ManualRemove(cardPath, "ghost"); err == nil {
		t.Fatal("ManualRemove of unknown id accepted")
	}
	if err := cardmut.ManualRemove(cardPath, "on-site-check"); err != nil {
		t.Fatalf("ManualRemove: %v", err)
	}
	card, _ = ledger.LoadCard(cardPath)
	for _, m := range card.Manual {
		if m.ID == "on-site-check" {
			t.Fatalf("removed ability still on card: %+v", card.Manual)
		}
	}
}

// TestAgentRemove covers deletion of a present agent and refusal of a name
// the card does not carry.
func TestAgentRemove(t *testing.T) {
	cardPath := filepath.Join(t.TempDir(), "capabilities.yaml")
	ag := ledger.Agent{Adapter: "generic.py", Tier: 1}
	if err := cardmut.AgentAdd(cardPath, "worker", ag); err != nil {
		t.Fatalf("AgentAdd: %v", err)
	}
	if err := cardmut.AgentRemove(cardPath, "worker"); err != nil {
		t.Fatalf("AgentRemove: %v", err)
	}
	card, err := ledger.LoadCard(cardPath)
	if err != nil {
		t.Fatalf("LoadCard: %v", err)
	}
	if _, ok := card.Agents["worker"]; ok {
		t.Fatal("removed agent still on card")
	}
	if err := cardmut.AgentRemove(cardPath, "worker"); err == nil {
		t.Fatal("AgentRemove of missing agent accepted")
	}
}

// TestAgentSetPartialPreserves updates only the named fields: the adapter and
// untouched entries must survive, and a set on an unknown agent refuses.
func TestAgentSetPartialPreserves(t *testing.T) {
	cardPath := filepath.Join(t.TempDir(), "capabilities.yaml")
	ag := ledger.Agent{Adapter: "generic.py", Tier: 1, Capabilities: []string{"coding"}}
	if err := cardmut.AgentAdd(cardPath, "worker", ag); err != nil {
		t.Fatalf("AgentAdd: %v", err)
	}
	tier := 2
	caps := []string{"coding", "review"}
	if err := cardmut.AgentSet(cardPath, "worker", cardmut.AgentUpdate{Tier: &tier, Capabilities: &caps}); err != nil {
		t.Fatalf("AgentSet: %v", err)
	}
	card, err := ledger.LoadCard(cardPath)
	if err != nil {
		t.Fatalf("LoadCard: %v", err)
	}
	got := card.Agents["worker"]
	if got.Tier != 2 || got.Adapter != "generic.py" {
		t.Fatalf("agent after set = %+v", got)
	}
	if len(got.Capabilities) != 2 || got.Capabilities[1] != "review" {
		t.Fatalf("capabilities after set = %v", got.Capabilities)
	}
	if err := cardmut.AgentSet(cardPath, "ghost", cardmut.AgentUpdate{Tier: &tier}); err == nil {
		t.Fatal("AgentSet on unknown agent accepted")
	}
}

// TestWriteRaw validates the raw-write path: a syntactically valid card lands
// atomically with a .bak of the previous contents, while an invalid document
// is refused without touching the file on disk.
func TestWriteRaw(t *testing.T) {
	cardPath := filepath.Join(t.TempDir(), "capabilities.yaml")
	if err := cardmut.NativeAdd(cardPath, ledger.NativeAbility{ID: "a", Command: "true", Tier: 1}); err != nil {
		t.Fatal(err)
	}
	orig, err := os.ReadFile(cardPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := cardmut.WriteRaw(cardPath, []byte("device: desktop\nnative: []\n")); err != nil {
		t.Fatalf("WriteRaw valid: %v", err)
	}
	card, err := ledger.LoadCard(cardPath)
	if err != nil {
		t.Fatalf("LoadCard after WriteRaw: %v", err)
	}
	if len(card.Native) != 0 {
		t.Fatalf("native after WriteRaw = %+v", card.Native)
	}
	bak, err := os.ReadFile(cardPath + ".bak")
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if string(bak) != string(orig) {
		t.Fatal(".bak does not hold the pre-write contents")
	}

	if err := cardmut.WriteRaw(cardPath, []byte("native: [unclosed")); err == nil {
		t.Fatal("invalid WriteRaw accepted")
	}
	after, _ := os.ReadFile(cardPath)
	card2, lerr := ledger.LoadCard(cardPath)
	if lerr != nil || card2.Device != "desktop" || len(after) == 0 {
		t.Fatal("refused WriteRaw damaged the card on disk")
	}
}
