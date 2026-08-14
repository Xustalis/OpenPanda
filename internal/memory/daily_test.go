package memory

import (
	"os"
	"path/filepath"
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
