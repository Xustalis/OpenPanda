// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/self"
	"github.com/Xustalis/OpenPanda/internal/updater"
	"github.com/Xustalis/OpenPanda/internal/version"
)

// specKindSelfMerge marks a task's SpecJSON as a self-evolution merge
// envelope instead of an entry-model spec — the field the proposal rides on.
const specKindSelfMerge = "self_merge"

// selfMergeSpec is the persisted envelope a merge proposal parks with: enough
// to identify the set at approve time without re-deriving it from mutable
// queue state, and enough for the review UI to summarize without reading the
// patch files.
type selfMergeSpec struct {
	Kind     string   `json:"kind"` // specKindSelfMerge
	PatchSet string   `json:"patchset"`
	Base     string   `json:"base"`
	Version  string   `json:"version"`
	Producer string   `json:"producer"`
	Title    string   `json:"title"`
	Critical bool     `json:"critical"`
	Paths    []string `json:"critical_paths,omitempty"`
	Files    int      `json:"files"`
	// Digest pins the manifest the reviewer saw. The boot-time rebase sweep
	// rewrites a queued set in place, so the artifact on disk at approve
	// time can differ from the one that was proposed — the digest is what
	// makes that visible instead of silently merging unseen bytes.
	Digest string `json:"digest"`
}

// SelfMergeSpec decodes a task row's merge envelope; ok=false on any row that
// is not a self-merge proposal.
func SelfMergeSpec(specJSON string) (selfMergeSpec, bool) {
	var s selfMergeSpec
	if json.Unmarshal([]byte(specJSON), &s) == nil && s.Kind == specKindSelfMerge {
		return s, true
	}
	return s, false
}

// SetSelfEvolution wires the self-evolution manager into the core. oneShot
// says the process is a CLI helper (panda approve), not the daemon — the
// merge's binary Apply then skips the in-process re-exec, leaving the service
// restart to install.RestartDaemon. producers is the config's
// self_evolution.producers: "self" (default) admits only sets signed by this
// node's own key, "mesh" admits any signer (reserved for phase 5).
func (c *Core) SetSelfEvolution(mgr *self.Manager, oneShot bool, producers string) {
	c.selfMgr = mgr
	c.selfOneShot = oneShot
	c.selfProducers = producers
}

// SelfManager exposes the wired manager for read surfaces (status, queue).
func (c *Core) SelfManager() *self.Manager { return c.selfMgr }

// ErrSelfMergeDisabled is the fail-closed answer when a merge action reaches
// a node that never opted into self-evolution.
var ErrSelfMergeDisabled = fmt.Errorf("self_evolution is not enabled on this node")

// Pipeline seams: the release gate and the build/swap step are slow and
// environment-dependent, so tests replace them to exercise the rest of the
// approval pipeline (claim → apply → gate → stage → archive → complete).
var (
	selfRunGate   = self.RunGate
	selfStageTree = func(m *self.Manager, ctx context.Context, wt, stage, version string) error {
		return m.StageTree(ctx, wt, stage, version)
	}
	selfNewUpdater = func(opts updater.Options) selfUpdater { return updater.New(opts) }
	selfRestart    = updater.RestartAfterSwap
)

// selfUpdater is the slice of updater.Manager the merge pipeline uses.
type selfUpdater interface {
	StageLocal(root, version string) error
	Apply(ctx context.Context) error
}

// selfBaseCurrent refuses to propose/merge a set whose recorded base is no
// longer the repo HEAD. Apply resets the worktree to that base, so a stale
// set would build the old tree plus the patch and swap it over newer commits
// — a silent rollback. The boot-time rebase sweep is not sufficient: HEAD can
// move while the daemon is up, and the sweep is optional.
func (c *Core) selfBaseCurrent(ctx context.Context, set self.Set) error {
	repo := c.selfMgr.Repo()
	if repo == "" {
		return nil // config validation requires it when enabled; tests may omit
	}
	head, err := self.GitHead(ctx, repo)
	if err != nil {
		return fmt.Errorf("resolve repo HEAD: %w", err)
	}
	if !self.SameRef(ctx, repo, set.Manifest.Base, head) {
		return fmt.Errorf("patch set %s targets base %s but the repo is at %s — rebase it first: panda self rebase %s %s",
			set.Manifest.ID, shortSHA(set.Manifest.Base), shortSHA(head), set.Manifest.ID, head)
	}
	return nil
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// selfVersionBase strips build metadata so NextVersion can count from the
// release line rather than from a stamp a previous proposal already claimed.
func selfVersionBase(v string) string {
	if i := strings.Index(v, "+"); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		v = version.Version
		if i := strings.Index(v, "+"); i >= 0 {
			v = v[:i]
		}
	}
	if v == "" {
		v = "0.0.0"
	}
	return v
}

