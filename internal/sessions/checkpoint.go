// SPDX-License-Identifier: AGPL-3.0-or-later

package sessions

// Checkpoints and rewind: putting a thread and its worktree back to a turn.
//
// Claude Code's rewind restores only what its own edit tools touched. OpenPanda's
// files are changed by a delegated agent CLI — through shells, scripts and
// sub-agents — so the only complete record of "what the tree looked like after
// turn N" is the tree itself. Each assistant turn therefore leaves a snapshot
// commit under refs/openpanda/checkpoints/<id>/<turn>, and a rewind reads it
// back.
//
// The snapshot is built in a throwaway index. Building it in the real one would
// make `git status` in the worktree leap from dirty to staged while a turn is
// finishing, and would fight any agent still writing files.
//
// Everything here is best-effort on a live tree: a snapshot taken while an agent
// is mid-write records what was on disk at that moment. That is the same
// guarantee a person gets by looking at the worktree, which is the honest one.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/executil"
)

// ErrNoCheckpoint is Rewind's answer when the requested turn has no snapshot —
// the thread ran before checkpoints existed, or the turn never finished.
var ErrNoCheckpoint = errors.New("sessions: no checkpoint for that turn")

// ErrBadTurn is Rewind's refusal for a turn outside the stored transcript.
var ErrBadTurn = errors.New("sessions: turn is outside this thread's history")

// Checkpoint is one recorded turn.
type Checkpoint struct {
	Turn int
	SHA  string
	// Dirty is what the worktree carried at snapshot time, in words — the
	// console shows it so a rewind target reads as "the moment after the
	// tests passed", not as an opaque hash.
	Files int
}

// RewindResult is what a rewind did.
type RewindResult struct {
	// Turn is the transcript length after the rewind.
	Turn int
	// Files is true when the worktree was restored; false for a thread with
	// no git isolation, where only the conversation moved back.
	Files bool
	// Removed lists untracked files that existed after the snapshot and were
	// deleted — the files the agent created between then and now.
	Removed []string
}

// checkpointRef names one turn's snapshot inside the repository's ref namespace,
// so `git branch` stays clean and gc still treats the commit as reachable.
func checkpointRef(id string, turn int) string {
	return "refs/openpanda/checkpoints/" + safeRefPart(id) + "/" + strconv.Itoa(turn)
}

func checkpointPrefix(id string) string {
	return "refs/openpanda/checkpoints/" + safeRefPart(id) + "/"
}

// maxCheckpointsPerThread bounds what one thread keeps. Every finished answer
// leaves a snapshot, so a long-running thread would otherwise pin a commit per
// turn forever — reachable, and therefore never collected by git gc. Old
// snapshots fall off; the recent ones are the ones anyone rewinds to.
const maxCheckpointsPerThread = 100

// Checkpoint snapshots the session's worktree as it stands now, tagging it with
// the transcript length it belongs to. A thread without git isolation has
// nothing to snapshot; that is not an error, and the caller should not treat it
// as one.
func (s *Store) Checkpoint(ctx context.Context, id string) error {
	sess, err := s.Get(id)
	if err != nil {
		return err
	}
	if sess.Worktree == "" {
		return nil
	}
	wt := sess.Worktree
	if _, err := os.Stat(filepath.Join(wt, ".git")); err != nil {
		return nil
	}
	turn := len(sess.Turns)
	if err := snapshotTree(ctx, wt, checkpointRef(id, turn), fmt.Sprintf("openpanda checkpoint: %s turn %d", id, turn)); err != nil {
		return err
	}
	pruneCheckpoints(ctx, wt, id)
	return nil
}

// snapshotTree writes a commit holding the worktree's complete current content
// and points ref at it. The commit is parented on HEAD so a rewind target sits
// in the repository's history rather than floating, and the tree is built in a
// temporary index so the worktree's own index is never touched.
func snapshotTree(ctx context.Context, wt, ref, message string) error {
	idx, err := os.CreateTemp("", "panda-checkpoint-*.index")
	if err != nil {
		return fmt.Errorf("sessions: temp index: %w", err)
	}
	idxPath := idx.Name()
	idx.Close()
	// git wants to create the index itself; a pre-existing empty file is read
	// as a corrupt index.
	_ = os.Remove(idxPath)
	defer os.Remove(idxPath)
	env := []string{"GIT_INDEX_FILE=" + idxPath}

	head, _ := gitOut(ctx, wt, nil, "rev-parse", "--verify", "HEAD")
	if head != "" {
		// Seeding from HEAD keeps unchanged and deleted files consistent with
		// the parent commit; `add -A` then layers the worktree on top.
		if _, err := gitOut(ctx, wt, env, "read-tree", head); err != nil {
			return err
		}
	}
	if _, err := gitOut(ctx, wt, env, "add", "-A"); err != nil {
		return err
	}
	tree, err := gitOut(ctx, wt, env, "write-tree")
	if err != nil {
		return err
	}
	args := []string{
		"-c", "user.name=OpenPanda", "-c", "user.email=openpanda@localhost",
		"commit-tree", tree, "-m", message,
	}
	if head != "" {
		args = append(args, "-p", head)
	}
	sha, err := gitOut(ctx, wt, nil, args...)
	if err != nil {
		return err
	}
	_, err = gitOut(ctx, wt, nil, "update-ref", ref, sha)
	return err
}

// Checkpoints lists the thread's recorded turns, oldest first. A turn with no
// snapshot is absent rather than zero-valued — callers treat "missing" and
// "empty" differently.
func (s *Store) Checkpoints(ctx context.Context, id string) ([]Checkpoint, error) {
	sess, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if sess.Worktree == "" {
		return nil, nil
	}
	return listCheckpointRefs(ctx, sess.Worktree, id), nil
}

