// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/todos"
)

// The to-do board's HTTP surface: a small CRUD over internal/todos plus the
// one action that crosses into the task plane — 开始, which enqueues a task
// from a todo and links the two rows.
//
// A todo is not a task (see internal/todos): the queue's own 待办事项 column
// holds submitted tasks that drain themselves, while these rows wait for a
// human. That is why the board lives on /api/todos and never reuses
// /api/tasks — a todo must not be claimable by the scheduler.

// parseDueAt accepts "" (no deadline), an RFC3339 stamp, or a bare
// YYYY-MM-DD date. A date-only deadline lands at the END of that local day:
// a todo due "today" must not read as overdue at noon, which is exactly what
// midnight-of-that-day would produce.
func parseDueAt(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.Unix(), nil
	}
	if d, err := time.ParseInLocation("2006-01-02", raw, time.Local); err == nil {
		return d.Add(24*time.Hour - time.Second).Unix(), nil
	}
	return 0, errors.New("due_at must be RFC3339 or YYYY-MM-DD")
}

// todoID reads the {id} path value.
func todoID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid todo id")
	}
	return id, nil
}

// listTodos serves GET /api/todos?status=&project=&q= — the whole board in one
// read. Filters are applied server-side because the page paints four lanes
// from a single fetch.
func (h *handler) listTodos(w http.ResponseWriter, r *http.Request) {
	if h.todos == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("to-do board not configured"))
		return
	}
	q := r.URL.Query()
	list, err := h.todos.List(r.Context(), todos.Filter{
		Status:  q.Get("status"),
		Project: q.Get("project"),
		Query:   q.Get("q"),
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// An empty board must serialize as [] — a null makes the client's
	// `todos ?? []` guard work by accident and hides a real empty result.
	if list == nil {
		list = []todos.Todo{}
	}
	writeJSON(w, list)
}

// createTodoRequest is the body of POST /api/todos.
type createTodoRequest struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	Priority    string   `json:"priority"`
	DueAt       string   `json:"due_at"` // "" | YYYY-MM-DD | RFC3339
	Tags        []string `json:"tags"`
	Project     string   `json:"project"`
}

// createTodo serves POST /api/todos — the board's inline "新建待办".
func (h *handler) createTodo(w http.ResponseWriter, r *http.Request) {
	if h.todos == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("to-do board not configured"))
		return
	}
	var req createTodoRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	due, err := parseDueAt(req.DueAt)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	t, err := h.todos.Create(r.Context(), todos.CreateInput{
		Title:       req.Title,
		Description: req.Description,
		Status:      req.Status,
		Priority:    req.Priority,
		DueAt:       due,
		Tags:        req.Tags,
		Project:     req.Project,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, t)
}

// todoPatchRequest is the body of PATCH /api/todos/{id}. Every field is a
// pointer so "leave it alone" and "clear it" stay distinguishable.
type todoPatchRequest struct {
	Title       *string   `json:"title"`
	Description *string   `json:"description"`
	Status      *string   `json:"status"`
	Priority    *string   `json:"priority"`
	DueAt       *string   `json:"due_at"`
	Tags        *[]string `json:"tags"`
	Project     *string   `json:"project"`
	Seq         *int64    `json:"seq"`
}

// patchTodo serves PATCH /api/todos/{id} — every inline edit on the board
// (tick, drag to another lane, priority cycle, due date, tags).
func (h *handler) patchTodo(w http.ResponseWriter, r *http.Request) {
	if h.todos == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("to-do board not configured"))
		return
	}
	id, err := todoID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var req todoPatchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	p := todos.Patch{
		Title:       req.Title,
		Description: req.Description,
		Status:      req.Status,
		Priority:    req.Priority,
		Tags:        req.Tags,
		Project:     req.Project,
		Seq:         req.Seq,
	}
	if req.DueAt != nil {
		due, err := parseDueAt(*req.DueAt)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		p.DueAt = &due
	}
	t, err := h.todos.Patch(r.Context(), id, p)
	if errors.Is(err, todos.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, t)
}

// deleteTodo serves DELETE /api/todos/{id}. A started task is untouched:
// deleting the note must not cancel work that is already running.
func (h *handler) deleteTodo(w http.ResponseWriter, r *http.Request) {
	if h.todos == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("to-do board not configured"))
		return
	}
	id, err := todoID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ok, err := h.todos.Delete(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, todos.ErrNotFound)
		return
	}
	writeJSON(w, map[string]any{"deleted": id})
}

// reorderTodos serves POST /api/todos/reorder — the board's drag order. The
// client sends the whole lane, exactly like /api/tasks/reorder.
func (h *handler) reorderTodos(w http.ResponseWriter, r *http.Request) {
	if h.todos == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("to-do board not configured"))
		return
	}
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	if len(req.IDs) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("ids must not be empty"))
		return
	}
	if err := h.todos.Reorder(r.Context(), req.IDs); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"reordered": len(req.IDs)})
}

// startTodo serves POST /api/todos/{id}/start — 交给熊猫: enqueue a task from
// this todo and link the two. The todo moves to 进行中 with its task_id set;
// the task is a normal board task (same requirements, same linked session), so
// it shows up in 队列·任务·项目 like any other.
//
// A second start is refused rather than silently minting a duplicate task: the
// card's button becomes a link to the running task once task_id is set.
func (h *handler) startTodo(w http.ResponseWriter, r *http.Request) {
	if h.todos == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("to-do board not configured"))
		return
	}
	id, err := todoID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	t, err := h.todos.Get(r.Context(), id)
	if errors.Is(err, todos.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if t.TaskID != "" {
		writeErr(w, http.StatusConflict, errors.New("this todo already has a task"))
		return
	}
	eng := h.currentEngine()
	if eng == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("task creation not configured (set model.base_url to enable)"))
		return
	}
	// The todo's description is the task's intent — that is the whole point of
	// writing it down. A todo with no description still carries a usable
	// title, which the queue falls back to.
	prompt := strings.TrimSpace(t.Description)
	if prompt == "" {
		prompt = t.Title
	}
	priority, ok := parsePriority(t.Priority)
	if !ok {
		priority = core.PriorityNormal
	}
	req := createTaskRequest{
		Title:    t.Title,
		Prompt:   prompt,
		Project:  t.Project,
		Requires: defaultBoardRequires,
	}
	taskID, sessionID, state, warn, err := h.enqueueBoardTask(r.Context(), eng, req, priority)
	if err != nil {
		writeEnqueueErr(w, err)
		return
	}
	running := todos.StatusRunning
	tid := taskID
	updated, err := h.todos.Patch(r.Context(), id, todos.Patch{Status: &running, TaskID: &tid})
	if err != nil {
		// The task exists and will run; the link is the only thing missing.
		// Report the task rather than a 500 — the board can retry the tick.
		writeJSON(w, map[string]any{
			"task_id": taskID, "session_id": sessionID, "state": state,
			"warning": warn, "link_error": err.Error(),
		})
		return
	}
	writeJSON(w, map[string]any{
		"todo": updated, "task_id": taskID, "session_id": sessionID,
		"state": state, "warning": warn,
	})
}

// cachedTodoFingerprint digests the board for the SSE change feed, behind the
// same shared cache the task/node scans use.
func (h *handler) cachedTodoFingerprint() string {
	if h.todos == nil {
		return ""
	}
	fp, _ := sharedFingerprints.get("todos", func() (string, error) {
		return h.todos.Fingerprint(context.Background())
	})
	return fp
}
