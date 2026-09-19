package carddetect_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/agents"
	"github.com/Xustalis/OpenPanda/internal/carddetect"
)

// stubAgentOnPATH creates a fake agent executable in a temp dir and prepends it
// to PATH so that exec.LookPath / InstalledBinary finds at least one known
// agent even on CI runners that have no real agent CLIs installed.
func stubAgentOnPATH(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	name := "claude"
	if runtime.GOOS == "windows" {
		name = "claude.exe"
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\necho stub\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(filepath.ListSeparator)+os.Getenv("PATH"))
}

func TestDetectCard(t *testing.T) {
	card := carddetect.DetectCard()
	if card.Device == "" {
		t.Error("expected non-empty Device in detected card")
	}
	if card.ResourceClass == "" {
		t.Error("expected non-empty ResourceClass in detected card")
	}
	if card.Capacity.CPUCores <= 0 {
		t.Errorf("expected positive CPUCores, got %d", card.Capacity.CPUCores)
	}
}

func TestEnsureCard(t *testing.T) {
	tmpDir := t.TempDir()
	cardPath := filepath.Join(tmpDir, "sub", "capabilities.yaml")

	// 1. File does not exist -> should create it
	card, created, err := carddetect.EnsureCard(cardPath)
	if err != nil {
		t.Fatalf("EnsureCard create failed: %v", err)
	}
	if !created {
		t.Error("expected created=true on first call")
	}
	if card.Device == "" {
		t.Error("expected valid card device")
	}
	if _, err := os.Stat(cardPath); err != nil {
		t.Fatalf("card file was not written: %v", err)
	}

	// 2. File exists -> should load without re-creating
	card2, created2, err := carddetect.EnsureCard(cardPath)
	if err != nil {
		t.Fatalf("EnsureCard load failed: %v", err)
	}
	if created2 {
		t.Error("expected created=false on second call")
	}
	if card2.Device != card.Device {
		t.Errorf("got device %q, want %q", card2.Device, card.Device)
	}
}

// TestCardAgentsDoNotPreSelectTheApprovalGate is the generator half of the same
// contract: whatever the registry says, the map a card is built from must not
// declare tier 2 for an agent. The literal that used to be here (2 for every
// agent, and again as the 0-fallback) overrode commander.Route's documented
// default of 1, so every `panda ask` that classified as an agent task was
// refused by defense.Authorize and waited for a human.
func TestCardAgentsDoNotPreSelectTheApprovalGate(t *testing.T) {
	stubAgentOnPATH(t)

	card := carddetect.CardAgents()
	if len(card) == 0 {
		t.Fatal("no agents to check")
	}
	for name, ag := range card {
		if ag.Tier != agents.TierAutoApproved {
			t.Errorf("generated card declares agent %q tier %d, want %d",
				name, ag.Tier, agents.TierAutoApproved)
		}
	}
}
