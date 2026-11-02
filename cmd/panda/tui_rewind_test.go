// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !lite

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/sessions"

	tea "github.com/charmbracelet/bubbletea"
)

// TestRewindQuestionNamesThePrompt pins how a checkpoint reads in the picker:
// the prompt that started the turn, not a message count. A list of "after turn
// 8 / after turn 6" is a list nobody can choose from.
func TestRewindQuestionNamesThePrompt(t *testing.T) {
	sess := &sessions.Session{Turns: []sessions.Turn{
		{Role: "user", Text: "把路由改成按负载加权"},
		{Role: "assistant", Text: "改好了"},
		{Role: "user", Text: "再跑一遍测试"},
		{Role: "assistant", Text: "全绿"},
	}}
	if got := rewindQuestion(sess, 2); got != "把路由改成按负载加权" {
		t.Errorf("turn 2 = %q", got)
	}
	if got := rewindQuestion(sess, 4); got != "再跑一遍测试" {
		t.Errorf("turn 4 = %q", got)
	}
	// Out-of-range and empty turns describe nothing rather than panicking.
	if got := rewindQuestion(sess, 0); got != "" {
		t.Errorf("turn 0 = %q", got)
	}
	if got := rewindQuestion(sess, 99); got != "" {
		t.Errorf("turn 99 = %q", got)
	}
	if got := rewindQuestion(nil, 1); got != "" {
		t.Errorf("nil session = %q", got)
	}
}

// TestRewindQuestionTruncates keeps one long prompt from filling the row.
func TestRewindQuestionTruncates(t *testing.T) {
	long := strings.Repeat("很长的提问", 20)
	sess := &sessions.Session{Turns: []sessions.Turn{
		{Role: "user", Text: long},
		{Role: "assistant", Text: "ok"},
	}}
	got := rewindQuestion(sess, 2)
	if len([]rune(got)) > 49 {
		t.Errorf("truncation did not hold: %d runes", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated prompt should end in an ellipsis: %q", got)
	}
}

// TestRewindWithoutSessionExplains is the surface promise for a thread that
// cannot rewind: it says why instead of doing nothing.
func TestRewindWithoutSessionExplains(t *testing.T) {
	m := newIsolatedTUI(t)
	next, cmd := m.submit("/rewind")
	m = next.(tuiModel)
	if cmd == nil {
		t.Fatal("/rewind answered with no command at all")
	}
	if m.mode != modeIdle {
		t.Errorf("mode = %v, want idle", m.mode)
	}
	if last := lastBlock(m); last.kind != blockNote || last.body == "" {
		t.Errorf("expected an explanatory note, got %+v", last)
	}
}

// newRewindRepo creates a throwaway git repository for the rewired UI tests.
func newRewindRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"git", "init", "-q", "-b", "main", dir},
		{"git", "-C", dir, "-c", "user.name=T", "-c", "user.email=t@t", "commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// newRewindTUI wires a TUI onto a real session store with one thread that has
// two finished turns and a worktree.
func newRewindTUI(t *testing.T) (tuiModel, *sessions.Store, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := newRewindRepo(t)
	w, err := sessions.OpenWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	store := sessions.NewStore(filepath.Join(t.TempDir(), "sessions"))
	sess, err := store.Create("rewind")
	if err != nil {
		t.Fatal(err)
	}
	wt, err := w.Ensure(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorktree(sess.ID, wt, sessions.Branch(sess.ID)); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(wt, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "one")
	if _, err := store.AppendTurn(sess.ID, sessions.Turn{Role: "user", Text: "第一步"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendTurn(sess.ID, sessions.Turn{Role: "assistant", Text: "好了"}); err != nil {
		t.Fatal(err)
	}
	write("a.txt", "two")
	write("b.txt", "new")
	if _, err := store.AppendTurn(sess.ID, sessions.Turn{Role: "user", Text: "第二步"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendTurn(sess.ID, sessions.Turn{Role: "assistant", Text: "也好了"}); err != nil {
		t.Fatal(err)
	}

	r := &repl{loc: i18n.Locale("en"), cfg: &config.Config{}, sessionsSt: store}
	r.setSess(sess.ID)
	m := newTUIModel(r)
	m.mode = modeIdle
	m.width, m.height = 100, 30
	return m, store, sess.ID
}

// TestRewindListShowsRecordedTurns is the picker's接线 check: the checkpoints
// the store recorded are the rows the user sees, newest first.
func TestRewindListShowsRecordedTurns(t *testing.T) {
	m, _, _ := newRewindTUI(t)

	next, _ := m.openRewindList()
	got := next.(tuiModel)
	if got.listKind != listRewind || got.mode != modeList {
		t.Fatalf("picker did not open: kind=%v mode=%v", got.listKind, got.mode)
	}
	if len(got.selectionList.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(got.selectionList.Items))
	}
	// Newest first, named by the prompt that started the turn.
	if got.selectionList.Items[0].Title != "第二步" {
		t.Errorf("first row = %q, want the latest prompt", got.selectionList.Items[0].Title)
	}
	if got.selectionList.Items[1].Title != "第一步" {
		t.Errorf("second row = %q", got.selectionList.Items[1].Title)
	}
}

// TestRewindConfirmRunsTheRewind walks the whole surface: pick, confirm, and
// the transcript plus the files moving back together.
func TestRewindConfirmRunsTheRewind(t *testing.T) {
	m, store, id := newRewindTUI(t)

	next, _ := m.confirmRewind(2)
	m = next.(tuiModel)
	if m.mode != modeConfirm || m.confirmRun == nil {
		t.Fatalf("no confirmation card: mode=%v", m.mode)
	}
	next, _ = m.onConfirmKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = next.(tuiModel)

	if m.mode != modeIdle {
		t.Errorf("mode = %v after the rewind", m.mode)
	}
	// The transcript is rebuilt from the truncated thread — two blocks from the
	// first turn — and the outcome note is appended under them.
	if len(m.chatHistory.blocks) != 3 {
		t.Fatalf("blocks = %d, want 2 turns + 1 note", len(m.chatHistory.blocks))
	}
	if m.chatHistory.blocks[0].body != "第一步" {
		t.Errorf("first block = %q", m.chatHistory.blocks[0].body)
	}
	if last := m.chatHistory.blocks[2]; last.kind != blockNote {
		t.Errorf("last block = %v, want the outcome note", last.kind)
	}
	sess, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Turns) != 2 {
		t.Errorf("stored turns = %d", len(sess.Turns))
	}
}

// TestRewindConfirmCancelLeavesEverything keeps the card honest: anything but
// "y" is a no, and nothing moves.
func TestRewindConfirmCancelLeavesEverything(t *testing.T) {
	m, store, id := newRewindTUI(t)

	next, _ := m.confirmRewind(2)
	m = next.(tuiModel)
	next, _ = m.onConfirmKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(tuiModel)

	if m.mode != modeIdle {
		t.Errorf("mode = %v", m.mode)
	}
	sess, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Turns) != 4 {
		t.Errorf("a cancelled rewind changed the transcript: %d turns", len(sess.Turns))
	}
}
