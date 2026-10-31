// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// writeTestConfig points cliConfigPath at a real temp config whose storage
// roots all live under dir. A path to a missing file is not enough isolation:
// config.Load then falls back to the default per-user paths, so convoPath's
// active-project lookup opens (and migrates) the developer's node database.
func writeTestConfig(t *testing.T, dir string) *config.Config {
	t.Helper()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	data := fmt.Sprintf(`node:
  name: test-node
storage:
  db_path: "%s"
  context_path: "%s"
  memory_path: "%s"
  projects_path: "%s"
  skills_path: "%s"
  work_path: "%s"
  artifact_path: "%s"
`,
		filepath.Join(dataDir, "panda.db"),
		filepath.Join(dataDir, "context"),
		filepath.Join(dataDir, "memory"),
		filepath.Join(dataDir, "projects"),
		filepath.Join(dataDir, "skills"),
		filepath.Join(dataDir, "work"),
		filepath.Join(dataDir, "artifacts"),
	)
	if err := os.WriteFile(cfgPath, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	old := cliConfigPath
	cliConfigPath = cfgPath
	t.Cleanup(func() { cliConfigPath = old })
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load test config: %v", err)
	}
	return cfg
}

func newThreadRepl(t *testing.T) (*repl, *sessions.Store) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg := writeTestConfig(t, t.TempDir())
	st := sessions.NewStore(t.TempDir())
	return &repl{loc: i18n.English, cfg: cfg, interactive: true, sessionsSt: st}, st
}

func TestSessionTitleCollapsesAndTruncates(t *testing.T) {
	const titleMax = 48
	if got := sessions.Title("hello\n\n  world  "); got != "hello world" {
		t.Fatalf("title = %q", got)
	}
	long := make([]rune, titleMax+10)
	for i := range long {
		long[i] = 'x'
	}
	got := []rune(sessions.Title(string(long)))
	if len(got) != titleMax+1 || got[titleMax] != '…' {
		t.Fatalf("truncated title = %q", string(got))
	}
	if got := sessions.Title("   "); got != "" {
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

func TestContinueTargetPicksNewestThread(t *testing.T) {
	cfg := &config.Config{}
	cfg.Storage.DBPath = filepath.Join(t.TempDir(), "panda.db")
	st := sessions.NewStore(sessionStoreRoot(cfg))

	older, err := st.Create("older")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendTurns(older.ID,
		sessions.Turn{Role: "user", Text: "old question"},
		sessions.Turn{Role: "assistant", Text: "old answer"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	newer, err := st.Create("newer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendTurns(newer.ID,
		sessions.Turn{Role: "user", Text: "new question"},
		sessions.Turn{Role: "assistant", Text: "new answer"}); err != nil {
		t.Fatal(err)
	}

	store, sess, history := continueTarget(context.Background(), cfg, "", nil)
	if store == nil || sess == nil || sess.ID != newer.ID {
		t.Fatalf("continueTarget = %+v, %+v", store, sess)
	}
	if len(history) != 2 || history[0].Content != "new question" || history[1].Content != "new answer" {
		t.Fatalf("replayed history = %+v", history)
	}
}

func TestSessionHistoryDropsDanglingUserTurn(t *testing.T) {
	st := sessions.NewStore(t.TempDir())
	sess, err := st.Create("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendTurns(sess.ID,
		sessions.Turn{Role: "user", Text: "question"},
		sessions.Turn{Role: "assistant", Text: "answer"},
		sessions.Turn{Role: "user", Text: "never answered"}); err != nil {
		t.Fatal(err)
	}
	sess, err = st.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, history := sessionHistory(context.Background(), st, sess, nil)
	if len(history) != 2 || history[1].Role != "assistant" {
		t.Fatalf("replay = %+v", history)
	}
}

func TestAppendExchangeToSessionRecordsPair(t *testing.T) {
	st := sessions.NewStore(t.TempDir())
	sess, err := st.Create("thread")
	if err != nil {
		t.Fatal(err)
	}
	appendExchangeToSession(st, sess.ID, "question", &askengine.Result{
		Kind: "task", TaskID: "t-1", Answer: "done",
	}, i18n.English)

	got, err := st.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Turns) != 2 {
		t.Fatalf("turns = %+v", got.Turns)
	}
	if got.Turns[0].Role != "user" || got.Turns[0].Text != "question" {
		t.Fatalf("user turn = %+v", got.Turns[0])
	}
	if got.Turns[1].Role != "assistant" || got.Turns[1].Kind != "task" || got.Turns[1].Ref != "t-1" {
		t.Fatalf("assistant turn = %+v", got.Turns[1])
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
