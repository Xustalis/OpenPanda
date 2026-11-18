// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// TestForkSessionReportsPartialCopy: a fork whose code state could not be
// carried over still returns the child, but the response has to say so. A
// silent 200 is how a user ends up reading a checkout that disagrees with the
// transcript it just copied. The parent's git link is broken here so the copy
// step fails while the child worktree itself is created normally.
func TestForkSessionReportsPartialCopy(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=T", "-c", "user.email=t@t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("baseline\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "baseline")

	wt, err := sessions.OpenWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	store := sessions.NewStore(t.TempDir())
	parent, err := store.Create("parent")
	if err != nil {
		t.Fatal(err)
	}
	path, err := wt.Ensure(context.Background(), parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorktree(parent.ID, path, sessions.Branch(parent.ID)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "tracked.txt"), []byte("uncommitted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Break only the parent's view of the repository. A dangling gitdir is
	// what a half-deleted worktree leaves behind: the child worktree is still
	// created from the repository itself, so this is the partial-success
	// shape, not a failed fork.
	if err := os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: /nonexistent\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := &handler{sessions: store, worktrees: wt}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"at":0}`))
	req.SetPathValue("id", parent.ID)
	rr := httptest.NewRecorder()
	h.forkSession(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("fork: %d %s", rr.Code, rr.Body.String())
	}
	var got struct {
		ID      string `json:"id"`
		Warning string `json:"warning"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID == "" {
		t.Fatalf("session fields are no longer flat in the response: %s", rr.Body.String())
	}
	if got.Warning == "" {
		t.Fatalf("partial fork reported no warning: %s", rr.Body.String())
	}
}
