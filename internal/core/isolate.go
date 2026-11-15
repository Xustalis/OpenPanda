// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/scheduler/queue"
)

// Isolated delegation: a child that declares "isolate" works in a private
// copy of the project tree — a git worktree when the project is a repository,
// a plain copy otherwise — and locks nothing shared, so any number of
// isolated siblings (and the parent) run truly concurrently. On success the
// workspace's changes merge back into the shared project tree with a
// three-way check (fork baseline vs main tree vs child tree): a path only the
// child touched applies cleanly, a path both sides changed is a reported
// conflict instead of a silent overwrite. This is the codex/claude-code
// worktree model — the same shape internal/sessions uses for session
// sandboxes — fitted to delegation, where the merge target is the project's
// live tree rather than the user's git branch.
//
// The baseline is a per-path sha256 map taken the moment the workspace is
// populated, stored inside the workspace as .openpanda-baseline.json. If the
// file is gone at merge time the merge is skipped and the workspace kept —
// an unverifiable three-way is a guess, and a guess must not overwrite the
// user's tree.

// isolatedBaselineFile is the baseline manifest inside an isolated workspace.
// A dotfile at the root: invisible to a casual `ls`, skipped by every walk
// here, and out of the way of the child's own files.
const isolatedBaselineFile = ".openpanda-baseline.json"

// maxIsolatedConflicts bounds the conflict list carried in the result note:
// the first few dozen names are the actionable set; a pathological run must
// not stuff the transcript with thousands of paths.
const maxIsolatedConflicts = 30

// setupIsolatedWorkDir prepares the private workspace for an isolated task
// and returns its path. An existing workspace with its baseline is reused —
// a resumed or crashed run keeps the work it already did, and the three-way
// merge still sees the original fork state. A fresh workspace is populated
// from source: a git worktree (HEAD checkout plus the working tree's
// uncommitted delta, so the child sees the tree as it is right now) when
// source is the root of a repository, a bounded copy otherwise.
func (c *Core) setupIsolatedWorkDir(ctx context.Context, task Task, source string) (string, error) {
	dir, err := c.attachedWorkDir(task.TaskID)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(dir, isolatedBaselineFile)); err == nil {
		return dir, nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("clear isolated dir: %w", err)
	}
	populated := false
	if repoRoot := gitRepoRootOf(ctx, source); repoRoot != "" {
		if err := gitWorktreeAdd(ctx, repoRoot, dir); err == nil {
			if oerr := overlayDirtyFiles(ctx, source, dir); oerr != nil {
				return "", fmt.Errorf("populate isolated dirty tree: %w", oerr)
			}
			populated = true
		} else {
			c.logger.Warn("git worktree add failed; copying tree",
				"task", task.TaskID, "source", source, "err", err)
		}
	}
	if !populated {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		if err := c.copyWorkTree(source, dir); err != nil {
			return "", err
		}
	}
	// The baseline is the exact fork state — taken after population so the
	// three-way's "unchanged" side means "unchanged since the child started".
	hashes, err := c.hashWorkTree(dir)
	if err != nil {
		return "", err
	}
	if err := writeIsolatedBaseline(dir, hashes); err != nil {
		return "", err
	}
	return dir, nil
}

