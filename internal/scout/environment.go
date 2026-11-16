// SPDX-License-Identifier: AGPL-3.0-or-later

package scout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const maxRuleBytes = 256 * 1024

// EnvironmentSnapshot describes facts at an attempt boundary, not a claim
// that dependencies, native configuration or all repository rules were loaded.
// Unlike the orientation cache it refreshes git identity and rule hashes.
type EnvironmentSnapshot struct {
	Version       int             `json:"version"`
	CapturedAt    time.Time       `json:"captured_at"`
	Root          string          `json:"root"`
	Available     bool            `json:"available"`
	Git           GitSnapshot     `json:"git"`
	RootRules     []RuleReference `json:"root_rules,omitempty"`
	RulesCoverage string          `json:"rules_coverage"`
	Dependencies  string          `json:"dependencies"`
}

type GitSnapshot struct {
	Known      bool   `json:"known"`
	RepoRoot   string `json:"repo_root,omitempty"`
	Commit     string `json:"commit,omitempty"`
	Branch     string `json:"branch,omitempty"`
	DirtyKnown bool   `json:"dirty_known"`
	Dirty      bool   `json:"dirty"`
}

type RuleReference struct {
	Path   string `json:"path"`
	Status string `json:"status"` // hashed | unreadable | outside_workspace | too_large
	SHA256 string `json:"sha256,omitempty"`
}

// CaptureEnvironment is read-only and bounded. It neither scans the tree nor
// runs project commands, installs dependencies, loads credentials or starts a
// service. Rule hashes identify content without persisting its possibly
// sensitive body. Nested/ancestor rules remain the native harness's job.
func CaptureEnvironment(ctx context.Context, root string) EnvironmentSnapshot {
	s := EnvironmentSnapshot{Version: 1, CapturedAt: time.Now().UTC(),
		RulesCoverage: "workspace_root_only; discover applicable ancestor/nested rules before acting",
		Dependencies:  "not_probed"}
	if root == "" {
		return s
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return s
	}
	s.Root = abs
	fi, err := os.Stat(abs)
	if err != nil || !fi.IsDir() || ctx.Err() != nil {
		return s
	}
	s.Available = true
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		if ref, exists := rootRule(abs, name); exists {
			s.RootRules = append(s.RootRules, ref)
		}
	}
	s.Git = captureGit(ctx, abs)
	return s
}

func captureGit(ctx context.Context, root string) GitSnapshot {
	// Root and status are independent read-only queries. Start both within the
	// same budget so Windows process startup does not consume it sequentially.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	type rootResult struct {
		top string
		ok  bool
	}
	rootReady := make(chan rootResult, 1)
	go func() {
		top, ok := environmentGit(ctx, root, 4096, "rev-parse", "--show-toplevel")
		rootReady <- rootResult{top: top, ok: ok}
	}()
	// Porcelain v2 reports HEAD, branch and tracked/untracked dirt together;
	// its writer discards every filename rather than collecting the listing.
	status, statusOK := environmentGitStatus(ctx, root)
	repo := <-rootReady
	if !repo.ok {
		return GitSnapshot{}
	}
	s := GitSnapshot{Known: true, RepoRoot: repo.top}
	if statusOK {
		s.Commit, s.Branch = status.commit, status.branch
		s.DirtyKnown, s.Dirty = true, status.dirty
	}
	return s
}

func rootRule(root, name string) (RuleReference, bool) {
	r := RuleReference{Path: name, Status: "unreadable"}
	path := filepath.Join(root, name)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return r, false
	}
	resolved, err := filepath.EvalSymlinks(path)
	base, baseErr := filepath.EvalSymlinks(root)
	if err != nil || baseErr != nil {
		return r, true
	}
	rel, err := filepath.Rel(base, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		r.Status = "outside_workspace"
		return r, true
	}
	if fi, err := os.Stat(resolved); err != nil || !fi.Mode().IsRegular() {
		return r, true
	}
	f, err := os.Open(resolved)
	if err != nil {
		return r, true
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return r, true
	}
	b, err := io.ReadAll(io.LimitReader(f, maxRuleBytes+1))
	if err != nil {
		return r, true
	}
	if len(b) > maxRuleBytes {
		r.Status = "too_large"
		return r, true
	}
	h := sha256.Sum256(b)
	r.Status, r.SHA256 = "hashed", hex.EncodeToString(h[:])
	return r, true
}

