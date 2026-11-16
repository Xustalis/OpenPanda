// SPDX-License-Identifier: AGPL-3.0-or-later

package queue

import (
	"path/filepath"
	"strings"
)

// Scope keys give a shared project worktree sub-path granularity: instead of
// every project task serializing on one monolithic "project:<name>" key, a
// task that declares the paths it will touch locks "scope:<name>/<path>"
// leaves — disjoint scopes run in parallel, overlapping ones serialize, and
// the root scope "scope:<name>" (held by any task that did NOT narrow its
// claim) still excludes every descendant, exactly like a database intent
// lock. The model mirrors the per-sender-slot idea: coordination at the
// granularity of what is actually contended, never a global broadcast.
//
// Conflict between two scope keys is prefix-overlap in EITHER direction:
// "scope:p/a" blocks "scope:p", "scope:p/a/b" and itself, but not
// "scope:p/ab" (sibling, different file) nor "scope:q/a" (different
// project). Physical "workspace:" keys use the same rule; every other key
// type keeps exact-match semantics.
const scopeKeyPrefix = "scope:"

// Workspace keys describe the physical paths the executor will write. They
// supplement project keys: a checkout can be registered under two names or
// reached through a projectless pin, but it still admits only one writer per
// overlapping subtree.
const workspaceKeyPrefix = "workspace:"

// lockFold folds a lock-key component to its case-insensitive identity.
// Scope leaves and pinned work dirs name filesystem paths, and the
// filesystems most users run on (APFS, NTFS) treat "Docs" and "docs" as one
// directory — a byte-precise key would let two writers of the same tree
// hold disjoint locks. Folding errs toward serialization: on a
// case-sensitive volume the odd colliding pair queues where it could have
// run in parallel, a safe direction. (Unicode normalization differs by
// volume and lies outside stdlib-only key math.)
func lockFold(s string) string { return strings.ToLower(s) }

// IsScopeKey reports whether key uses hierarchical subtree exclusion.
func IsScopeKey(key string) bool {
	return strings.HasPrefix(key, scopeKeyPrefix) || strings.HasPrefix(key, workspaceKeyPrefix)
}

// ScopeRootKey returns the whole-project scope key — the claim every task
// holds that did not narrow itself to declared paths. A scoped child never
// carries it (that would re-serialize siblings); an unscoped project task
// always does, so whole-project work still excludes scoped children and
// vice versa.
func ScopeRootKey(project string) string {
	return scopeKeyPrefix + lockFold(project)
}

// sessionScopeSegment recognizes keys persisted by the former conversation
// lane scheme. A conversation is not a writable workspace: two conversations
// in one checkout must contend for the same paths.
const sessionScopeSegment = "@session"

// SessionScopeKey keeps the submission API compatible while making the lock
// identity independent of conversation identity. Private worktrees use their
// own directory keys instead of weakening the shared-checkout lock.
func SessionScopeKey(project, _ string) string {
	return ScopeRootKey(project)
}

// SessionProjectKey is the corresponding legacy whole-project key.
func SessionProjectKey(project, _ string) string {
	return ProjectKey(project)
}

// SessionScopeLeafKey locks the same project-relative path for every session
// and for submitters without a session. Disjoint declared paths still coexist.
func SessionScopeLeafKey(project, _ string, path string) string {
	return ScopeLeafKey(project, path)
}

// CanonicalKey upgrades a persisted conversation-lane identity without a
// schema migration. Other resource namespaces, including physical workspace
// paths that happen to contain "@session", keep their original identity.
func CanonicalKey(key string) string {
	if !strings.HasPrefix(key, scopeKeyPrefix) && !strings.HasPrefix(key, "project:") {
		return key
	}
	root, tail, ok := strings.Cut(key, "/")
	if !ok || !strings.HasPrefix(tail, sessionScopeSegment+"/") {
		return key
	}
	tail = strings.TrimPrefix(tail, sessionScopeSegment+"/")
	_, leaf, hasLeaf := strings.Cut(tail, "/")
	if hasLeaf && strings.HasPrefix(key, scopeKeyPrefix) && leaf != "" {
		return root + "/" + leaf
	}
	return root
}

// CanonicalKeys also removes duplicates created when old session identities
// collapse onto one shared workspace identity.
func CanonicalKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		key = CanonicalKey(key)
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	return out
}

// WorkspaceScopeKey resolves a real directory and optional relative path to
// a hierarchical physical lock. Resolving each leaf also catches symlinks
// between otherwise disjoint declared paths.
func WorkspaceScopeKey(dir, path string) string {
	if dir == "" {
		return ""
	}
	return workspaceKeyPrefix + filepath.ToSlash(workdirKey(filepath.Join(dir, filepath.FromSlash(path))))
}

// ScopeLeafKey returns the scope key for one declared project-relative path.
// An empty path names the root itself.
func ScopeLeafKey(project, path string) string {
	if path == "" {
		return ScopeRootKey(project)
	}
	return scopeKeyPrefix + lockFold(project) + "/" + lockFold(path)
}

// ScopeKeysConflict reports whether two scope keys contend for the same
// subtree: equal, or one a strict ancestor of the other ("/"-bounded, so
// "…/a" never bleeds into "…/ab").
func ScopeKeysConflict(a, b string) bool {
	a, b = CanonicalKey(a), CanonicalKey(b)
	if a == b {
		return true
	}
	return strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// ScopeAncestors lists a scope key's strict ancestor keys — for
// "scope:p/a/b": [scope:p, scope:p/a]. The acquiring side checks these for
// already-held rows; its own descendants are covered by a LIKE pattern.
func ScopeAncestors(key string) []string {
	if !IsScopeKey(key) {
		return nil
	}
	key = CanonicalKey(key)
	prefixLen := len(scopeKeyPrefix)
	if strings.HasPrefix(key, workspaceKeyPrefix) {
		prefixLen = len(workspaceKeyPrefix)
	}
	var out []string
	for i := prefixLen; ; {
		j := strings.IndexByte(key[i:], '/')
		if j < 0 {
			return out
		}
		out = append(out, key[:i+j])
		i += j + 1
	}
}
