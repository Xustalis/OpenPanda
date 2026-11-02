// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// End-to-end checks for the terminal/console session handoff: these drive the
// real entry points (askContext, recordOutcome, prepareConversation) and the
// console's own HTTP API over one store, instead of asserting the helpers in
// isolation. A passing test here means the feature is wired, not just present.

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	"github.com/Xustalis/OpenPanda/internal/storage"
	"github.com/Xustalis/OpenPanda/webui/panel"
)

func handoffEnv(t *testing.T) (*config.Config, *sql.DB, *core.TaskStore, *sessions.Store) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg := writeTestConfig(t, t.TempDir())
	if err := os.MkdirAll(filepath.Dir(cfg.Storage.DBPath), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(cfg.Storage.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Migrate(db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return cfg, db, core.NewSigningTaskStore(db, nil), sessions.NewStore(sessionStoreRoot(cfg))
}

func handoffConsole(store *core.TaskStore, db *sql.DB, st *sessions.Store, cfg *config.Config) http.Handler {
	return panel.New(panel.Deps{Store: store, DB: db, Sessions: st, Cfg: cfg, Token: "handoff-token"})
}

func handoffReq(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Authorization", "Bearer handoff-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// TestTerminalConversationIsVisibleInConsole drives the first prompt through
// askContext (the entry point both front ends use), then reads the console's
// own API over the same store: the thread must be there, and a task spawned by
// the turn must carry its id.
func TestTerminalConversationIsVisibleInConsole(t *testing.T) {
	cfg, db, store, st := handoffEnv(t)
	r := &repl{loc: i18n.English, cfg: cfg, db: db, store: store, sessionsSt: st, interactive: true}
	r.convo = []entry.Turn{
		{Role: "user", Content: "earlier question"},
		{Role: "assistant", Content: "earlier answer"},
	}

	history, workDir := r.askContext("ship the fix")
	if len(history) != 2 || history[0].Content != "earlier question" {
		t.Fatalf("replayed history = %+v", history)
	}
	if workDir != "" {
		t.Fatalf("implicit thread should not pin a worktree, got %q", workDir)
	}
	if sid := r.sessID(); sid == "" {
		t.Fatal("askContext did not bind a session")
	}

	task, err := store.Create(context.Background(), "", "", "ship the fix", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.recordOutcome(context.Background(), "done", &askengine.Result{Kind: "task", TaskID: task.TaskID, OK: true})
	got, err := store.Get(context.Background(), task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != r.sessID() {
		t.Fatalf("task session_id = %q, want %q", got.SessionID, r.sessID())
	}

	h := handoffConsole(store, db, st, cfg)
	rr := handoffReq(t, h, http.MethodGet, "/api/sessions", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/sessions = %d: %s", rr.Code, rr.Body.String())
	}
	var list []sessions.Session
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode sessions: %v", err)
	}
	for _, sess := range list {
		if sess.ID == r.sessID() {
			return
		}
	}
	t.Fatalf("terminal session %q not in console list: %s", r.sessID(), rr.Body.String())
}

// TestConsoleThreadResumesInTerminal creates a thread through the console's
// HTTP API, writes a completed exchange into it, then launches a fresh repl:
// prepareConversation must bind that thread, so the terminal picks up where
// the console left off.
func TestConsoleThreadResumesInTerminal(t *testing.T) {
	cfg, db, store, st := handoffEnv(t)
	h := handoffConsole(store, db, st, cfg)
	rr := handoffReq(t, h, http.MethodPost, "/api/sessions", `{"title":"from the console"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST /api/sessions = %d: %s", rr.Code, rr.Body.String())
	}
	var created sessions.Session
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	// The console records the user turn immediately and the answer when the
	// turn settles; simulate the completed pair.
	if _, err := st.AppendTurns(created.ID,
		sessions.Turn{Role: "user", Text: consolePrompt},
		sessions.Turn{Role: "assistant", Text: "console answer"}); err != nil {
		t.Fatal(err)
	}

	r := &repl{loc: i18n.English, cfg: cfg, db: db, store: store, sessionsSt: st}
	sess, err := r.prepareConversation("", false)
	if err != nil {
		t.Fatal(err)
	}
	if sess == nil || sess.ID != created.ID {
		t.Fatalf("prepareConversation = %+v, want %s", sess, created.ID)
	}
	if r.sessID() != created.ID {
		t.Fatalf("bound id = %q, want %q", r.sessID(), created.ID)
	}
	got := r.boundSession()
	if got == nil || len(got.Turns) != 2 || got.Turns[0].Text != consolePrompt {
		t.Fatalf("bound thread = %+v", got)
	}
}

const consolePrompt = "console question"
