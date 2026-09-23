package defense

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHashDirStableAcrossMtime(t *testing.T) {
	root := t.TempDir()
	write := func(rel, data string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", "package a\n")
	write("sub/b.go", "package b\n")

	h1, n1, err := HashDir(root, 100)
	if err != nil || h1 == "" || n1 != 2 {
		t.Fatalf("h1=%q n=%d err=%v", h1, n1, err)
	}
	// Touch-only rewrite: same content, new mtime → identical hash.
	write("a.go", "package a\n")
	h2, n2, _ := HashDir(root, 100)
	if h1 != h2 || n2 != 2 {
		t.Fatalf("mtime-only change moved the hash: %s -> %s", h1, h2)
	}
	// Content change moves the hash.
	write("a.go", "package a // edited\n")
	h3, _, _ := HashDir(root, 100)
	if h3 == h1 {
		t.Fatal("content change did not move the hash")
	}
	// Add a file.
	write("c.go", "package c\n")
	h4, n4, _ := HashDir(root, 100)
	if h4 == h1 || n4 != 3 {
		t.Fatalf("added file: h=%q n=%d", h4, n4)
	}
}

func TestHashDirDeterministicOrder(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"z.txt", "a.txt", "m.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h1, _, _ := HashDir(root, 100)
	h2, _, _ := HashDir(root, 100)
	if h1 != h2 {
		t.Fatal("same tree hashed differently")
	}
}

func TestHashDirMaxFiles(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(root, string(rune('a'+i))+".txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h, n, err := HashDir(root, 3)
	if err != nil {
		t.Fatal(err)
	}
	if h != "" || n <= 3 {
		t.Fatalf("expected over-cap bail, got h=%q n=%d", h, n)
	}
}

func TestHashDirMissingAndEmpty(t *testing.T) {
	if h, n, err := HashDir(filepath.Join(t.TempDir(), "nope"), 10); err != nil || h != "" || n != 0 {
		t.Fatalf("missing dir: h=%q n=%d err=%v", h, n, err)
	}
	if h, n, err := HashDir(t.TempDir(), 10); err != nil || h != "" || n != 0 {
		t.Fatalf("empty dir: h=%q n=%d err=%v", h, n, err)
	}
}
