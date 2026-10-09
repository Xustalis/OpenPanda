// SPDX-License-Identifier: AGPL-3.0-or-later

package core

// The project plane: what has to travel with a task so that a project means the
// same thing on the machine that executes it as on the machine that asked.
//
// A delegated task used to carry the project's *name* and nothing else. The
// executor looked that name up in its own projects directory, found no memory and
// no tree, and ran an agent that could not tell what it was working on — the
// "另一个设备不知道做什么" failure. Two things close that gap, and both reuse
// machinery that already existed for plan stages:
//
//   - the project's memory directory, packed inline into the delegation (small by
//     design: memory is character-capped and skills are Markdown);
//   - the project's work tree, as an artifact reference the executor pulls
//     through the same chunked artifact_fetch a stage input uses.
//
// Nothing is replicated in the background and no node holds a copy it was not
// sent. Push-on-delegation means there is no half-synchronised state to recover
// after a disconnect: the payload either arrived with the task or it did not.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/artifact"
	"github.com/Xustalis/OpenPanda/internal/bus"
)

// projectArtifactStage labels the project tree inside a task's Inputs. Inputs is
// shared with the plan plane, where the field names the producing stage; a
// reserved label keeps a project tree tellable apart from a stage output when
// both appear (a plan stage that also belongs to a project).
const projectArtifactStage = "__project__"

// worktreeArtifactStage labels an ad-hoc work tree inside a task's Inputs —
// the project-tree sibling for a file task that belongs to no project.
const worktreeArtifactStage = "__worktree__"

// worktreeAttachLimit bounds the content a file task ships to its executor.
// A tree past the cap degrades to a no-attach delegation (the task still runs
// with the context manifest only) rather than blocking dispatch on a repo
// that was never sized for the wire.
const worktreeAttachLimit int64 = 256 << 20 // 256 MiB of content

// worktreeSkipDirs names directories pruned from an attached work tree. The
// set keeps derived, vendored and bookkeeping content off the wire: it is
// regenerable on the executor (or fetchable via the project's own tooling)
// and routinely dwarfs the source by an order of magnitude. .git is excluded
// on top of that for a second reason — its config can embed credentials in
// remote URLs, and a peer that asked for a task's code has no business
// receiving the origin's VCS plumbing.
var worktreeSkipDirs = map[string]bool{
	".git":          true,
	"node_modules":  true,
	"vendor":        true,
	"__pycache__":   true,
	".venv":         true,
	"venv":          true,
	".tox":          true,
	".mypy_cache":   true,
	".pytest_cache": true,
	".ruff_cache":   true,
	".gocache":      true,
	".panda-shadow": true,
	"target":        true,
}

// extractProtectedDirs is the return-leg counterpart of worktreeSkipDirs.
// The outbound set prunes for weight; this set answers a different question —
// what a peer's output must never write back over the user's checkout. A
// valid artifact — hash-correct, no traversal — can still carry these, and
// artifact.Unpack alone cannot tell plumbing or auto-run surfaces from
// content:
//
//   - .git, .panda-shadow: the repository's hooks/credentials plumbing and
//     this node's own arbitration backup;
//   - open-or-run surfaces: editor task/debug configs that fire on folder
//     open (.vscode/tasks.json, launch.json), agent-CLI hook files
//     (.claude/settings*.json can name shell commands a later agent session
//     executes), direnv's .envrc, and .devcontainer post-create commands;
//   - push-triggered CI definitions: any workflow/pipeline file that runs
//     with the push's secrets scope the moment the user commits it.
//
// Keys match a leading path element or a path prefix (".github/workflows",
// ".vscode/tasks.json"), so file-level and directory-level protection share
// one set. A legitimately edited CI or settings file still lands in the
// artifact — the hash is unchanged — and is listed in the sync event's
// withheld paths for the operator to apply by hand. Regenerable weight like
// node_modules is NOT here: whatever the executor honestly produced lands
// intact. Makefile stays writable too: it is core work product, and a
// poisoned target is visible in the same diff the user already reviews.
var extractProtectedDirs = map[string]bool{
	".git":                    true,
	".panda-shadow":           true,
	".vscode/tasks.json":      true,
	".vscode/launch.json":     true,
	".claude":                 true,
	".devcontainer":           true,
	".envrc":                  true,
	".github/workflows":       true,
	".gitlab-ci.yml":          true,
	".gitea/workflows":        true,
	".forgejo/workflows":      true,
	".circleci":               true,
	".travis.yml":             true,
	"azure-pipelines.yml":     true,
	"Jenkinsfile":             true,
	"bitbucket-pipelines.yml": true,
	".drone.yml":              true,
}