// gitRepoRootOf returns the repository root when dir IS that root (physical
// path, symlinks resolved) — an isolated copy of a SUBDIRECTORY cannot be a
// worktree checkout of the whole repo without changing what the child sees,
// so those fall back to the plain copy. Empty string = not a usable repo root.
func gitRepoRootOf(ctx context.Context, dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	out, err := exec.CommandContext(ctx, "git", "-C", abs, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	if a, err := filepath.EvalSymlinks(abs); err == nil {
		abs = a
	}
	if root != abs {
		return ""
	}
	return root
}

// gitWorktreeAdd creates a detached worktree of repoRoot at dir. A stale
// registration under the same name (a crashed earlier attempt) is pruned and
// retried once.
func gitWorktreeAdd(ctx context.Context, repoRoot, dir string) error {
	add := func() error {
		out, err := exec.CommandContext(ctx, "git", "-C", repoRoot, "worktree", "add", "--detach", dir, "HEAD").CombinedOutput()
		if err != nil {
			return fmt.Errorf("git worktree add: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := add(); err != nil {
		_ = exec.CommandContext(ctx, "git", "-C", repoRoot, "worktree", "prune").Run()
		_ = os.RemoveAll(dir)
		return add()
	}
	return nil
}

// overlayDirtyFiles copies the source tree's uncommitted delta (modified,
// deleted and untracked files) into a fresh worktree, so the child works on
// the tree as it is now rather than the last commit. Derived/scratch paths
// are never overlaid — the same skip set the baseline walk uses.
func overlayDirtyFiles(ctx context.Context, source, dir string) error {
	// -uall keeps untracked directories expanded: the default collapses a new
	// directory into a single "?? dir/" entry the file-level overlay below
	// would skip, silently dropping every file inside it from the worktree.
	// --ignored=matching lists ignored files individually so the child sees
	// what the copy fallback sees — .env and friends are part of the working
	// tree even though git ignores them (the skip set still shields the big
	// derived dirs like node_modules).
	out, err := exec.CommandContext(ctx, "git", "-C", source, "status", "--porcelain", "-z",
		"--untracked-files=all", "--ignored=matching").Output()
	if err != nil {
		return fmt.Errorf("git status: %w", err)
	}
	fields := strings.Split(string(out), "\x00")
	for i := 0; i < len(fields); i++ {
		e := fields[i]
		if len(e) < 4 {
			continue
		}
		xy, p := e[:2], e[3:]
		if xy[0] == 'R' || xy[0] == 'C' {
			// The renamed/copied-from path rides the next NUL field: a rename
			// must also clear the old path in the worktree, a copy keeps it.
			old := ""
			if i+1 < len(fields) {
				old = fields[i+1]
			}
			i++
			if xy[0] == 'R' && old != "" && !isolateSkippedPath(old) {
				_ = os.Remove(filepath.Join(dir, filepath.FromSlash(old)))
			}
		}
		if isolateSkippedPath(p) {
			continue
		}
		src := filepath.Join(source, filepath.FromSlash(p))
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if strings.Contains(xy, "D") {
			_ = os.Remove(dst)
			continue
		}
		st, err := os.Lstat(src)
		if err != nil {
			continue
		}
		if st.Mode()&os.ModeSymlink != 0 {
			// git tracks symlinks; a flattened copy would rewrite the link
			// as its target's bytes at merge time.
			target, rerr := os.Readlink(src)
			if rerr != nil {
				return fmt.Errorf("overlay %s: %w", p, rerr)
			}
			if err := linkIsolatedPath(dst, target); err != nil {
				return fmt.Errorf("overlay %s: %w", p, err)
			}
			continue
		}
		if st.Mode().IsRegular() {
			if err := copyRegularFile(src, dst, st.Mode()); err != nil {
				return fmt.Errorf("overlay %s: %w", p, err)
			}
		}
	}
	return nil
}

// copyWorkTree copies source into dst (which exists) skipping the derived-dir
// set: the non-git fallback for isolation. Symlinks are recreated as links —
// the copy must preserve what git would have, or the three-way merge later
// flattens every link into its target's bytes. A link the platform refuses
// (Windows without developer mode) degrades to a skip, the pre-symlink
// behavior, rather than failing the whole workspace over it.
func (c *Core) copyWorkTree(source, dst string) error {
	return filepath.WalkDir(source, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(source, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if d.IsDir() {
			if worktreeSkipDirs[d.Name()] || c.isHostStatePath(p) {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, filepath.FromSlash(relSlash)), 0o755)
		}
		if isolateSkippedPath(relSlash) || c.isHostStatePath(p) {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			target, terr := os.Readlink(p)
			if terr != nil {
				return terr
			}
			if lerr := linkIsolatedPath(filepath.Join(dst, filepath.FromSlash(relSlash)), target); lerr != nil {
				c.logger.Debug("copy symlink skipped", "path", relSlash, "err", lerr)
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		return copyRegularFile(p, filepath.Join(dst, filepath.FromSlash(relSlash)), info.Mode())
	})
}

// hashWorkTree hashes every regular file under root into a
// slash-relative-path → sha256 map, skipping the derived-dir set, the
// baseline file itself, and this node's bookkeeping paths inside root. The
// same walk defines the baseline and the merge's "after" side, so the two
// are always comparable.
func (c *Core) hashWorkTree(root string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if d.IsDir() {
			if worktreeSkipDirs[d.Name()] || c.isHostStatePath(p) {
				return filepath.SkipDir
			}
			return nil
		}
		if relSlash == isolatedBaselineFile || isolateSkippedPath(relSlash) || c.isHostStatePath(p) {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			// A symlink's "content" is its target: hashing the target text
			// (under a prefix no file bytes can collide with) lets the
			// three-way see link creation, retarget, and deletion exactly
			// like file create/modify/delete.
			target, terr := os.Readlink(p)
			if terr != nil {
				return terr
			}
			out[relSlash] = hashLinkTarget(target)
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		h, herr := sha256File(p)
		if herr != nil {
			return herr
		}
		out[relSlash] = h
		return nil
	})
	return out, err
}

// isHostStatePath reports whether p is one of this node's own bookkeeping
// files sitting inside the walked tree (the live SQLite database and friends)
// — never content, never merged.
func (c *Core) isHostStatePath(p string) bool {
	abs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	for _, h := range c.hostState() {
		if h == abs {
			return true
		}
	}
	return false
}

// isolateSkippedPath reports whether a repo-relative path's first element is
// in the derived-dir skip set (".git", "node_modules", ".openpanda", ...).
func isolateSkippedPath(rel string) bool {
	rel = strings.TrimPrefix(filepath.ToSlash(rel), "./")
	if rel == "" {
		return false
	}
	first := rel
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		first = rel[:i]
	}
	return worktreeSkipDirs[first]
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashLinkTarget is a symlink's three-way identity: the digest of its
// target text namespaced so no file's content hash can alias it — a file
// replacing a link (or vice versa) always compares unequal, which is the
// change the merge must see.
func hashLinkTarget(target string) string {
	h := sha256.New()
	h.Write([]byte("symlink\x00"))
	h.Write([]byte(target))
	return hex.EncodeToString(h.Sum(nil))
}

// linkIsolatedPath recreates a symlink at dst inside the walked trees:
// anything already occupying the path (a file, an older link) is removed
// first — os.Symlink refuses to overwrite — and the parent directory is
// made on demand like copyRegularFile's.
func linkIsolatedPath(dst, target string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if _, err := os.Lstat(dst); err == nil {
		if err := os.Remove(dst); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(target, dst)
}

func copyRegularFile(src, dst string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func writeIsolatedBaseline(dir string, hashes map[string]string) error {
	raw, err := json.Marshal(hashes)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, isolatedBaselineFile), raw, 0o644)
}

func loadIsolatedBaseline(dir string) (map[string]string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, isolatedBaselineFile))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// mergeIsolatedRun lands an isolated child's work on the shared project tree
// and returns the note to append to its result ("" when nothing happened).
// The merge runs under the project's exec locks — a shared-tree writer must
// never interleave with it — and reports conflicts instead of guessing:
// conflicting paths keep the main tree's bytes, and the workspace is kept so
// the child's versions stay recoverable.
type isolatedMergeResult struct {
	Note      string
	Blocked   bool
	Conflicts []string
}

func (c *Core) mergeIsolatedRun(ctx context.Context, task Task) isolatedMergeResult {
	blocked := func(note string) isolatedMergeResult { return isolatedMergeResult{Note: note, Blocked: true} }
	ctx, run, delivered, stop, err := c.holdMergeLease(ctx, task)
	if err != nil {
		return blocked(fmt.Sprintf("[isolated] merge lease unavailable: %v; work retained", err))
	}
	defer stop()
	if delivered {
		return isolatedMergeResult{Note: "[isolated] workspace already delivered for this attempt"}
	}
	dir, err := c.attachedWorkDir(task.TaskID)
	if err != nil {
		return blocked("[isolated] merge baseline or workspace unavailable; work retained")
	}
	if _, err := os.Stat(filepath.Join(dir, isolatedBaselineFile)); err != nil {
		return blocked("[isolated] merge baseline or workspace unavailable; work retained") // never isolated (or already merged and cleaned)
	}
	// The merge lands where the workspace was populated from: the pinned dir
	// when the task carried one, else the project's working directory —
	// the same source setupIsolatedWorkDir read so the three-way compares
	// what the child actually forked from.
	main := task.WorkDir
	if main == "" {
		var perr error
		main, perr = c.projectWorkDir(task.Project)
		if perr != nil {
			return blocked(fmt.Sprintf("[isolated] merge skipped: project dir unavailable: %v — workspace kept at %s", perr, dir))
		}
	}
	baseline, after, err := c.isolatedChanges(dir)
	if err != nil {
		return blocked(fmt.Sprintf("[isolated] merge failed: %v — workspace kept at %s", err, dir))
	}
	// Inline isolated runs can hold the shared key under their execution
	// nonce. Release workspace keys before acquiring the merge nonce.
	c.execReleaseFor(task.TaskID)
	release, err := c.acquireMergeLocks(ctx, task, run)
	if err != nil {
		return blocked(fmt.Sprintf("[isolated] merge skipped: %v — workspace kept at %s", err, dir))
	}
	defer release()
	if err := c.store.renewMergeLease(ctx, task, run, false); err != nil {
		return blocked(fmt.Sprintf("[isolated] merge lease lost: %v; work retained", err))
	}
	merged, conflicts, err := c.applyIsolatedChangesContext(ctx, dir, main, baseline, after)
	if err != nil {
		return blocked(fmt.Sprintf("[isolated] merge failed: %v — workspace kept at %s", err, dir))
	}
	c.EvTrace(context.WithoutCancel(ctx), task.TaskID, "isolated_merge", map[string]any{
		"workspace": dir, "project_dir": main, "merged": merged, "conflicts": conflicts,
	})
	if len(conflicts) > 0 {
		shown := conflicts
		if len(shown) > maxIsolatedConflicts {
			shown = shown[:maxIsolatedConflicts]
		}
		note := fmt.Sprintf("[isolated] merged %d file(s); %d conflicting path(s) left with the project's version: %s",
			merged, len(conflicts), strings.Join(shown, ", "))
		if len(conflicts) > len(shown) {
			note += fmt.Sprintf(" (+%d more)", len(conflicts)-len(shown))
		}
		note += " — the workspace with this task's versions is kept at " + dir
		if task.ParentID != "" {
			if derr := c.deliverTaskMessage(ctx, task, msgRequest{
				To: "parent", Grade: MsgGradeReport, Body: clipRunes(note, maxMsgReportBody),
			}); derr != nil {
				c.logger.Debug("post merge conflicts", "task", task.TaskID, "err", derr)
			}
		}
		return isolatedMergeResult{Note: note, Blocked: true, Conflicts: conflicts}
	}
	if err := c.store.renewMergeLease(ctx, task, run, true); err != nil {
		return blocked(fmt.Sprintf("[isolated] delivery receipt failed: %v; workspace kept at %s", err, dir))
	}
	c.cleanupIsolatedDir(ctx, dir)
	if merged > 0 {
		return isolatedMergeResult{Note: fmt.Sprintf("[isolated] merged %d file(s) into the project tree", merged)}
	}
	return isolatedMergeResult{}
}

// acquireMergeLocks takes the merge target's whole-tree exec locks on
// behalf of taskID, polling until free — the isolated child itself holds
// no shared keys, so this is where its merge synchronizes with every
// shared-tree writer (the parent's later rounds included). A pinned work
// dir adds its dir key on top: a writer pinned to the same tree locks
// dir:<path>, not the project key, and the merge must wait it out too.
func (c *Core) acquireMergeLocks(ctx context.Context, task Task, runID string) (func(), error) {
	keys := queue.DefaultKeys(nil, task.Project, task.WorkDir, task.TaskID)
	if wd := queue.WorkDirKey(task.WorkDir); wd != "" && !slices.Contains(keys, "dir:"+wd) {
		keys = append(keys, "dir:"+wd)
	}
	if len(keys) == 0 {
		return func() {}, nil
	}
	tick := time.NewTicker(execLockPollEvery)
	defer tick.Stop()
	for {
		ok, err := c.store.TryAcquireExecLocks(ctx, task.TaskID, c.nodeID, runID, keys)
		if err != nil {
			return nil, fmt.Errorf("merge locks: %w", err)
		}
		if ok {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick.C:
		}
	}
	return func() {
		wCtx, cancel := c.storeWriteCtx(context.Background())
		defer cancel()
		if rerr := c.store.ReleaseExecLocksKeys(wCtx, task.TaskID, runID, keys); rerr != nil {
			c.logger.Warn("release merge locks", "task", task.TaskID, "err", rerr)
		}
	}, nil
}

// mergeIsolatedWork is the three-way itself: for every path the child
// changed (present or deleted vs the baseline), apply it when the main tree
// still matches the baseline, skip it when the main tree already matches the
// child, and report a conflict when both sides diverged. Returns the applied
// count and the sorted conflicting paths.
func (c *Core) mergeIsolatedWork(isoDir, mainDir string) (int, []string, error) {
	baseline, after, err := c.isolatedChanges(isoDir)
	if err != nil {
		return 0, nil, err
	}
	return c.applyIsolatedChanges(isoDir, mainDir, baseline, after)
}

func (c *Core) isolatedChanges(dir string) (map[string]string, map[string]string, error) {
	baseline, err := loadIsolatedBaseline(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("baseline unreadable: %w", err)
	}
	after, err := c.hashWorkTree(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("hash workspace: %w", err)
	}
	return baseline, after, nil
}

func (c *Core) applyIsolatedChanges(isoDir, mainDir string, baseline, after map[string]string) (int, []string, error) {
	return c.applyIsolatedChangesContext(context.Background(), isoDir, mainDir, baseline, after)
}

func (c *Core) applyIsolatedChangesContext(ctx context.Context, isoDir, mainDir string, baseline, after map[string]string) (int, []string, error) {
	paths := make(map[string]bool, len(baseline)+len(after))
	for p := range baseline {
		paths[p] = true
	}
	for p := range after {
		paths[p] = true
	}
	merged := 0
	var conflicts []string
	ordered := make([]string, 0, len(paths))
	for p := range paths {
		ordered = append(ordered, p)
	}
	sort.Slice(ordered, func(i, j int) bool {
		// Deletions first, deepest first, before file/directory replacements.
		di, dj := after[ordered[i]] == "", after[ordered[j]] == ""
		if di != dj {
			return di
		}
		if di && len(ordered[i]) != len(ordered[j]) {
			return len(ordered[i]) > len(ordered[j])
		}
		return ordered[i] < ordered[j]
	})
	for _, p := range ordered {
		if err := ctx.Err(); err != nil {
			return merged, conflicts, err
		}
		b, a := baseline[p], after[p]
		if a == b {
			continue // the child never touched it
		}
		m, err := isolatedPathHash(mainDir, p)
		if err != nil {
			conflicts = append(conflicts, p)
			continue
		}
		if m == b {
			// The main tree still matches the fork: the child's state wins —
			// its bytes when it changed or added the path, deletion when it
			// removed it. An apply failure (a path that is a directory on the
			// main side, a permission) reports the path as a conflict instead
			// of aborting the merge — one bad path must not strand the rest
			// of the child's work.
			if err := applyIsolatedPath(isoDir, mainDir, p, a != ""); err != nil {
				conflicts = append(conflicts, p)
				continue
			}
			merged++
			continue
		}
		if m == a {
			continue // both sides landed on the same bytes: nothing to do
		}
		conflicts = append(conflicts, p)
	}
	sort.Strings(conflicts)
	return merged, conflicts, nil
}

// applyIsolatedPath copies (or deletes) one path from the workspace into the
// main tree. Parent directories are created for additions; deletions of
// already-absent paths are no-ops.
func applyIsolatedPath(isoDir, mainDir, rel string, present bool) error {
	dst := filepath.Join(mainDir, filepath.FromSlash(rel))
	if !present {
		if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete %s: %w", rel, err)
		}
		// Directories are not baseline entries. Remove only empty ancestors
		// so a child can replace a deleted subtree with a file. A concurrent
		// addition keeps the directory nonempty and therefore blocks replacement.
		for d := filepath.Dir(filepath.FromSlash(rel)); d != "."; d = filepath.Dir(d) {
			p := filepath.Join(mainDir, d)
			st, err := os.Lstat(p)
			if err != nil || !st.IsDir() {
				break
			}
			if err := os.Remove(p); err != nil {
				break
			}
		}
		return nil
	}
	src := filepath.Join(isoDir, filepath.FromSlash(rel))
	st, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("stat %s: %w", rel, err)
	}
	if st.Mode()&os.ModeSymlink != 0 {
		target, rerr := os.Readlink(src)
		if rerr != nil {
			return fmt.Errorf("readlink %s: %w", rel, rerr)
		}
		if err := linkIsolatedPath(dst, target); err != nil {
			return fmt.Errorf("apply %s: %w", rel, err)
		}
		return nil
	}
	if old, err := os.Lstat(dst); err == nil && old.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(dst); err != nil {
			return err
		}
	}
	if err := copyRegularFile(src, dst, st.Mode()); err != nil {
		return fmt.Errorf("apply %s: %w", rel, err)
	}
	return nil
}