// ProposeSelfMerge turns a queued patch set into a review-gated merge task
// (docs/design/self-evolution.md): a human approve runs the pipeline, a reject archives the
// set. The proposal carries the manifest's recomputed critical flag so the
// review UI can highlight what the patch touches.
func (c *Core) ProposeSelfMerge(ctx context.Context, patchSetID string) (Task, error) {
	if c.selfMgr == nil {
		return Task{}, ErrSelfMergeDisabled
	}
	set, err := c.selfMgr.Get(patchSetID)
	if err != nil {
		return Task{}, err
	}
	if set.State != self.StatePending {
		return Task{}, fmt.Errorf("patch set %s is %s — only pending sets can be proposed", patchSetID, set.State)
	}
	// producers bounds whose signature may enter this node's merge queue.
	// Propose is the admission point; resume re-checks (defense in depth).
	if !c.producerTrusted(set.Manifest.Producer) {
		return Task{}, fmt.Errorf("patch set %s is signed by %s, which self_evolution.producers=%q does not admit on this node",
			patchSetID, set.Manifest.Producer, c.producersMode())
	}
	if err := c.selfBaseCurrent(ctx, set); err != nil {
		return Task{}, err
	}
	spec := selfMergeSpec{
		Kind:     specKindSelfMerge,
		PatchSet: set.Manifest.ID,
		Base:     set.Manifest.Base,
		Version:  set.Manifest.Version,
		Producer: set.Manifest.Producer,
		Title:    set.Manifest.Title,
		Critical: set.Critical,
		Paths:    set.CriticalPaths,
		Files:    len(set.Manifest.Files),
		Digest:   set.Manifest.Digest(),
	}
	specJSON, _ := json.Marshal(spec)
	title := "self-merge: " + set.Manifest.Title
	if set.Critical {
		title += " [critical]"
	}
	t, err := c.store.Create(ctx, "", "", title, c.nodeID, []string{c.nodeID})
	if err != nil {
		return Task{}, err
	}
	intent := fmt.Sprintf("merge self-evolution patch set %s onto base %s as %s",
		set.Manifest.ID, set.Manifest.Base, set.Manifest.Version)
	if err := c.store.SetDetail(ctx, t.TaskID, TaskDetail{
		Intent:   intent,
		SpecJSON: string(specJSON),
		Requires: []string{"self:merge"}, // never matches a routing capability — the row can only be settled locally
	}); err != nil {
		_ = c.store.Fail(ctx, t.TaskID, c.nodeID, "self-merge spec: "+err.Error())
		return Task{}, err
	}
	if err := c.store.applyReviewCAS(ctx, t.TaskID, StateSubmitted, c.nodeID, t.AttemptID,
		EvReview, map[string]any{"self_merge": spec}, nil, ApprovalSelfMerge); err != nil {
		return Task{}, fmt.Errorf("park self-merge proposal: %w", err)
	}
	c.audit(ctx, t.TaskID, "self:propose", set.Manifest.ID, "parked",
		fmt.Sprintf("ver=%s critical=%v files=%d", set.Manifest.Version, set.Critical, len(set.Manifest.Files)))
	return c.store.Get(ctx, t.TaskID)
}

// selfSwapGuard refuses a merge in a process whose own binary is not the
// panda install target: the updater's swap replaces os.Executable(), and
// writing a panda build over the panel sidecar (`panda-webui`) would destroy
// the console binary. The match is exact — a prefix test let `panda-webui`
// through, which is precisely the binary this guard exists to protect.
func selfSwapGuardFor(exe string) error {
	switch strings.ToLower(filepath.Base(exe)) {
	case "panda", "panda.exe":
		return nil
	}
	return fmt.Errorf("self-merge approvals run from the panda daemon or `panda approve` — this process (%s) would overwrite its own binary", filepath.Base(exe))
}

