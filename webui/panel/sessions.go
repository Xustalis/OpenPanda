// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/guard"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	"github.com/Xustalis/OpenPanda/internal/util"
)

// ---- Session CRUD ----

// sessionWorktrees resolves the repository a session's worktree belongs to.
// The recorded worktree path is authoritative; a session created before this
// fix (or one whose project changed) falls back to its project's work dir.
// The handler-level Worktrees remains the last resort for a node whose
// storage.work_path is itself a repository.
func (h *handler) sessionWorktrees(sess *sessions.Session) *sessions.Worktrees {
	if sess == nil {
		return nil
	}
	if sess.Worktree != "" {
		// worktreeDir layout: <repo>/.openpanda/worktrees/<id>.
		repo := filepath.Dir(filepath.Dir(filepath.Dir(sess.Worktree)))
		if wt, err := sessions.OpenWorktrees(repo); err == nil {
			return wt
		}
	}
	// An older session has no recorded worktree. Only resolve a repository
	// when this id's worktree directory actually exists there; otherwise the
	// handler answers "no git worktree" instead of guessing.
	if sess.Project != "" && h.projectStore != nil {
		if p, err := h.projectStore.Get(sess.Project); err == nil {
			if dir := p.UsableWorkDir(); dir != "" {
				if wt, err := sessions.OpenWorktrees(dir); err == nil {
					if _, statErr := os.Stat(wt.Path(sess.ID)); statErr == nil {
						return wt
					}
				}
			}
		}
	}
	if h.worktrees != nil {
		if _, statErr := os.Stat(h.worktrees.Path(sess.ID)); statErr == nil {
			return h.worktrees
		}
	}
	return nil
}

// newSessionWorktrees picks the base for a session about to be created: the
// project's own work dir when it is a repository, otherwise the handler's
// configured fallback.
func (h *handler) newSessionWorktrees(project string) *sessions.Worktrees {
	if project != "" && h.projectStore != nil {
		if p, err := h.projectStore.Get(project); err == nil {
			if dir := p.UsableWorkDir(); dir != "" {
				if wt, err := sessions.OpenWorktrees(dir); err == nil {
					return wt
				}
			}
		}
	}
	return h.worktrees
}

// cleanTeam trims, dedupes and validates a roster off the wire. The rule
// (including the 16-agent ceiling) lives in sessions.CleanTeam so the console,
// the REPL/TUI /team command and the bare conversation's roster file all
// validate identically.
func cleanTeam(in []string) ([]string, error) {
	return sessions.CleanTeam(in)
}

// listSessions serves GET /api/sessions — chat sessions, optionally filtered by ?project=.
func (h *handler) listSessions(w http.ResponseWriter, r *http.Request) {
	var list []*sessions.Session
	var err error
	if projectParam, ok := r.URL.Query()["project"]; ok && len(projectParam) > 0 {
		list, err = h.sessions.ListByProject(projectParam[0])
	} else {
		list, err = h.sessions.List()
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("list sessions failed"))
		return
	}
	if list == nil {
		list = []*sessions.Session{}
	}
	// The rail renders titles, not transcripts. Side conversations are the
	// biggest thing a session file carries and the list has no use for them —
	// with several chats per thread they would dominate the payload.
	for _, sess := range list {
		sess.SideChats = nil
		sess.SideTurns = nil
	}
	writeJSON(w, list)
}

// createSession serves POST /api/sessions — starts a session, carving its git
// worktree when the work path is a repository (isolation is best-effort: a
// non-repo work path still gets a session, just without a worktree). If
// project is not specified, it inherits the active project if set.
func (h *handler) createSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title   string   `json:"title"`
		Project string   `json:"project"`
		Team    []string `json:"team"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
			return
		}
	}
	project := strings.TrimSpace(req.Project)
	if project == "" && h.projectStore != nil {
		if act, err := h.projectStore.Active(); err == nil && act != "" {
			project = act
		}
	}
	team, err := cleanTeam(req.Team)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	sess, err := h.sessions.Create(strings.TrimSpace(req.Title), project)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// The roster is written after creation so the create path stays a single
	// store call: a thread born with a team is a thread created and then told
	// who works on it, which is also how the composer's later edits arrive.
	if len(team) > 0 {
		if err := h.sessions.SetTeam(sess.ID, team); err == nil {
			sess, _ = h.sessions.Get(sess.ID)
		}
	}
	if wt := h.newSessionWorktrees(project); wt != nil {
		if path, err := wt.Ensure(r.Context(), sess.ID); err == nil {
			_ = h.sessions.SetWorktree(sess.ID, path, sessions.Branch(sess.ID))
			sess, _ = h.sessions.Get(sess.ID)
		}
	}
	writeJSON(w, sess)
}

// forkSession serves POST /api/sessions/{id}/fork — splits the thread at a
// turn boundary into a child session. Body: {"at": N} (0/absent copies the
// whole thread). In a repository the child's worktree branches off the
// parent's branch so it inherits the parent's code state.
func (h *handler) forkSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		At int `json:"at"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
			return
		}
	}
	parent, err := h.sessions.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such session"))
		return
	}
	child, err := h.sessions.Fork(parent.ID, req.At)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if wt := h.sessionWorktrees(parent); wt != nil {
		base := "HEAD"
		if parent.Branch != "" {
			base = parent.Branch
		}
		if path, err := wt.EnsureFrom(r.Context(), child.ID, base); err == nil {
			_ = h.sessions.SetWorktree(child.ID, path, sessions.Branch(child.ID))
			child, _ = h.sessions.Get(child.ID)
		}
	}
	writeJSON(w, child)
}

// getSession serves GET /api/sessions/{id}.
func (h *handler) getSession(w http.ResponseWriter, r *http.Request) {
	sess, err := h.sessions.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such session"))
		return
	}
	// The thread's own transcript is the payload here; the conversations
	// beside it have their own read surface (/side) and would otherwise be
	// shipped twice on every open.
	sess.SideChats = nil
	sess.SideTurns = nil
	writeJSON(w, sess)
}

// deleteSession serves DELETE /api/sessions/{id} — removes the worktree (and
// its branch) plus the stored thread.
func (h *handler) deleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// The thread is going away: its waiting notes and any parked carry have no
	// turn left to open, so release them with it instead of leaking a map entry
	// per deleted thread.
	h.dropSteer(id)
	if sess, gerr := h.sessions.Get(id); gerr == nil {
		if wt := h.sessionWorktrees(sess); wt != nil {
			_ = wt.Remove(r.Context(), id)
		}
	}
	h.removeSessionUploads(id)
	if err := h.sessions.Delete(id); err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such session"))
		return
	}
	writeJSON(w, map[string]string{"id": id, "status": "deleted"})
}

type patchSessionRequest struct {
	Title   *string `json:"title,omitempty"`
	Project *string `json:"project,omitempty"`
	Pinned  *bool   `json:"pinned,omitempty"`
	// Team replaces the thread's agent roster. A pointer to a slice so
	// "absent" (leave the roster alone) and "empty" (clear it — no
	// restriction) stay distinguishable; the console sends [] to clear.
	Team *[]string `json:"team,omitempty"`
	// PermissionMode replaces the thread's permission policy (auto, manual,
	// edits, plan, bypass). A pointer so "absent" (leave it alone) and ""
	// (back to auto) stay distinguishable.
	PermissionMode *string `json:"permission_mode,omitempty"`
}

