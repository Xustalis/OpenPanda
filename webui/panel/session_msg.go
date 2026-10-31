// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

// User messages into a live session turn — the console's half of the
// queue/inject pair, so the chat composer stops swallowing what the user types
// while the agent runs.
//
// Two delivery paths, picked by what the thread actually is:
//
//	task-backed (a classified task is moving) — the durable board note
//	   (askengine.SendTaskNote): survives a page reload, reaches the
//	   executor's round whether or not this browser stays open, and carries the
//	   same queue-vs-inject semantics the task detail page and the CLI use.
//	   Every note is individually addressable afterwards (`user:<seq>`), which
//	   is what lets the console edit, drop or promote one without re-sending.
//
//	plain answer turn — there is no task row to land a note on, so the text
//	   rides the streaming ask's steer channel (askengine.StreamCallbacks
//	   .GetSteer), which the round loop folds into its next prompt. This is
//	   what the TUI has always done. The queue is process-local and is listed
//	   alongside the board's rows so the composer's pending strip is one list.
//
// The distinction is not cosmetic: only the board path can interrupt a round,
// because the cancel handle is process-local (core/steer.go). An answer turn
// therefore reports "queued" even when the caller asked to inject, and the
// response says so instead of letting the user believe the model was cut off.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// steerCap bounds one session's steering queue. The same bound the TUI's
// channel uses: a user typing additions faster than the round loop runs is the
// only way to reach it, and refusing loudly beats growing memory behind a
// spinner.
const steerCap = 32

// memNotePrefix keeps the in-memory queue's keys out of the board's
// `user:<seq>` namespace — a console must be able to tell which path a listed
// note came from, because only the board path survives a reload.
const memNotePrefix = "mem:"

// pendingNote is one note waiting on the session's steer channel. Unlike a
// bare chan string it carries an identity and a timestamp, which is the whole
// difference between "the text went somewhere" and "the user can see it, take
// it back, or fix it".
type pendingNote struct {
	key    string
	text   string
	at     int64
	inject bool
}

// pushSteer lands one note on the queue and reports whether it fit. A full
// queue must stay visible to the caller: claiming success on a note no round
// will read is the failure mode this whole path exists to remove.
func (h *handler) pushSteer(id, text string, inject bool) (pendingNote, bool) {
	h.steerMu.Lock()
	defer h.steerMu.Unlock()
	if h.steers == nil {
		h.steers = map[string][]pendingNote{}
	}
	q := h.steers[id]
	if len(q) >= steerCap {
		return pendingNote{}, false
	}
	n := pendingNote{
		key:    memNotePrefix + strconv.Itoa(h.memSeq),
		text:   text,
		at:     time.Now().Unix(),
		inject: inject,
	}
	h.memSeq++
	h.steers[id] = append(q, n)
	return n, true
}

// drainSteer pops the oldest note for the round loop's GetSteer poll. It
// deliberately does NOT create an entry: GetSteer runs on every round of every
// turn, and a read that allocated would leave one behind for each turn the
// console ever served.
func (h *handler) drainSteer(id string) (text, key string) {
	h.steerMu.Lock()
	defer h.steerMu.Unlock()
	q := h.steers[id]
	if len(q) == 0 {
		return "", ""
	}
	n := q[0]
	text, key = n.text, n.key
	if len(q) == 1 {
		delete(h.steers, id)
	} else {
		h.steers[id] = q[1:]
	}
	return text, key
}

// listSteer snapshots everything still owed to the session, oldest first: the
// live queue a streaming turn drains, then the carry a finished turn parked. A
// carried note is not "lost" — it opens the next turn — so the strip has to
// show it, or a reload reads as the message having vanished.
func (h *handler) listSteer(id string) []pendingNote {
	h.steerMu.Lock()
	defer h.steerMu.Unlock()
	live, carried := h.steers[id], h.carry[id]
	out := make([]pendingNote, 0, len(live)+len(carried))
	out = append(out, live...)
	out = append(out, carried...)
	return out
}

