package panel

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// defaultBoardRequires is the ability a board-created task routes on when the
// request carries no explicit requires: every configured agent CLI declares
// "coding" (see detect.go), so a user-typed task lands on an agent.
var defaultBoardRequires = []string{"coding"}

// createTaskRequest is the body of POST /api/tasks — the board's "new task"
// form. Prompt doubles as the task intent and the linked session's first
// message.
type createTaskRequest struct {
	Title        string   `json:"title"`
	Prompt       string   `json:"prompt"`   // empty falls back to title
	Priority     string   `json:"priority"` // "high"|"normal"|"low"; default normal
	Project      string   `json:"project"`
	ResourceKeys []string `json:"resource_keys"`
	Requires     []string `json:"requires"`
	Authorize    bool     `json:"authorize"`
}

// createTask serves POST /api/tasks (queue redesign): enqueue a user task and
// auto-create its linked session (title = task title, prompt = first user
// turn). The response carries both ids so the board can jump straight into
// the session. Needs the ask engine with a capability card — the same
// requirement as task execution via /api/ask.
func (h *handler) createTask(w http.ResponseWriter, r *http.Request) {
	eng := h.currentEngine()
	if eng == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("task creation not configured (set model.base_url to enable)"))
		return
	}
	var req createTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Title == "" {
		writeErr(w, http.StatusBadRequest, errors.New("title must not be empty"))
		return
	}
	if req.Prompt == "" {
		req.Prompt = req.Title
	}
	priority := core.PriorityNormal
	if req.Priority != "" {
		var ok bool
		priority, ok = parsePriority(req.Priority)
		if !ok {
			writeErr(w, http.StatusBadRequest, errors.New("priority must be high, normal or low"))
			return
		}
	}
	requires := req.Requires
	if len(requires) == 0 {
		requires = defaultBoardRequires
	}

	in := core.TaskInput{
		Title:      req.Title,
		Project:    req.Project,
		Intent:     req.Prompt,
		Requires:   requires,
		Authorized: req.Authorize,
	}
	q := core.DefaultQueueSpec()
	q.Priority = priority
	q.ResourceKeys = req.ResourceKeys
	// No WorkDir: the node-wide default the executor falls back to is the same
	// value eng.WorkPath() would pin, and a pinned task never forwards to a
	// peer — a board task with `requires` pointing at another device's ability
	// must be able to leave this node.

	task, err := eng.EnqueueTask(r.Context(), in, q)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	// Linked session: the board card jumps into it and the task's progress
	// streams there. Created after a successful enqueue so a failure leaves
	// no orphan session behind.
	sessionID := ""
	if h.sessions != nil {
		if sess, err := h.sessions.Create(req.Title); err == nil {
			sessionID = sess.ID
			_, _ = h.sessions.AppendTurn(sess.ID, sessions.Turn{Role: "user", Text: req.Prompt})
			_ = h.store.SetSessionID(r.Context(), task.TaskID, sess.ID)
		}
	}
	writeJSON(w, map[string]string{"task_id": task.TaskID, "session_id": sessionID, "state": task.State})
}

// patchTaskRequest is the body of PATCH /api/tasks/{id}.
type patchTaskRequest struct {
	Priority string `json:"priority"` // "high"|"normal"|"low"
}

// patchTask serves PATCH /api/tasks/{id}: quick priority setting from the
// board card (queue redesign).
func (h *handler) patchTask(w http.ResponseWriter, r *http.Request) {
	var req patchTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	priority, ok := parsePriority(req.Priority)
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("priority must be high, normal or low"))
		return
	}
	id := r.PathValue("id")
	if err := h.store.SetPriority(r.Context(), id, priority); err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("set priority failed"))
		return
	}
	writeJSON(w, map[string]string{"id": id, "priority": priorityLabel(priority)})
}

// reorderTasksRequest is the body of POST /api/tasks/reorder.
type reorderTasksRequest struct {
	IDs []string `json:"ids"`
}

// reorderTasks serves POST /api/tasks/reorder: the board's drag order — ids
// arrive top-to-bottom and get seq 1..n, which the scheduler honours ahead of
// priority/FIFO. Unknown ids are skipped so a stale board cannot fail the
// whole reorder.
func (h *handler) reorderTasks(w http.ResponseWriter, r *http.Request) {
	var req reorderTasksRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	if len(req.IDs) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("ids must not be empty"))
		return
	}
	applied := 0
	for i, id := range req.IDs {
		if err := h.store.SetSeq(r.Context(), id, int64(i+1)); err != nil {
			continue // stale board entry; keep the rest
		}
		applied++
	}
	writeJSON(w, map[string]int{"updated": applied})
}
