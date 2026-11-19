// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/scheduler/queue"
)

// maxScopePaths bounds how many scope leaves one delegate request may
// declare — a child holding dozens of locks is a worse-than-monolithic
// claim, so the cap doubles as the "you are not really scoped" boundary.
const maxScopePaths = 8

// ValidateDispatchPaths rejects ambiguous or aliased write scopes, rather
// than cleaning them into narrower claims than the submitter intended.
func ValidateDispatchPaths(workDir string, paths []string) error {
	if len(paths) > maxScopePaths {
		return fmt.Errorf("paths: at most %d write scopes are allowed", maxScopePaths)
	}
	for _, p := range paths {
		if p == "" || p == "." || p != strings.TrimSpace(p) || len(p) > 512 ||
			p != path.Clean(p) || !filepath.IsLocal(filepath.FromSlash(p)) ||
			strings.ContainsAny(p, "\\:*?[]{}\x00\r\n\t") {
			return fmt.Errorf("paths: %q must be an exact workspace-relative path", p)
		}
		dir := workDir
		for _, part := range strings.Split(p, "/") {
			if isolateSkippedPath(strings.ToLower(part)) || part == isolatedBaselineFile {
				return fmt.Errorf("paths: %q includes workspace metadata or an excluded directory", p)
			}
			if workDir == "" {
				continue
			}
			dir = filepath.Join(dir, part)
			st, err := os.Lstat(dir)
			if st != nil && st.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("paths: %q traverses a symbolic link", p)
			}
			if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("paths: inspect %q: %w", p, err)
			}
		}
	}
	return nil
}

func scopedDispatch(task Task) bool {
	return task.ParentID == "" && task.PlanID == "" && task.Isolate && len(task.ScopePaths) > 0
}

func (c *Core) scopedDispatchQueueKeys(id string, in TaskInput) []string {
	keys := make([]string, 0, len(in.Paths)*2)
	for _, p := range normalizeScopePaths(in.Paths) {
		keys = append(keys, queue.WorkspaceScopeKey(in.WorkDir, p))
	}
	if root, err := c.projectWorkDir(in.Project); err == nil {
		if prefix, nested := relUnderDir(root, in.WorkDir); nested {
			for _, p := range normalizeScopePaths(in.Paths) {
				keys = append(keys, queue.ScopeLeafKey(in.Project, path.Join(prefix, p)))
			}
			return queue.CanonicalKeys(mergeActuatorKeys(keys, in.Requires, in.SpecJSON))
		}
	}
	return queue.CanonicalKeys(append(keys, c.childResourceKeysFor(id, in)...))
}

func pathInScope(p string, paths []string) bool {
	for _, scope := range paths {
		if p == scope || strings.HasPrefix(p, scope+"/") {
			return true
		}
	}
	return false
}

// normalizeScopePaths cleans declared project-relative paths into canonical
// scope leaves: separators fold to "/", "." and ".." resolve against the
// project root (an escaping claim clamps to the root rather than silently
// leaking outside the project), and any leaf nested inside another declared
// leaf is redundant and dropped. An all-invalid or empty declaration leaves
// nothing — the caller then falls back to the ordinary project key, which
// is the honest serialization for an unscoped claim.
func normalizeScopePaths(paths []string) []string {
	seen := map[string]bool{}
	var clean []string
	for _, p := range paths {
		p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
		if p == "" {
			continue // a blank entry is a malformed list element, not a root claim
		}
		p = path.Clean("/" + p)
		p = strings.TrimPrefix(p, "/")
		if seen[p] {
			continue
		}
		seen[p] = true
		clean = append(clean, p)
	}
	// Shallowest-first so a nested leaf can never shadow its ancestor.
	sort.SliceStable(clean, func(i, j int) bool {
		return strings.Count(clean[i], "/") < strings.Count(clean[j], "/")
	})
	var out []string
	for _, p := range clean {
		nested := false
		for _, kept := range out {
			if kept == p || (kept != "" && strings.HasPrefix(p, kept+"/")) {
				nested = true
				break
			}
		}
		if !nested {
			out = append(out, p)
		}
		if len(out) >= maxScopePaths {
			break
		}
	}
	return out
}

// scopeLeafKeys renders project-relative claims independent of conversation
// identity. All writers of one shared path contend; disjoint declarations
// retain their parallelism, including submitters without a conversation.
func scopeLeafKeys(project, sessionID string, paths []string) []string {
	keys := make([]string, 0, len(paths))
	for _, p := range paths {
		keys = append(keys, queue.SessionScopeLeafKey(project, sessionID, p))
	}
	return keys
}

// relUnderDir reports p relative to root when p is STRICTLY inside it, in slash
// form.
//
// Both arguments are REAL paths, not lock identities. A lock identity is folded
// to lower case (queue.lockFold), and on a case-sensitive filesystem a folded
// path names nothing on disk: resolving one finds no existing prefix at all and
// quietly answers with the input, which turns the containment check back into
// the path math it was meant to stop being. The caller keeps the identity for
// the equality test and hands the real paths here.
//
// The comparison resolves symlinks on both sides, through the longest existing
// prefix: a pin may name a directory that is created when the task runs, and
// filepath.EvalSymlinks gives up on the whole path when its tail is missing.
// A link inside the tree pointing out of it is then not "inside" — which
// matters, because that answer hands out the project's lock for a tree the
// project never sees.
//
// It exists because a directory inside the project's own tree is not a separate
// tree: a dir: lock would be invisible to the project's root scope, letting a
// whole-project task and this one write one checkout concurrently. Such a
// directory has to be expressed as a scope leaf instead.
func relUnderDir(root, p string) (string, bool) {
	if root == "" || p == "" {
		return "", false
	}
	rel, err := filepath.Rel(resolveDir(root), resolveDir(p))
	if err != nil {
		return "", false
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false
	}
	// The lane itself is a lock identity, so queue.ScopeLeafKey folds it — the
	// case here is the tree's own, which is the only spelling that can be read
	// back from disk.
	return filepath.ToSlash(rel), true
}

