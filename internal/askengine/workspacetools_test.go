// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/entry"
)

// TestWorkspaceToolsRead covers the read tool's two jobs: return numbered
// lines a model can cite, and page through a file without re-reading it.
func TestWorkspaceToolsRead(t *testing.T) {
	root := t.TempDir()
	src := "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := entry.NewRegistry()
	registerWorkspaceTools(reg, root)
	read, ok := reg.Lookup("workspace_read")
	if !ok {
		t.Fatal("workspace_read was not registered")
	}

	out, err := read.Run(context.Background(), map[string]any{"path": "main.go"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, "     1\tpackage main") || !strings.Contains(out, "     5\t}") {
		t.Errorf("read should return numbered lines, got:\n%s", out)
	}

	out, err = read.Run(context.Background(), map[string]any{"path": "main.go", "start_line": 3, "max_lines": 1})
	if err != nil {
		t.Fatalf("paged read: %v", err)
	}
	if strings.Contains(out, "package main") || !strings.Contains(out, "     3\tfunc main() {") {
		t.Errorf("paged read returned the wrong slice:\n%s", out)
	}

	// A binary file is named as such instead of being dumped as mojibake.
	bin := filepath.Join(root, "blob.bin")
	if err := os.WriteFile(bin, []byte{0x00, 0x01, 0x02, 'a'}, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = read.Run(context.Background(), map[string]any{"path": "blob.bin"})
	if err != nil {
		t.Fatalf("binary read: %v", err)
	}
	if !strings.Contains(out, "binary file") {
		t.Errorf("binary file should be recognized, got %q", out)
	}
}

// TestWorkspaceToolsStayInsideRoot pins the boundary the tools exist behind:
// "..", an absolute path elsewhere, and a symlink planted in the tree all have
// to be refused, because an admitted read is an exfiltration path.
func TestWorkspaceToolsStayInsideRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "inside.txt"), []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	reg := entry.NewRegistry()
	registerWorkspaceTools(reg, root)
	read, _ := reg.Lookup("workspace_read")

	for _, p := range []string{
		"../secret.txt",
		filepath.Join(outside, "secret.txt"),
		"link",
		filepath.Join("..", filepath.Base(outside), "secret.txt"),
	} {
		if _, err := read.Run(context.Background(), map[string]any{"path": p}); err == nil {
			t.Errorf("read of %q should have been refused", p)
		} else if !strings.Contains(err.Error(), "outside the workspace") {
			t.Errorf("read of %q refused with %v, want the boundary error", p, err)
		}
	}
}

// TestWorkspaceToolsListAndSearch covers the two discovery tools, including
// the trees a code search must not wade into.
func TestWorkspaceToolsListAndSearch(t *testing.T) {
	root := t.TempDir()
	mustWrite := func(rel, body string) {
		t.Helper()
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("main.go", "package main\nconst Answer = 42\n")
	mustWrite("pkg/x.go", "package pkg\n// Answer is defined in main\n")
	mustWrite("node_modules/dep/index.js", "const Answer = 'noise'\n")
	mustWrite(".git/config", "Answer = not source\n")

	reg := entry.NewRegistry()
	registerWorkspaceTools(reg, root)
	list, _ := reg.Lookup("workspace_list")
	search, _ := reg.Lookup("workspace_search")

	out, err := list.Run(context.Background(), map[string]any{"path": "."})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "main.go") || !strings.Contains(out, "pkg/") {
		t.Errorf("list is missing the tree's own files:\n%s", out)
	}
	if strings.Contains(out, "node_modules") || strings.Contains(out, ".git") {
		t.Errorf("list should hide dependency and VCS trees:\n%s", out)
	}

	out, err = search.Run(context.Background(), map[string]any{"pattern": "Answer"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(out, "main.go:2:") || !strings.Contains(out, "pkg/x.go:2:") {
		t.Errorf("search missed a source match:\n%s", out)
	}
	if strings.Contains(out, "node_modules") || strings.Contains(out, ".git/") {
		t.Errorf("search waded into a skipped tree:\n%s", out)
	}

	// Scope by path and by case: the same tools, narrowed.
	out, err = search.Run(context.Background(), map[string]any{"pattern": "answer", "path": "pkg", "ignore_case": true})
	if err != nil {
		t.Fatalf("scoped search: %v", err)
	}
	if !strings.Contains(out, "pkg/x.go:2:") || strings.Contains(out, "main.go:2:") {
		t.Errorf("scoped search did not honor path/ignore_case:\n%s", out)
	}
}

// TestWorkspaceToolsRefuseWideRoot: a channel pinned to $HOME must not become
// a whole-disk reader. The projects breadth rule is the same gate the console's
// file viewer uses.
func TestWorkspaceToolsRefuseWideRoot(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	for _, dir := range []string{"", home} {
		reg := entry.NewRegistry()
		registerWorkspaceTools(reg, dir)
		if _, ok := reg.Lookup("workspace_read"); ok {
			t.Errorf("workspace_read registered for work dir %q", dir)
		}
	}
}