// patchSession serves PATCH /api/sessions/{id} — updates title, project
// association, the pinned flag, or the agent roster.
func (h *handler) patchSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if h.sessions == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("sessions store not configured"))
		return
	}
	var req patchSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	_, err := h.sessions.Get(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such session"))
		return
	}
	if req.Title != nil {
		if err := h.sessions.SetTitle(id, *req.Title); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if req.Project != nil {
		if err := h.sessions.SetProject(id, *req.Project); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if req.Pinned != nil {
		if err := h.sessions.SetPinned(id, *req.Pinned); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if req.Team != nil {
		team, err := cleanTeam(*req.Team)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := h.sessions.SetTeam(id, team); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if req.PermissionMode != nil {
		mode := defense.NormalizeMode(*req.PermissionMode)
		// NormalizeMode maps "" to auto, so an explicit clear works; anything
		// else the caller sent must be a real policy, not a typo.
		if *req.PermissionMode != "" && !defense.ValidMode(*req.PermissionMode) {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("unknown permission mode %q", *req.PermissionMode))
			return
		}
		if err := h.sessions.SetPermissionMode(id, mode); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	sess, _ := h.sessions.Get(id)
	writeJSON(w, sess)
}

// bulkDeleteSessionRequest is the body of POST /api/sessions/bulk-delete.
type bulkDeleteSessionRequest struct {
	IDs []string `json:"ids"`
}

// bulkDeleteSession serves POST /api/sessions/bulk-delete — one round trip
// for the "select N threads, delete" rail flow. Each id is independent:
// worktree+branch cleanup and the stored thread removal are best-effort per
// id, and the reply reports the outcome per id so the console can keep the
// failures selected instead of pretending they are gone.
func (h *handler) bulkDeleteSession(w http.ResponseWriter, r *http.Request) {
	if h.sessions == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("sessions store not configured"))
		return
	}
	var req bulkDeleteSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	if len(req.IDs) == 0 || len(req.IDs) > 512 {
		writeErr(w, http.StatusBadRequest, errors.New("ids must contain 1-512 session ids"))
		return
	}
	type failure struct {
		ID    string `json:"id"`
		Error string `json:"error"`
	}
	deleted := make([]string, 0, len(req.IDs))
	var failed []failure
	seen := map[string]bool{}
	for _, raw := range req.IDs {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if sess, gerr := h.sessions.Get(id); gerr == nil {
			if wt := h.sessionWorktrees(sess); wt != nil {
				_ = wt.Remove(r.Context(), id)
			}
		}
		h.removeSessionUploads(id)
		if err := h.sessions.Delete(id); err != nil {
			failed = append(failed, failure{ID: id, Error: err.Error()})
			continue
		}
		deleted = append(deleted, id)
	}
	if failed == nil {
		failed = []failure{}
	}
	writeJSON(w, map[string]any{"deleted": deleted, "failed": failed})
}

// ---- Worktree diff & merge ----

// sessionDiff serves GET /api/sessions/{id}/diff — the session's worktree
// changes (porcelain status per file) plus a capped unified patch and the
// merge-preview stat roll-up (files/+/-, untracked, sensitive & binary flags),
// so the web can show "what did this session do" before deciding to merge.
func (h *handler) sessionDiff(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := h.sessions.Get(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such session"))
		return
	}
	wt := h.sessionWorktrees(sess)
	if wt == nil {
		writeErr(w, http.StatusBadRequest, errors.New("session has no git worktree"))
		return
	}
	changes, err := wt.Status(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	patch, err := wt.Diff(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if changes == nil {
		changes = []sessions.Change{}
	}
	// Stat is best-effort alongside the patch it summarizes — a stat failure
	// should not blind the whole diff view.
	stat, _ := wt.Stat(r.Context(), id)
	writeJSON(w, map[string]any{
		"id":      id,
		"branch":  sessions.Branch(id),
		"changes": changes,
		"patch":   patch,
		"stat":    stat,
	})
}

// sessionRevert serves POST /api/sessions/{id}/revert — undoes a recorded
// session merge, the web equivalent of `panda session revert`. A dirty
// checkout answers 409 (the rollback would entangle uncommitted work); a
// session with no recorded merge answers 400.
func (h *handler) sessionRevert(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, serr := h.sessions.Get(id)
	if serr != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such session"))
		return
	}
	wt := h.sessionWorktrees(sess)
	if wt == nil {
		writeErr(w, http.StatusBadRequest, errors.New("session has no git worktree"))
		return
	}
	subject, err := wt.Revert(r.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, sessions.ErrRevertDirty):
			writeErr(w, http.StatusConflict, err)
		case errors.Is(err, sessions.ErrNotFound):
			writeErr(w, http.StatusNotFound, err)
		case errors.Is(err, sessions.ErrNothingToRevert):
			writeErr(w, http.StatusBadRequest, err)
		default:
			writeErr(w, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(w, map[string]any{"id": id, "reverted": true, "subject": subject})
}

// sessionMergeRequest is the body of POST /api/sessions/{id}/merge.
type sessionMergeRequest struct {
	Message string `json:"message"`
}

// sessionMerge serves POST /api/sessions/{id}/merge — commits any uncommitted
// session work on its branch, then merges the branch into the repository's
// current HEAD. Conflicts abort cleanly and surface as 409.
func (h *handler) sessionMerge(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, serr := h.sessions.Get(id)
	if serr != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such session"))
		return
	}
	wt := h.sessionWorktrees(sess)
	if wt == nil {
		writeErr(w, http.StatusBadRequest, errors.New("session has no git worktree"))
		return
	}
	var req sessionMergeRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req) // empty body = default message
	}
	subject, err := wt.Merge(r.Context(), id, req.Message)
	if err != nil {
		if errors.Is(err, sessions.ErrMergeConflict) {
			writeErr(w, http.StatusConflict, err)
			return
		}
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]any{"id": id, "merged": true, "subject": subject})
}

// ---- Active session asks & cancellation ----

type sessionOperation struct {
	generation  uint64
	operationID string
	cancel      context.CancelFunc
}

func (h *handler) registerSessionAsk(id string, cancel context.CancelFunc) (sessionOperation, error) {
	operationID, err := util.UUIDv7()
	if err != nil {
		return sessionOperation{}, err
	}
	h.askMu.Lock()
	if h.activeAsks == nil {
		h.activeAsks = make(map[string]sessionOperation)
	}
	h.askGeneration++
	op := sessionOperation{generation: h.askGeneration, operationID: operationID, cancel: cancel}
	old, exists := h.activeAsks[id]
	h.activeAsks[id] = op
	h.askMu.Unlock()
	if exists && old.cancel != nil {
		old.cancel()
	}
	return op, nil
}

func (h *handler) unregisterSessionAsk(id string, op sessionOperation) {
	h.askMu.Lock()
	defer h.askMu.Unlock()
	current, ok := h.activeAsks[id]
	if ok && current.generation == op.generation && current.operationID == op.operationID {
		delete(h.activeAsks, id)
	}
}

func (h *handler) cancelSessionAsk(id, operationID string) bool {
	h.askMu.Lock()
	op, ok := h.activeAsks[id]
	if ok && operationID != "" && op.operationID == operationID {
		delete(h.activeAsks, id)
	} else {
		ok = false
	}
	h.askMu.Unlock()
	if ok && op.cancel != nil {
		op.cancel()
		return true
	}
	return false
}

// clarifyWaiter parks one running ask on the browser's answer. The session id
// rides along so an operation id from another thread cannot answer this one.
type clarifyWaiter struct {
	sessionID string
	ch        chan []entry.ClarifyAnswer
}

// setClarifyWaiter registers the channel a question event will be answered on.
func (h *handler) setClarifyWaiter(sessionID, operationID string, ch chan []entry.ClarifyAnswer) {
	h.clarifyMu.Lock()
	defer h.clarifyMu.Unlock()
	if h.clarifyWaiters == nil {
		h.clarifyWaiters = make(map[string]clarifyWaiter)
	}
	h.clarifyWaiters[operationID] = clarifyWaiter{sessionID: sessionID, ch: ch}
}

func (h *handler) clearClarifyWaiter(operationID string) {
	h.clarifyMu.Lock()
	delete(h.clarifyWaiters, operationID)
	h.clarifyMu.Unlock()
}