// hostStatePrune returns this node's own bookkeeping paths that sit inside
// root, for PackDirExceptPaths: an artifact must never carry live node state.
// A project tree rooted at the checkout can contain the SQLite database
// (whose settings table holds the node private key — shipping it leaks the
// node identity to every peer that pulls the tree), its WAL/SHM sidecars,
// and the pid file. A host path equal to root is dropped: pruning it would
// empty the artifact, and the file-level entries still cover the live files.
func (c *Core) hostStatePrune(root string) []string {
	if len(c.hostStatePaths) == 0 {
		return nil
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil
	}
	abs = filepath.Clean(abs)
	var out []string
	for _, h := range c.hostStatePaths {
		if h == abs {
			continue
		}
		if rel, rerr := filepath.Rel(abs, h); rerr == nil && rel != ".." &&
			!strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			out = append(out, h)
		}
	}
	return out
}

// extractSkipSet returns the skip set for adopting a peer's tree into dst:
// the static protected paths plus this node's bookkeeping paths that fall
// inside dst — mapped to the dst-relative keys skippedEntry matches on. An
// incoming tree must never overwrite the live SQLite file: replacing an open
// database orphans the daemon's connection on a dead inode while later
// readers see the imported snapshot — the node silently splits from its own
// state until restart.
func (c *Core) extractSkipSet(dst string) map[string]bool {
	if len(c.hostStatePaths) == 0 {
		return extractProtectedDirs
	}
	abs, err := filepath.Abs(dst)
	if err != nil {
		return extractProtectedDirs
	}
	abs = filepath.Clean(abs)
	skip := make(map[string]bool, len(extractProtectedDirs)+len(c.hostStatePaths))
	for k := range extractProtectedDirs {
		skip[k] = true
	}
	for _, h := range c.hostStatePaths {
		if h == abs {
			continue // a state path equal to dst cannot prune the whole tree
		}
		if rel, rerr := filepath.Rel(abs, h); rerr == nil && rel != ".." &&
			!strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			skip[filepath.ToSlash(rel)] = true
		}
	}
	return skip
}

// attachProject fills in the project half of a delegation payload: the memory
// pack inline, and the work tree as an artifact reference. Called from every
// place that builds a payload, so a task cannot be delegated project-aware on one
// path and blind on another.
//
// Every failure here is a warning, never an error. A task that arrives without
// its project context still runs — with less to go on — and that is strictly
// better than a delegation refused because a directory could not be read.
func (c *Core) attachProject(ctx context.Context, p *bus.TaskDelegatePayload, project string) {
	if project == "" {
		return
	}
	if pack, err := c.packProjectMemory(project); err != nil {
		c.logger.Warn("pack project memory", "project", project, "err", err)
		if p.TaskID != "" && c.store != nil {
			_ = c.store.RecordEvent(ctx, p.TaskID, EvContextDegraded, map[string]any{
				"stage": "pack_memory", "project": project, "err": err.Error(),
			})
		}
	} else if len(pack) > 0 {
		p.ProjectPack = pack
	}
	dir := c.projectDir(project)
	if dir == "" {
		return
	}
	p.ProjectDir = dir
	ref, err := c.packProjectTree(ctx, p.TaskID, dir)
	if err != nil {
		c.logger.Warn("pack project tree", "project", project, "dir", dir, "err", err)
		if p.TaskID != "" && c.store != nil {
			_ = c.store.RecordEvent(ctx, p.TaskID, EvContextDegraded, map[string]any{
				"stage": "pack_tree", "project": project, "dir": dir, "err": err.Error(),
			})
		}
		return
	}
	if ref.Hash != "" {
		p.Inputs = append(p.Inputs, ref)
	}
}