// prefixOutput discards excess output while reporting a full write. A dirty
// monorepo must not allocate its complete status listing just to answer yes/no.
type prefixOutput struct {
	b []byte
	n int
}

func (w *prefixOutput) Write(p []byte) (int, error) {
	n := len(p)
	if left := w.n - len(w.b); left > 0 {
		w.b = append(w.b, p[:min(left, n)]...)
	}
	return n, nil
}

func environmentGit(ctx context.Context, root string, limit int, args ...string) (string, bool) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "-C", root}, args...)...)
	w := &prefixOutput{n: limit}
	cmd.Stdout = w
	cmd.WaitDelay = 100 * time.Millisecond
	err := cmd.Run()
	return strings.TrimSpace(string(w.b)), err == nil
}

// Only branch headers enter memory. Every status path is discarded from its
// first byte, while the command is still drained so huge dirty trees cannot
// fill a pipe or become a persisted list of sensitive filenames.
type environmentStatusOutput struct {
	header         []byte
	dropLine       bool
	commit, branch string
	dirty          bool
}

func (w *environmentStatusOutput) Write(data []byte) (int, error) {
	for _, b := range data {
		if b == '\n' {
			w.finishLine()
			continue
		}
		if w.dropLine {
			continue
		}
		if len(w.header) == 0 && b != '#' {
			w.dirty, w.dropLine = true, true
			continue
		}
		if len(w.header) < 512 {
			w.header = append(w.header, b)
		}
	}
	return len(data), nil
}

func (w *environmentStatusOutput) finishLine() {
	line := strings.TrimSpace(string(w.header))
	if oid, ok := strings.CutPrefix(line, "# branch.oid "); ok && oid != "(initial)" {
		w.commit = oid
	}
	if branch, ok := strings.CutPrefix(line, "# branch.head "); ok && branch != "(detached)" {
		w.branch = branch
	}
	w.header = w.header[:0]
	w.dropLine = false
}

func environmentGitStatus(ctx context.Context, root string) (*environmentStatusOutput, bool) {
	cmd := exec.CommandContext(ctx, "git", "--no-optional-locks", "-C", root,
		"status", "--porcelain=v2", "--branch", "--no-ahead-behind", "--untracked-files=normal")
	w := &environmentStatusOutput{}
	cmd.Stdout = w
	cmd.WaitDelay = 100 * time.Millisecond
	err := cmd.Run()
	w.finishLine()
	return w, err == nil
}

// Render names the observation boundary so an agent never treats a baseline
// hash or a declared rule path as proof of its current contents or behavior.
func (s EnvironmentSnapshot) Render() string {
	if !s.Available {
		return "[attempt environment baseline]\nWorkspace unavailable; do not claim repository inspection."
	}
	var b strings.Builder
	b.WriteString("[attempt environment baseline — not current verification]\n")
	if s.Git.Known {
		fmt.Fprintf(&b, "git commit: %s; branch: %s\n", s.Git.Commit, s.Git.Branch)
	}
	state := "unknown"
	if s.Git.DirtyKnown {
		state = "clean"
		if s.Git.Dirty {
			state = "has pre-existing changes (including untracked files)"
		}
	}
	fmt.Fprintf(&b, "initial working tree: %s\n", state)
	for _, r := range s.RootRules {
		fmt.Fprintf(&b, "root rule: %s (%s)\n", r.Path, r.Status)
	}
	b.WriteString("Root rule references are not loaded instructions; read applicable rules, including ancestor/nested rules. Dependencies and declared checks are not verified by this snapshot.")
	return b.String()
}

// RootRulesFingerprint detects changed standing instructions before reusing a
// conversation's context. Ancestor/nested rules remain the harness's boundary.
func RootRulesFingerprint(root string) string {
	h := sha256.New()
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		ref, exists := rootRule(root, name)
		fmt.Fprintf(h, "%s:%t:%s:%s\n", name, exists, ref.Status, ref.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil))
}
