// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/defense"
)

// TestFileLineStats covers the one thing the session card can print without
// version control: a created file's line count. An edit has no "before"
// content left to diff and a deletion has no "after", so both must come back
// with a kind and no number — inventing one would be worse than omitting it.
func TestFileLineStats(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "edited.txt"), []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gone.txt"), []byte("bye\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := defense.SnapshotDir(dir)
	if err != nil {
		t.Fatalf("snapshot before: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "edited.txt"), []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	// No trailing newline: the last partial line still counts, the way every
	// editor numbers it.
	if err := os.WriteFile(filepath.Join(dir, "created.txt"), []byte("a\nb\nc"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := defense.SnapshotDir(dir)
	if err != nil {
		t.Fatalf("snapshot after: %v", err)
	}

	paths := before.Changed(after)
	stats := fileLineStats(dir, before, after, paths)
	byPath := make(map[string]fileStat, len(stats))
	for _, s := range stats {
		byPath[s.Path] = s
	}

	if got := byPath["created.txt"]; got.Kind != "added" || got.Lines != 3 {
		t.Errorf("created.txt = %+v, want added/3", got)
	}
	if got := byPath["edited.txt"]; got.Kind != "modified" || got.Lines != 0 {
		t.Errorf("edited.txt = %+v, want modified/0", got)
	}
	if got := byPath["gone.txt"]; got.Kind != "removed" || got.Lines != 0 {
		t.Errorf("gone.txt = %+v, want removed/0", got)
	}
}

// TestFileLineStatsEmpty keeps the decoration out of the way of a round that
// changed nothing.
func TestFileLineStatsEmpty(t *testing.T) {
	if stats := fileLineStats(t.TempDir(), defense.Snapshot{}, defense.Snapshot{}, nil); stats != nil {
		t.Fatalf("stats = %+v, want nil", stats)
	}
}
