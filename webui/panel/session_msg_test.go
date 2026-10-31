// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// newSessionMsgHandler mounts only the endpoint under test, over a real
// ServeMux (the handler reads r.PathValue, so the pattern has to be routed).
// New() returns the fully-routed console as an http.Handler, which hides the
// in-memory steering queue this suite has to inspect — it has no HTTP read
// side by design, since only the round loop's GetSteer poll drains it.
func newSessionMsgHandler(store *core.TaskStore, eng *askengine.Engine, sess *sessions.Store) (*handler, http.Handler) {
	h := &handler{
		store:      store,
		engine:     eng,
		sessions:   sess,
		cfgMu:      &sync.RWMutex{},
		serviceCtx: context.Background(),
		activeAsks: map[string]sessionOperation{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions/{id}/messages", h.sendSessionMessage)
	mux.HandleFunc("GET /api/sessions/{id}/notes", h.listSessionNotes)
	mux.HandleFunc("PATCH /api/sessions/{id}/notes/{key}", h.editSessionNote)
	mux.HandleFunc("DELETE /api/sessions/{id}/notes/{key}", h.deleteSessionNote)
	mux.HandleFunc("POST /api/sessions/{id}/notes/{key}/promote", h.promoteSessionNote)
	return h, mux
}

// decodeNotes reads the pending-strip payload.
func decodeNotes(t *testing.T, rr *httptest.ResponseRecorder) []sessionNote {
	t.Helper()
	var res struct {
		Notes []sessionNote `json:"notes"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode notes %q: %v", rr.Body.String(), err)
	}
	return res.Notes
}

// getNotes lists a session's pending notes.
func getNotes(t *testing.T, h http.Handler, sessionID string) []sessionNote {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID+"/notes", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("list notes: %d %s", rr.Code, rr.Body.String())
	}
	return decodeNotes(t, rr)
}

// noteReq drives one of the three per-note routes.
func noteReq(t *testing.T, h http.Handler, method, sessionID, key, suffix, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(method, "/api/sessions/"+sessionID+"/notes/"+key+suffix, rdr))
	return rr
}

// decodeSessionMessage reads the endpoint's three-field response.
func decodeSessionMessage(t *testing.T, rr *httptest.ResponseRecorder) (taskID, status, warning string) {
	t.Helper()
	var res map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode response %q: %v", rr.Body.String(), err)
	}
	return res["task_id"], res["status"], res["warning"]
}

// postSessionMessage posts one message into a session.
func postSessionMessage(t *testing.T, h http.Handler, sessionID, body string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/messages",
		strings.NewReader(body))
	h.ServeHTTP(rr, req)
	return rr
}

// TestSessionMessageSteersLiveTurn covers the task-less path: an answer turn
// has no board to land a note on, so the text rides the streaming ask's steer
// queue — the same channel the TUI uses. The queue only exists while a turn is
// live, which is why the test registers an active ask first.
func TestSessionMessageSteersLiveTurn(t *testing.T) {
	sessStore := sessions.NewStore(t.TempDir())
	sess, err := sessStore.Create("steer me")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	h, mux := newSessionMsgHandler(newTestStore(t), nil, sessStore)

	// A live turn: what sessionAsk registers for the duration of the ask.
	h.askMu.Lock()
	h.activeAsks[sess.ID] = sessionOperation{operationID: "op-1", generation: 1}
	h.askMu.Unlock()

	rr := postSessionMessage(t, mux, sess.ID, `{"text":"  also check the tests  "}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("send: %d %s", rr.Code, rr.Body.String())
	}
	taskID, status, warning := decodeSessionMessage(t, rr)
	if taskID != "" || status != "queued" || warning != "" {
		t.Fatalf("response = task:%q status:%q warn:%q", taskID, status, warning)
	}
	// The round loop's GetSteer poll is what drains it — assert the text landed
	// where that poll reads, trimmed.
	if got, key := h.drainSteer(sess.ID); got != "also check the tests" || key == "" {
		t.Fatalf("steer queue delivered %q (key %q)", got, key)
	}
	if got, _ := h.drainSteer(sess.ID); got != "" {
		t.Fatalf("steer queue replayed %q", got)
	}

	// inject on an answer turn cannot abort the model call, and says so instead
	// of letting the console claim an interruption that never happened.
	rr = postSessionMessage(t, mux, sess.ID, `{"text":"stop and redo","inject":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("inject: %d %s", rr.Code, rr.Body.String())
	}
	_, status, warning = decodeSessionMessage(t, rr)
	if status != "queued" || warning == "" {
		t.Fatalf("inject on an answer turn = status:%q warn:%q, want a queued warning", status, warning)
	}
}

// TestSessionMessageWithoutTurnRefuses pins the honest refusal: with no live
// turn there is nothing to steer, and accepting the note would park it in a
// queue nobody will ever drain.
func TestSessionMessageWithoutTurnRefuses(t *testing.T) {
	sessStore := sessions.NewStore(t.TempDir())
	sess, err := sessStore.Create("idle thread")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	_, mux := newSessionMsgHandler(newTestStore(t), nil, sessStore)
	rr := postSessionMessage(t, mux, sess.ID, `{"text":"hello"}`)
	if rr.Code != http.StatusConflict {
		t.Fatalf("send with no turn: %d %s", rr.Code, rr.Body.String())
	}
}

// TestSessionMessageTargetsLiveTask covers the durable path: a thread that
// spawned a classified task takes the board note instead of the in-memory
// queue, so the message survives a reload and reaches the executor's round.
// The task is parked in review (a live state the executor will resume from), so
// it cannot race the scheduler to a terminal one mid-test.
func TestSessionMessageTargetsLiveTask(t *testing.T) {
	engine, _ := newApprovalQueueEngine(t)
	store := engine.TaskStore()
	sessStore := sessions.NewStore(t.TempDir())
	h := New(Deps{
		Store: store, Engine: engine, Sessions: sessStore,
		StaticDir: t.TempDir(), Token: testToken,
	})
	_, mux := newSessionMsgHandler(store, engine, sessStore)

	// The board-created task needs the full console (it links a session), so
	// create it through New's routes rather than the one-route mux.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks", strings.NewReader(
		`{"title":"parked task","prompt":"do it","requires":["test:once"],"authorize":false}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("create task: %d %s", rr.Code, rr.Body.String())
	}
	var created struct {
		TaskID    string `json:"task_id"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.TaskID == "" || created.SessionID == "" {
		t.Fatalf("create result = %+v", created)
	}
	// Wait for the row to park: a settled row would send this test down the
	// steer path and prove nothing about the board.
	deadline := time.Now().Add(5 * time.Second)
	for {
		task, err := store.Get(context.Background(), created.TaskID)
		if err == nil && task.State == core.StateReview {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task did not reach review: %+v, %v", task, err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The thread's live task is the target — not the steer queue.
	rr = postSessionMessage(t, mux, created.SessionID, `{"text":"note for the task"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("send to task: %d %s", rr.Code, rr.Body.String())
	}
	taskID, status, _ := decodeSessionMessage(t, rr)
	if taskID != created.TaskID || status != "queued" {
		t.Fatalf("response = task:%q status:%q, want the linked task", taskID, status)
	}
	msgs, err := store.TaskMessages(context.Background(), "task:"+created.TaskID)
	if err != nil {
		t.Fatalf("read board: %v", err)
	}
	var got bool
	for _, m := range msgs {
		if m.Body == "note for the task" && m.SenderLabel == "user" {
			got = true
			// Unread on arrival: the round has not folded it yet, and the
			// console draws "waiting for the next round" off exactly this.
			if m.ConsumedAt != 0 {
				t.Fatalf("fresh note already marked consumed: %+v", m)
			}
		}
	}
	if !got {
		t.Fatalf("board has no user note: %+v", msgs)
	}
}

// parkedTaskThread builds the shape the pending strip actually reads: a live
// (parked) task linked to a session, plus the one-route mux. Parked rather than
// running so it cannot settle mid-test.
func parkedTaskThread(t *testing.T) (*core.TaskStore, http.Handler, string, string) {
	t.Helper()
	engine, _ := newApprovalQueueEngine(t)
	store := engine.TaskStore()
	sessStore := sessions.NewStore(t.TempDir())
	full := New(Deps{
		Store: store, Engine: engine, Sessions: sessStore,
		StaticDir: t.TempDir(), Token: testToken,
	})
	_, mux := newSessionMsgHandler(store, engine, sessStore)

	rr := httptest.NewRecorder()
	full.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks", strings.NewReader(
		`{"title":"parked task","prompt":"do it","requires":["test:once"],"authorize":false}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("create task: %d %s", rr.Code, rr.Body.String())
	}
	var created struct {
		TaskID    string `json:"task_id"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		task, err := store.Get(context.Background(), created.TaskID)
		if err == nil && task.State == core.StateReview {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task did not reach review: %+v, %v", task, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return store, mux, created.TaskID, created.SessionID
}

// TestSessionNotesEditAndDrop walks the pending strip's two maintenance verbs on
// the durable path: the note is listed with its prose (not the attachment
// header the agent reads), an edit rewrites that same row, and a drop removes it.
func TestSessionNotesEditAndDrop(t *testing.T) {
	store, mux, taskID, sessID := parkedTaskThread(t)

	rr := postSessionMessage(t, mux, sessID,
		`{"text":"look at the shot","attachments":["uploads/shot.png"]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("send: %d %s", rr.Code, rr.Body.String())
	}
	_, status, _ := decodeSessionMessage(t, rr)
	if status != "queued" {
		t.Fatalf("status = %q", status)
	}

	notes := getNotes(t, mux, sessID)
	if len(notes) != 1 {
		t.Fatalf("notes = %+v, want 1", notes)
	}
	n := notes[0]
	if n.Text != "look at the shot" {
		t.Fatalf("note text = %q — the attachment block leaked into the prose", n.Text)
	}
	if !n.Durable || n.Mode != "queue" || n.TaskID != taskID {
		t.Fatalf("note = %+v", n)
	}
	if len(n.Attachments) != 1 || n.Attachments[0] != "uploads/shot.png" {
		t.Fatalf("attachments = %+v", n.Attachments)
	}

	rr = noteReq(t, mux, http.MethodPatch, sessID, n.Key, "", `{"text":"look at both shots"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", rr.Code, rr.Body.String())
	}
	notes = getNotes(t, mux, sessID)
	if len(notes) != 1 || notes[0].Text != "look at both shots" {
		t.Fatalf("after edit: %+v", notes)
	}
	if len(notes[0].Attachments) != 0 {
		t.Fatalf("edit should replace the attachment list, got %+v", notes[0].Attachments)
	}

	rr = noteReq(t, mux, http.MethodDelete, sessID, n.Key, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("drop: %d %s", rr.Code, rr.Body.String())
	}
	if notes := getNotes(t, mux, sessID); len(notes) != 0 {
		t.Fatalf("note survived the drop: %+v", notes)
	}
	msgs, _ := store.TaskMessages(context.Background(), "task:"+taskID)
	if len(msgs) != 0 {
		t.Fatalf("board still holds rows: %+v", msgs)
	}
}

// TestSessionNotesPromoteInPlace is the strip's ▲: one row before and after, so
// the round cannot read the same words twice. No live round runs here, so the
// honest answer is "queued", not "interrupted".
func TestSessionNotesPromoteInPlace(t *testing.T) {
	store, mux, taskID, sessID := parkedTaskThread(t)
	if rr := postSessionMessage(t, mux, sessID, `{"text":"use tabs"}`); rr.Code != http.StatusOK {
		t.Fatalf("send: %d %s", rr.Code, rr.Body.String())
	}
	notes := getNotes(t, mux, sessID)
	if len(notes) != 1 {
		t.Fatalf("notes = %+v", notes)
	}

	rr := noteReq(t, mux, http.MethodPost, sessID, notes[0].Key, "/promote", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("promote: %d %s", rr.Code, rr.Body.String())
	}
	var res map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode promote: %v", err)
	}
	if res["status"] != "queued" {
		t.Fatalf("promote status = %q, want queued (nothing was running)", res["status"])
	}
	msgs, _ := store.TaskMessages(context.Background(), "task:"+taskID)
	if len(msgs) != 1 {
		t.Fatalf("promote posted a second row: %+v", msgs)
	}
	if msgs[0].Grade != core.MsgGradeSteer {
		t.Fatalf("grade = %q, want steer", msgs[0].Grade)
	}
	notes = getNotes(t, mux, sessID)
	if len(notes) != 1 || notes[0].Mode != "inject" {
		t.Fatalf("strip after promote = %+v", notes)
	}
}

// TestLeftoverSteerNotesBecomeTheNextTurn pins the fix for the silent drop: a
// note typed into a turn's last round has no later boundary to land on, and the
// queue used to be thrown away with the turn — the composer printed "queued"
// and the words never reached the model. They are parked instead, and the turn
// that follows opens with them exactly once.
func TestLeftoverSteerNotesBecomeTheNextTurn(t *testing.T) {
	sessStore := sessions.NewStore(t.TempDir())
	h, _ := newSessionMsgHandler(newTestStore(t), nil, sessStore)
	const sid = "leftover-thread"

	if _, ok := h.pushSteer(sid, "check the tests", false); !ok {
		t.Fatal("first note refused")
	}
	if _, ok := h.pushSteer(sid, "  and the docs  ", true); !ok {
		t.Fatal("second note refused")
	}

	// What sessionAsk's defer does when the turn ends with notes unread.
	left := h.takeSteer(sid)
	if len(left) != 2 {
		t.Fatalf("takeSteer = %d notes, want 2", len(left))
	}
	h.carryNotes(sid, left)
	if got, _ := h.drainSteer(sid); got != "" {
		t.Fatalf("queue still delivers %q after the turn ended", got)
	}

	// The continuation turn opens with both, in the order they were typed, and
	// the parked copy is consumed — a second drain would deliver the same words
	// to the model twice.
	carried := h.takeCarry(sid)
	if got := carryText(carried); got != "check the tests\n\nand the docs" {
		t.Fatalf("carryText = %q", got)
	}
	if again := h.takeCarry(sid); len(again) != 0 {
		t.Fatalf("carry replayed %+v", again)
	}
}

// TestDropSteerReleasesTheCarry covers the thread going away: a deleted session
// has no turn left to open, so its waiting notes and its parked leftovers go
// with it instead of leaking a map entry per deleted thread.
func TestDropSteerReleasesTheCarry(t *testing.T) {
	sessStore := sessions.NewStore(t.TempDir())
	h, _ := newSessionMsgHandler(newTestStore(t), nil, sessStore)
	const sid = "deleted-thread"

	h.pushSteer(sid, "waiting", false)
	h.carryNotes(sid, h.takeSteer(sid))
	h.dropSteer(sid)

	if got := h.listSteer(sid); len(got) != 0 {
		t.Fatalf("steer queue survived the drop: %+v", got)
	}
	if got := h.takeCarry(sid); len(got) != 0 {
		t.Fatalf("carry survived the drop: %+v", got)
	}
}

// TestSessionAskOpensWithCarriedNotes covers the server half of the
// continuation: a turn that starts with parked notes takes them as its prompt
// even when the console's request carries no text of its own (resume_queued),
// and the parked copy is consumed — the words must not reach the model twice.
func TestSessionAskOpensWithCarriedNotes(t *testing.T) {
	engine, _ := newApprovalQueueEngine(t)
	store := engine.TaskStore()
	sessStore := sessions.NewStore(t.TempDir())
	// Built directly rather than through New: the carry is process-local state
	// on the handler, and New only hands back the routed http.Handler. The
	// fields mirror what New seeds for the same reason newSessionMsgHandler
	// does — this suite drives one route, not the console.
	h := &handler{
		store: store, engine: engine, sessions: sessStore,
		cfgMu:      &sync.RWMutex{},
		serviceCtx: context.Background(),
		activeAsks: map[string]sessionOperation{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions/{id}/ask", h.sessionAsk)
	sess, err := sessStore.Create("continuation")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	// What the previous turn's defer leaves behind when its last round never
	// reached the queue.
	h.carryNotes(sess.ID, []pendingNote{
		{key: memNotePrefix + "0", text: "check the tests", at: time.Now().Unix()},
		{key: memNotePrefix + "1", text: "and the docs", at: time.Now().Unix()},
	})

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, authedReq(http.MethodPost, "/api/sessions/"+sess.ID+"/ask",
		strings.NewReader(`{"prompt":"","resume_queued":true}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("ask: %d %s", rr.Code, rr.Body.String())
	}

	// The user turn on record is the parked text, not the empty prompt: the
	// continuation replayed what the user actually typed.
	got, err := sessStore.Get(sess.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if len(got.Turns) == 0 || got.Turns[0].Text != "check the tests\n\nand the docs" {
		t.Fatalf("first turn = %+v, want the carried notes", got.Turns)
	}
	if left := h.takeCarry(sess.ID); len(left) != 0 {
		t.Fatalf("carry replayed %+v", left)
	}
}

// TestParkLeftoversAnnouncesTheContinuation is the whole bug in one test: a
// note typed into a turn whose rounds are already past their steer poll used to
// be dropped when the turn ended — "queued" on screen, never read, gone. The
// turn's defer parks it and announces it, which is what the console turns into
// the continuation turn.
func TestParkLeftoversAnnouncesTheContinuation(t *testing.T) {
	h, _ := newSessionMsgHandler(newTestStore(t), nil, sessions.NewStore(t.TempDir()))
	const sid = "leftover-thread"

	h.pushSteer(sid, "check the tests", false)
	h.pushSteer(sid, "  and the docs  ", true)

	var gotText string
	var gotCount int
	h.parkLeftovers(sid, func(text string, count int) {
		gotText, gotCount = text, count
	})
	if gotCount != 2 || gotText != "check the tests\n\nand the docs" {
		t.Fatalf("announce = %q (%d notes), want both in arrival order", gotText, gotCount)
	}
	if left := h.takeCarry(sid); len(left) != 2 {
		t.Fatalf("carry = %+v, want the unread notes parked", left)
	}
	if got, _ := h.drainSteer(sid); got != "" {
		t.Fatalf("leftover stayed in the live queue: %q", got)
	}

	// An empty turn announces nothing: the console must not be told to open a
	// continuation that has no words in it.
	h.parkLeftovers(sid, func(string, int) { t.Fatal("announced an empty turn") })
}

// TestSessionNotesSteerQueueCRUD covers the process-local queue a task-less
// answer turn uses: the same strip, the same three verbs, no board row behind
// it. Promote cannot abort anything here, and says so.
func TestSessionNotesSteerQueueCRUD(t *testing.T) {
	sessStore := sessions.NewStore(t.TempDir())
	sess, err := sessStore.Create("answer turn")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	h, mux := newSessionMsgHandler(newTestStore(t), nil, sessStore)
	h.askMu.Lock()
	h.activeAsks[sess.ID] = sessionOperation{operationID: "op-1", generation: 1}
	h.askMu.Unlock()

	if rr := postSessionMessage(t, mux, sess.ID, `{"text":"faster please"}`); rr.Code != http.StatusOK {
		t.Fatalf("send: %d %s", rr.Code, rr.Body.String())
	}
	notes := getNotes(t, mux, sess.ID)
	if len(notes) != 1 || notes[0].Durable || notes[0].Mode != "queue" {
		t.Fatalf("notes = %+v", notes)
	}
	if !strings.HasPrefix(notes[0].Key, memNotePrefix) {
		t.Fatalf("key = %q, want a mem: key", notes[0].Key)
	}

	if rr := noteReq(t, mux, http.MethodPatch, sess.ID, notes[0].Key, "", `{"text":"slower please"}`); rr.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", rr.Code, rr.Body.String())
	}
	if got := getNotes(t, mux, sess.ID); len(got) != 1 || got[0].Text != "slower please" {
		t.Fatalf("after edit: %+v", got)
	}

	rr := noteReq(t, mux, http.MethodPost, sess.ID, notes[0].Key, "/promote", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("promote: %d %s", rr.Code, rr.Body.String())
	}
	var res map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode promote: %v", err)
	}
	if res["status"] != "queued" || res["warning"] == "" {
		t.Fatalf("promote on a queued-only channel = %+v, want queued + caveat", res)
	}
	if got := getNotes(t, mux, sess.ID); len(got) != 1 || got[0].Mode != "inject" {
		t.Fatalf("after promote: %+v", got)
	}

	if rr := noteReq(t, mux, http.MethodDelete, sess.ID, notes[0].Key, "", ""); rr.Code != http.StatusOK {
		t.Fatalf("drop: %d %s", rr.Code, rr.Body.String())
	}
	if got := getNotes(t, mux, sess.ID); len(got) != 0 {
		t.Fatalf("note survived the drop: %+v", got)
	}
	if got, _ := h.drainSteer(sess.ID); got != "" {
		t.Fatalf("dropped note still reaches the round: %q", got)
	}
}