// selfSwapGuard is a variable so tests can exercise the pipeline from a test
// binary; production calls resolve os.Args[0] through selfSwapGuardFor.
var selfSwapGuard = func() error { return selfSwapGuardFor(os.Args[0]) }

// resumeSelfMerge is the ApprovalSelfMerge half of resumeApproved: the human
// said yes, so apply the set to the worktree, re-run the release gate, build,
// stage, and swap. Any failure leaves the task failed and the patch set
// flagged — nothing swaps silently.
func (c *Core) resumeSelfMerge(ctx context.Context, cur Task) (Task, bus.TaskResultPayload, error) {
	if c.selfMgr == nil {
		return cur, bus.TaskResultPayload{}, ErrSelfMergeDisabled
	}
	spec, ok := SelfMergeSpec(cur.SpecJSON)
	if !ok {
		return cur, bus.TaskResultPayload{}, fmt.Errorf("self-merge task %s carries no merge envelope", cur.TaskID)
	}
	if err := selfSwapGuard(); err != nil {
		return cur, bus.TaskResultPayload{}, err
	}
	// Claim the review exactly once — concurrent approvers collide on the CAS.
	if err := c.store.applyCAS(ctx, cur.TaskID, StateReview, StateRunning, c.nodeID, cur.AttemptID,
		EvReview, map[string]any{"approved": true, "self_merge": spec.PatchSet}, nil); err != nil {
		final, _ := c.store.Get(ctx, cur.TaskID)
		return final, bus.TaskResultPayload{}, fmt.Errorf("claim self-merge: %w", err)
	}
	fail := func(err error) (Task, bus.TaskResultPayload, error) {
		c.audit(ctx, cur.TaskID, "self:merge", spec.PatchSet, "failed", err.Error())
		_ = c.store.Fail(ctx, cur.TaskID, c.nodeID, "self-merge: "+err.Error())
		final, gerr := c.store.Get(ctx, cur.TaskID)
		if gerr != nil {
			return cur, bus.TaskResultPayload{}, gerr
		}
		return final, bus.TaskResultPayload{OK: false, TaskID: cur.TaskID, State: StateFailed, Stderr: err.Error()}, nil
	}

	set, err := c.selfMgr.Get(spec.PatchSet)
	if err != nil {
		return fail(fmt.Errorf("load patch set: %w", err))
	}
	if set.State != self.StatePending {
		return fail(fmt.Errorf("patch set %s is %s", spec.PatchSet, set.State))
	}
	// The set on disk must still be the artifact the reviewer approved. A
	// rebase (the boot-time sweep, or an operator) rewrites the patches and
	// the manifest in place; approving the proposal then would merge bytes
	// nobody looked at.
	if spec.Digest != "" && set.Manifest.Digest() != spec.Digest {
		return fail(fmt.Errorf("patch set %s changed since it was proposed (manifest digest %s, expected %s) — re-review it",
			spec.PatchSet, shortSHA(set.Manifest.Digest()), shortSHA(spec.Digest)))
	}
	if !c.producerTrusted(set.Manifest.Producer) {
		return fail(fmt.Errorf("patch set %s is signed by %s, which self_evolution.producers=%q does not admit",
			spec.PatchSet, set.Manifest.Producer, c.producersMode()))
	}
	if err := c.selfBaseCurrent(ctx, set); err != nil {
		return fail(err)
	}
	// Re-derive the critical flag from the patches on disk — the manifest's
	// claim was advisory; the merge decision keys on what is actually there.
	if set.Critical && !spec.Critical {
		return fail(fmt.Errorf("patch set now touches critical paths (%s) — re-mint and re-review it", strings.Join(set.CriticalPaths, ", ")))
	}
	// The restart defer is registered BEFORE the lock defer so LIFO releases
	// the worktree lock first: RestartAfterSwap may kill this process, and a
	// lock left behind would block merges until its heartbeat goes stale.
	swapped := false
	defer func() {
		if swapped {
			selfRestart(c.logger, c.selfOneShot)
		}
	}()
	// One merge at a time, across processes: two approved sets share the
	// same worktree, and applying/gating one under the other resets files
	// mid-build. The lock is held through swap; a crashed holder is cleared
	// by its heartbeat without marking any set conflicted.
	release, lerr := c.selfMgr.AcquireWorktreeLock(ctx)
	if lerr != nil {
		return fail(lerr)
	}
	defer release()
	if err := c.selfMgr.ApplyLocked(ctx, spec.PatchSet); err != nil {
		_ = c.selfMgr.MarkConflicted(spec.PatchSet)
		return fail(fmt.Errorf("apply patches: %w", err))
	}
	wt := c.selfMgr.WorktreePath()
	gate := selfRunGate(ctx, wt)
	if !gate.OK {
		_ = c.selfMgr.MarkConflicted(spec.PatchSet)
		last := ""
		if n := len(gate.Legs); n > 0 {
			last = fmt.Sprintf("leg %s failed", gate.Legs[n-1].Name)
		}
		return fail(fmt.Errorf("release gate failed in merge worktree: %s", last))
	}
	// The manifest's version was a target chosen at mint time; two queued
	// sets can carry the same stamp. Allocate the next free +self.N here,
	// under the worktree lock, so the swapped binary's provenance is unique.
	targetVersion := c.selfMgr.NextVersion(selfVersionBase(spec.Version))
	// Persist the stamp BEFORE the build/swap. The high-water mark exists to
	// stop a purge from reissuing a version the node already ran; a crash
	// between the swap and a later write would reopen exactly that window.
	// Burning a number on a failed build is harmless.
	if err := c.selfMgr.NoteVersionUsed(targetVersion); err != nil {
		return fail(fmt.Errorf("record version high-water: %w", err))
	}
	stage, err := os.MkdirTemp("", "openpanda-selfbuild-")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(stage)
	if err := selfStageTree(c.selfMgr, ctx, wt, stage, targetVersion); err != nil {
		return fail(err)
	}
	upd := selfNewUpdater(updater.Options{
		Current:     version.Version,
		Logger:      c.logger,
		NoRestart:   true, // the pipeline restarts only after bookkeeping
		Idle:        func(ctx context.Context) bool { return c.idleExcept(ctx, cur.TaskID) },
		SchemaFloor: c.schemaFloor,
		// A service bounce inside applyRelease would kill this process before
		// Archive/Complete ran; defer it until the review row is settled.
		SkipDaemonRestart: true,
	})
	if err := upd.StageLocal(stage, targetVersion); err != nil {
		return fail(fmt.Errorf("stage build: %w", err))
	}
	if err := upd.Apply(ctx); err != nil {
		return fail(fmt.Errorf("apply build: %w", err))
	}
	swapped = true
	res := bus.TaskResultPayload{
		OK: true, TaskID: cur.TaskID, State: StateDone,
		Stdout: fmt.Sprintf("merged %s → %s; release gate green (%d legs), binary swapped",
			spec.PatchSet, targetVersion, len(gate.Legs)),
	}
	// Complete the review before moving the set: if the row cannot be
	// settled, the binary is already swapped and the set must stay visibly
	// in flight (pending), not silently archived as if the merge succeeded.
	if err := c.store.Complete(ctx, cur.TaskID, c.nodeID, res); err != nil {
		return fail(fmt.Errorf("mark merged: %w", err))
	}
	if err := c.selfMgr.Archive(spec.PatchSet, self.StateMerged); err != nil {
		c.logger.Warn("self: merge applied but archive failed", "set", spec.PatchSet, "err", err)
		if merr := c.selfMgr.MarkState(spec.PatchSet, self.StateMerged); merr != nil {
			c.logger.Warn("self: merge applied but state marker failed", "set", spec.PatchSet, "err", merr)
		}
	}
	c.audit(ctx, cur.TaskID, "self:merge", spec.PatchSet, "applied",
		fmt.Sprintf("ver=%s (manifest %s) critical=%v", targetVersion, spec.Version, spec.Critical))
	final, gerr := c.store.Get(ctx, cur.TaskID)
	return final, res, gerr
}