// editSteer rewrites one in-memory note's text. Reports whether the key named
// a waiting note. Both lists are searched: a note the turn already parked is
// still the user's to fix before the next turn opens with it.
func (h *handler) editSteer(id, key, text string) bool {
	h.steerMu.Lock()
	defer h.steerMu.Unlock()
	for _, m := range []map[string][]pendingNote{h.steers, h.carry} {
		q := m[id]
		for i := range q {
			if q[i].key == key {
				q[i].text = text
				m[id] = q
				return true
			}
		}
	}
	return false
}

// deleteSteer drops one in-memory note, from whichever list holds it.
func (h *handler) deleteSteer(id, key string) bool {
	h.steerMu.Lock()
	defer h.steerMu.Unlock()
	for _, m := range []map[string][]pendingNote{h.steers, h.carry} {
		q := m[id]
		for i := range q {
			if q[i].key == key {
				q = append(q[:i], q[i+1:]...)
				if len(q) == 0 {
					delete(m, id)
				} else {
					m[id] = q
				}
				return true
			}
		}
	}
	return false
}

// promoteSteer marks one in-memory note as an inject. The delivery point does
// not move — the round loop reads the queue at its next boundary either way —
// so the caller still reports "queued" plus the cannot-abort caveat. It is an
// honest no-op with a name, not a promise the process cannot keep.
func (h *handler) promoteSteer(id, key string) bool {
	h.steerMu.Lock()
	defer h.steerMu.Unlock()
	for _, m := range []map[string][]pendingNote{h.steers, h.carry} {
		q := m[id]
		for i := range q {
			if q[i].key == key {
				q[i].inject = true
				m[id] = q
				return true
			}
		}
	}
	return false
}

// takeSteer removes and returns every waiting note for a session. A turn ends
// by calling this: notes no round read are not garbage, they are the next
// turn's input, so the caller parks them in the carry (see carryNotes).
func (h *handler) takeSteer(id string) []pendingNote {
	h.steerMu.Lock()
	defer h.steerMu.Unlock()
	q := h.steers[id]
	if len(q) == 0 {
		return nil
	}
	delete(h.steers, id)
	return q
}

// carryNotes parks leftover notes for the turn that follows. Appending rather
// than replacing keeps two turns' leftovers in the order they were typed: a
// turn that ends with one unread note, resumed and ending with another, must
// not lose the first.
func (h *handler) carryNotes(id string, notes []pendingNote) {
	if len(notes) == 0 {
		return
	}
	h.steerMu.Lock()
	defer h.steerMu.Unlock()
	if h.carry == nil {
		h.carry = map[string][]pendingNote{}
	}
	h.carry[id] = append(h.carry[id], notes...)
}

// takeCarry drains the parked notes: the resumed turn opens with them, and a
// prompt the user sends first absorbs them.
// hasCarry reports whether a session has notes parked for its next turn,
// without taking them. sessionAsk refuses an empty prompt before any of the
// checks that can also fail — a refusal must not drain the carry.
func (h *handler) hasCarry(id string) bool {
	h.steerMu.Lock()
	defer h.steerMu.Unlock()
	return len(h.carry[id]) > 0
}

func (h *handler) takeCarry(id string) []pendingNote {
	h.steerMu.Lock()
	defer h.steerMu.Unlock()
	q := h.carry[id]
	if len(q) == 0 {
		return nil
	}
	delete(h.carry, id)
	return q
}

