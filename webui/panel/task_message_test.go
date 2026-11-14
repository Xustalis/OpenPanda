// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/testdb"
)

// TestSendTaskMessageWithoutEngine pins the scheduler-less path: queue mode
// still lands the durable note (the store is enough), inject honestly refuses
// with 503 — a console without the engine can never abort a live round.
func TestSendTaskMessageWithoutEngine(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	task, err := store.Create(ctx, "", "proj", "message target", "node", []string{"node"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.Queue(ctx, task.TaskID, "node"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	h := New(Deps{Store: store, StaticDir: t.TempDir(), Token: testToken})

	body := `{"text":"also update the docs"}`
	rr := httptest.NewRecorder()
	req := authedReq(http.MethodPost, "/api/tasks/"+task.TaskID+"/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("queue send: status = %d, body %s", rr.Code, rr.Body.String())
	}
	var out struct {
		TaskID string `json:"task_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Status != "queued" || out.TaskID != task.TaskID {
		t.Fatalf("out = %+v, want queued for the task", out)
	}
	msgs, err := store.TaskMessages(ctx, "task:"+task.TaskID)
	if err != nil || len(msgs) != 1 || msgs[0].Body != "also update the docs" || msgs[0].Grade != core.MsgGradeStatus {
		t.Fatalf("board after send: %v msgs=%+v", err, msgs)
	}

	rr = httptest.NewRecorder()
	req = authedReq(http.MethodPost, "/api/tasks/"+task.TaskID+"/messages", strings.NewReader(`{"text":"stop","inject":true}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("inject without engine: status = %d, want 503", rr.Code)
	}
}

// TestSendTaskMessageWithoutEngineWarnsDeadQueue pins the engine-less
// warning: the console's own listening port is not a consumer, so a note to a
// still-queued row must carry the advisory unless a foreign heartbeat proves
// something drains this store. The handler is built without Deps.DB on
// purpose — the probe belongs to the store the note was posted to.
func TestSendTaskMessageWithoutEngineWarnsDeadQueue(t *testing.T) {
	db := testdb.Open(t)
	store := core.NewTaskStore(db, slog.New(slog.DiscardHandler))
	ctx := context.Background()
	cfg := &config.Config{
		Node: config.NodeConfig{Name: "panel-warn", Kind: config.NodeKindVM, Identity: "panel-warn-identity"},
		UI:   config.UIConfig{Locale: "en"},
	}
	nodeID := core.RuntimeNodeID(cfg.Node.Name, cfg.Node.Kind, cfg.Node.EffectiveIdentity())
	task, err := store.Create(ctx, "", "proj", "queued target", nodeID, []string{nodeID})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.Queue(ctx, task.TaskID, nodeID); err != nil {
		t.Fatalf("queue: %v", err)
	}
	h := New(Deps{Store: store, Cfg: cfg, StaticDir: t.TempDir(), Token: testToken})

	send := func() string {
		rr := httptest.NewRecorder()
		req := authedReq(http.MethodPost, "/api/tasks/"+task.TaskID+"/messages", strings.NewReader(`{"text":"later"}`))
		req.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("send: status = %d, body %s", rr.Code, rr.Body.String())
		}
		var out struct {
			Warning string `json:"warning"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return out.Warning
	}

	if warn := send(); warn == "" {
		t.Fatal("a queued task on a store with no heartbeat must carry the no-consumer warning")
	}
	if err := ledger.Register(db, ledger.Card{Device: "panel-warn"}, nodeID, 1); err != nil {
		t.Fatalf("register heartbeat row: %v", err)
	}
	if warn := send(); warn != "" {
		t.Fatalf("a fresh foreign heartbeat must clear the warning, got %q", warn)
	}
}

// TestSendTaskMessageGuards pins the input and state guards: empty text is a
// 400, an unknown task a 404, a finished task a 409 (state conflict, not a
// server fault).
func TestSendTaskMessageGuards(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	h := New(Deps{Store: store, StaticDir: t.TempDir(), Token: testToken})

	post := func(path, body string) int {
		rr := httptest.NewRecorder()
		req := authedReq(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(rr, req)
		return rr.Code
	}
	if got := post("/api/tasks/nope/messages", `{"text":"hi"}`); got != http.StatusNotFound {
		t.Fatalf("unknown task: status = %d, want 404", got)
	}

	task, err := store.Create(ctx, "", "proj", "guarded", "node", []string{"node"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := post("/api/tasks/"+task.TaskID+"/messages", `{"text":"   "}`); got != http.StatusBadRequest {
		t.Fatalf("empty text: status = %d, want 400", got)
	}
	if _, err := store.CancelCascade(ctx, task.TaskID); err != nil {
		t.Fatalf("finish task: %v", err)
	}
	if got := post("/api/tasks/"+task.TaskID+"/messages", `{"text":"late"}`); got != http.StatusConflict {
		t.Fatalf("finished task: status = %d, want 409", got)
	}
}

// TestListTaskMessagesEndpoint pins the board read-back: the send endpoint's
// GET sibling must return exactly what lands on the task's scope — a posted
// note renders (the "queued" state the detail view shows), an empty board is
// a list not an error, and unknown ids are a 404.
func TestListTaskMessagesEndpoint(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	h := New(Deps{Store: store, StaticDir: t.TempDir(), Token: testToken})

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/tasks/nope/messages", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown task: status = %d, want 404", rr.Code)
	}

	task, err := store.Create(ctx, "", "proj", "board target", "node", []string{"node"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.Queue(ctx, task.TaskID, "node"); err != nil {
		t.Fatalf("queue: %v", err)
	}

	get := func() []core.TaskMessage {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/tasks/"+task.TaskID+"/messages", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("board get: status = %d, body %s", rr.Code, rr.Body.String())
		}
		var out struct {
			Messages []core.TaskMessage `json:"messages"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return out.Messages
	}

	if msgs := get(); len(msgs) != 0 {
		t.Fatalf("empty board should return [], got %+v", msgs)
	}
	if err := store.PostUserMessage(ctx, task.TaskID, "while you run, also check tests", core.MsgGradeStatus, ""); err != nil {
		t.Fatalf("post note: %v", err)
	}
	msgs := get()
	if len(msgs) != 1 || msgs[0].Body != "while you run, also check tests" || msgs[0].Grade != core.MsgGradeStatus {
		t.Fatalf("board after post: %+v", msgs)
	}
}

// parkFollowupTask drives a fresh task through the real transitions into the
// exact row a supervised follow-up park leaves: running -> review under
// accept_work with needs_followup inside the stored result.
func parkFollowupTask(t *testing.T, store *core.TaskStore, nodeID, title string, followup bool) string {
	t.Helper()
	ctx := context.Background()
	task, err := store.Create(ctx, "", "proj", title, nodeID, []string{nodeID})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.Queue(ctx, task.TaskID, nodeID); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := store.Dispatch(ctx, task.TaskID, nodeID, nodeID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := store.Accept(ctx, task.TaskID, nodeID); err != nil {
		t.Fatalf("accept: %v", err)
	}
	result := map[string]any{"ok": false, "stderr": "round budget exhausted"}
	if followup {
		result["needs_followup"] = true
	}
	if err := store.PauseWithResult(ctx, task.TaskID, nodeID, result); err != nil {
		t.Fatalf("park: %v", err)
	}
	return task.TaskID
}

// TestContinueTaskEndpoint pins the endpoint's honest gating: a follow-up
// park requeues (200 + queued row + the note landed as a steer message), a
// plain review is a 409, and without an engine the answer is 503 — never a
// silent state change.
func TestContinueTaskEndpoint(t *testing.T) {
	engine := newQueueEngine(t)
	store := engine.TaskStore()
	h := New(Deps{Store: store, Engine: engine, StaticDir: t.TempDir(), Token: testToken})
	ctx := context.Background()
	// The queue engine's stable runtime id (vm kind → name@vm-<identity>):
	// owner and dispatch target must resolve to it or the continue's
	// locality guards (correctly) refuse the requeue.
	nodeID := core.RuntimeNodeID("panel-test", "vm", config.PhysicalIdentity())

	id := parkFollowupTask(t, store, nodeID, "long work", true)
	rr := httptest.NewRecorder()
	req := authedReq(http.MethodPost, "/api/tasks/"+id+"/continue", strings.NewReader(`{"note":"finish part two"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("continue: %d %s", rr.Code, rr.Body.String())
	}
	var out struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil || out.State != core.StateQueued {
		t.Fatalf("out = %d %+v, want queued", rr.Code, out)
	}
	cur, err := store.Get(ctx, id)
	if err != nil || cur.State != core.StateQueued {
		t.Fatalf("row after continue: %+v %v", cur, err)
	}
	msgs, err := store.TaskMessages(ctx, "task:"+id)
	if err != nil {
		t.Fatalf("board read: %v", err)
	}
	found := false
	for _, m := range msgs {
		if m.Grade == core.MsgGradeSteer && strings.Contains(m.Body, "finish part two") {
			found = true
		}
	}
	if !found {
		t.Fatalf("operator note must land as a steer message, got %+v", msgs)
	}

	plain := parkFollowupTask(t, store, nodeID, "plain park", false)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks/"+plain+"/continue", nil))
	if rr.Code != http.StatusConflict {
		t.Fatalf("non-followup continue: %d, want 409", rr.Code)
	}

	// Without an engine the same parked task is a 503 — the console cannot
	// validate ownership for the requeue.
	h2 := New(Deps{Store: store, StaticDir: t.TempDir(), Token: testToken})
	id2 := parkFollowupTask(t, store, nodeID, "engineless", true)
	rr = httptest.NewRecorder()
	h2.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks/"+id2+"/continue", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("engineless continue: %d, want 503", rr.Code)
	}
}

func TestApproveUnfinishedTaskReturnsConflict(t *testing.T) {
	engine := newQueueEngine(t)
	store := engine.TaskStore()
	h := New(Deps{Store: store, Engine: engine, StaticDir: t.TempDir(), Token: testToken})
	nodeID := core.RuntimeNodeID("panel-test", "vm", config.PhysicalIdentity())
	id := parkFollowupTask(t, store, nodeID, "not yet running", true)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks/"+id+"/approve", nil))
	cur, err := store.Get(context.Background(), id)
	if rr.Code != http.StatusConflict || err != nil || cur.State != core.StateReview {
		t.Fatalf("incomplete approval code=%d state=%s err=%v body=%s", rr.Code, cur.State, err, rr.Body.String())
	}
}
