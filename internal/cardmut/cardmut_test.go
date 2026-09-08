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