// producersMode is the configured admission policy, with the empty value
// meaning "self" — the config default documented in config.example.yaml.
func (c *Core) producersMode() string {
	return self.ProducerMode(c.selfProducers)
}

// producerTrusted reports whether a set's signer may enter this node's merge
// queue. The manifest signature already proved the set was signed by the key
// it names; this decides whether that key is one this node accepts.
func (c *Core) producerTrusted(producer string) bool {
	pub, _, ok := c.nodeKeyPair()
	if !ok && c.producersMode() != self.ProducersMesh {
		return false
	}
	return self.ProducerAdmitted(c.producersMode(), producer, pub)
}

// idleExcept reports whether no task other than taskID is running, dispatched,
// or waiting for context. The merge task itself is RUNNING while it applies the
// swap, so the plain Idle gate would refuse every self-merge — "queue busy"
// caused by the very task doing the work.
func (c *Core) idleExcept(ctx context.Context, taskID string) bool {
	for _, state := range []string{StateRunning, StateDispatched, StateWaitingCtx} {
		tasks, err := c.store.ListByState(ctx, state)
		if err != nil {
			return false // fail closed: an unreadable queue is not an idle one
		}
		for _, t := range tasks {
			if t.TaskID != taskID {
				return false
			}
		}
	}
	return true
}