// projectDir is the project's work tree on this node, or "" when the project has
// none (or this node has no project table).
func (c *Core) projectDir(project string) string {
	if c.projects == nil || project == "" {
		return ""
	}
	pr, err := c.projects.Get(project)
	if err != nil {
		return ""
	}
	return pr.WorkDir
}

// packProjectMemory packs the project's memory directory for inline carriage.
// Returns nil when there is nothing to send or the pack exceeds the wire cap.
func (c *Core) packProjectMemory(project string) ([]byte, error) {
	if c.projectsRoot == "" {
		return nil, nil
	}
	dir, err := c.projectMemoryDir(c.projectsRoot, project)
	if err != nil {
		return nil, err
	}
	if st, serr := os.Stat(dir); serr != nil || !st.IsDir() {
		return nil, nil // no memory directory yet: nothing to carry
	}
	// Report credentials before the memory leaves the node. The scan reads the
	// files, never the packed archive: a regex over compressed bytes matches at
	// random, and "redacting" the archive in place would corrupt the stream the
	// peer has to unpack. Detection only — stripping a value has to happen in
	// the memory file itself.
	for _, file := range scanProjectMemoryCredentials(dir) {
		c.logger.Warn("project memory may carry a credential",
			"project", project, "file", file)
	}

	var buf bytes.Buffer
	if _, err := artifact.Pack(dir, &buf); err != nil {
		return nil, err
	}
	if buf.Len() > bus.MaxProjectPackBytes {
		return nil, fmt.Errorf("project pack is %d bytes, over the %d cap",
			buf.Len(), bus.MaxProjectPackBytes)
	}
	return buf.Bytes(), nil
}

// packProjectTree packs the project's work tree into the local pool and returns
// the reference the executor pulls it with. The bytes stay here: the reference
// names this node as the holder, and a peer that already has the hash skips the
// transfer entirely.
func (c *Core) packProjectTree(ctx context.Context, taskID, dir string) (bus.ArtifactRef, error) {
	if c.artifacts == nil {
		return bus.ArtifactRef{}, nil
	}
	if st, err := os.Stat(dir); err != nil {
		return bus.ArtifactRef{}, fmt.Errorf("stat project work tree %s: %w", dir, err)
	} else if !st.IsDir() {
		return bus.ArtifactRef{}, fmt.Errorf("project work tree %s is not a directory", dir)
	}
	m, err := c.artifacts.PackSourceDirExceptPaths(dir, worktreeSkipDirs, c.hostStatePrune(dir), 0)
	if err != nil {
		return bus.ArtifactRef{}, err
	}
	if manifestJSON, jerr := json.Marshal(m); jerr == nil {
		if rerr := c.store.RecordArtifact(ctx, m.Hash, m.Size, taskID, string(manifestJSON)); rerr != nil {
			c.logger.Warn("index project artifact", "task", taskID, "hash", m.Hash, "err", rerr)
		}
	}
	return bus.ArtifactRef{Stage: projectArtifactStage, Hash: m.Hash, Source: c.nodeID}, nil
}

// attachWorktree fills the context half of a delegation for a file task that
// has no project: the task's work tree packs into the artifact pool and rides
// as an input reference, so the remote agent edits the real code instead of
// guessing at a distilled intent in an empty directory. Triggered only for
// context_type=file tasks whose working directory sits inside a repo — a bare
// ask from $HOME must never ship the user's home directory to a peer. Same
// failure posture as attachProject: degraded delegation beats refused
// dispatch, so every error is a warning.
func (c *Core) attachWorktree(ctx context.Context, p *bus.TaskDelegatePayload, in TaskInput) {
	if in.Project != "" || len(p.Inputs) > 0 || in.ContextType != "file" {
		return
	}
	dir := in.RepoPath
	if dir == "" {
		dir = in.WorkDir
	}
	if dir == "" || !looksLikeRepo(dir) {
		return
	}
	ref, err := c.packWorktree(ctx, p.TaskID, dir)
	if err != nil {
		c.logger.Warn("pack worktree", "dir", dir, "err", err)
		if p.TaskID != "" && c.store != nil {
			_ = c.store.RecordEvent(ctx, p.TaskID, EvContextDegraded, map[string]any{
				"stage": "pack_worktree", "dir": dir, "err": err.Error(),
			})
		}
		return
	}
	if ref.Hash == "" {
		return
	}
	p.Inputs = append(p.Inputs, ref)
	// The return leg (adoptWorktreeOutput) writes the produced tree back over
	// the row's work_dir — which must therefore name the directory the bytes
	// were packed from, or the round-trip silently lands in the wrong place
	// (or nowhere, when only RepoPath was set). Keep the row honest: when the
	// packed dir differs from the persisted work dir, the shipped tree is the
	// authoritative origin workspace.
	if in.WorkDir != dir && p.TaskID != "" && c.store != nil {
		if err := c.store.SetWorkDir(ctx, p.TaskID, dir); err != nil {
			c.logger.Warn("pin worktree dir on task", "task", p.TaskID, "dir", dir, "err", err)
		}
	}
}

