// SPDX-License-Identifier: AGPL-3.0-or-later

package scout

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func hasGit() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func TestScanReportsLayoutAndCommands(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/x\n")
	write(t, root, "Makefile", "build:\n\tgo build ./...\n\ntest:\n\tgo test ./...\n\n.PHONY: build test\n")
	write(t, root, "README.md", "# x\n")
	write(t, root, "src/a.go", "package src\n")
	write(t, root, "src/b.go", "package src\n")
	write(t, root, "webui/app/src/x.ts", "export {}\n")
	write(t, root, "node_modules/pkg/index.js", "module.exports = {}\n")

	r := Scan(root)
	if r.Empty() {
		t.Fatal("report is empty for a populated tree")
	}
	if r.Files != 6 {
		t.Fatalf("files = %d, want 6 (node_modules skipped)", r.Files)
	}
	if r.Dirs != 4 { // src, webui, webui/app, webui/app/src
		t.Fatalf("dirs = %d, want 4", r.Dirs)
	}
	if len(r.TopDirs) == 0 || r.TopDirs[0].Name != "." || r.TopDirs[0].Files != 3 {
		t.Fatalf("top dirs = %+v, want root files leading with 3", r.TopDirs)
	}
	if r.TopDirs[1].Name != "src" || r.TopDirs[1].Files != 2 {
		t.Fatalf("top dirs = %+v, want src second with 2 files", r.TopDirs)
	}
	// go.mod also yields go build/test; both are legitimate entries, and the
	// manifest command must not repeat.
	var haveMakeBuild, haveGoBuild, haveMakeTest, haveGoTest bool
	for _, c := range r.Build {
		haveMakeBuild = haveMakeBuild || c.Run == "make build"
		haveGoBuild = haveGoBuild || c.Run == "go build ./..."
	}
	for _, c := range r.Test {
		haveMakeTest = haveMakeTest || c.Run == "make test"
		haveGoTest = haveGoTest || c.Run == "go test ./..."
	}
	if !haveMakeBuild || !haveGoBuild || !haveMakeTest || !haveGoTest {
		t.Fatalf("build=%+v test=%+v, want make and go entries", r.Build, r.Test)
	}
	var keyed bool
	for _, k := range r.KeyFiles {
		keyed = keyed || k == "go.mod"
	}
	if !keyed {
		t.Fatalf("key files = %v, want go.mod", r.KeyFiles)
	}
}

func TestScanEmptyWorkspaceIsEmpty(t *testing.T) {
	if r := Scan(t.TempDir()); !r.Empty() {
		t.Fatalf("empty dir produced a report: %+v", r)
	}
	if got := (Report{}).Render(); got != "" {
		t.Fatalf("empty render = %q, want empty", got)
	}
}

func TestRenderNamesTheWorkspace(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Makefile", "build:\n\ttrue\ntest:\n\ttrue\n")
	write(t, root, "main.go", "package main\n")
	r := Scan(root)
	out := r.Render()
	for _, want := range []string{
		"[workspace map", "root: " + root, "size: 2 files", "top dirs: (root) 2",
		"languages: go 1", "key files: Makefile", "build: make build", "verify: make test",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
}

func TestRenderIsBounded(t *testing.T) {
	r := Report{
		Root:  "/x",
		Files: 9,
		KeyFiles: []string{
			"README.md", "AGENTS.md", "CLAUDE.md", "CONTRIBUTING.md",
			"Makefile", "go.mod", "package.json", "pyproject.toml",
		},
		TopDirs: []DirCount{{Name: "a", Files: 1}, {Name: "b", Files: 1}, {Name: "c", Files: 1}},
		Langs:   []DirCount{{Name: "go", Files: 1}},
	}
	out := r.Render()
	if len(out) > maxRender+64 {
		t.Fatalf("render is %d bytes, cap %d", len(out), maxRender)
	}
}

// TestScanUsesGitIdentity pins the one subprocess path. git may be absent on
// a minimal host; the test then only asserts that scanning still works.
func TestScanDetectsPackageScripts(t *testing.T) {
	root := t.TempDir()
	write(t, root, "package.json", `{"scripts":{"build":"vite build","test":"vitest run","lint":"eslint ."}}`)
	write(t, root, "index.ts", "export {}\n")
	r := Scan(root)
	if len(r.Build) != 1 || r.Build[0].Run != "npm run build" {
		t.Fatalf("build = %+v, want npm run build", r.Build)
	}
	var test, lint bool
	for _, c := range r.Test {
		test = test || c.Run == "npm run test"
		lint = lint || c.Run == "npm run lint"
	}
	if !test || !lint {
		t.Fatalf("verify = %+v, want npm run test and npm run lint", r.Test)
	}
}

func TestScanUsesGitIdentity(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Makefile", "build:\n\ttrue\n")
	if !hasGit() {
		if r := Scan(root); r.GitKnown {
			t.Fatal("GitKnown set without git")
		}
		return
	}
	runGit(t, root, "init", "-q")
	runGit(t, root, "add", "Makefile")
	runGit(t, root, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-qm", "init")
	r := Scan(root)
	if !r.GitKnown || r.Commit == "" {
		t.Fatalf("git identity missing: %+v", r)
	}
	if r.Branch == "" || r.Branch == "HEAD" {
		t.Fatalf("branch = %q, want a named branch", r.Branch)
	}
	write(t, root, "dirty.txt", "x")
	write(t, root, "Makefile", "build:\n\ttrue\n# dirty\n")
	if r := Scan(root); !r.Dirty {
		t.Fatal("dirty tree not reported")
	}
}
