package main

// workspace.go is the one place that answers "where does work happen?":
// the directory panda was launched in IS the project space ("在哪里启动
// 哪里就是项目空间"). REPL, `panda ask`, and the daemon share the helpers
// below so every entry point resolves the same workspace, binds the same
// project row, and lets the executing harness inherit the same cwd.
//
// Two rules keep the convenience from becoming spam:
//   - adoption only fires on directories that look like workspaces
//     (VCS root, manifest, project config) — a stray cd into $HOME or /tmp
//     does not mint a project row;
//   - an explicit operator choice always wins: --project, storage.work_path,
//     or OPENPANDA_WORK_PATH outrank the launch directory.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/projects"
)

// workspaceMarkers are the files that make a directory a project workspace:
// a VCS root, a language manifest, a build file, or agent/node config dirs.
// A directory carrying none of them is treated as ambient — launching panda
// from $HOME or a temp dir does not mint a project.
var workspaceMarkers = []string{
	".git", "go.mod", "package.json", "Cargo.toml", "pyproject.toml",
	"pom.xml", "build.gradle", "build.gradle.kts", "Makefile", "makefile",
	"CMakeLists.txt", "setup.py", "requirements.txt",
	".panda", ".pi", ".claude", "capabilities.yaml", "AGENTS.md", "CLAUDE.md",
}

// looksLikeWorkspace reports whether dir carries at least one workspace
// marker — the cheap, deterministic side of "this directory is a project".
func looksLikeWorkspace(dir string) bool {
	if dir == "" {
		return false
	}
	for _, m := range workspaceMarkers {
		if _, err := os.Lstat(filepath.Join(dir, m)); err == nil {
			return true
		}
	}
	return false
}

// projectForDir resolves the project owning dir: the project whose WorkDir
// equals dir or — walking outward — is a proper ancestor of it. Longest
// matching WorkDir wins, so a nested repo binds to its own project rather
// than the enclosing one. "" means no project owns the directory.
func projectForDir(store *projects.Store, dir string) (name, workDir string) {
	if store == nil || dir == "" {
		return "", ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", ""
	}
	abs = filepath.Clean(abs)
	list, err := store.List()
	if err != nil {
		return "", ""
	}
	best := ""
	for _, p := range list {
		wd := filepath.Clean(p.WorkDir)
		if wd == "" || wd == "." {
			continue
		}
		if abs != wd && !strings.HasPrefix(abs, wd+string(filepath.Separator)) {
			continue
		}
		if len(wd) > len(best) {
			best = wd
			name, workDir = p.Name, p.WorkDir
		}
	}
	return name, workDir
}

// adoptWorkspaceProject binds cwd to a project row: the project already
// owning the directory wins; otherwise a fresh "<basename>" project is
// created with the cwd as its work dir. Either way the project becomes the
// active one, so every later entry point (task add, schedule, agents) lands
// in the same space. Returns the adopted project name, "" on failure —
// adoption is best-effort, never fatal.
func adoptWorkspaceProject(store *projects.Store, cwd string) string {
	if store == nil || cwd == "" {
		return ""
	}
	// Deepest owning project wins first: launching inside a subdirectory of
	// an existing project binds that project — minting a row for the subdir
	// would split one tree into two spaces and disagree with the
	// ancestor-match rule ask/task add already apply.
	if name, _ := projectForDir(store, cwd); name != "" {
		_ = store.SetActive(name)
		return name
	}
	if existing, err := store.FindByWorkDir(cwd); err == nil {
		_ = store.SetActive(existing.Name)
		return existing.Name
	}
	base := sanitizeProjectName(filepath.Base(cwd))
	candidate := base
	for i := 1; i <= 100; i++ {
		if pr, err := store.Get(candidate); err == nil {
			if pr.WorkDir == "" {
				_, _ = store.Update(candidate, cwd, "Workspace at "+cwd)
				_ = store.SetActive(candidate)
				return candidate
			}
			candidate = fmt.Sprintf("%s-%d", base, i+1)
		} else {
			if created, err := store.Create(candidate, cwd, "Workspace at "+cwd); err == nil {
				_ = store.SetActive(created.Name)
				return created.Name
			}
			return ""
		}
	}
	return ""
}

// ambientProject resolves the launch directory's project for entry points
// with no explicit --project: the project owning or actively matching the
// cwd wins; otherwise the cwd is adopted when it looks like a workspace.
// dir is the resolved workspace (the cwd even when no project exists).
func ambientProject(cfg *config.Config) (name, dir string) {
	name, dir = activeProject(cfg)
	if name == "" && dir != "" && looksLikeWorkspace(dir) {
		if db, _, err := panelStore(cfg); err == nil {
			defer db.Close()
			if adopted := adoptWorkspaceProject(projects.NewStore(db), dir); adopted != "" {
				name = adopted
			}
		}
	}
	return
}
