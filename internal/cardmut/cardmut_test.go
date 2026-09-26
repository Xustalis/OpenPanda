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