// carryText joins parked notes into the single prompt a turn opens with. The
// notes were separate messages typed seconds apart; one paragraph break keeps
// them readable to the model without inventing structure the user did not type.
func carryText(notes []pendingNote) string {
	parts := make([]string, 0, len(notes))
	for _, n := range notes {
		if text := strings.TrimSpace(n.text); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// parkLeftovers moves whatever a finished turn never read into the carry and
// tells the console to open the next turn with it. Split out of sessionAsk's
// defer so the behaviour is testable without a live model: the drop this
// replaces was invisible precisely because nothing could exercise the end of a
// turn on its own.
func (h *handler) parkLeftovers(sessID string, announce func(text string, count int)) {
	left := h.takeSteer(sessID)
	if len(left) == 0 {
		return
	}
	h.carryNotes(sessID, left)
	if announce != nil {
		announce(carryText(left), len(left))
	}
}

// dropSteer releases the session's queue when its ask ends. Notes still sitting
// in it are parked in the carry by the turn that owns them (see takeSteer) —
// this is the last-resort release for paths that never reach that point (a
// deleted thread, a cancelled setup), where nothing is left to resume.
func (h *handler) dropSteer(id string) {
	h.steerMu.Lock()
	delete(h.steers, id)
	delete(h.carry, id)
	h.steerMu.Unlock()
}

// sessionStreaming reports whether a turn is live for this session — the only
// state in which a steer note can reach anything at all.
func (h *handler) sessionStreaming(id string) bool {
	h.askMu.Lock()
	defer h.askMu.Unlock()
	_, ok := h.activeAsks[id]
	return ok
}

// liveTaskFor returns the newest task a session still owes work to, or nil.
// Newest row wins (ListBySession is created_at DESC) so a thread that spawned a
// second task after the first settled still targets the current one.
func (h *handler) liveTaskFor(ctx context.Context, sessID string) *core.Task {
	if h.store == nil {
		return nil
	}
	tasks, err := h.store.ListBySession(ctx, sessID)
	if err != nil {
		return nil
	}
	for i := range tasks {
		if !taskSettled(tasks[i].State) {
			return &tasks[i]
		}
	}
	return nil
}

// sessionNote is one row of the composer's pending strip, whichever path it
// came from. Key is what the edit/drop/promote routes address.
type sessionNote struct {
	Key         string   `json:"key"`
	TaskID      string   `json:"task_id,omitempty"`
	Text        string   `json:"text"`
	Attachments []string `json:"attachments,omitempty"`
	// Mode is "queue" (waits for the next round) or "inject" (leads it).
	Mode string `json:"mode"`
	At   int64  `json:"at"`
	// Durable marks a board row: it survives a reload and a restart, unlike the
	// process-local steer queue.
	Durable bool `json:"durable"`
}

// sendSessionMessage serves POST /api/sessions/{id}/messages: type into the
// thread while the agent is working. Body {text, attachments, inject}; the
// response mirrors the task endpoint — {task_id, sender, status, warning} — so
// both surfaces phrase the outcome from the same fields.
func (h *handler) sendSessionMessage(w http.ResponseWriter, r *http.Request) {
	sessID := r.PathValue("id")
	if h.sessions == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("sessions are not configured"))
		return
	}
	var req sendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	req.Attachments = cleanAttachments(req.Attachments)
	if req.Text == "" && len(req.Attachments) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("text must not be empty"))
		return
	}
	if _, err := h.sessions.Get(sessID); err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such session"))
		return
	}

	// A live task linked to this thread takes the durable path first.
	if eng := h.currentEngine(); eng != nil {
		if t := h.liveTaskFor(r.Context(), sessID); t != nil {
			res, serr := eng.SendTaskNote(r.Context(), t.TaskID, req.Text, req.Attachments, req.Inject)
			if serr != nil {
				writeTaskMessageErr(w, serr)
				return
			}
			writeJSON(w, map[string]string{
				"task_id": t.TaskID, "sender": res.Sender,
				"status": res.Status, "warning": res.Warning,
			})
			return
		}
	}

	// No task row: a plain answer turn. Its only live channel is the steer
	// queue, and it exists only while the turn streams.
	if !h.sessionStreaming(sessID) {
		writeErr(w, http.StatusConflict,
			errors.New("no turn is running in this session — send it as a new prompt"))
		return
	}
	n, ok := h.pushSteer(sessID, req.Text, req.Inject)
	if !ok {
		writeErr(w, http.StatusTooManyRequests,
			errors.New("steering queue is full — wait for the next round"))
		return
	}
	warning := ""
	if req.Inject {
		warning = i18n.T(h.locale(), "web.session.injectNoAbort")
	}
	writeJSON(w, map[string]string{
		"task_id": "", "sender": n.key, "status": "queued", "warning": warning,
	})
}

