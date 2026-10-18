// SPDX-License-Identifier: AGPL-3.0-or-later

package memory

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDailyAppend(t *testing.T) {
	d := NewDaily(filepath.Join(t.TempDir(), "daily"))
	now := time.Date(2026, 8, 13, 19, 30, 0, 0, time.UTC)
	if err := d.Append(now, "compiled the daemon"); err != nil {
		t.Fatalf("append: %v", err)
	}
	data, err := os.ReadFile(d.PathFor(now))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "compiled the daemon") {
		t.Errorf("log missing entry: %q", data)
	}
	if !strings.HasPrefix(string(data), "- 19:30:00 ") {
		t.Errorf("log missing timestamp: %q", data)
	}
}

// TestDailyAppendTightensExistingLooseMode pins the older-file path: a log
// created before the 0600/0700 pass (or under a looser umask) must be
// tightened on the next append — O_CREATE's mode only applies at creation.
func TestDailyAppendTightensExistingLooseMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits are best-effort on Windows")
	}
	dir := filepath.Join(t.TempDir(), "daily")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	now := time.Date(2026, 8, 13, 19, 30, 0, 0, time.UTC)
	path := filepath.Join(dir, now.Format("2006-01-02")+".md")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod file: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}

	d := NewDaily(dir)
	if err := d.Append(now, "compiled the daemon"); err != nil {
		t.Fatalf("append: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("daily log mode = %o, want 0600", fi.Mode().Perm())
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("daily dir mode = %o, want 0700", di.Mode().Perm())
	}
}

func TestDailyPrune(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "daily")
	d := NewDaily(dir)
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)

	// Write files at three ages: recent, archivable (>30d), deletable (>90d),
	// plus a non-daily file that Prune must leave alone.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("2026-08-12.md", "recent")
	write("2026-07-01.md", "archive me") // ~43 days old
	write("2026-04-01.md", "delete me")  // ~134 days old
	write("README.md", "keep")

	if err := d.Prune(now); err != nil {
		t.Fatalf("prune: %v", err)
	}

	// Recent stays in place.
	if _, err := os.Stat(filepath.Join(dir, "2026-08-12.md")); err != nil {
		t.Errorf("recent file should remain: %v", err)
	}
	// Archivable moved to archive/.
	if _, err := os.Stat(filepath.Join(dir, "archive", "2026-07-01.md")); err != nil {
		t.Errorf("archive-me file should be archived: %v", err)
	}
	// Deletable removed.
	if _, err := os.Stat(filepath.Join(dir, "2026-04-01.md")); !os.IsNotExist(err) {
		t.Errorf("delete-me file should be gone, stat err = %v", err)
	}
	// Non-daily untouched.
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Errorf("non-daily file should remain: %v", err)
	}
}

func TestDailyPruneSweepsArchive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "daily")
	arc := filepath.Join(dir, "archive")
	d := NewDaily(dir)
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)

	// The delete window reaches into archive/: an archived log is cooled, not
	// exempt. A ~45-day archive stays, a ~134-day one goes, non-daily files
	// are untouched.
	if err := os.MkdirAll(arc, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(arc, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("2026-07-01.md", "young archive")
	write("2026-04-01.md", "old archive")
	write("README.md", "keep")

	if err := d.Prune(now); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if _, err := os.Stat(filepath.Join(arc, "2026-07-01.md")); err != nil {
		t.Errorf("recent archive should remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(arc, "2026-04-01.md")); !os.IsNotExist(err) {
		t.Errorf("expired archive should be deleted, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(arc, "README.md")); err != nil {
		t.Errorf("non-daily archive file should remain: %v", err)
	}
}

func TestDailyPruneMissingDir(t *testing.T) {
	d := NewDaily(filepath.Join(t.TempDir(), "nope", "daily"))
	if err := d.Prune(time.Now()); err != nil {
		t.Fatalf("prune on missing dir should be a no-op: %v", err)
	}
}

func TestParseDailyName(t *testing.T) {
	if _, ok := parseDailyName("2026-08-13.md"); !ok {
		t.Errorf("valid daily name should parse")
	}
	if _, ok := parseDailyName("not-a-date.md"); ok {
		t.Errorf("invalid date should not parse")
	}
	if _, ok := parseDailyName("2026-08-13.txt"); ok {
		t.Errorf("wrong extension should not parse")
	}
}
