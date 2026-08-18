package panel

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/xenith/openpanda/internal/askengine"
	"github.com/xenith/openpanda/internal/entry"
	"github.com/xenith/openpanda/internal/sessions"
)

// ---- Session CRUD ----

// listSessions serves GET /api/sessions — every chat session, newest first.
func (h *handler) listSessions(w http.ResponseWriter, r *http.Request) {
	list, err := h.sessions.List()
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
// non-repo work path still gets a session, just without a worktree).
func (h *handler) createSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
			return
		}
	}
	sess, err := h.sessions.Create(strings.TrimSpace(req.Title))
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

// ---- Streaming session ask ----

// sessionAskRequest is the body of POST /api/sessions/{id}/ask.
type sessionAskRequest struct {
	Prompt    string `json:"prompt"`
	Authorize bool   `json:"authorize"`
}

// sessionAsk serves POST /api/sessions/{id}/ask as a Server-Sent Events
// stream: the conversation runs with the session's full history, and the
// session's git worktree is the execution directory for any classified task.
// Events: "delta" (answer text chunk), "status" (one-line progress), "result"
// (the final askResult), "error".
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
	if h.engine == nil {
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
	send := func(event string, v any) bool {
		data, err := json.Marshal(v)
		if err != nil {
			return false
		}
		if _, err := w.Write([]byte("event: " + event + "\ndata: " + string(data) + "\n\n")); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// Persist the user turn immediately (a failed ask still leaves the
	// question in the thread, like codex/claude code), then run with history.
	if _, err := h.sessions.AppendTurn(sess.ID, sessions.Turn{Role: "user", Text: req.Prompt}); err != nil {
		send("error", map[string]string{"message": "save turn failed"})
		return
	}
	var history []entry.Turn
	for _, t := range sess.Turns {
		history = append(history, entry.Turn{Role: t.Role, Content: t.Text})
	}

	cb := askengine.StreamCallbacks{
		OnDelta: func(text string) { send("delta", map[string]string{"text": text}) },
		OnStatus: func(text string) { send("status", map[string]string{"text": text}) },
	}
	out, err := h.engine.AskTurns(r.Context(), history, req.Prompt, sess.Worktree, req.Authorize, cb)
	if err != nil {
		send("error", map[string]string{"message": err.Error()})
		_, _ = h.sessions.AppendTurn(sess.ID, sessions.Turn{Role: "assistant", Text: "⚠ " + err.Error(), Kind: "error"})
		return
	}

	res := askResult{
		Kind:      out.Kind,
		Answer:    out.Answer,
		TaskID:    out.TaskID,
		TaskState: out.TaskState,
		OK:        out.OK,
		Stdout:    out.Stdout,
		Stderr:    out.Stderr,
		ExitCode:  out.ExitCode,
	}
	send("result", res)

	turn := sessions.Turn{Role: "assistant", Kind: out.Kind}
	if out.Kind == "task" {
		turn.Text = out.TaskID
		turn.Ref = out.TaskID
	} else {
		turn.Text = out.Answer
	}
	if _, err := h.sessions.AppendTurn(sess.ID, turn); err != nil {
		// The reply already streamed; a failed bookkeeping write is not worth
		// killing the stream over.
		_ = err
	}
}
