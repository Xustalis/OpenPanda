package updater

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLiveReleaseDownload(t *testing.T) {
	if os.Getenv("OPENPANDA_LIVE_UPDATE_TEST") != "1" {
		t.Skip("set OPENPANDA_LIVE_UPDATE_TEST=1 to download the latest GitHub release")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	m := New(Options{Current: "0.0.0"})
	if err := m.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if !m.Status().Available {
		t.Fatalf("latest release not newer than test baseline: %+v", m.Status())
	}
	if err := m.Download(ctx); err != nil {
		t.Fatal(err)
	}

	m.mu.Lock()
	staged := m.staged
	m.mu.Unlock()
	if staged == nil {
		t.Fatal("download completed without a staged release")
	}
	if _, err := os.Stat(filepath.Join(staged.root, "bin", exeName())); err != nil {
		t.Fatalf("staged release binary: %v", err)
	}
	m.Cancel()
	if _, err := os.Stat(staged.dir); !os.IsNotExist(err) {
		t.Fatalf("cancel did not remove staging directory: %v", err)
	}
}
