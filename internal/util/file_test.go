package util

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteFileAtomic verifies the content lands and no temp file leaks.
func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "MEMORY.md")

	if err := WriteFileAtomic(path, []byte("v1"), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	if err := WriteFileAtomic(path, []byte("v2"), 0o644); err != nil {
		t.Fatalf("write v2 (replace): %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "v2" {
		t.Fatalf("content = %q, want v2", data)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("temp files leaked: %v", names)
	}
}

// TestWriteFileAtomicBadDir fails cleanly (no panic, no temp leak elsewhere).
func TestWriteFileAtomicBadDir(t *testing.T) {
	if err := WriteFileAtomic(filepath.Join(t.TempDir(), "gone", "f.md"), []byte("x"), 0o644); err == nil {
		t.Fatalf("expected error for missing dir")
	}
}
