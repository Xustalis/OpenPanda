// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/skills"
	"path/filepath"
	"strings"
)

// SetMemoryStores attaches the memory layer (design §17/§8): the Hermes
// injector (entry-model conversation context; no longer used for agent-prompt
// injection since A1), the daily log writer feeding the Dreaming engine, and
// the skill store for progressive loading. Any may be nil to disable its layer.
func (c *Core) SetMemoryStores(inj *memory.Injector, daily *memory.Daily, sk *skills.Store) {
	c.memory = inj
	c.daily = daily
	c.skills = sk
	if sk != nil {
		c.tracker = skills.NewTracker(sk)
	}
}

// SetProjectStores attaches the project plane: the metadata table (which knows a
// project's work tree) and the root of the project memory directories. Both are
// needed to make a delegated project task mean anything on the receiving machine
// — one supplies the tree to send, the other the directory to land memory in.
// Either may be zero, which leaves project-aware delegation off.
func (c *Core) SetProjectStores(store *projects.Store, projectsRoot string) {
	c.projects = store
	c.projectsRoot = projectsRoot
}

// SetWorkDir pins the directory agents execute in and scope drift is measured
// against. Empty resets to the default (the daemon's working directory).
func (c *Core) SetWorkDir(dir string) {
	if dir == "" {
		dir = "."
	}
	c.workDir = dir
}

// SetScratchRoot pins where the node's runtime scratch lives — workDir/tasks,
// workDir/plans and workDir/projects all derive from it. The empty-string
// default keeps scratch directly under workDir, which is right when workDir
// IS the node's data dir (the default layout, where these dirs sit beside
// openpanda.db). A workDir that is a user-facing workspace — an adopted
// launch directory or an explicit storage.work_path pointing at a checkout —
// should pass workDir/.openpanda so panda-owned scratch hides in one
// git-ignorable directory instead of three visible ones.
//
// The root is host state — scratch churn (a stage unpacking inputs, a peer's
// project tree landing) is the node's own bookkeeping, not agent scope
// drift, and must never ride out inside an artifact pack. hostState()
// unions it in at read time so wiring order can never drop it.
func (c *Core) SetScratchRoot(root string) {
	if root == "" {
		return
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = filepath.Clean(abs)
	}
	c.scratchDir = root
}

// scratchRoot returns the directory runtime scratch derives from (see
// scratchDir); unset it falls back to the work dir.
func (c *Core) scratchRoot() string {
	if c.scratchDir != "" {
		return c.scratchDir
	}
	return c.workDir
}

// hostState is the effective host-state path list: the configured paths
// plus the scratch root when it differs from the work dir (the workspace
// layout — under the data-dir layout scratch already rides the WorkPath
// entry, so an equal root adds nothing). Read-time union keeps the
// membership independent of setter call order.
func (c *Core) hostState() []string {
	if c.scratchDir == "" || c.scratchDir == c.workDir {
		return c.hostStatePaths
	}
	out := make([]string, 0, len(c.hostStatePaths)+1)
	out = append(out, c.hostStatePaths...)
	return append(out, c.scratchDir)
}

// SetHostStatePaths records the node's own bookkeeping paths — its SQLite/memory
// trees and the agent CLI's own config dir — so scope-drift detection never
// flags the host's side-effect writes as agent drift. Paths may be relative to
// the working directory; they are normalized to absolute on entry.
func (c *Core) SetHostStatePaths(paths []string) {
	c.hostStatePaths = make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue // Abs("") is the process cwd — never host state
		}
		if abs, err := filepath.Abs(p); err == nil {
			c.hostStatePaths = append(c.hostStatePaths, filepath.Clean(abs))
		}
	}
}

// SetPackProtectedPaths records the node's own never-ship files — the config
// (mesh secret, API keys) and the capability card — so artifact packs prune
// them wherever a tree happens to contain them, and extracts refuse to let an
// incoming tree overwrite them. Kept separate from hostStatePaths because the
// drift detector must still flag a task that rewrites the live config.
func (c *Core) SetPackProtectedPaths(paths []string) {
	c.packProtectedPaths = make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		if abs, err := filepath.Abs(p); err == nil {
			c.packProtectedPaths = append(c.packProtectedPaths, filepath.Clean(abs))
		}
	}
}

// filterHostDrift drops changed paths that live under a host-owned directory.
// Changes there (SQLite WAL, skill/dream files, the agent CLI's own config) are
// the node's own bookkeeping, not the agent's task output, so they must not
// pause a task for scope drift. The drift paths are relative to the run's
// workDir — a stage dir or an attached task's private dir, not always the
// node root — so the caller passes the root they were snapshotted under.
//
// A host path that IS the workDir or contains it is skipped rather than
// applied: host state masks only what it covers inside the snapshot tree,
// and a container match would swallow every change the agent made. The
// default layout puts the database in the work root, so the data dir is
// such a container there — the precise file/subdir entries still cover the
// host's own writes under it.
func (c *Core) filterHostDrift(workDir string, drift []string) []string {
	host := c.hostState()
	if len(host) == 0 {
		return drift
	}
	wd, _ := filepath.Abs(workDir)
	wd = filepath.Clean(wd)
	out := make([]string, 0, len(drift))
	for _, p := range drift {
		ap := filepath.Join(wd, p)
		keep := true
		for _, h := range host {
			if h == wd || strings.HasPrefix(wd, h+string(filepath.Separator)) {
				continue
			}
			if ap == h || strings.HasPrefix(ap, h+string(filepath.Separator)) {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, p)
		}
	}
	return out
}
