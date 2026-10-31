// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
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

// scopeLeafKeys renders the exec/queue lock set for one scoped child. The
// session lanes ride on top: a scoped child inside a conversation locks
// "scope:<p>/@session/<sid>/<path>", so its siblings in that same conversation
// keep the overlap rules while another conversation's identical path stays a
// separate lane.
//
// A submitter with no conversation (CLI `task add --paths`, an inbound
// delegate frame) stays on the bare leaf "scope:<p>/<path>". That leaf is a
// sibling of every conversation's lane, so it does not conflict with a
// conversation writing the same path — the same allowance two conversations
// already have. Claiming the project root here would restore the exclusion but
// serialize every conversation-less scoped task against every other one, which
// TestScopedProjectChildrenRunInParallel pins as a regression. The trade-off is
// deliberate and documented in docs/status.md.
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