// schemaFloor reports the data directory's migration level, wired into the
// updater so a self build is held to the same schema-compatibility gate an
// official release is (design Phase 1 acceptance).
func (c *Core) schemaFloor(ctx context.Context) (int, error) {
	var v int
	if err := c.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("read schema floor: %w", err)
	}
	return v, nil
}

// SelfRebaseSweep carries queued patch sets onto the repo's current HEAD
// after an official update moved the base out from under them (docs/design/self-evolution.md q3,
// phase 4 coexistence). Each stale set is rebased and re-signed with THIS
// node's key — the node doing the rebase is the one whose operator will
// review it — while a conflict marks the set conflicted and leaves it for
// the operator (or the producing task) to regenerate. Runs on the daemon at
// startup; a no-op when nothing is queued or every base is already current.
func (c *Core) SelfRebaseSweep(ctx context.Context) {
	if c.selfMgr == nil {
		return
	}
	repo := c.selfMgr.Repo()
	if repo == "" {
		return
	}
	head, err := self.GitHead(ctx, repo)
	if err != nil {
		c.logger.Warn("self: rebase sweep cannot resolve repo HEAD", "repo", repo, "err", err)
		return
	}
	sets, err := c.selfMgr.Queue()
	if err != nil {
		return
	}
	for _, s := range sets {
		if s.State != self.StatePending {
			continue
		}
		if self.SameRef(ctx, repo, s.Manifest.Base, head) {
			continue
		}
		// Re-signing a foreign set with this node's key would defeat the
		// producers=self admission rule: a set that could never be proposed
		// would become "this node's" after one boot sweep. Skip it; mesh
		// mode admits it anyway and rebases.
		if !c.producerTrusted(s.Manifest.Producer) {
			c.logger.Debug("self: rebase sweep skips an unadmitted producer", "set", s.Manifest.ID)
			continue
		}
		pub, priv, ok := c.nodeKeyPair()
		if !ok {
			c.logger.Warn("self: rebase sweep needs the node key", "set", s.Manifest.ID)
			return
		}
		if err := c.selfMgr.RebaseOnto(ctx, s.Manifest.ID, head, pub, priv); err != nil {
			if errors.Is(err, self.ErrWorktreeBusy) {
				c.logger.Info("self: rebase sweep skipped — worktree busy", "set", s.Manifest.ID)
				return
			}
			c.logger.Warn("self: rebase onto new base failed — set marked conflicted",
				"set", s.Manifest.ID, "base", head, "err", err)
			_ = c.selfMgr.MarkConflicted(s.Manifest.ID)
			c.audit(ctx, "", "self:rebase", s.Manifest.ID, "conflicted", err.Error())
			continue
		}
		c.audit(ctx, "", "self:rebase", s.Manifest.ID, "applied",
			fmt.Sprintf("%s → %s", s.Manifest.Base, head))
		c.logger.Info("self: patch set rebased onto new base", "set", s.Manifest.ID, "base", head)
	}
}