// answerClarify delivers one answer set to the parked ask. The waiter is
// consumed on delivery: a second POST for the same operation is a conflict,
// not a second answer.
func (h *handler) answerClarify(sessionID, operationID string, answers []entry.ClarifyAnswer) bool {
	h.clarifyMu.Lock()
	w, ok := h.clarifyWaiters[operationID]
	if !ok || w.sessionID != sessionID {
		h.clarifyMu.Unlock()
		return false
	}
	delete(h.clarifyWaiters, operationID)
	h.clarifyMu.Unlock()
	select {
	case w.ch <- answers:
		return true
	default:
		return false
	}
}

// sessionClarify serves POST /api/sessions/{id}/clarify — the browser's answer
// to a pre-execution question the running SSE turn is parked on. The same
// AskTurnsScoped call resumes with the answers; this is not a new turn.
func (h *handler) sessionClarify(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		OperationID string                `json:"operation_id"`
		Answers     []entry.ClarifyAnswer `json:"answers"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
			return
		}
	}
	req.OperationID = strings.TrimSpace(req.OperationID)
	if req.OperationID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("operation_id is required"))
		return
	}
	if !h.answerClarify(id, req.OperationID, req.Answers) {
		writeErr(w, http.StatusConflict, errors.New("no pending question for this operation"))
		return
	}
	writeJSON(w, map[string]any{"id": id, "operation_id": req.OperationID, "answered": true})
}

// sessionCancel serves POST /api/sessions/{id}/cancel and /stop.
func (h *handler) sessionCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		OperationID string `json:"operation_id"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
			return
		}
	}
	if strings.TrimSpace(req.OperationID) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("operation_id is required"))
		return
	}
	cancelled := h.cancelSessionAsk(id, req.OperationID)
	if !cancelled {
		// The side ask lives under its own slot; the pane's Stop names the same
		// operation id it was handed on the stream.
		cancelled = h.cancelSessionAsk(id+sideOperationSuffix, req.OperationID)
	}
	if !cancelled {
		// A stored side operation whose ask died with a previous panel process
		// has no live cancel to call. Stop still ends it on the record, so a
		// reopened pane does not sit on a spinner that can never finish. The
		// operation id names the chat it belongs to, so the write lands on the
		// conversation that is actually stuck.
		if sess, err := h.sessions.Get(id); err == nil {
			for _, chat := range sess.SideChats {
				if chat.Operation == nil || chat.Operation.ID != req.OperationID ||
					chat.Operation.Status != core.StateRunning {
					continue
				}
				if ok, _ := h.sessions.SetSideOperation(id, sessions.Operation{
					ID: req.OperationID, Status: core.StateCancelled, Error: "cancelled",
				}); ok {
					cancelled = true
				}
				break
			}
		}
	}
	writeJSON(w, map[string]any{"id": id, "operation_id": req.OperationID, "cancelled": cancelled})
}

// hasActiveAsk reports whether an ask currently holds one operation slot. The
// side conversation uses it to tell a live background answer from a record a
// previous panel process left behind.
func (h *handler) hasActiveAsk(key string) bool {
	h.askMu.Lock()
	defer h.askMu.Unlock()
	_, ok := h.activeAsks[key]
	return ok
}

// sideChatRow is one conversation in the pane's history list: what the header
// shows, without the transcript.
type sideChatRow struct {
	ID        string `json:"id"`
	Title     string `json:"title,omitempty"`
	Turns     int    `json:"turns"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
	Running   bool   `json:"running,omitempty"`
}

// sidePayload is what every /side response carries: the list the pane's header
// renders, and the transcript it is showing. `chat` names the conversation the
// turns belong to — a GET for one chat does not move the stored pointer, so
// the pane must be told which one it got.
type sidePayload struct {
	Chats     []sideChatRow       `json:"chats"`
	Active    string              `json:"active,omitempty"`
	Chat      string              `json:"chat,omitempty"`
	Turns     []sessions.SideTurn `json:"turns"`
	Operation *sessions.Operation `json:"operation"`
}

// sessionSide serves the conversations beside a thread.
//
// GET returns every chat in the list plus the transcript of one of them —
// `?chat=` picks it, and the active chat is the default. This is the reconnect
// surface: a pane that reloads, reopens on another device, or comes back after
// a background answer reads the conversation from here instead of starting
// over, and a running operation tells it to keep polling until the answer
// lands.
//
// DELETE discards one chat (`?chat=`, default the active one) — the pane's
// trash. The other conversations are untouched.
func (h *handler) sessionSide(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := h.sessions.Get(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such session"))
		return
	}
	if r.Method == http.MethodDelete {
		chatID := r.URL.Query().Get("chat")
		if chatID == "" {
			if chat := sess.ActiveSideChat(); chat != nil {
				chatID = chat.ID
			}
		}
		if chatID == "" {
			h.writeSide(w, sess, "")
			return
		}
		if chat := sess.SideChatByID(chatID); chat != nil && chat.Operation != nil &&
			chat.Operation.Status == core.StateRunning {
			// Stop owns the live ask; the delete owns the record. Cancelling
			// first keeps the late finalizer from appending into a transcript
			// the user just discarded (FinishSideOperation would drop it
			// anyway, but the goroutine should stop too).
			h.cancelSessionAsk(id+sideOperationSuffix, chat.Operation.ID)
		}
		cleared, err := h.sessions.DeleteSideChat(id, chatID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, errors.New("delete side conversation failed"))
			return
		}
		h.writeSide(w, cleared, "")
		return
	}
	h.writeSide(w, sess, r.URL.Query().Get("chat"))
}

// writeSide answers with the pane's whole state. A `running` operation with no
// live ask behind it is settled as interrupted on the way out: the answer died
// with the panel process, and a spinner that can never finish is worse than a
// stated failure. It walks every chat, not only the one being read, so a stale
// spinner cannot hide in a conversation the reader is not looking at.
func (h *handler) writeSide(w http.ResponseWriter, sess *sessions.Session, want string) {
	live := h.hasActiveAsk(sess.ID + sideOperationSuffix)
	for i := range sess.SideChats {
		op := sess.SideChats[i].Operation
		if live || op == nil || op.Status != core.StateRunning {
			continue
		}
		updated := *op
		updated.Status = core.StateFailed
		updated.Error = "interrupted"
		if ok, _ := h.sessions.SetSideOperation(sess.ID, updated); ok {
			sess.SideChats[i].Operation = &updated
		}
	}

	chats := make([]sideChatRow, 0, len(sess.SideChats))
	for _, c := range sess.SideChats {
		// A chat with no turns is a draft the writer never sent; it has no
		// title to show and nothing to open, so it does not earn a row.
		if len(c.Turns) == 0 {
			continue
		}
		chats = append(chats, sideChatRow{
			ID: c.ID, Title: c.Title, Turns: len(c.Turns), UpdatedAt: c.UpdatedAt,
			Running: c.Operation != nil && c.Operation.Status == core.StateRunning,
		})
	}
	// Newest first: the list is a history, and the conversation just used is
	// the one being reopened.
	sort.SliceStable(chats, func(i, j int) bool { return chats[i].UpdatedAt > chats[j].UpdatedAt })

	chat := sess.SideChatByID(want)
	if chat == nil {
		chat = sess.ActiveSideChat()
	}
	out := sidePayload{Chats: chats, Active: sess.SideActive, Turns: []sessions.SideTurn{}}
	if chat != nil {
		out.Chat = chat.ID
		out.Turns = legacyFreeSideTurns(chat.Turns)
		out.Operation = chat.Operation
	}
	writeJSON(w, out)
}

// sessionSideNew starts another conversation beside the thread. The pane's `+`
// is this call: the previous chats stay in the list, nothing is discarded.
func (h *handler) sessionSideNew(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := h.sessions.NewSideChat(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("create side conversation failed"))
		return
	}
	h.writeSide(w, sess, sess.SideActive)
}

// sessionSideSelect moves the pane to another stored conversation and returns
// its transcript.
func (h *handler) sessionSideSelect(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		ChatID string `json:"chat_id"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
			return
		}
	}
	req.ChatID = strings.TrimSpace(req.ChatID)
	if req.ChatID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("chat_id is required"))
		return
	}
	sess, err := h.sessions.SelectSideChat(id, req.ChatID)
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such side conversation"))
		return
	}
	h.writeSide(w, sess, req.ChatID)
}

