// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

func newThreadRepl(t *testing.T) (*repl, *sessions.Store) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	old := cliConfigPath
	cliConfigPath = filepath.Join(t.TempDir(), "missing-config.yaml")
	t.Cleanup(func() { cliConfigPath = old })
	st := sessions.NewStore(t.TempDir())
	return &repl{loc: i18n.English, cfg: &config.Config{}, interactive: true, sessionsSt: st}, st
}

func TestSessionTitleCollapsesAndTruncates(t *testing.T) {
	if got := sessionTitle("hello\n\n  world  "); got != "hello world" {
		t.Fatalf("title = %q", got)
	}
	long := make([]rune, sessionTitleMax+10)
	for i := range long {
		long[i] = 'x'
	}
	got := []rune(sessionTitle(string(long)))
	if len(got) != sessionTitleMax+1 || got[sessionTitleMax] != '…' {
		t.Fatalf("truncated title = %q", string(got))
	}
	if got := sessionTitle("   "); got != "" {
		t.Fatalf("blank title = %q", got)
	}
}

func TestMigrateLegacyConvosImportsAndArchives(t *testing.T) {
	r, st := newThreadRepl(t)
	path := convoPath()
	if path == "" {
		t.Fatal("no conversation path")
	}
	saveConvo([]entry.Turn{
		{Role: "user", Content: "import this"},
		{Role: "assistant", Content: "done"},
	})

	r.migrateLegacyConvos()

	list, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || len(list[0].Turns) != 2 || list[0].Turns[0].Text != "import this" {
		t.Fatalf("imported sessions = %+v", list)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("source file should be archived, stat err = %v", err)
	}
	if _, err := os.Stat(path + ".migrated"); err != nil {
		t.Fatalf("archived copy missing: %v", err)
	}

	// A second launch must not import the same conversation again.
	r.migrateLegacyConvos()
	if again, _ := st.List(); len(again) != 1 {
		t.Fatalf("second migration duplicated sessions: %+v", again)
	}
}

func TestPrepareConversationResumesNewestAndHonorsFlags(t *testing.T) {
	r, st := newThreadRepl(t)
	older, err := st.Create("older")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendTurn(older.ID, sessions.Turn{Role: "user", Text: "old"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	newer, err := st.Create("newer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendTurn(newer.ID, sessions.Turn{Role: "user", Text: "new"}); err != nil {
		t.Fatal(err)
	}

	sess, err := r.prepareConversation("", false)
	if err != nil || sess == nil || sess.ID != newer.ID {
		t.Fatalf("auto-resume = %+v, %v", sess, err)
	}
	if r.sessID() != newer.ID {
		t.Fatalf("bound id = %q, want %q", r.sessID(), newer.ID)
	}

	if sess, err := r.prepareConversation(older.ID, false); err != nil || sess == nil || sess.ID != older.ID {
		t.Fatalf("explicit resume = %+v, %v", sess, err)
	}

	r.setSess(older.ID)
	if sess, err := r.prepareConversation("", true); err != nil || sess != nil || r.sessID() != "" {
		t.Fatalf("--new = %+v, bound %q, err %v", sess, r.sessID(), err)
	}

	if _, err := r.prepareConversation("no-such-session", false); err == nil {
		t.Fatal("an unknown --resume id must be an error")
	}
}

func TestNewTUIModelSeedsBoundThread(t *testing.T) {
	m, st := newSessionTUI(t)
	sess, err := st.Create("thread")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendTurn(sess.ID, sessions.Turn{Role: "user", Text: "earlier question"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendTurn(sess.ID, sessions.Turn{Role: "assistant", Text: "earlier answer"}); err != nil {
		t.Fatal(err)
	}
	m.r.setSess(sess.ID)

	seeded := newTUIModel(m.r)
	if len(seeded.chatHistory.blocks) != 2 {
		t.Fatalf("transcript blocks = %d", len(seeded.chatHistory.blocks))
	}
	if seeded.chatHistory.blocks[0].body != "earlier question" || seeded.chatHistory.blocks[1].body != "earlier answer" {
		t.Fatalf("transcript = %+v", seeded.chatHistory.blocks)
	}
}

func TestStartImplicitSessionMovesConvoAndTeam(t *testing.T) {
	r, st := newThreadRepl(t)
	r.convo = []entry.Turn{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
	}
	if err := r.saveBareTeam([]string{"codex"}); err != nil {
		t.Fatalf("seed team: %v", err)
	}

	if !r.startImplicitSession("next question") {
		t.Fatal("implicit session was not created")
	}
	sess := r.boundSession()
	if sess == nil {
		t.Fatal("no session bound")
	}
	if len(sess.Turns) != 2 || sess.Turns[0].Text != "hi" {
		t.Fatalf("moved turns = %+v", sess.Turns)
	}
	if sess.Title != "next question" {
		t.Fatalf("title = %q", sess.Title)
	}
	if len(sess.Team) != 1 || sess.Team[0] != "codex" {
		t.Fatalf("team = %v", sess.Team)
	}
	if len(r.convoNow()) != 0 {
		t.Fatalf("bare convo should be empty after binding: %+v", r.convoNow())
	}
	if team := r.loadBareTeam(); len(team) != 0 {
		t.Fatalf("bare team should move with the conversation: %v", team)
	}

	// The store must be the single source: a fresh repl resuming it sees the
	// same turns.
	if got, err := st.Get(sess.ID); err != nil || len(got.Turns) != 2 {
		t.Fatalf("stored session = %+v, %v", got, err)
	}
}
