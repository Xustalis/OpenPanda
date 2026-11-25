// SPDX-License-Identifier: AGPL-3.0-or-later

package self

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/updater"
)

// versionStampRe bounds what may travel into -ldflags from a manifest. A
// version is a display string, never a flag list.
var versionStampRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+-]*$`)

// StageTree builds a staged-release-shaped tree from the worktree at wt:
// {dst}/bin/panda plus the adapters/ and extensions/voice/ resource dirs the
// apply step syncs. The binary is stamped with the patch set's version and
// Provenance=self so `panda version`, the wire provenance, and the trust layer
// all see the build for what it is — a locally produced artifact, never a
// signed release — and which self build is running survives the swap.
func (m *Manager) StageTree(ctx context.Context, wt, dst, version string) error {
	version = strings.TrimSpace(version)
	if version != "" && !versionStampRe.MatchString(version) {
		return fmt.Errorf("version stamp %q is not a plain version string", version)
	}
	binDir := filepath.Join(dst, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	out := filepath.Join(binDir, "panda")
	// The staged name must match what the updater looks for, and that is
	// decided by the OS, not by argv[0]: a daemon started through a wrapper
	// (or a symlink without the suffix) would otherwise stage "panda" on
	// Windows and fail the swap with "no panda.exe binary".
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	ldflags := "-X github.com/Xustalis/OpenPanda/internal/version.Provenance=self"
	if version != "" {
		ldflags += " -X github.com/Xustalis/OpenPanda/internal/version.Version=" + version
	}
	args := []string{"build", "-trimpath", "-ldflags", ldflags}
	// A lite node must stay lite: footprint table and the updater's asset-name
	// selection both key on the build tag (docs/design/self-evolution.md).
	if updater.LiteBuild() {
		args = append(args, "-tags", "lite")
	}
	args = append(args, "-o", out, "./cmd/panda")
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = wt
	if combined, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build: %s", combined)
	}
	for _, dir := range []string{"adapters", "extensions/voice"} {
		src := filepath.Join(wt, filepath.FromSlash(dir))
		if _, err := os.Stat(src); err != nil {
			continue // a tree legitimately lacking a resource dir builds fine
		}
		if err := copyDir(src, filepath.Join(dst, filepath.FromSlash(dir))); err != nil {
			return fmt.Errorf("stage %s: %w", dir, err)
		}
	}
	return nil
}

// NextVersion derives the target stamp for a fresh patch set: the base
// release version plus a +self.<n> build counter, where n counts this node's
// already-merged sets + 1. CompareVersion ignores +meta, so the stamp sorts
// on its base release and an official update still surfaces as available.
//
// The counter never moves backwards: the merged-set count alone would restart
// at 1 after `panda self reset --purge` and hand out a stamp older than the
// binary already installed, so the highest counter ever issued is remembered
// on disk.
func (m *Manager) NextVersion(base string) string {
	n := 1
	if sets, err := m.Queue(); err == nil {
		for _, s := range sets {
			if s.State == StateMerged {
				n++
			}
		}
	}
	if c := m.loadCounter(); c >= n {
		n = c + 1
	}
	// A base that already carries +self.N keeps counting from it.
	if i := strings.Index(base, "+self."); i >= 0 {
		if seen := selfCounter(base[i:]); seen >= n {
			n = seen + 1
		}
		base = base[:i]
	}
	return fmt.Sprintf("%s+self.%d", base, n)
}

// NoteVersionUsed remembers the counter a built binary was stamped with, so
// the next stamp is strictly newer than the last one handed out. The caller
// must treat a failure as fatal to the merge: a stamp that was not persisted
// can be handed out again after a purge.
func (m *Manager) NoteVersionUsed(version string) error {
	n := selfCounter(version)
	if n <= 0 {
		return nil
	}
	if cur := m.loadCounter(); n > cur {
		return m.saveCounter(n)
	}
	return nil
}

// selfCounter reads the +self.N suffix off a version stamp.
func selfCounter(v string) int {
	i := strings.Index(v, "+self.")
	if i < 0 {
		return 0
	}
	var n int
	if _, err := fmt.Sscanf(v[i:], "+self.%d", &n); err != nil {
		return 0
	}
	return n
}

// counterPath is where the high-water counter lives. It sits beside the
// queue so `panda self reset --purge` (which clears the queue and the
// archive) keeps it — the point of the counter is to outlive exactly that.
func (m *Manager) counterPath() string { return filepath.Join(m.root, "version-counter") }

func (m *Manager) loadCounter() int {
	b, err := os.ReadFile(m.counterPath())
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// saveCounter persists the high-water mark atomically: write a sibling temp
// file, fsync it, then rename over the target. A crash mid-write can then
// leave the temp file behind but never a truncated counter — the counter's
// whole job is to survive exactly that kind of crash.
func (m *Manager) saveCounter(n int) error {
	if n <= 0 {
		return nil
	}
	if err := os.MkdirAll(m.root, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(m.root, "version-counter-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds
	if _, err := tmp.WriteString(strconv.Itoa(n)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpName, m.counterPath()); err != nil {
		return err
	}
	// The rename itself must reach the disk; a best-effort directory sync is
	// all the portable API offers.
	if dir, err := os.Open(m.root); err == nil {
		_ = dir.Sync()
		dir.Close()
	}
	return nil
}