// resolveDir resolves the symlinks of a REAL directory path that may not exist
// yet, by resolving its longest existing prefix and re-attaching the rest. A
// plain EvalSymlinks answers "does not exist" for the whole path and leaves
// every link in the prefix unfolded — the one case a containment check cannot
// afford to guess, since the unfolded prefix is what decides which tree the path
// belongs to. Nothing on the path existing keeps the caller's own spelling,
// which is all there is to compare.
func resolveDir(dir string) string {
	if dir == "" {
		return ""
	}
	rest := ""
	for cur := dir; ; {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return dir
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// childResourceKeys computes the lock set a delegate child holds. An
// isolated child works in a private copy, so it locks nothing shared — just
// its own id (plus actuators, which are physical and always exclusive). A
// child that declares project-relative paths gets the matching scope leaves —
// siblings with disjoint scopes then run in parallel inside the same
// worktree, while any task holding the project root (an unscoped claim, a
// fresh whole-project task) still conflicts normally. A child without a
// declaration keeps the monolithic project serialization: an agent that
// cannot name its scope cannot be allowed to narrow its locks on a guess.
//
// A pinned work directory outranks the project key: it names a DIFFERENT tree
// (delegateChild clears a pin that resolves to the parent's own work dir, and
// turns a pin INSIDE that tree into scope lanes), and holding the project key
// as well would serialize the child behind every sibling editing a tree it
// never touches — the whole point of naming a directory is that two business
// domains do not queue behind each other. Paths win over the pin because the
// pin's inside-the-project form arrives as both: the directory says where to
// run, the lanes say which subtree the locks stand for.
func childResourceKeys(taskID string, in TaskInput) []string {
	if in.Project != "" && in.Isolate {
		return mergeActuatorKeys([]string{"task:" + taskID}, in.Requires, in.SpecJSON)
	}
	if in.Project != "" {
		if paths := normalizeScopePaths(in.Paths); len(paths) > 0 {
			return mergeActuatorKeys(scopeLeafKeys(in.Project, in.SessionID, paths), in.Requires, in.SpecJSON)
		}
	}
	if wd := queue.WorkDirKey(in.WorkDir); wd != "" {
		return mergeActuatorKeys([]string{"dir:" + wd}, in.Requires, in.SpecJSON)
	}
	return deriveResourceKeys(in)
}

// childResourceKeysFor resolves the one distinction the input-only helper
// cannot: declared project paths in an explicitly separate worktree belong to
// that tree, rather than to the project's shared checkout.
func (c *Core) childResourceKeysFor(taskID string, in TaskInput) []string {
	if in.Project != "" && !in.Isolate && in.WorkDir != "" {
		paths := normalizeScopePaths(in.Paths)
		if len(paths) > 0 {
			shared := false
			if projectDir, err := c.projectWorkDir(in.Project); err == nil {
				shared = queue.WorkDirKey(projectDir) == queue.WorkDirKey(in.WorkDir)
				if !shared {
					_, shared = relUnderDir(projectDir, in.WorkDir)
				}
			}
			if !shared {
				keys := make([]string, 0, len(paths))
				for _, p := range paths {
					keys = append(keys, queue.WorkspaceScopeKey(in.WorkDir, p))
				}
				return mergeActuatorKeys(keys, in.Requires, in.SpecJSON)
			}
		}
	}
	return childResourceKeys(taskID, in)
}

// workspaceExecKeys mirrors prepareRunDir's directory precedence before any
// workspace is populated or modified. These physical claims supplement the
// queue's project/custom keys, so aliases of a project, nested pins and
// projectless pins cannot hide a shared writable tree. Private task/stage
// workspaces have different identities and remain independent.
func (c *Core) workspaceExecKeys(task Task) ([]string, error) {
	var dir string
	var err error
	switch {
	case task.PlanID != "":
		dir, err = c.stageWorkDir(task.PlanID, task.StageID)
	case task.Isolate && task.Project != "":
		dir, err = c.attachedWorkDir(task.TaskID)
	case projectInputs(task) || (task.Project != "" && task.WorkDir == ""):
		dir, err = c.projectWorkDir(task.Project)
	case task.WorkDir != "":
		dir = task.WorkDir
	default:
		dir, err = c.attachedWorkDir(task.TaskID)
	}
	if err != nil {
		return nil, err
	}
	paths := normalizeScopePaths(task.ScopePaths)
	if len(paths) == 0 || task.Isolate || task.PlanID != "" || task.Project == "" {
		return []string{queue.WorkspaceScopeKey(dir, "")}, nil
	}
	// Scope paths are relative to the project, even when execution was pinned
	// to one of its subdirectories. A pin in a separate worktree instead uses
	// that worktree as its scope root.
	if task.WorkDir != "" && !projectInputs(task) {
		if projectDir, perr := c.projectWorkDir(task.Project); perr == nil {
			if queue.WorkDirKey(dir) == queue.WorkDirKey(projectDir) {
				dir = projectDir
			} else if _, inside := relUnderDir(projectDir, dir); inside {
				dir = projectDir
			}
		}
	}
	keys := make([]string, 0, len(paths))
	for _, p := range paths {
		keys = append(keys, queue.WorkspaceScopeKey(dir, p))
	}
	return queue.CanonicalKeys(keys), nil
}