// attachWorktreeFrom is the task-row half of attachWorktree for paths that
// rebuild the payload from the persisted row rather than a live TaskInput —
// the queue forward and the decline re-route. The row's work_dir is the
// packed dir by construction (the submit path wrote it), so adoption
// symmetry is already guaranteed.
func (c *Core) attachWorktreeFrom(ctx context.Context, p *bus.TaskDelegatePayload, t Task) {
	if t.Project != "" || len(p.Inputs) > 0 || t.ContextType != "file" {
		return
	}
	dir := t.WorkDir
	if dir == "" || !looksLikeRepo(dir) {
		return
	}
	ref, err := c.packWorktree(ctx, p.TaskID, dir)
	if err != nil {
		c.logger.Warn("pack worktree", "dir", dir, "err", err)
		if p.TaskID != "" && c.store != nil {
			_ = c.store.RecordEvent(ctx, p.TaskID, EvContextDegraded, map[string]any{
				"stage": "pack_worktree", "dir": dir, "err": err.Error(),
			})
		}
		return
	}
	if ref.Hash != "" {
		p.Inputs = append(p.Inputs, ref)
	}
}

// looksLikeRepo reports whether dir sits inside a version-controlled tree —
// a .git entry in it or a near ancestor. It is the gate on attaching a work
// tree: only code the user tracks may ship, which rules out accidental cwd
// captures like $HOME or /tmp scratch dirs.
func looksLikeRepo(dir string) bool {
	for i := 0; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
	return false
}

// packWorktree packs a file task's work tree into the local pool under the
// attach bound and the derived-dir skip set, returning the reference the
// executor pulls it with.
func (c *Core) packWorktree(ctx context.Context, taskID, dir string) (bus.ArtifactRef, error) {
	if c.artifacts == nil {
		return bus.ArtifactRef{}, nil
	}
	if st, err := os.Stat(dir); err != nil {
		return bus.ArtifactRef{}, fmt.Errorf("stat worktree %s: %w", dir, err)
	} else if !st.IsDir() {
		return bus.ArtifactRef{}, fmt.Errorf("worktree %s is not a directory", dir)
	}
	m, err := c.artifacts.PackSourceDirExceptPaths(dir, worktreeSkipDirs, c.hostStatePrune(dir), worktreeAttachLimit)
	if err != nil {
		return bus.ArtifactRef{}, err
	}
	if manifestJSON, jerr := json.Marshal(m); jerr == nil {
		if rerr := c.store.RecordArtifact(ctx, m.Hash, m.Size, taskID, string(manifestJSON)); rerr != nil {
			c.logger.Warn("index worktree artifact", "task", taskID, "hash", m.Hash, "err", rerr)
		}
	}
	return bus.ArtifactRef{Stage: worktreeArtifactStage, Hash: m.Hash, Source: c.nodeID}, nil
}

// landProjectPack extracts a delegated project's memory into this node's own
// projects directory, so the executing agent reads the project's memory from the
// same path a local task would. Best-effort: a task without its memory still runs.
func (c *Core) landProjectPack(project string, pack []byte) error {
	if len(pack) == 0 || project == "" || c.projectsRoot == "" {
		return nil
	}
	dir, err := c.projectMemoryDir(c.projectsRoot, project)
	if err != nil {
		c.logger.Warn("land project pack: unsafe project name", "project", project, "err", err)
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		c.logger.Warn("land project pack: create dir", "project", project, "err", err)
		return err
	}

	if _, err := artifact.Unpack(bytes.NewReader(pack), dir); err != nil {
		c.logger.Warn("land project pack: unpack", "project", project, "err", err)
		return err
	}
	// Adopt the project locally too, so `panda project list` on the executor shows
	// the work it is doing rather than a task belonging to nothing.
	if c.projects != nil {
		if _, err := c.projects.EnsureFromName(project); err != nil {
			c.logger.Warn("adopt delegated project", "project", project, "err", err)
		}
	}
	c.logger.Info("project context landed", "project", project, "dir", dir, "bytes", len(pack))
	return nil
}

