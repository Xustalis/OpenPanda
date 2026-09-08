package carddetect_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/carddetect"
)

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
