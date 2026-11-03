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
	return mux
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
	if len(got.SideTurns) != 2 {
		t.Fatalf("side turns = %+v, want the question and its answer", got.SideTurns)
	}
	if got.SideTurns[0].Role != "user" || got.SideTurns[0].Text != "现在做到哪了" {
		t.Errorf("first side turn = %+v", got.SideTurns[0])
	}
	if got.SideTurns[1].Role != "assistant" || got.SideTurns[1].Text == "" {
		t.Errorf("second side turn = %+v", got.SideTurns[1])
	}
	if got.SideOperation == nil || got.SideOperation.Status == core.StateRunning {
		t.Errorf("side operation did not settle: %+v", got.SideOperation)
	}

	// The reconnect surface returns the same conversation.
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, authedReq(http.MethodGet, "/api/sessions/"+sess.ID+"/side", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("get side: %d %s", rr.Code, rr.Body.String())
	}
	var conv struct {
		Turns []sessions.SideTurn `json:"turns"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &conv); err != nil {
		t.Fatal(err)
	}
	if len(conv.Turns) != 2 || conv.Turns[1].Text != got.SideTurns[1].Text {
		t.Errorf("GET /side = %+v, want the stored turns", conv.Turns)
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

// TestSideResetClearsTheConversation covers the pane's `+`: the stored turns
// and operation are gone afterwards.
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

	rr := httptest.NewRecorder()
	sideMux(h).ServeHTTP(rr, authedReq(http.MethodDelete, "/api/sessions/"+sess.ID+"/side", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("delete side: %d %s", rr.Code, rr.Body.String())
	}
	got, err := sessStore.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SideTurns) != 0 || got.SideOperation != nil {
		t.Fatalf("reset left state behind: turns=%+v op=%+v", got.SideTurns, got.SideOperation)
	}
}
