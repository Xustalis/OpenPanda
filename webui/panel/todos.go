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

	"github.com/Xustalis/OpenPanda/internal/sessions"
	"github.com/Xustalis/OpenPanda/internal/taskhub"
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

// dateOnlyLayout is the accepted bare-date form.
const dateOnlyLayout = "2006-01-02"

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
	// Parse without a location on purpose: the date components must be the
	// ones the user wrote. See endOfLocalDay.
	if d, err := time.Parse(dateOnlyLayout, raw); err == nil {
		return endOfLocalDay(d, time.Local).Unix(), nil
	}
	return 0, errors.New("due_at must be RFC3339 or YYYY-MM-DD")
}

// endOfLocalDay is the last second of d's calendar day in loc.
//
// d carries the date as written, which is why the bare date is parsed without
// a location: zones that start DST at midnight (America/Havana,
// America/Santiago) have no 00:00 on the switch day, and ParseInLocation
// resolves that gap backwards — to 23:00 the evening before — so the parsed
// Date() is the previous day and the deadline would slide a day early.
//
// Naming the wall-clock instant, rather than adding a fixed 24h to midnight,
// keeps the deadline inside the chosen day: a calendar day is 23 or 25 hours
// long across a DST switch, so midnight+24h-1s lands at 00:59:59 the next day
// (spring forward) or 22:59:59 the same day (fall back) — an hour late in
// March, an hour early in November.
func endOfLocalDay(d time.Time, loc *time.Location) time.Time {
	y, m, dd := d.Date()
	return time.Date(y, m, dd, 23, 59, 59, 0, loc)
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
// startTodo retains the old response fields and uses the shared atomic service.
func (h *handler) startTodo(w http.ResponseWriter, r *http.Request) {
	if h.todos == nil {
		writeErr(w, 503, errors.New("to-do board not configured"))
		return
	}
	id, err := todoID(r)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	eng := h.currentEngine()
	if eng == nil {
		writeErr(w, 503, errors.New("task execution is not configured"))
		return
	}
	result, err := taskhub.New(h.todos.DB()).StartLocal(r.Context(), eng, id, taskhub.RunOptions{})
	if err != nil {
		writeWorkError(w, err)
		return
	}
	sessionID := result.Task.SessionID
	if !result.AlreadyStarted && h.sessions != nil && result.Todo != nil {
		t := result.Todo
		if sess, serr := h.sessions.Create(t.Title, t.Project); serr == nil {
			sessionID = sess.ID
			prompt := strings.TrimSpace(t.Description)
			if prompt == "" {
				prompt = t.Title
			}
			_, _ = h.sessions.AppendTurn(sess.ID, sessions.Turn{Role: "user", Text: prompt})
			_ = h.store.SetSessionID(r.Context(), result.Task.TaskID, sess.ID)
		}
	}
	writeJSON(w, map[string]any{"todo": result.Todo, "task_id": result.Task.TaskID, "session_id": sessionID, "state": result.Task.State, "warning": result.Warning, "alreadyStarted": result.AlreadyStarted, "requestId": result.RequestID})
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