// legacyFreeSideTurns rewrites answers that were stored as raw tool-call
// markup before the recovery path existed (GLM's <tool_call> template, DSML).
// A side conversation is a transcript, not a log: the pane re-renders every
// stored turn on open, so an old row would keep showing protocol text the
// model never meant as prose — and there is no way to re-run it. The calls
// become tool chips, which is what the reader can still use; the prose around
// them (usually none) stays.
func legacyFreeSideTurns(turns []sessions.SideTurn) []sessions.SideTurn {
	var out []sessions.SideTurn
	for i, t := range turns {
		if t.Role != "assistant" || t.Text == "" {
			continue
		}
		names, prose, ok := entry.ParseTextualToolCalls(t.Text)
		if !ok {
			continue
		}
		if out == nil {
			out = append([]sessions.SideTurn(nil), turns...)
		}
		row := out[i]
		row.Text = prose
		for _, name := range names {
			seen := false
			for _, have := range row.Tools {
				if have == name {
					seen = true
					break
				}
			}
			if !seen {
				row.Tools = append(row.Tools, name)
			}
		}
		out[i] = row
	}
	if out == nil {
		return turns
	}
	return out
}

// ---- Session approval state ----

// getSessionApproval serves GET /api/sessions/{id}/approval — the effective
// tier-2 approval policy as this session sees it: the resolved mode, the
// default remember scope, and any remembered decision (and which scope it
// lives in). The console renders it next to the authorize toggle so a chat
// can see what its next irreversible action will do before it asks.
func (h *handler) getSessionApproval(w http.ResponseWriter, r *http.Request) {
	eng := h.currentEngine()
	if eng == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("ask engine not configured"))
		return
	}
	sess, err := h.sessions.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such session"))
		return
	}
	writeJSON(w, eng.ApprovalState(sess.ID, sess.Project))
}

// clearSessionApproval serves DELETE /api/sessions/{id}/approval — forgets
// this session's remembered approval answers (its session bucket plus the
// project row's stored decision, so "forget" really forgets). It is how a
// remembered deny gets lifted without restarting the panel.
func (h *handler) clearSessionApproval(w http.ResponseWriter, r *http.Request) {
	eng := h.currentEngine()
	if eng == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("ask engine not configured"))
		return
	}
	sess, err := h.sessions.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such session"))
		return
	}
	if err := eng.ClearApproval(sess.ID, sess.Project); err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("clear approval failed"))
		return
	}
	writeJSON(w, eng.ApprovalState(sess.ID, sess.Project))
}

// ---- Streaming session ask ----

// sideOperationSuffix keys the side channel apart from the thread it sits
// beside, for two things that are per-id: the active-ask slot (one per key, and
// registering cancels the previous holder) and the remembered-approval bucket.
// The TUI spells the same suffix on its side session, so one conversation has
// one name on both surfaces.
const sideOperationSuffix = ":side"

// sessionAskRequest is the body of POST /api/sessions/{id}/ask.
type sessionAskRequest struct {
	Prompt    string `json:"prompt"`
	Authorize bool   `json:"authorize"`
	// Node is the composer's environment pick: the preferred executor node
	// (id or name) for any task this ask spawns. Empty lets the scheduler
	// route by capability, which is also the right behavior for plans.
	Node string `json:"node"`
	// Agents is the thread's roster as the composer currently shows it. Null
	// (absent) means "use the roster stored on the session"; an explicit
	// empty list means "no restriction" — the same distinction PATCH makes,
	// so a pick made a moment before sending does not need a second request.
	Agents []string `json:"agents"`
	// PermissionMode overrides the session's stored permission policy for
	// this turn only. Absent means "use the thread's policy" — the console
	// normally PATCHes the session when the user flips the switch, so a
	// resumed thread on another device keeps the same answer.
	PermissionMode string `json:"permission_mode"`
	// ResumeQueued marks the console's automatic continuation of notes a
	// finished turn never read. The prompt may repeat their text for display,
	// but the parked notes are the authority: the server drains them as this
	// turn's input so the words reach the model exactly once.
	ResumeQueued bool `json:"resume_queued"`
	// Side marks a side-channel ask: a separate conversation beside the
	// thread, asked from the console's dock. It gets the thread's full tool
	// face (dispatch included) and its own transcript on the session; the
	// thread's turns are replayed as reference and nothing this ask produces
	// is appended to them. The question and answer are filed in the side chat,
	// so the ask can outlive the pane that started it.
	Side bool `json:"side"`
	// SideChat names the conversation beside the thread this question belongs
	// to. Empty means the active one, which is what a pane that never switched
	// sends.
	SideChat string `json:"side_chat"`
	// SideNew starts a fresh conversation for this question instead of
	// appending to an existing one — the pane's `+` followed by a send. The
	// previous chats stay in the list.
	SideNew bool `json:"side_new"`
	// SideTurns is deprecated and ignored: the server owns the side
	// conversation (Session.SideChats), so a side question outlives the pane
	// and a second device sees the same history. The field stays on the wire
	// so an older console does not fail to decode its own request.
	SideTurns []entry.Turn `json:"side_turns"`
}

// statusEventOf is the JSON one progress event becomes on the SSE stream.
// Text is the English fallback wording; the browser prefers the structured
// fields (kind + the retry numbers) so it can phrase the status in its own
// locale — a retry milestone scraped from English prose is what "一直加载"
// used to look like.
func statusEventOf(p askengine.Progress) map[string]any {
	ev := map[string]any{"text": askengine.StatusText(p), "kind": string(p.Kind)}
	if p.Kind == askengine.ProgressRetry {
		ev["attempt"] = p.Attempt
		ev["max"] = p.MaxAttempt
		ev["delay_ms"] = p.Delay.Milliseconds()
		if p.Status > 0 {
			ev["http_status"] = p.Status
		}
		if p.Reason != "" {
			ev["reason"] = p.Reason
		}
	}
	if p.Kind == askengine.ProgressTool {
		// A tool call's subject (workspace_search's pattern, workspace_read's
		// path) lets the console show what was checked instead of only which
		// tool ran. Detail is already bounded by the engine.
		ev["name"] = p.Name
		if p.Detail != "" {
			ev["detail"] = p.Detail
		}
	}
	if p.Kind == askengine.ProgressSpawn {
		// The fan-out milestone carries its subject and mode descriptor so
		// the console can phrase "spawning sub-agent X (isolated)…" in the
		// browser's own language instead of echoing the English fallback.
		ev["name"] = p.Name
		if p.Detail != "" {
			ev["detail"] = p.Detail
		}
	}
	if p.Kind == askengine.ProgressOffline {
		// An outage milestone: the console needs the phase ("waiting" or
		// "resumed") and how long it lasted to say it in its own language —
		// the wire text is only the fallback.
		ev["name"] = p.Name
		ev["detail"] = p.Detail
		ev["waited_ms"] = p.Delay.Milliseconds()
	}
	return ev
}