// listCheckpointRefs reads one thread's snapshot refs straight out of git.
// A repository with no refs under the prefix answers empty, which is also what
// a failed read answers — there is nothing a caller can do about either.
func listCheckpointRefs(ctx context.Context, wt, id string) []Checkpoint {
	out, err := gitOut(ctx, wt, nil, "for-each-ref",
		"--format=%(refname) %(objectname)", checkpointPrefix(id))
	if err != nil || strings.TrimSpace(out) == "" {
		return nil
	}
	var list []Checkpoint
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		turn, err := strconv.Atoi(strings.TrimPrefix(fields[0], checkpointPrefix(id)))
		if err != nil {
			continue
		}
		list = append(list, Checkpoint{Turn: turn, SHA: fields[1]})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Turn < list[j].Turn })
	return list
}

// pruneCheckpoints drops the oldest snapshots past the per-thread cap.
func pruneCheckpoints(ctx context.Context, wt, id string) {
	list := listCheckpointRefs(ctx, wt, id)
	if len(list) <= maxCheckpointsPerThread {
		return
	}
	for _, cp := range list[:len(list)-maxCheckpointsPerThread] {
		_, _ = gitOut(ctx, wt, nil, "update-ref", "-d", checkpointRef(id, cp.Turn))
	}
}

// dropCheckpoints removes every snapshot of a thread, for when the thread
// itself goes away. Without this the commits stay reachable from refs forever
// and git gc never reclaims them.
func dropCheckpoints(ctx context.Context, wt, id string) {
	for _, cp := range listCheckpointRefs(ctx, wt, id) {
		_, _ = gitOut(ctx, wt, nil, "update-ref", "-d", checkpointRef(id, cp.Turn))
	}
}

// Rewind moves the thread and (when it has one) its worktree back to the state
// just after turn `turn`. It is destructive by definition: everything written
// after that point — in the conversation and in the tree — is discarded.
//
// The file restore runs before the transcript is truncated so a failed git
// operation leaves the thread untouched and the rewind retryable; a thread
// whose files were restored but whose save failed is the one inconsistent
// outcome, and it is reported rather than hidden.
func (s *Store) Rewind(ctx context.Context, id string, turn int) (*RewindResult, error) {
	sess, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if turn < 0 || turn > len(sess.Turns) {
		return nil, fmt.Errorf("%w: %d", ErrBadTurn, turn)
	}
	res := &RewindResult{Turn: turn}
	if sess.Worktree != "" {
		sha, err := gitOut(ctx, sess.Worktree, nil, "rev-parse", "--verify", checkpointRef(id, turn))
		if err != nil || sha == "" {
			return nil, ErrNoCheckpoint
		}
		removed, err := restoreTree(ctx, sess.Worktree, sha)
		if err != nil {
			return nil, err
		}
		res.Files = true
		res.Removed = removed
	}

	unlock, err := lockStore(s.root)
	if err != nil {
		return nil, fmt.Errorf("sessions: lock: %w", err)
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	// Re-read under the lock: another surface may have appended while the
	// worktree was being restored, and truncating to a stale length would
	// silently keep or drop the wrong turns.
	sess, err = s.load(id)
	if err != nil {
		return nil, err
	}
	if turn > len(sess.Turns) {
		return nil, fmt.Errorf("%w: %d", ErrBadTurn, turn)
	}
	sess.Turns = sess.Turns[:turn]
	sess.UpdatedAt = time.Now()
	if err := s.save(sess); err != nil {
		return nil, err
	}
	res.Turn = len(sess.Turns)
	return res, nil
}

// restoreTree makes the worktree match the snapshot: tracked files come back
// through the index, and files created after the snapshot are removed — they
// are the work a rewind is undoing. Deletion is limited to paths absent from
// the snapshot, so nothing the snapshot knew about is lost.
func restoreTree(ctx context.Context, wt, sha string) ([]string, error) {
	others, _ := gitOut(ctx, wt, nil, "ls-files", "--others", "--exclude-standard")
	var removed []string
	for _, p := range strings.Split(others, "\n") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, err := gitOut(ctx, wt, nil, "cat-file", "-e", sha+":"+p); err == nil {
			continue // the snapshot had this file; read-tree will restore it
		}
		if err := os.Remove(filepath.Join(wt, filepath.FromSlash(p))); err != nil {
			continue // a directory or a race: leave it, the report omits it
		}
		removed = append(removed, p)
	}
	if _, err := gitOut(ctx, wt, nil, "read-tree", "-u", "--reset", sha); err != nil {
		return removed, err
	}
	// The index goes back to HEAD so `git status` reads the way it did before:
	// the snapshot's files show as modified or untracked work, not as a staged
	// commit the user never made. Best-effort — with no HEAD (an empty repo)
	// there is nothing to reset to, and the restored files are still correct.
	_, _ = gitOut(ctx, wt, nil, "reset", "--mixed")
	return removed, nil
}

// DropCheckpointsAfter removes the snapshots a rewind invalidated. Without this
// a later turn would overwrite them one by one as the thread grows back, but
// the ones past the new end would linger and reappear as valid targets for a
// history that no longer exists.
func (s *Store) DropCheckpointsAfter(ctx context.Context, id string, turn int) {
	sess, err := s.Get(id)
	if err != nil || sess.Worktree == "" {
		return
	}
	for _, cp := range listCheckpointRefs(ctx, sess.Worktree, id) {
		if cp.Turn > turn {
			_, _ = gitOut(ctx, sess.Worktree, nil, "update-ref", "-d", checkpointRef(id, cp.Turn))
		}
	}
}

// gitOut runs one git command and returns its trimmed stdout.
func gitOut(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := executil.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