// projectWorkDir is where a project's tasks execute on *this* node. The origin's
// path is meaningless here (and trusting it would let a peer aim execution at any
// directory), so it is derived locally under the node's work dir, the same way a
// plan stage's directory is.
func (c *Core) projectWorkDir(project string) (string, error) {
	// A project the user configured locally wins: if this node has the tree, work
	// in it rather than in a copy.
	if dir := c.projectDir(project); dir != "" {
		return dir, nil
	}
	root := c.workDir
	if root == "" {
		root = os.TempDir()
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	name, err := safeProjectSegment(project)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "projects", name)
	if !strings.HasPrefix(dir, filepath.Clean(root)+string(os.PathSeparator)) {
		return "", fmt.Errorf("project work dir escapes root: %q", dir)
	}
	return dir, nil
}

// projectMemoryDir joins a project name onto a root after checking it is a single
// safe path segment.
func (c *Core) projectMemoryDir(root, project string) (string, error) {
	name, err := safeProjectSegment(project)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, name), nil
}

// safeProjectSegment rejects a project name that could escape a directory it is
// joined onto. The name arrives over the bus, so it is checked here rather than
// trusted: a peer must never be able to aim a write at an arbitrary path (the
// same reasoning that whitelists plan and stage ids at the wire boundary).
func safeProjectSegment(project string) (string, error) {
	name := strings.TrimSpace(project)
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("unsafe project name %q", project)
	}
	return name, nil
}

// projectInputs reports whether a task carries a project tree to pull. A plan
// stage's inputs are the plan's business (fetchStageInputs already handles them);
// this is the standalone-task case.
func projectInputs(t Task) bool {
	return t.PlanID == "" && t.Project != "" && len(t.Inputs) > 0
}

// attachedInputs reports a standalone task carrying an ad-hoc work tree (no
// plan, no project): the executor unpacks it into a private task dir instead
// of running blind in the shared node work dir.
func attachedInputs(t Task) bool {
	return t.PlanID == "" && t.Project == "" && len(t.Inputs) > 0
}

// projectNames lists the projects this node holds a checkout of — the
// residence set it advertises on hello, card beats and heartbeats so routing
// can prefer landing a project-bound task where its tree already lives (§6.3).
// A read failure returns nil, which the wire reads as "no list sent": peers
// leave their stored set alone rather than clobbering it on a transient
// error. A node with no project store reports an empty set — accurate, and it
// clears any stale claim a peer may hold.
func (c *Core) projectNames() []string {
	if c.projects == nil {
		return []string{}
	}
	ps, err := c.projects.List()
	if err != nil {
		c.logger.Debug("list projects for residence advert", "err", err)
		return nil
	}
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, p.Name)
	}
	return names
}