// listSessionNotes serves GET /api/sessions/{id}/notes: everything the user
// queued or injected that no round has folded yet — board rows first (they are
// the durable ones), then the process-local steer queue in arrival order.
func (h *handler) listSessionNotes(w http.ResponseWriter, r *http.Request) {
	sessID := r.PathValue("id")
	if h.sessions == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("sessions are not configured"))
		return
	}
	if _, err := h.sessions.Get(sessID); err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such session"))
		return
	}
	notes := make([]sessionNote, 0, 8)
	// Newest live task only: a settled row's leftovers are history, and two
	// live tasks in one thread would make "which board does the composer type
	// into" ambiguous.
	if t := h.liveTaskFor(r.Context(), sessID); t != nil {
		if msgs, merr := h.store.UserNotes(r.Context(), t.TaskID); merr == nil {
			for _, m := range msgs {
				notes = append(notes, sessionNote{
					Key:         m.Sender,
					TaskID:      t.TaskID,
					Text:        core.NoteProse(m.Body),
					Attachments: m.Attachments,
					Mode:        noteMode(m.Grade),
					At:          m.SentAt,
					Durable:     true,
				})
			}
		}
	}
	for _, n := range h.listSteer(sessID) {
		notes = append(notes, sessionNote{
			Key:  n.key,
			Text: n.text,
			Mode: noteMode(gradeOf(n.inject)),
			At:   n.at,
		})
	}
	writeJSON(w, map[string]any{"session_id": sessID, "notes": notes})
}

