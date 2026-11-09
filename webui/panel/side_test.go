// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// newSideHandler wires the three routes a side conversation uses against a
// real sessions store and the approval-queue engine's fake model.
func newSideHandler(t *testing.T) (*handler, *sessions.Store) {
	t.Helper()
	engine, _ := newApprovalQueueEngine(t)
	sessStore := sessions.NewStore(t.TempDir())
	h := &handler{
		store: engine.TaskStore(), engine: engine, sessions: sessStore,
		cfgMu:      &sync.RWMutex{},
		serviceCtx: context.Background(),
		activeAsks: map[string]sessionOperation{},
	}
	return h, sessStore
}

func sideMux(h *handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions/{id}/ask", h.sessionAsk)
	mux.HandleFunc("GET /api/sessions/{id}/side", h.sessionSide)
	mux.HandleFunc("DELETE /api/sessions/{id}/side", h.sessionSide)
	mux.HandleFunc("POST /api/sessions/{id}/side/new", h.sessionSideNew)
	mux.HandleFunc("POST /api/sessions/{id}/side/select", h.sessionSideSelect)
	return mux
}

// sidePayloadOf decodes one /side response.
func sidePayloadOf(t *testing.T, body []byte) sidePayload {
	t.Helper()
	var out sidePayload
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode side payload: %v (%s)", err, body)
	}
	return out
}

// TestSideAskIsStoredServerSide pins the background contract: a side question
// files its own transcript in the session record, leaves the thread's turns
// untouched, and is readable back through GET /side after the stream is gone.
func TestSideAskIsStoredServerSide(t *testing.T) {
	h, sessStore := newSideHandler(t)
	mux := sideMux(h)
	sess, err := sessStore.Create("thread")
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, authedReq(http.MethodPost, "/api/sessions/"+sess.ID+"/ask",
		strings.NewReader(`{"prompt":"现在做到哪了","side":true}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("side ask: %d %s", rr.Code, rr.Body.String())
	}

	got, err := sessStore.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Turns) != 0 {
		t.Fatalf("side ask wrote into the thread: %+v", got.Turns)
	}
	chat := got.ActiveSideChat()
	if chat == nil || len(chat.Turns) != 2 {
		t.Fatalf("side turns = %+v, want the question and its answer", got.SideChats)
	}
	if chat.Turns[0].Role != "user" || chat.Turns[0].Text != "现在做到哪了" {
		t.Errorf("first side turn = %+v", chat.Turns[0])
	}
	if chat.Turns[1].Role != "assistant" || chat.Turns[1].Text == "" {
		t.Errorf("second side turn = %+v", chat.Turns[1])
	}
	if chat.Operation == nil || chat.Operation.Status == core.StateRunning {
		t.Errorf("side operation did not settle: %+v", chat.Operation)
	}

	// The reconnect surface returns the same conversation.
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, authedReq(http.MethodGet, "/api/sessions/"+sess.ID+"/side", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("get side: %d %s", rr.Code, rr.Body.String())
	}
	conv := sidePayloadOf(t, rr.Body.Bytes())
	if len(conv.Turns) != 2 || conv.Turns[1].Text != chat.Turns[1].Text {
		t.Errorf("GET /side = %+v, want the stored turns", conv.Turns)
	}
	if conv.Chat != chat.ID || conv.Active != chat.ID {
		t.Errorf("GET /side named chat=%q active=%q, want %q", conv.Chat, conv.Active, chat.ID)
	}
	if len(conv.Chats) != 1 || conv.Chats[0].Turns != 2 {
		t.Errorf("history list = %+v, want one row of two turns", conv.Chats)
	}

	// A second question with no chat id appends to the conversation already
	// there: an empty side_chat means "the active one", not "a new one". The
	// question is filed before the model runs, so this holds even though the
	// test model is not a streaming endpoint and the answer itself fails.
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, authedReq(http.MethodPost, "/api/sessions/"+sess.ID+"/ask",
		strings.NewReader(`{"prompt":"接着问","side":true}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("second side ask: %d %s", rr.Code, rr.Body.String())
	}
	after, err := sessStore.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.SideChats) != 1 {
		t.Fatalf("a chat-less ask minted another conversation: %+v", after.SideChats)
	}
	if c := after.ActiveSideChat(); len(c.Turns) < 3 || c.Turns[2].Text != "接着问" {
		t.Fatalf("second question did not join the same chat: %+v", c)
	}
}