// cleanupIsolatedDir removes a merged workspace: a git worktree is removed
// through git (so its admin registration goes with it), with the plain
// removal + prune as the fallback; everything else is a recursive delete.
func (c *Core) cleanupIsolatedDir(ctx context.Context, dir string) {
	if _, err := os.Stat(dir); err != nil {
		return
	}
	if st, err := os.Stat(filepath.Join(dir, ".git")); err == nil && !st.IsDir() {
		// A linked worktree carries a .git *file*; the removal must run from
		// the main repository, resolved through the common dir (show-toplevel
		// from inside names the worktree itself).
		if root := gitMainRepoRoot(ctx, dir); root != "" {
			if out, err := exec.CommandContext(ctx, "git", "-C", root, "worktree", "remove", "--force", dir).CombinedOutput(); err == nil {
				return
			} else {
				c.logger.Debug("git worktree remove", "dir", dir, "err", strings.TrimSpace(string(out)))
			}
			_ = os.RemoveAll(dir)
			_ = exec.CommandContext(ctx, "git", "-C", root, "worktree", "prune").Run()
			return
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		c.logger.Warn("remove isolated dir", "dir", dir, "err", err)
	}
}

// gitMainRepoRoot resolves the main repository root from inside a linked
// worktree via the common git dir; "" when the layout is not the ordinary
// <root>/.git one (bare repos, odd setups) and the caller should just delete.
func gitMainRepoRoot(ctx context.Context, dir string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return ""
	}
	gd := strings.TrimSpace(string(out))
	if gd == "" {
		return ""
	}
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(dir, gd)
	}
	gd = filepath.Clean(gd)
	if filepath.Base(gd) != ".git" {
		return ""
	}
	return filepath.Dir(gd)
}

// Read only changed target paths while holding the shared-tree lease.
func isolatedPathHash(root, rel string) (string, error) {
	if !filepath.IsLocal(filepath.FromSlash(rel)) {
		return "", fmt.Errorf("unsafe merge path %q", rel)
	}
	// Never traverse a target symlink ancestor when applying a child path.
	for d := filepath.Dir(filepath.FromSlash(rel)); d != "."; d = filepath.Dir(d) {
		st, err := os.Lstat(filepath.Join(root, d))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if !st.IsDir() {
			return "", fmt.Errorf("merge ancestor is not a directory: %s", d)
		}
	}
	p := filepath.Join(root, filepath.FromSlash(rel))
	st, err := os.Lstat(p)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(p)
		if err != nil {
			return "", err
		}
		return hashLinkTarget(target), nil
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("merge target is not a file: %s", rel)
	}
	return sha256File(p)
}