// editSessionNote serves PATCH /api/sessions/{id}/notes/{key}: fix a note
// before a round reads it. The board path rewrites the slot in place; the
// steer-queue path rewrites the buffered text.
func (h *handler) editSessionNote(w http.ResponseWriter, r *http.Request) {
	sessID, key := r.PathValue("id"), r.PathValue("key")
	var req struct {
		Text        string   `json:"text"`
		Attachments []string `json:"attachments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	req.Attachments = cleanAttachments(req.Attachments)
	if req.Text == "" && len(req.Attachments) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("text must not be empty"))
		return
	}
	if strings.HasPrefix(key, memNotePrefix) {
		if !h.editSteer(sessID, key, req.Text) {
			writeErr(w, http.StatusNotFound, errors.New("no such note"))
			return
		}
		writeJSON(w, map[string]string{"key": key, "status": "edited"})
		return
	}
	eng := h.currentEngine()
	if eng == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("editing a board note requires the ask engine"))
		return
	}
	taskID := h.noteTaskID(r.Context(), sessID, key)
	if taskID == "" {
		writeErr(w, http.StatusNotFound, errors.New("no such note"))
		return
	}
	if err := eng.EditTaskNote(r.Context(), taskID, key, req.Text, req.Attachments); err != nil {
		writeTaskMessageErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"key": key, "task_id": taskID, "status": "edited"})
}

// deleteSessionNote serves DELETE /api/sessions/{id}/notes/{key}.
func (h *handler) deleteSessionNote(w http.ResponseWriter, r *http.Request) {
	sessID, key := r.PathValue("id"), r.PathValue("key")
	if strings.HasPrefix(key, memNotePrefix) {
		if !h.deleteSteer(sessID, key) {
			writeErr(w, http.StatusNotFound, errors.New("no such note"))
			return
		}
		writeJSON(w, map[string]string{"key": key, "status": "dropped"})
		return
	}
	eng := h.currentEngine()
	if eng == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("dropping a board note requires the ask engine"))
		return
	}
	taskID := h.noteTaskID(r.Context(), sessID, key)
	if taskID == "" {
		writeErr(w, http.StatusNotFound, errors.New("no such note"))
		return
	}
	if err := eng.DeleteTaskNote(r.Context(), taskID, key); err != nil {
		writeTaskMessageErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"key": key, "task_id": taskID, "status": "dropped"})
}

// promoteSessionNote serves POST /api/sessions/{id}/notes/{key}/promote: turn a
// queued note into an inject. The note is escalated in place — a second post
// would make the round read the same words twice.
func (h *handler) promoteSessionNote(w http.ResponseWriter, r *http.Request) {
	sessID, key := r.PathValue("id"), r.PathValue("key")
	if strings.HasPrefix(key, memNotePrefix) {
		if !h.promoteSteer(sessID, key) {
			writeErr(w, http.StatusNotFound, errors.New("no such note"))
			return
		}
		// The delivery point does not move for a plain answer turn, so the
		// honest answer is "queued" plus the cannot-abort note.
		writeJSON(w, map[string]string{
			"key": key, "status": "queued",
			"warning": i18n.T(h.locale(), "web.session.injectNoAbort"),
		})
		return
	}
	eng := h.currentEngine()
	if eng == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("promoting a board note requires the ask engine"))
		return
	}
	taskID := h.noteTaskID(r.Context(), sessID, key)
	if taskID == "" {
		writeErr(w, http.StatusNotFound, errors.New("no such note"))
		return
	}
	status, warning, err := eng.PromoteTaskNote(r.Context(), taskID, key)
	if err != nil {
		writeTaskMessageErr(w, err)
		return
	}
	writeJSON(w, map[string]string{
		"key": key, "task_id": taskID, "status": status, "warning": warning,
	})
}

// noteTaskID resolves the task a board key belongs to. The key is only unique
// within one board, so the task comes from the session's live row rather than
// from the key itself. A key that names no pending note on that row answers "".
func (h *handler) noteTaskID(ctx context.Context, sessID, key string) string {
	t := h.liveTaskFor(ctx, sessID)
	if t == nil {
		return ""
	}
	notes, err := h.store.UserNotes(ctx, t.TaskID)
	if err != nil {
		return ""
	}
	for _, n := range notes {
		if n.Sender == key {
			return t.TaskID
		}
	}
	return ""
}

// cleanAttachments normalizes the attachment list: trimmed, non-empty, and
// de-duplicated while keeping the user's order.
func cleanAttachments(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, a := range in {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if _, dup := seen[a]; dup {
			continue
		}
		seen[a] = struct{}{}
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// noteMode maps a board grade onto the strip's two words.
func noteMode(grade string) string {
	if grade == core.MsgGradeSteer {
		return "inject"
	}
	return "queue"
}

// gradeOf is noteMode's inverse for the in-memory queue, which stores the flag.
func gradeOf(inject bool) string {
	if inject {
		return core.MsgGradeSteer
	}
	return core.MsgGradeStatus
}

// locale resolves the console's language for the rare response that carries
// prose rather than a structured field. Same source and fallback the task
// endpoints use (see sendTaskMessage's no-consumer warning).
func (h *handler) locale() i18n.Locale {
	loc := i18n.Locale("")
	h.readCfg(func(c *config.Config) { loc = i18n.Parse(c.UI.Locale) })
	return loc
}

// taskSettled reports whether a state ends the row's life — the same set the
// detail page uses to hide its send box.
func taskSettled(state string) bool {
	switch state {
	case core.StateDone, core.StateFailed, core.StateCancelled, core.StateExpired:
		return true
	}
	return false
}
