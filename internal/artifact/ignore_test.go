// SPDX-License-Identifier: AGPL-3.0-or-later

package artifact

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseIgnoreRules(t *testing.T) {
	rules := parseIgnoreRules(`
# comment
*.db
/rootbin
node_modules/
build/*.out
webui/panel/dist/*
!webui/panel/dist/
!webui/panel/dist/index.html
`)
	tests := []struct {
		rel  string
		dir  bool
		want bool
	}{
		{"x.db", false, true},
		{"a/b/c.db", false, true},
		{"rootbin", false, true},
		{"sub/rootbin", false, false}, // anchored: only root
		{"node_modules", true, true},
		{"a/node_modules", true, true}, // unanchored dir pattern
		{"node_modules", false, false}, // dir-only rule skips files
		{"build/x.out", false, true},
		{"other/build/x.out", false, false}, // interior slash anchors
		{"webui/panel/dist", true, false},   // '!' re-include of the dir
		{"webui/panel/dist/app.js", false, true},
		{"webui/panel/dist/index.html", false, false},
	}
	for _, tc := range tests {
		if got := ignoredBy(rules, tc.rel, tc.dir); got != tc.want {
			t.Errorf("ignoredBy(%q, dir=%v) = %v, want %v", tc.rel, tc.dir, got, tc.want)
		}
	}
}

func TestIgnoreDoubleStar(t *testing.T) {
	rules := parseIgnoreRules("a/**/b\n**/gen/\n")
	for _, tc := range []struct {
		rel  string
		dir  bool
		want bool
	}{
		{"a/b", true, true},
		{"a/x/b", true, true},
		{"a/x/y/b", true, true},
		{"gen", true, true},
		{"x/y/gen", true, true},
		{"a/b/c", true, false}, // a/b matched; c below it is excluded via pruning
	} {
		if got := ignoredBy(rules, tc.rel, tc.dir); got != tc.want {
			t.Errorf("ignoredBy(%q, dir=%v) = %v, want %v", tc.rel, tc.dir, got, tc.want)
		}
	}
}

// TestPackSourceHonorsGitignore pins the recursion fix: a node runtime dir
// (testdata/node-b/) that is git-ignored at the tree root must not be packed,
// otherwise every adopt+re-pack round ships the nested copy one level deeper.
func TestPackSourceHonorsGitignore(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "testdata/node-b/\nconfig.yaml\ndist/*\n!dist/keep.txt\n")
	write("main.go", "package main\n")
	write("testdata/node-b/node-b.db", "sqlite bytes")
	write("testdata/node-b/internal/code.go", "nested project copy")
	write("testdata/node-a/node-a.db", "not ignored — but testdata/node-a/ should be")
	write("config.yaml", "shared_secret: hunter2")
	write("dist/app.js", "build output")
	write("dist/keep.txt", "re-included")

	s := NewStore(filepath.Join(t.TempDir(), "pool"))
	m, err := s.PackSourceDirExceptPaths(root, nil, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, e := range m.Entries {
		paths = append(paths, e.Path)
	}
	has := func(want string) bool {
		for _, p := range paths {
			if p == want {
				return true
			}
		}
		return false
	}
	if !has("main.go") {
		t.Errorf("main.go missing from %v", paths)
	}
	for _, dropped := range []string{
		"testdata/node-b", "testdata/node-b/node-b.db",
		"testdata/node-b/internal/code.go", "config.yaml", "dist/app.js",
	} {
		if has(dropped) {
			t.Errorf("%q should have been ignored, entries=%v", dropped, paths)
		}
	}
	// node-a is NOT in the ignore file here — it must ship. (The real repo's
	// .gitignore lists it; this test pins rule-driven behavior, not fixture
	// contents.)
	if !has("testdata/node-a/node-a.db") {
		t.Errorf("unignored file should ship, entries=%v", paths)
	}
	if !has("dist/keep.txt") {
		t.Errorf("negated file should ship, entries=%v", paths)
	}
}

// TestPackDirExceptPathsKeepsIgnored pins the other half: executor OUTPUT packs
// must carry gitignored files verbatim — dist/ build products are the result.
func TestPackDirExceptPathsKeepsIgnored(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("dist/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dist", "out.bin"), []byte("built"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(filepath.Join(t.TempDir(), "pool"))
	m, err := s.PackDirExceptPaths(root, nil, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range m.Entries {
		if e.Path == "dist/out.bin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("output pack dropped gitignored dist/out.bin: %v", m.Entries)
	}
}
