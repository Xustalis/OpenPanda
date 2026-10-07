// SPDX-License-Identifier: AGPL-3.0-or-later

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
	"slices"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/projects"
)

// workspaceMarkersStrong are the signals that say "this directory is a
// project" beyond doubt: a VCS root, a language/build manifest, or agent/node
// config dirs. The daemon's launch-directory adoption uses ONLY these —
// running `panda daemon` inside a stray directory must not silently relocate
// the node's work space.
var workspaceMarkersStrong = []string{
	".git", "go.mod", "package.json", "Cargo.toml", "pyproject.toml",
	"pom.xml", "build.gradle", "build.gradle.kts", "CMakeLists.txt",
	".panda", ".pi", ".claude", "capabilities.yaml",
}

// workspaceMarkers extends the strong set with files that mean "project" to
// an operator at a prompt but are too common to drive an unattended daemon's
// adoption: a Makefile or requirements.txt sits in any old scratch or home
// dir. Interactive entry points (ask, repl) use this broader set; the daemon
// path does not.
var workspaceMarkers = append(slices.Clone(workspaceMarkersStrong),
	"Makefile", "makefile", "setup.py", "requirements.txt",
	"AGENTS.md", "CLAUDE.md")

func hasMarker(dir string, markers []string) bool {
	for _, m := range markers {
		if _, err := os.Lstat(filepath.Join(dir, m)); err == nil {
			return true
		}
	}
	return false
}

// looksLikeWorkspace reports whether dir carries at least one workspace
// marker — the cheap, deterministic side of "this directory is a project"
// for interactive entry points.
func looksLikeWorkspace(dir string) bool {
	return dir != "" && hasMarker(dir, workspaceMarkers)
}

// looksLikeWorkspaceStrong is looksLikeWorkspace restricted to the
// unambiguous marker set — the bar the daemon's unattended adoption uses.
func looksLikeWorkspaceStrong(dir string) bool {
	return dir != "" && hasMarker(dir, workspaceMarkersStrong)
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
