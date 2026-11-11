// SPDX-License-Identifier: AGPL-3.0-or-later

package scout

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEnvironmentHashesRulesWithoutStoringContents(t *testing.T) {
	root := t.TempDir()
	write(t, root, "AGENTS.md", "private-rule-content: do not persist me")
	write(t, root, "nested/CLAUDE.md", "nested rules")
	s := CaptureEnvironment(context.Background(), root)
	if !s.Available || s.Root != root || s.Version != 1 || s.Dependencies != "not_probed" {
		t.Fatalf("snapshot: %+v", s)
	}
	if len(s.RootRules) != 1 || s.RootRules[0].Status != "hashed" || len(s.RootRules[0].SHA256) != 64 {
		t.Fatalf("rules: %+v", s.RootRules)
	}
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), "private-rule-content") || !strings.Contains(s.RulesCoverage, "workspace_root_only") {
		t.Fatalf("unsafe coverage/content: %s", b)
	}
	write(t, root, "AGENTS.md", "updated")
	fresh := CaptureEnvironment(context.Background(), root)
	if fresh.RootRules[0].SHA256 == s.RootRules[0].SHA256 {
		t.Fatal("stale rule hash reused")
	}
	for _, want := range []string{"baseline", "not current verification", "read applicable rules", "not verified"} {
		if !strings.Contains(s.Render(), want) {
			t.Errorf("render missing %q", want)
		}
	}
}

func TestEnvironmentRuleBoundsAndSymlinks(t *testing.T) {
	root := t.TempDir()
	write(t, root, "AGENTS.md", strings.Repeat("x", maxRuleBytes+1))
	if runtime.GOOS != "windows" {
		outside := filepath.Join(t.TempDir(), "secret")
		if err := os.WriteFile(outside, []byte("must not read"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "CLAUDE.md")); err != nil {
			t.Fatal(err)
		}
	}
	s := CaptureEnvironment(context.Background(), root)
	if s.RootRules[0].Status != "too_large" || s.RootRules[0].SHA256 != "" {
		t.Fatalf("large: %+v", s.RootRules)
	}
	if runtime.GOOS != "windows" && (len(s.RootRules) != 2 || s.RootRules[1].Status != "outside_workspace") {
		t.Fatalf("symlink: %+v", s.RootRules)
	}
}

func TestEnvironmentRefreshesGitAndIncludesUntrackedChanges(t *testing.T) {
	if !hasGit() {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	write(t, root, "tracked.txt", "initial")
	runGit(t, root, "add", "tracked.txt")
	runGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "initial")
	s := CaptureEnvironment(context.Background(), root)
	if !s.Git.Known || !s.Git.DirtyKnown || s.Git.Dirty || len(s.Git.Commit) != 40 {
		t.Fatalf("clean: %+v", s.Git)
	}
	write(t, root, "untracked.txt", "new")
	s = CaptureEnvironment(context.Background(), root)
	if !s.Git.DirtyKnown || !s.Git.Dirty {
		t.Fatalf("untracked: %+v", s.Git)
	}
	if err := os.Remove(filepath.Join(root, "untracked.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, root, "tracked.txt", "unstaged")
	s = CaptureEnvironment(context.Background(), root)
	if !s.Git.DirtyKnown || !s.Git.Dirty {
		t.Fatalf("unstaged (status starts with space): %+v", s.Git)
	}
}

func TestEnvironmentUnavailableAndCancelledAreHonest(t *testing.T) {
	for _, root := range []string{"", filepath.Join(t.TempDir(), "missing")} {
		s := CaptureEnvironment(context.Background(), root)
		if s.Available || s.Git.Known || s.Git.DirtyKnown {
			t.Fatalf("missing root: %+v", s)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s := CaptureEnvironment(ctx, t.TempDir()); s.Available || s.Git.DirtyKnown {
		t.Fatalf("cancelled: %+v", s)
	}
	w := &prefixOutput{n: 2}
	data := []byte(" M lots of sensitive filenames")
	if n, err := w.Write(data); n != len(data) || err != nil || string(w.b) != " M" {
		t.Fatalf("bounded capture: n=%d err=%v b=%q", n, err, w.b)
	}
}
