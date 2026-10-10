// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	"github.com/Xustalis/OpenPanda/internal/util"
)

// ---- Session CRUD ----

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
	writeJSON(w, list)
}

// createSession serves POST /api/sessions — starts a session, carving its git
// worktree when the work path is a repository (isolation is best-effort: a
// non-repo work path still gets a session, just without a worktree). If
// project is not specified, it inherits the active project if set.
func (h *handler) createSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title   string `json:"title"`
		Project string `json:"project"`
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
	sess, err := h.sessions.Create(strings.TrimSpace(req.Title), project)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if h.worktrees != nil {
		if path, err := h.worktrees.Ensure(r.Context(), sess.ID); err == nil {
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
	if h.worktrees != nil {
		base := "HEAD"
		if parent.Branch != "" {
			base = parent.Branch
		}
		if path, err := h.worktrees.EnsureFrom(r.Context(), child.ID, base); err == nil {
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
	writeJSON(w, sess)
}

// deleteSession serves DELETE /api/sessions/{id} — removes the worktree (and
// its branch) plus the stored thread.
func (h *handler) deleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if h.worktrees != nil {
		_ = h.worktrees.Remove(r.Context(), id)
	}
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
}

// patchSession serves PATCH /api/sessions/{id} — updates title, project
// association, or the pinned flag.
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
		if h.worktrees != nil {
			_ = h.worktrees.Remove(r.Context(), id)
		}
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
// changes (porcelain status per file) plus a capped unified patch, so the web
// can show "what did this session do" before deciding to merge.
func (h *handler) sessionDiff(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	changes, err := h.worktrees.Status(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	patch, err := h.worktrees.Diff(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if changes == nil {
		changes = []sessions.Change{}
	}
	writeJSON(w, map[string]any{
		"id":      id,
		"branch":  sessions.Branch(id),
		"changes": changes,
		"patch":   patch,
	})
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
	var req sessionMergeRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req) // empty body = default message
	}
	subject, err := h.worktrees.Merge(r.Context(), id, req.Message)
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
	writeJSON(w, map[string]any{"id": id, "operation_id": req.OperationID, "cancelled": cancelled})
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

// sessionAskRequest is the body of POST /api/sessions/{id}/ask.
type sessionAskRequest struct {
	Prompt    string `json:"prompt"`
	Authorize bool   `json:"authorize"`
	// Node is the composer's environment pick: the preferred executor node
	// (id or name) for any task this ask spawns. Empty lets the scheduler
	// route by capability, which is also the right behavior for plans.
	Node string `json:"node"`
}

// sessionAsk serves POST /api/sessions/{id}/ask as a Server-Sent Events
// stream: the conversation runs with the session's full history, and the
// session's git worktree is the execution directory for any classified task.
// Events: "reasoning" (chain-of-thought chunk), "delta" (answer text chunk),
// "status" (one-line progress), "result" (the final askResult), "error".
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
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		writeErr(w, http.StatusBadRequest, errors.New("prompt must not be empty"))
		return
	}
	eng := h.currentEngine()
	if eng == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("ask engine not configured (configure the model in Settings)"))
		return
	}
	sess, err := h.sessions.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such session"))
		return
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

	go func() {
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
	}()

	// History is the thread as it stands, auto-compacted when it overflows
	// the replay budget (the compacted prefix survives as sess.Summary and
	// replays as a leading context note). The user turn is persisted first
	// (a failed ask still leaves the question in the thread, like codex/claude
	// code) but excluded from the replay: AskTurns carries the prompt itself,
	// so replaying the persisted copy too would send two consecutive user
	// messages — a 400 from strict providers.
	var history []entry.Turn
	if updated, err := h.sessions.CompactHistory(r.Context(), sess.ID, sessions.DefaultHistoryBudget,
		func(ctx context.Context, evicted []sessions.Turn) (string, error) {
			turns := make([]entry.Turn, 0, len(evicted))
			for _, t := range evicted {
				turns = append(turns, entry.Turn{Role: t.Role, Content: t.Text})
			}
			return eng.SummarizeTurns(ctx, turns)
		}); err == nil {
		sess = updated
	}
	if sess.Summary != "" {
		history = append(history, entry.Turn{Role: "user", Content: sessions.SummaryMarker + "\n" + sess.Summary})
	}
	// The replay stays budget-bounded even when compaction could not run
	// (summarize failed or thread fits): the stored thread keeps every turn,
	// only the model's view is trimmed to the tail that fits.
	_, kept := sessions.SplitForBudget(sess.Turns, sessions.DefaultHistoryBudget)
	for _, t := range kept {
		history = append(history, entry.Turn{Role: t.Role, Content: t.Text})
	}
	if _, err := h.sessions.AppendTurn(sess.ID, sessions.Turn{Role: "user", Text: req.Prompt}); err != nil {
		send("error", map[string]string{"message": "save turn failed"})
		return
	}

	cb := askengine.StreamCallbacks{
		OnReasoning: func(text string) { send("reasoning", map[string]string{"text": text}) },
		OnDelta:     func(text string) { send("delta", map[string]string{"text": text}) },
		OnStatus:    func(text string) { send("status", map[string]string{"text": text}) },
	}

	// Every panel session is a workspace conversation: repo sessions run in
	// their worktree, non-repo ones in the shared work path. Pinning a
	// non-empty workDir for both keeps the memory wall (§17.2) intact —
	// personal memory never enters a session prompt.
	workDir := sess.Worktree
	if workDir == "" && sess.Project != "" && h.projectStore != nil {
		if p, err := h.projectStore.Get(sess.Project); err == nil && p.WorkDir != "" {
			workDir = p.WorkDir
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
	op, err := h.registerSessionAsk(sess.ID, cancel)
	if err != nil {
		send("error", map[string]string{"message": "create operation failed"})
		return
	}
	defer h.unregisterSessionAsk(sess.ID, op)
	if !send("operation", map[string]string{"operation_id": op.operationID}) {
		// The operation remains detached from the failed response writer and
		// continues to persist its final task/session result.
	}
	if err := h.sessions.StartOperation(sess.ID, sessions.Operation{
		ID: op.operationID, Status: core.StateRunning,
	}); err != nil {
		send("error", map[string]string{"message": "save operation failed"})
		return
	}

	out, err := eng.AskTurnsScoped(execCtx, history, req.Prompt, askengine.AskScope{
		Project:   sess.Project,
		WorkDir:   workDir,
		SessionID: sess.ID,
		Node:      req.Node,
	}, req.Authorize, cb)
	if err != nil {
		msg := err.Error()
		status := core.StateFailed
		if errors.Is(err, context.Canceled) || execCtx.Err() != nil {
			msg = "任务已被用户取消"
			status = core.StateCancelled
		}
		_, _ = h.sessions.SetOperation(sess.ID, sessions.Operation{
			ID: op.operationID, Status: status, Error: msg,
		})
		send("error", map[string]string{"message": msg})
		_, _ = h.sessions.AppendTurn(sess.ID, sessions.Turn{Role: "assistant", Text: "⚠ " + msg, Kind: "error"})
		return
	}

	res := planResultOf(out)
	operationStatus := core.StateDone
	if out.Kind == "task" && out.TaskState != "" {
		operationStatus = out.TaskState
	}
	_, _ = h.sessions.SetOperation(sess.ID, sessions.Operation{
		ID: op.operationID, Status: operationStatus, TaskID: out.TaskID, Error: out.Stderr,
	})
	send("result", res)

	// Queue redesign: bind a spawned task back to this session so the board
	// card can jump into it and the session streams the task's progress.
	if out.Kind == "task" && out.TaskID != "" {
		// Bookkeeping only; the result already streamed to the client.
		_ = h.store.SetSessionID(context.WithoutCancel(r.Context()), out.TaskID, sess.ID)
	}

	turn := sessions.Turn{Role: "assistant", Kind: out.Kind}
	switch out.Kind {
	case "task":
		// Queue mode: the turn records the dispatch (the finalizer folds the
		// outcome as its own turn once the task lands in a terminal state).
		// A bare task id used to be the text — unreadable in the thread and
		// meaningless noise when the next ask replays the history to the
		// model.
		turn.Text = "已派发任务：" + out.TaskTitle + "（" + out.TaskID + "）"
		if out.Answer != "" {
			turn.Text = out.Answer
		}
		turn.Ref = out.TaskID
	case "plan":
		turn.Text = "已启动多阶段计划：" + out.PlanGoal + "（" + out.PlanID + "）"
		turn.Ref = out.PlanID
	default:
		turn.Text = out.Answer
	}
	if _, err := h.sessions.AppendTurn(sess.ID, turn); err != nil {
		// The reply already streamed; a failed bookkeeping write is not worth
		// killing the stream over.
		_ = err
	}
}