// TestSideGetReportsAStaleOperation: a panel restart leaves a running side
// operation with no live ask behind it. GET must say it was interrupted
// instead of handing the pane a spinner that can never finish.
func TestSideGetReportsAStaleOperation(t *testing.T) {
	h, sessStore := newSideHandler(t)
	sess, err := sessStore.Create("thread")
	if err != nil {
		t.Fatal(err)
	}
	if err := sessStore.StartSideOperation(sess.ID, sessions.Operation{ID: "op1", Status: core.StateRunning}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	sideMux(h).ServeHTTP(rr, authedReq(http.MethodGet, "/api/sessions/"+sess.ID+"/side", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("get side: %d %s", rr.Code, rr.Body.String())
	}
	var conv struct {
		Operation *sessions.Operation `json:"operation"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &conv); err != nil {
		t.Fatal(err)
	}
	if conv.Operation == nil || conv.Operation.Status != core.StateFailed || conv.Operation.Error != "interrupted" {
		t.Fatalf("stale operation = %+v, want an interrupted record", conv.Operation)
	}
}

// TestSideResetClearsTheConversation covers DELETE without a chat id: it drops
// the conversation the pane is showing and leaves any other one alone.
func TestSideResetClearsTheConversation(t *testing.T) {
	h, sessStore := newSideHandler(t)
	sess, err := sessStore.Create("thread")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessStore.AppendSideTurn(sess.ID, sessions.SideTurn{Role: "user", Text: "q"}); err != nil {
		t.Fatal(err)
	}
	if _, err := sessStore.AppendSideTurn(sess.ID, sessions.SideTurn{Role: "assistant", Text: "a"}); err != nil {
		t.Fatal(err)
	}
	before, err := sessStore.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	firstID := before.SideActive
	kept, err := sessStore.NewSideChat(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if kept.SideActive == firstID {
		t.Fatal("new chat did not become the active one")
	}
	if _, err := sessStore.AppendSideTurn(sess.ID, sessions.SideTurn{Role: "user", Text: "other"}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	sideMux(h).ServeHTTP(rr, authedReq(http.MethodDelete, "/api/sessions/"+sess.ID+"/side", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("delete side: %d %s", rr.Code, rr.Body.String())
	}
	got, err := sessStore.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SideChats) != 1 || got.SideChats[0].ID != firstID {
		t.Fatalf("delete took the wrong chat: %+v", got.SideChats)
	}
}

// TestSideChatsAreListedCreatedAndSelected pins the history the pane's header
// renders: `new` adds a conversation without discarding the previous one, the
// list reports both newest-first, `select` moves the pane, an ask names the
// conversation it belongs to, and DELETE ?chat removes exactly one.
func TestSideChatsAreListedCreatedAndSelected(t *testing.T) {
	h, sessStore := newSideHandler(t)
	mux := sideMux(h)
	sess, err := sessStore.Create("thread")
	if err != nil {
		t.Fatal(err)
	}

	// Two conversations beside the thread, each with one exchange. The store
	// is the real one, so seeding it directly keeps this test about the
	// endpoints instead of about the model; explicit clocks make the list's
	// order deterministic (two writes in the same second would tie).
	first, err := sessStore.AppendSideTurn(sess.ID, sessions.SideTurn{Role: "user", Text: "第一个问题", TS: 100})
	if err != nil {
		t.Fatal(err)
	}
	firstID := first.SideActive
	if _, err := sessStore.AppendSideTurnTo(sess.ID, firstID,
		sessions.SideTurn{Role: "assistant", Text: "第一个回答", TS: 101}); err != nil {
		t.Fatal(err)
	}
	second, err := sessStore.NewSideChat(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondID := second.SideActive
	if _, err := sessStore.AppendSideTurnTo(sess.ID, secondID,
		sessions.SideTurn{Role: "user", Text: "第二个问题", TS: 200}); err != nil {
		t.Fatal(err)
	}
	if _, err := sessStore.AppendSideTurnTo(sess.ID, secondID,
		sessions.SideTurn{Role: "assistant", Text: "第二个回答", TS: 201}); err != nil {
		t.Fatal(err)
	}

	// The list is a history: newest activity first, one row per conversation.
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, authedReq(http.MethodGet, "/api/sessions/"+sess.ID+"/side", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("get side: %d %s", rr.Code, rr.Body.String())
	}
	list := sidePayloadOf(t, rr.Body.Bytes())
	ids := make([]string, 0, len(list.Chats))
	for _, c := range list.Chats {
		ids = append(ids, c.ID)
	}
	if len(ids) != 2 || ids[0] != secondID || ids[1] != firstID {
		t.Fatalf("history = %v, want [%s %s]", ids, secondID, firstID)
	}
	if list.Chats[0].Title != "第二个问题" || list.Chats[1].Title != "第一个问题" {
		t.Errorf("titles = %q / %q, want each chat's first question",
			list.Chats[0].Title, list.Chats[1].Title)
	}
	if list.Chat != secondID || len(list.Turns) != 2 {
		t.Errorf("default read = chat %q with %d turns, want the active one", list.Chat, len(list.Turns))
	}

	// `new` starts another conversation and leaves the others in place.
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, authedReq(http.MethodPost, "/api/sessions/"+sess.ID+"/side/new", strings.NewReader(`{}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("new side chat: %d %s", rr.Code, rr.Body.String())
	}
	created := sidePayloadOf(t, rr.Body.Bytes())
	if created.Active == "" || created.Active == firstID || created.Active == secondID {
		t.Fatalf("new chat active = %q, want a fresh id", created.Active)
	}
	// A conversation with no turns has no title and nothing to open, so it
	// does not earn a row yet.
	if len(created.Chats) != 2 {
		t.Errorf("empty chat listed: %+v", created.Chats)
	}

	// Selecting an older conversation returns its transcript.
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, authedReq(http.MethodPost, "/api/sessions/"+sess.ID+"/side/select",
		strings.NewReader(`{"chat_id":"`+firstID+`"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("select side chat: %d %s", rr.Code, rr.Body.String())
	}
	selected := sidePayloadOf(t, rr.Body.Bytes())
	if selected.Chat != firstID || selected.Active != firstID || len(selected.Turns) != 2 {
		t.Fatalf("select returned chat=%q active=%q turns=%d", selected.Chat, selected.Active, len(selected.Turns))
	}

	// An ask that names a conversation files its question there — the question
	// lands before the model runs, so the target is proven even though this
	// test's model endpoint is not a streaming one and the answer fails.
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, authedReq(http.MethodPost, "/api/sessions/"+sess.ID+"/ask",
		strings.NewReader(`{"prompt":"接着第一个问","side":true,"side_chat":"`+firstID+`"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("follow-up side ask: %d %s", rr.Code, rr.Body.String())
	}
	got, err := sessStore.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c := got.SideChatByID(firstID); c == nil || len(c.Turns) < 3 || c.Turns[2].Text != "接着第一个问" {
		t.Fatalf("follow-up did not land in the named chat: %+v", c)
	}
	if c := got.SideChatByID(secondID); c == nil || len(c.Turns) != 2 {
		t.Fatalf("follow-up leaked into another chat: %+v", c)
	}

	// side_new starts a conversation of its own rather than appending to the
	// one on screen.
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, authedReq(http.MethodPost, "/api/sessions/"+sess.ID+"/ask",
		strings.NewReader(`{"prompt":"另起一个","side":true,"side_new":true}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("side_new ask: %d %s", rr.Code, rr.Body.String())
	}
	got, _ = sessStore.Get(sess.ID)
	if got.SideActive == firstID || got.SideActive == secondID {
		t.Fatalf("side_new reused chat %q", got.SideActive)
	}
	if c := got.ActiveSideChat(); c == nil || len(c.Turns) == 0 || c.Turns[0].Text != "另起一个" {
		t.Fatalf("side_new question not filed in a fresh chat: %+v", c)
	}
	freshID := got.SideActive

	// DELETE ?chat removes that one row and keeps the rest.
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, authedReq(http.MethodDelete, "/api/sessions/"+sess.ID+"/side?chat="+freshID, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("delete side chat: %d %s", rr.Code, rr.Body.String())
	}
	got, _ = sessStore.Get(sess.ID)
	if got.SideChatByID(freshID) != nil {
		t.Fatal("deleted chat is still stored")
	}
	if got.SideChatByID(firstID) == nil || got.SideChatByID(secondID) == nil {
		t.Fatal("delete removed the wrong chat")
	}
}

// A side answer stored before the textual-tool-call recovery path existed is
// raw markup. The pane re-renders every stored turn, so the read surface has
// to clean it: the calls become chips, the markup never reaches the reader,
// and the stored record itself is left alone.
func TestLegacyFreeSideTurnsRewritesMarkup(t *testing.T) {
	const markup = "<tool_call>workspace_list<arg_key>path</arg_key><arg_value>projects</arg_value></tool_call>"
	turns := []sessions.SideTurn{
		{Role: "user", Text: "这个线程改了哪些文件？"},
		{Role: "assistant", Text: markup},
		{Role: "assistant", Text: "正常回答"},
	}
	got := legacyFreeSideTurns(turns)
	if len(got) != len(turns) {
		t.Fatalf("len = %d, want %d", len(got), len(turns))
	}
	if got[1].Text != "" {
		t.Errorf("markup survived as prose: %q", got[1].Text)
	}
	if len(got[1].Tools) != 1 || got[1].Tools[0] != "workspace_list" {
		t.Errorf("tools = %+v, want the recovered call", got[1].Tools)
	}
	if got[2].Text != "正常回答" || len(got[2].Tools) != 0 {
		t.Errorf("plain answer rewritten: %+v", got[2])
	}
	if turns[1].Text != markup {
		t.Error("the stored turn was mutated; the read path must not write back")
	}
}