// sessionAsk serves POST /api/sessions/{id}/ask as a Server-Sent Events
// stream: the conversation runs with the session's full history, and the
// session's git worktree is the execution directory for any classified task.
// Events: "reasoning" (chain-of-thought chunk), "delta" (answer text chunk),
// "status" (one-line progress), "task" (the id of a task row this turn
// dispatched, sent the moment the row exists — the turn keeps running while
// the task executes, so this is what lets the console show live progress),
// "result" (the final askResult), "error".
//
// Reasoning travels on its own event so the console can show it apart from the
// answer. It is display-only — D14 keeps it out of the answer text, out of the
// stored turn and out of the task result — so a thread reloaded from disk shows
// the reply without the reasoning that produced it.
func (h *handler) sessionAsk(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, errNoFlusher)
		return
	}
	var req sessionAskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	sessID := r.PathValue("id")
	req.Prompt = strings.TrimSpace(req.Prompt)
	// An empty prompt is a client bug unless a parked note is about to become
	// one. Checking it here keeps the 400 ahead of the engine/session checks
	// (the same order the endpoint always had) while still leaving the carry
	// untouched until everything else has passed.
	if req.Prompt == "" && !req.ResumeQueued && !h.hasCarry(sessID) {
		writeErr(w, http.StatusBadRequest, errors.New("prompt must not be empty"))
		return
	}
	eng := h.currentEngine()
	if eng == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("ask engine not configured (configure the model in Settings)"))
		return
	}
	sess, err := h.sessions.Get(sessID)
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such session"))
		return
	}
	// A side ask is a parallel conversation with its own stored transcript:
	// it shares the thread's history snapshot, work dir and permission
	// policy, and nothing else.
	side := req.Side
	// Who may take this turn's work. The composer sends what is on screen;
	// a client that knows nothing about rosters (or a resumed thread) gets
	// the session's own team, so the pick survives a reload and a second
	// device instead of being a per-request decoration.
	team := sess.Team
	if req.Agents != nil {
		if team, err = cleanTeam(req.Agents); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}

	// The conversation's permission policy: an explicit per-turn value wins
	// over the thread's stored mode, and an unknown value is a client bug
	// worth refusing rather than silently downgrading to auto — "the mode I
	// picked was ignored" is exactly the failure this feature exists to
	// prevent.
	permission := sess.PermissionMode
	if req.PermissionMode != "" {
		if !defense.ValidMode(req.PermissionMode) {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("unknown permission mode %q", req.PermissionMode))
			return
		}
		permission = req.PermissionMode
	}

	// Only now take the notes a finished turn parked: every check above can
	// still refuse the request, and a refusal must not swallow text the user
	// typed. An empty carry cannot lose anything here — it only leaves the
	// prompt empty, which the check below rejects. A side ask never drains
	// them: those words belong to the thread's next turn, and a question asked
	// beside the thread must not eat what the thread was about to say.
	if !side {
		if carried := h.takeCarry(sessID); len(carried) > 0 {
			switch {
			case req.ResumeQueued, req.Prompt == "":
				req.Prompt = carryText(carried)
			default:
				req.Prompt = carryText(carried) + "\n\n" + req.Prompt
			}
		}
	}
	if req.Prompt == "" {
		// A continuation request with nothing parked has nothing to resume;
		// falling through to the display prompt would execute it as a new
		// message.
		if req.ResumeQueued {
			writeErr(w, http.StatusConflict, errors.New("nothing queued to resume"))
			return
		}
		writeErr(w, http.StatusBadRequest, errors.New("prompt must not be empty"))
		return
	}

	// A side ask owns a server-side transcript from here on. The question is
	// filed before the model runs — a crash or a closed tab must not lose it —
	// and the replay the model sees is the conversation as it stood before this
	// question. The pane no longer sends its own copy: the session file is the
	// truth, so the conversation survives a reload, a second device and a panel
	// restart, and the answer keeps arriving after the pane is gone.
	var sideReplay []entry.Turn
	sideChatID := ""
	if side {
		// Which conversation beside the thread this question belongs to. A
		// pane that pressed `+` asks for a fresh one; a pane that names no
		// chat (an older console, or a thread whose list is empty) means the
		// active one, and only a thread with none at all mints a new one.
		// Either way the id is resolved before the question is filed, so the
		// answer can never land in a different transcript than the question.
		chat := sess.SideChatByID(req.SideChat)
		if chat == nil && req.SideChat == "" {
			chat = sess.ActiveSideChat()
		}
		if req.SideNew || chat == nil {
			updated, err := h.sessions.NewSideChat(sess.ID)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, errors.New("create side conversation failed"))
				return
			}
			sess = updated
			chat = sess.ActiveSideChat()
		}
		if chat == nil {
			writeErr(w, http.StatusInternalServerError, errors.New("no side conversation"))
			return
		}
		sideChatID = chat.ID
		budget := sessions.ReplayBudget(eng.ContextWindow()) / 2
		if budget < 4000 {
			budget = 4000
		}
		stored := sessions.TrimSideTurns(chat.Turns, budget)
		sideReplay = make([]entry.Turn, 0, len(stored))
		for _, t := range stored {
			sideReplay = append(sideReplay, entry.Turn{Role: t.Role, Content: t.Text})
		}
		if _, err := h.sessions.AppendSideTurnTo(sess.ID, sideChatID, sessions.SideTurn{Role: "user", Text: req.Prompt}); err != nil {
			writeErr(w, http.StatusInternalServerError, errors.New("save side turn failed"))
			return
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	// Flush headers immediately so the browser and reverse proxies establish the
	// live SSE stream right away rather than buffering until the first delta.
	flusher.Flush()

	var mu sync.Mutex
	send := func(event string, v any) bool {
		data, err := json.Marshal(v)
		if err != nil {
			return false
		}
		mu.Lock()
		defer mu.Unlock()
		if _, err := w.Write([]byte("event: " + event + "\ndata: " + string(data) + "\n\n")); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	sendKeepAlive := func() bool {
		mu.Lock()
		defer mu.Unlock()
		if _, err := w.Write([]byte(": keep-alive\n\n")); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// SSE keep-alive heartbeat comment every 5s keeps reverse proxies,
	// browsers, and OS power management from dropping an idle connection
	// while the model is thinking or waiting for sub-tasks.
	heartbeat := time.NewTicker(5 * time.Second)
	defer heartbeat.Stop()
	heartbeatDone := make(chan struct{})
	defer close(heartbeatDone)

	guard.Go(nil, "panel: sse keepalive", nil, func() {
		for {
			select {
			case <-heartbeat.C:
				if !sendKeepAlive() {
					return
				}
			case <-r.Context().Done():
				return
			case <-heartbeatDone:
				return
			}
		}
	})

	// History is the thread as it stands, auto-compacted when it overflows
	// the replay budget (the compacted prefix survives as sess.Summary and
	// replays as a leading context note). The user turn is persisted first
	// (a failed ask still leaves the question in the thread, like codex/claude
	// code) but excluded from the replay: AskTurns carries the prompt itself,
	// so replaying the persisted copy too would send two consecutive user
	// messages — a 400 from strict providers.
	var history []entry.Turn
	budget := sessions.ReplayBudget(eng.ContextWindow())
	// Compaction rewrites the stored thread, so a side ask leaves it alone: its
	// replay is a snapshot, and a snapshot must not change what it copies. A
	// thread long enough to need compaction still replays its budget-bounded
	// tail below.
	if !side {
		if updated, err := h.sessions.CompactHistory(r.Context(), sess.ID, budget,
			func(ctx context.Context, evicted []sessions.Turn) (string, error) {
				turns := make([]entry.Turn, 0, len(evicted))
				for _, t := range evicted {
					turns = append(turns, entry.Turn{Role: t.Role, Content: t.Text})
				}
				return eng.SummarizeTurns(ctx, turns)
			}); err == nil {
			sess = updated
		}
	}
	if sess.Summary != "" {
		history = append(history, entry.Turn{Role: "user", Content: sessions.SummaryMarker + "\n" + sess.Summary})
	}
	// The replay stays budget-bounded even when compaction could not run
	// (summarize failed or thread fits): the stored thread keeps every turn,
	// only the model's view is trimmed to the tail that fits.
	_, kept := sessions.SplitForBudget(sess.Turns, budget)
	for _, t := range kept {
		history = append(history, entry.Turn{Role: t.Role, Content: t.Text})
	}
	if !side {
		if _, err := h.sessions.AppendTurn(sess.ID, sessions.Turn{Role: "user", Text: req.Prompt}); err != nil {
			send("error", map[string]string{"message": "save turn failed"})
			return
		}
	}

	var streamed strings.Builder
	// The answer's evidence trail: a side turn keeps the tool calls behind it,
	// so a reloaded pane shows what was checked, not only what was said. It is
	// display-only and never replayed to the model.
	var sideTrace []string
	cb := askengine.StreamCallbacks{
		OnReasoning: func(text string) { send("reasoning", map[string]string{"text": text}) },
		OnDelta: func(text string) {
			// The streamed prose is the only copy of a reply that a cancelled
			// turn will ever have: once execCtx dies the model is never asked
			// again, so a turn that stops mid-answer used to persist the error
			// line alone and lose everything the user had already read. Keep
			// it here and file it with the stopped turn below.
			streamed.WriteString(text)
			send("delta", map[string]string{"text": text})
		},
		// OnProgress (not OnStatus) so the structured event survives: the
		// browser re-localizes retry milestones from the fields instead of
		// scraping the English fallback sentence. Usage reports ride the same
		// callback but their own event: they are a counter update, not a
		// phase, and a status handler that overwrote the one-line note with
		// "usage" would erase what the turn is doing.
		OnProgress: func(p askengine.Progress) {
			if side && p.Kind == askengine.ProgressTool {
				label := p.Name
				if p.Detail != "" {
					label += " " + p.Detail
				}
				if len(sideTrace) < 50 && (len(sideTrace) == 0 || sideTrace[len(sideTrace)-1] != label) {
					sideTrace = append(sideTrace, label)
				}
			}
			if p.Kind == askengine.ProgressUsage {
				send("usage", map[string]any{
					"input": p.InputTokens, "output": p.OutputTokens, "estimated": p.Estimated,
				})
				return
			}
			send("status", statusEventOf(p))
		},
		// The task row exists now — name it on the wire immediately. This turn
		// does not finish until the task settles (askengine follows the row to
		// its outcome), so without this frame the console has no handle on the
		// work for the whole execution and can only show a spinner and one
		// line of status. With it the thread renders the employee's live
		// progress row from the moment the job is dispatched.
		OnTaskRow: func(taskID string) {
			send("task", map[string]string{"task_id": taskID})
		},
		// What the user types while this turn streams. The queue is drained at
		// every round boundary, so the note lands in the round after the one
		// that was running when it was typed — the same contract the TUI's
		// steer channel gives, and the only channel a task-less turn has.
		GetSteer: func() string {
			text, _ := h.drainSteer(sess.ID)
			return text
		},
		// The proof the note actually reached the model: a queued channel write
		// alone cannot distinguish "delivered next round" from "the turn ended
		// first". The console upgrades its "queued" note on this event.
		OnSteerConsumed: func(text string, round int) {
			send("steer", map[string]any{"text": text, "round": round})
		},
	}

	// Every panel session is a workspace conversation: repo sessions run in
	// their worktree, non-repo ones in the shared work path. Pinning a
	// non-empty workDir for both keeps the memory wall (§17.2) intact —
	// personal memory never enters a session prompt.
	workDir := sess.Worktree
	if workDir == "" && sess.Project != "" && h.projectStore != nil {
		// UsableWorkDir: a row naming $HOME predates the breadth rule and would
		// otherwise pin this turn's work dir to a path the executor refuses, so
		// every ask in the thread fails on a rule the user never invoked.
		if p, err := h.projectStore.Get(sess.Project); err == nil {
			workDir = p.UsableWorkDir()
		}
	}
	if workDir == "" {
		workDir = eng.WorkPath()
	}
	// Run under the panel lifetime, not the HTTP connection. Only the matching
	// operation ID can cancel this generation through the explicit stop API.
	baseCtx := h.serviceCtx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	execCtx, cancel := context.WithCancel(baseCtx)
	defer cancel()
	// The operation key separates the side channel from the thread it sits
	// beside. registerSessionAsk is one slot per key and cancels the previous
	// holder, so a side ask filed under the thread's own id would stop the
	// turn the user is watching. The suffix mirrors the TUI's
	// sideSessionSuffix: the same conversation has one name on both surfaces.
	opKey := sess.ID
	if side {
		opKey = sess.ID + sideOperationSuffix
	}
	op, err := h.registerSessionAsk(opKey, cancel)
	if err != nil {
		send("error", map[string]string{"message": "create operation failed"})
		return
	}
	// Persist the dispatch boundary before the entry model can submit work.
	// A unique operation, rather than session ID, separates overlapping asks.
	roundID := op.operationID
	if err := h.store.BeginAskRound(execCtx, roundID, sess.ID, string(eng.Locale())); err != nil {
		send("error", map[string]string{"message": "create round failed"})
		h.unregisterSessionAsk(opKey, op)
		return
	}
	roundCtx, stopRound := context.WithCancel(execCtx)
	roundStatus := "interrupted"
	guard.Go(nil, "panel: ask round lease", nil, func() {
		tick := time.NewTicker(core.AskRoundLease / 3)
		defer tick.Stop()
		for {
			select {
			case <-roundCtx.Done():
				return
			case <-tick.C:
				_ = h.store.TouchAskRound(roundCtx, roundID)
			}
		}
	})
	defer func() {
		stopRound()
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer closeCancel()
		_ = h.store.CloseAskRound(closeCtx, roundID, roundStatus)
	}()
	defer h.unregisterSessionAsk(opKey, op)
	// The steer queue lives exactly as long as the turn that drains it. Notes
	// still in it when the turn ends are not dropped: a note typed into the
	// last round has no later boundary to land on, and throwing it away made
	// the composer's "queued" a promise nothing kept. They are parked in the
	// carry and announced on the stream, so the console opens the next turn
	// with them — and a console that never hears the frame (tab closed
	// mid-answer) leaves them for whatever prompt comes next. A side ask owns
	// no queue: its bytes never reach the thread.
	if !side {
		defer h.parkLeftovers(sess.ID, func(text string, count int) {
			send("queued_turn", map[string]any{"text": text, "count": count})
		})
	}
	if !send("operation", map[string]string{"operation_id": op.operationID}) {
		// The operation remains detached from the failed response writer and
		// continues to persist its final task/session result.
	}
	if side {
		if err := h.sessions.StartSideOperationFor(sess.ID, sideChatID, sessions.Operation{
			ID: op.operationID, Status: core.StateRunning,
		}); err != nil {
			send("error", map[string]string{"message": "save side operation failed"})
			return
		}
	} else {
		if err := h.sessions.StartOperation(sess.ID, sessions.Operation{
			ID: op.operationID, Status: core.StateRunning,
		}); err != nil {
			send("error", map[string]string{"message": "save operation failed"})
			return
		}
	}

	// Pre-execution questions (entry clarify + the large-task plan card) park
	// this turn on the browser: the question rides the same SSE stream, the
	// answer comes back through POST /api/sessions/{id}/clarify, and the same
	// AskTurnsScoped call continues with it. A closed stream, a cancelled
	// operation or the request context ending releases the wait; the engine
	// then treats the turn as interrupted instead of holding a goroutine.
	cb.OnQuestion = func(spec entry.ClarifySpec) ([]entry.ClarifyAnswer, bool) {
		ch := make(chan []entry.ClarifyAnswer, 1)
		h.setClarifyWaiter(sess.ID, op.operationID, ch)
		defer h.clearClarifyWaiter(op.operationID)
		if !send("question", map[string]any{
			"operation_id":  op.operationID,
			"clarification": spec,
		}) {
			return nil, false
		}
		select {
		case answers := <-ch:
			return answers, true
		case <-execCtx.Done():
			return nil, false
		case <-r.Context().Done():
			return nil, false
		}
	}

	// The clock starts here rather than at the top of the handler: setup above
	// (history replay, compaction) is not the turn, and a duration that
	// included it would overstate a fast reply.
	askStarted := time.Now()
	// A side ask binds remembered approvals to its own bucket: a "session"
	// grant given beside the thread must not silently answer for the thread.
	approvalSession := sess.ID
	if side {
		approvalSession = sess.ID + sideOperationSuffix
	}
	out, err := eng.AskTurnsScoped(execCtx, history, req.Prompt, askengine.AskScope{
		RoundID:        roundID,
		Project:        sess.Project,
		WorkDir:        workDir,
		SessionID:      approvalSession,
		Node:           req.Node,
		Agents:         team,
		PermissionMode: permission,
		Side:           side,
		SideTurns:      sideReplay,
	}, req.Authorize, cb)
	roundStatus = "closed"
	if execCtx.Err() != nil {
		roundStatus = "stopped"
	}
	askDuration := time.Since(askStarted).Milliseconds()
	if err != nil {
		msg := err.Error()
		status := core.StateFailed
		if errors.Is(err, context.Canceled) || execCtx.Err() != nil {
			msg = "任务已被用户取消"
			status = core.StateCancelled
		}
		// A model-config failure (dead/absent key, bad endpoint, retired
		// model id) is the one error class with a dedicated fix surface:
		// tag it so the console renders a jump-to-settings affordance on
		// the bubble, including on the persisted turn a reload replays.
		code := ""
		if entry.IsModelConfigError(err) {
			code = "model_config"
		}
		// A classified model failure carries its kind + provider detail so
		// the console can render the troubleshooting <details> under the
		// bubble — and so a thread reloaded from disk can too, via the
		// persisted turn.
		var ce *entry.ClassifyError
		var errKind, errDetail string
		var errStatus int
		if errors.As(err, &ce) {
			errKind, errDetail, errStatus = ce.Kind, ce.Detail, ce.Status
		}
		errPayload := map[string]any{"message": msg, "code": code}
		if errKind != "" {
			errPayload["kind"] = errKind
		}
		if errStatus > 0 {
			errPayload["http_status"] = errStatus
		}
		if errDetail != "" {
			errPayload["detail"] = errDetail
		}
		// A side operation settles together with its answer in
		// FinishSideOperation below, so a reader never sees "done" before the
		// turn is on disk. The main thread keeps its own two-step order.
		if !side {
			_, _ = h.sessions.SetOperation(sess.ID, sessions.Operation{
				ID: op.operationID, Status: status, Error: msg,
			})
		}
		send("error", errPayload)
		// The failed turn carries its duration too: "it died after 40 seconds"
		// is the first thing you want to know about a timeout.
		//
		// A stopped turn keeps the prose it already showed: the reply is not
		// coming back — cancellation ends the ask — so filing only the error
		// line would erase from the thread everything the user just read. The
		// partial answer is filed as the turn's text and the stop note rides
		// along, rather than the other way round.
		text := "⚠ " + msg
		kind := "error"
		if status == core.StateCancelled {
			kind = "answer"
			text = strings.TrimRight(streamed.String(), " \t\r\n")
			if text == "" {
				text = msg
			}
		}
		if side {
			_, _ = h.sessions.FinishSideOperation(sess.ID, op.operationID, sessions.SideTurn{
				Role: "assistant", Text: text, Tools: sideTrace, Tokens: out.Tokens(),
				DurationMS: askDuration, Error: msg,
			}, sessions.Operation{ID: op.operationID, Status: status, Error: msg})
		} else {
			_, _ = h.sessions.AppendTurn(sess.ID, sessions.Turn{
				Role: "assistant", Text: text, Kind: kind, Code: code,
				ErrKind: errKind, ErrDetail: errDetail,
				DurationMS: askDuration,
				// A stopped turn still consumed tokens up to the cut, and the
				// reloaded thread should show what it cost.
				Tokens: out.Tokens(),
			})
		}
		return
	}

	res := planResultOf(out)
	operationStatus := core.StateDone
	if out.Kind == "task" && out.TaskState != "" {
		operationStatus = out.TaskState
	}
	// A side operation settles together with its answer in
	// FinishSideOperation below: a poller that sees "done" can read the turn in
	// the same breath.
	if !side {
		_, _ = h.sessions.SetOperation(sess.ID, sessions.Operation{
			ID: op.operationID, Status: operationStatus, TaskID: out.TaskID, Error: out.Stderr,
		})
	}
	send("result", res)

	// Queue redesign: bind a spawned task back to this session so the board
	// card can jump into it and the session streams the task's progress. A
	// side ask dispatches real work too, so it binds the same way.
	if out.Kind == "task" && out.TaskID != "" {
		// Bookkeeping only; the result already streamed to the client.
		_ = h.store.SetSessionID(context.WithoutCancel(r.Context()), out.TaskID, sess.ID)
	}

	// A round can dispatch a batch: the model emits several `task_submit`
	// calls and the engine enqueues every one, but the Result only carries the
	// last receipt. Filing one turn from that receipt left the thread showing
	// a single card while the task board showed all three rows — "two agents
	// running" above a board of three. One turn per dispatch puts the
	// transcript back in step with the board, and gives each card its own
	// link. The model's own answer, when it wrote one, keeps the single-turn
	// path: prose plus a batch of receipts has no place in one bubble.
	if !side && out.Kind == "task" && len(out.Dispatched) > 1 && out.Answer == "" {
		for i, d := range out.Dispatched {
			batch := sessions.Turn{
				Role: "assistant",
				Kind: "task",
				Text: i18n.Tf(h.locale(), "web.session.dispatched", "title", d.TaskTitle, "id", d.TaskID),
				Ref:  d.TaskID,
			}
			// The clock belongs to the ask, not to each row: only the last
			// receipt carries it, so the thread does not print the same
			// duration three times.
			if i == len(out.Dispatched)-1 {
				batch.DurationMS = askDuration
				batch.Model = out.EntryModel
			}
			if _, err := h.sessions.AppendTurn(sess.ID, batch); err != nil {
				// The reply already streamed; a failed bookkeeping write is
				// not worth killing the stream over.
				_ = err
			}
		}
		return
	}

	turn := sessions.Turn{
		Role:       "assistant",
		Kind:       out.Kind,
		DurationMS: askDuration,
		// The model that answered, filed with the turn: the console prints it
		// beside the reply's copy button, and `out` only exists for the live
		// stream — a reloaded thread reads this instead.
		Model:  out.EntryModel,
		Tokens: out.Tokens(),
	}
	switch out.Kind {
	case "task":
		// Queue mode: the turn records the dispatch (the finalizer folds the
		// outcome as its own turn once the task lands in a terminal state).
		// A bare task id used to be the text — unreadable in the thread and
		// meaningless noise when the next ask replays the history to the
		// model.
		turn.Text = "已派发任务：" + out.TaskTitle + "（" + out.TaskID + "）"
		if side {
			turn.Text = i18n.Tf(h.locale(), "web.session.dispatched", "title", out.TaskTitle, "id", out.TaskID)
			// A side turn has no per-task cards; a batch reads as one list of
			// receipts, each naming its own id.
			if len(out.Dispatched) > 1 && out.Answer == "" {
				lines := make([]string, 0, len(out.Dispatched))
				for _, d := range out.Dispatched {
					lines = append(lines, i18n.Tf(h.locale(), "web.session.dispatched", "title", d.TaskTitle, "id", d.TaskID))
				}
				turn.Text = strings.Join(lines, "\n")
			}
		}
		if out.TaskID == "" {
			// Submission failed before a row existed — the id/title are
			// empty and the bare template above renders as "已派发任务：（）",
			// hiding the actual reason. Surface the failure instead.
			turn.Kind = "error"
			turn.Text = "⚠ " + out.Stderr
			if turn.Text == "⚠ " {
				turn.Text = "⚠ 任务提交失败"
			}
		}
		if out.Answer != "" {
			turn.Text = out.Answer
		}
		turn.Ref = out.TaskID
	case "plan":
		// The receipt is the orchestration half of the report: what was
		// started, how many stages it was cut into, and the id the plan board
		// answers to. Each stage's own answer arrives as its employee finishes
		// (the session finalizer, one compact turn per stage).
		turn.Text = fmt.Sprintf("已启动多阶段计划：%s（%d 个阶段，%s）", out.PlanGoal, len(out.PlanStages), out.PlanID)
		turn.Ref = out.PlanID
	default:
		turn.Text = out.Answer
	}
	if side {
		errText := ""
		if turn.Kind == "error" {
			errText = strings.TrimPrefix(turn.Text, "⚠ ")
		}
		_, _ = h.sessions.FinishSideOperation(sess.ID, op.operationID, sessions.SideTurn{
			Role: "assistant", Text: turn.Text, Tools: sideTrace, TaskID: out.TaskID,
			Tokens: turn.Tokens, DurationMS: turn.DurationMS, Error: errText,
		}, sessions.Operation{
			// The side operation tracks the ask, not the work it dispatched: the
			// answer is filed, so the ask is done even if the task is still
			// queued or running.
			ID: op.operationID, Status: core.StateDone, TaskID: out.TaskID, Error: out.Stderr,
		})
	} else {
		if _, err := h.sessions.AppendTurn(sess.ID, turn); err != nil {
			// The reply already streamed; a failed bookkeeping write is not worth
			// killing the stream over.
			_ = err
		}
	}
}

// ---- Session uploads ----

// Upload caps. One file is capped per part, and a session's uploads directory
// is capped in total — the oldest files are dropped to make room, so a
// thread's attachments cannot grow without bound.
const (
	maxUploadFileBytes  = 25 << 20  // 25 MiB per file
	maxUploadTotalBytes = 128 << 20 // 128 MiB per session
	// maxUploadBodyBytes bounds one multipart request: several files, each of
	// which the handler caps at maxUploadFileBytes, plus envelope overhead and
	// slack for the other form fields.
	maxUploadBodyBytes = 4*maxUploadFileBytes + (1 << 20)
)

// sessionUploadDir returns the directory a session's uploads live in,
// creating it on demand. With a worktree it is <worktree>/.openpanda/uploads
// — inside the worktree, so it rides the session's own lifecycle: deleting a
// thread deletes its uploads with it, which is the per-session cleanup.
// Without a worktree (a work path that is not a repository) it falls back to
// a per-session directory under the work path, which the panel deletes with
// the thread instead.
func (h *handler) sessionUploadDir(sess *sessions.Session) (string, error) {
	if sess.Worktree != "" {
		dir := filepath.Join(sess.Worktree, ".openpanda", "uploads")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		return dir, nil
	}
	var workPath string
	h.readCfg(func(c *config.Config) { workPath = c.Storage.WorkPath })
	if workPath == "" {
		return "", errors.New("session has no worktree and no work path")
	}
	dir := filepath.Join(workPath, ".openpanda", "uploads", sess.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// removeSessionUploads deletes a session's fallback uploads directory — the
// non-repo case, where uploads would otherwise outlive the thread. The
// worktree case is handled by removing the worktree itself.
func (h *handler) removeSessionUploads(id string) {
	var workPath string
	h.readCfg(func(c *config.Config) { workPath = c.Storage.WorkPath })
	if workPath == "" {
		return
	}
	_ = os.RemoveAll(filepath.Join(workPath, ".openpanda", "uploads", id))
}

// uploadedFile is the wire form of one uploaded file in the response to
// POST /api/sessions/{id}/upload.
type uploadedFile struct {
	Name string `json:"name"`
	Path string `json:"path"` // absolute — what /api/fs/read accepts
	// Mention is what the composer inserts: a path the fs endpoints resolve —
	// work-path relative when the file sits inside it (the same shape the
	// @file completion types), absolute otherwise.
	Size    int64  `json:"size"`
	Mention string `json:"mention"`
}

// uploadSessionFile serves POST /api/sessions/{id}/upload — multipart bodies
// ("file" parts) saved into the session's uploads directory, then referenced
// from the composer as @path the same way browsed files are.
func (h *handler) uploadSessionFile(w http.ResponseWriter, r *http.Request) {
	sess, err := h.sessions.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such session"))
		return
	}
	dir, err := h.sessionUploadDir(sess)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("multipart body required"))
		return
	}

	var workPath string
	h.readCfg(func(c *config.Config) { workPath = c.Storage.WorkPath })

	out := []uploadedFile{}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, errors.New("bad multipart body"))
			return
		}
		if part.FormName() != "file" {
			part.Close()
			continue
		}
		name := filepath.Base(part.FileName())
		if name == "" || name == "." || name == string(filepath.Separator) {
			part.Close()
			continue
		}
		// A mention is one whitespace-delimited token, so the on-disk name
		// must be too — and a leading dot would hide it from the fs listing.
		name = strings.Map(func(r rune) rune {
			if r == ' ' || r == '\t' {
				return '_'
			}
			return r
		}, name)
		name = strings.TrimPrefix(name, ".")
		if name == "" {
			part.Close()
			continue
		}
		// Timestamp prefix: two uploads of the same name must not overwrite
		// each other mid-conversation.
		stamped := fmt.Sprintf("%d-%s", time.Now().UnixMilli(), name)

		dst, err := os.OpenFile(filepath.Join(dir, stamped), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, errors.New("save failed"))
			return
		}
		n, copyErr := io.Copy(dst, io.LimitReader(part, maxUploadFileBytes+1))
		closeErr := dst.Close()
		if copyErr != nil || closeErr != nil || n > maxUploadFileBytes {
			_ = os.Remove(filepath.Join(dir, stamped))
			var over *http.MaxBytesError
			switch {
			case n > maxUploadFileBytes:
				writeErr(w, http.StatusRequestEntityTooLarge,
					fmt.Errorf("file exceeds %d MiB", maxUploadFileBytes>>20))
			case errors.As(copyErr, &over):
				// The request as a whole went past the body cap (several files
				// in one call). Saying "save failed" pointed at the disk for a
				// limit the client can see and stay under.
				writeErr(w, http.StatusRequestEntityTooLarge,
					fmt.Errorf("upload exceeds %d MiB per request", over.Limit>>20))
			default:
				writeErr(w, http.StatusInternalServerError, errors.New("save failed"))
			}
			return
		}
		abs := filepath.Join(dir, stamped)
		out = append(out, uploadedFile{
			Name:    stamped,
			Path:    abs,
			Mention: h.uploadMention(workPath, abs),
			Size:    n,
		})
	}
	if len(out) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("no file parts"))
		return
	}
	h.trimSessionUploads(dir)
	writeJSON(w, map[string]any{"files": out})
}

// uploadMention renders the path the composer inserts for an uploaded file:
// work-path relative when the file sits inside it, absolute otherwise. Both
// resolve through /api/fs/read, and a relative mention stays short.
func (h *handler) uploadMention(workPath, abs string) string {
	if workPath == "" {
		return abs
	}
	rabs, err := filepath.Abs(workPath)
	if err != nil {
		return abs
	}
	if real, err := filepath.EvalSymlinks(rabs); err == nil {
		rabs = real
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	rel, err := filepath.Rel(rabs, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return abs
	}
	return filepath.ToSlash(rel)
}

// trimSessionUploads keeps a session's uploads under its total budget by
// dropping the oldest files first. Best-effort: a failed trim is not a failed
// upload.
func (h *handler) trimSessionUploads(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type up struct {
		name string
		size int64
		mod  time.Time
	}
	var files []up
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if info, err := e.Info(); err == nil {
			files = append(files, up{e.Name(), info.Size(), info.ModTime()})
			total += info.Size()
		}
	}
	if total <= maxUploadTotalBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files {
		if total <= maxUploadTotalBytes {
			return
		}
		if os.Remove(filepath.Join(dir, f.name)) == nil {
			total -= f.size
		}
	}
}