// adoptProjectOutput pulls the tree an executor produced for a project task back
// into this node's copy of the project, so work done on another machine is
// visible where the user asked for it. It is the return leg of the push: without
// it a delegated task would edit files on a machine the user never looks at.
//
// The extraction is additive — the artifact's files land over the local tree —
// and the count of what changed is recorded as a task event rather than merged.
// Automatic merging is deliberately absent: two machines editing one project is a
// real conflict, and silently resolving it in favour of whichever result arrived
// last would lose work without telling anyone. `panda task <id>` shows what came
// back; the user decides.
func (c *Core) adoptProjectOutput(ctx context.Context, t Task, from, hash string) {
	if hash == "" || c.artifacts == nil {
		return
	}
	dir := c.projectDir(t.Project)
	if dir == "" {
		// No local tree for this project: keep the artifact in the pool (it is
		// already recorded on the row) rather than inventing a directory the user
		// never asked for.
		if err := c.store.SetOutputArtifact(ctx, t.TaskID, hash); err != nil {
			c.logger.Warn("record project output", "task", t.TaskID, "err", err)
		}
		return
	}
	// The pull is a multi-chunk round trip, so it must not block the message
	// handler, and it must outlive the envelope's context.
	ctx = context.WithoutCancel(ctx)
	go func() {
		if from != "" && from != c.nodeID {
			if _, held := c.artifacts.Has(hash); !held {
				if _, err := c.FetchArtifact(ctx, from, t.TaskID, hash); err != nil {
					c.logger.Warn("adopt project artifact", "task", t.TaskID,
						"project", t.Project, "hash", hash, "from", from, "err", err)
					return
				}
			}
		}
		m, err := c.artifacts.ExtractExcept(hash, dir, c.extractSkipSet(dir))
		if err != nil {
			c.logger.Warn("extract project artifact", "task", t.TaskID, "hash", hash, "err", err)
			return
		}
		if c.projects != nil {
			_ = c.projects.Touch(t.Project)
		}
		if err := c.store.SetOutputArtifact(ctx, t.TaskID, hash); err != nil {
			c.logger.Warn("record project output", "task", t.TaskID, "err", err)
		}
		c.EvTrace(ctx, t.TaskID, EvProjectSync, map[string]any{
			"project": t.Project,
			"dir":     dir,
			"from":    from,
			"hash":    hash,
			"files":   len(m.Entries),
			"bytes":   m.Size,
			"skipped": m.Skipped,
			// withheld names the protected paths the archive carried but the
			// extract declined to write — auto-run surfaces and CI plumbing a
			// peer's output may not land on the checkout unattended.
			"withheld": m.SkippedPaths,
		})
		c.logger.Info("project tree adopted", "task", t.TaskID, "project", t.Project,
			"dir", dir, "files", len(m.Entries))
	}()
}

// adoptWorktreeOutput is the ad-hoc sibling of adoptProjectOutput: a file task
// that shipped its work tree to a peer gets the produced tree back over the
// origin directory it packed from, so the remote agent's edits land where the
// user's code actually lives. Same additive-extract posture — no merge, the
// row records the hash and the event says what came back — because the same
// conflict reality applies: the user may have edited the same files at home.
func (c *Core) adoptWorktreeOutput(ctx context.Context, t Task, from, hash string) {
	if hash == "" || c.artifacts == nil || t.WorkDir == "" {
		return
	}
	dir := t.WorkDir
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		// The origin dir vanished while the task was away: keep the artifact
		// on the row rather than recreating a directory the user removed.
		if err := c.store.SetOutputArtifact(ctx, t.TaskID, hash); err != nil {
			c.logger.Warn("record worktree output", "task", t.TaskID, "err", err)
		}
		return
	}
	ctx = context.WithoutCancel(ctx)
	go func() {
		if from != "" && from != c.nodeID {
			if _, held := c.artifacts.Has(hash); !held {
				if _, err := c.FetchArtifact(ctx, from, t.TaskID, hash); err != nil {
					c.logger.Warn("adopt worktree artifact", "task", t.TaskID,
						"hash", hash, "from", from, "err", err)
					return
				}
			}
		}
		m, err := c.artifacts.ExtractExcept(hash, dir, c.extractSkipSet(dir))
		if err != nil {
			c.logger.Warn("extract worktree artifact", "task", t.TaskID, "hash", hash, "err", err)
			return
		}
		if err := c.store.SetOutputArtifact(ctx, t.TaskID, hash); err != nil {
			c.logger.Warn("record worktree output", "task", t.TaskID, "err", err)
		}
		c.EvTrace(ctx, t.TaskID, EvProjectSync, map[string]any{
			"dir": dir, "from": from, "hash": hash,
			"files": len(m.Entries), "bytes": m.Size, "skipped": m.Skipped,
			"withheld": m.SkippedPaths,
		})
		if m.Skipped > 0 {
			c.logger.Warn("worktree adoption withheld protected paths", "task", t.TaskID,
				"dir", dir, "withheld", m.SkippedPaths)
		}
		c.logger.Info("worktree adopted", "task", t.TaskID, "dir", dir, "files", len(m.Entries))
	}()
}
