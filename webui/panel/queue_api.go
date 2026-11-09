// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/i18n"
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
	// Node is the board's device picker: the operator chose a machine, so it
	// is a HARD pin, not a scoring hint. Empty leaves routing to the scheduler.
	// A soft hint would let localBias win whenever the chosen device is
	// offline — the task then ran in this node's agent CLI while the UI still
	// showed the other device as its destination. See docs/routing-layers.md.
	Node string `json:"node"`
	// Same-tree parallelism declaration — the same semantics a delegate
	// marker carries: paths names the project-relative lanes this task holds
	// (disjoint siblings run in parallel), isolate gives it a private copy
	// that merges back on completion. Both need a project to mean anything.
	ScopePaths []string `json:"scope_paths"`
	Isolate    bool     `json:"isolate"`
	// WorkDir pins the task to a directory of its own — one business domain
	// per directory. The task then holds only that directory's lock (it never
	// queues behind siblings in the project's tree) and runs on THIS node
	// only, since a peer has no such tree to run in. Mutually exclusive with
	// the two lane modes above, which describe the project's shared tree.
	WorkDir string `json:"workdir"`
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
	// The parallel-mode fields carry the same contract the CLI flags do:
	// they only mean something with a project to scope, and scope lanes and
	// the private copy are mutually exclusive (the copy ignores paths
	// entirely). Reject rather than silently degrade — a submitted lane the
	// row never honors is the worse failure.
	trimmedPaths := req.ScopePaths[:0]
	for _, p := range req.ScopePaths {
		if p = strings.TrimSpace(p); p != "" {
			trimmedPaths = append(trimmedPaths, p)
		}
	}
	if (len(trimmedPaths) > 0 || req.Isolate) && strings.TrimSpace(req.Project) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("scope_paths/isolate require a project"))
		return
	}
	if len(trimmedPaths) > 0 && req.Isolate {
		writeErr(w, http.StatusBadRequest, errors.New("scope_paths and isolate are mutually exclusive"))
		return
	}
	// A pinned work directory is a different tree, so the shared-tree lane modes
	// contradict it: paths are project-relative (an absolute tree has no lanes)
	// and isolate copies the project's tree (not the pinned one). Unlike them it
	// does NOT require a project — an anonymous task may name its own directory.
	req.WorkDir = strings.TrimSpace(req.WorkDir)
	if req.WorkDir != "" {
		if len(trimmedPaths) > 0 || req.Isolate {
			writeErr(w, http.StatusBadRequest, errors.New("workdir cannot combine with scope_paths or isolate"))
			return
		}
		abs, aerr := filepath.Abs(req.WorkDir)
		if aerr != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("workdir: %w", aerr))
			return
		}
		req.WorkDir = abs
	}
	// Hand the tail the trimmed lanes rather than the raw field: trimming
	// reuses req.ScopePaths' backing array and only shortens the slice, so the
	// field still carries the pre-trim tail. The core normalizes paths anyway,
	// but a caller should not have to know that to read this correctly.
	req.ScopePaths = trimmedPaths

	taskID, sessionID, state, warn, err := h.enqueueBoardTask(r.Context(), eng, req, priority)
	if err != nil {
		writeEnqueueErr(w, err)
		return
	}
	// warn carries the dead-queue advisory ("no consumer will drain this
	// store") — the board surfaces it as a toast so an accepted task that can
	// never run never reads as a success.
	writeJSON(w, map[string]string{"task_id": taskID, "session_id": sessionID, "state": state, "warning": warn})
}

// enqueueBoardTask is the shared tail of every "a human asked for a task"
// path: POST /api/tasks and the to-do board's 开始 (POST /api/todos/{id}/start).
// boardTaskInput is the board's device-picker contract, kept as a pure seam
// so a regression cannot quietly turn an operator's device choice back into a
// scoring hint again. req.Node must become TargetNode (hard pin): as a soft
// PreferredNode it was only honoured while the named device was online and
// capable, and an offline device handed the task to localBias — i.e. to this
// machine's own agent CLI, with the UI still showing the other device.
func boardTaskInput(req createTaskRequest, requires []string) core.TaskInput {
	return core.TaskInput{
		Title:      req.Title,
		Project:    strings.TrimSpace(req.Project),
		Intent:     req.Prompt,
		Requires:   requires,
		Authorized: req.Authorize,
		TargetNode: req.Node,
		Paths:      req.ScopePaths,
		Isolate:    req.Isolate,
		WorkDir:    req.WorkDir,
	}
}

// It enqueues the row and mints the linked session so the card can jump into
// the thread where the work streams.
//
// The caller keeps the validation (it knows which fields its surface offers);
// this function owns the contract both surfaces must honour — no WorkDir, so
// a task whose `requires` names another device's ability can still leave this
// node, and the session created only after a successful enqueue so a failure
// leaves no orphan thread behind.
func (h *handler) enqueueBoardTask(
	ctx context.Context,
	eng *askengine.Engine,
	req createTaskRequest,
	priority int,
) (taskID, sessionID, state, warn string, err error) {
	// The routing ability both surfaces fall back to. It lives here rather
	// than in the callers because it is part of the contract this function
	// owns: a board task with no `requires` still has to route somewhere, and
	// a surface that forgets to say so must not file a task nothing can claim.
	requires := req.Requires
	if len(requires) == 0 {
		requires = defaultBoardRequires
	}
	in := boardTaskInput(req, requires)
	q := core.DefaultQueueSpec()
	q.Priority = priority
	q.ResourceKeys = req.ResourceKeys
	// Enqueue stamps work_dir from the QueueSpec (SetQueueMeta overwrites the
	// column), so the pin has to ride both surfaces or the row and the lock set
	// end up disagreeing.
	q.WorkDir = req.WorkDir

	task, warn, err := eng.EnqueueTask(ctx, in, q)
	if err != nil {
		return "", "", "", "", err
	}
	if h.sessions != nil {
		if sess, serr := h.sessions.Create(req.Title, req.Project); serr == nil {
			sessionID = sess.ID
			_, _ = h.sessions.AppendTurn(sess.ID, sessions.Turn{Role: "user", Text: req.Prompt})
			_ = h.store.SetSessionID(ctx, task.TaskID, sess.ID)
		}
	}
	return task.TaskID, sessionID, task.State, warn, nil
}

// writeEnqueueErr maps an enqueue failure onto the board's HTTP vocabulary.
func writeEnqueueErr(w http.ResponseWriter, err error) {
	if errors.Is(err, askengine.ErrApprovalDenied) {
		// A remembered deny answered before the row existed: report it as a
		// policy refusal the board can show, not a server fault.
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeErr(w, http.StatusInternalServerError, err)
}

// sendMessageRequest is the body of POST /api/tasks/{id}/messages and
// POST /api/sessions/{id}/messages — the two send modes: a queued note the
// agent reads at its next supervision round, or an injected steer that leads
// the next round and aborts the in-flight one when this console's engine runs
// the task. Attachments are workspace paths the composer already uploaded;
// the store appends one `@path` line each, so the note stays self-contained.
type sendMessageRequest struct {
	Text        string   `json:"text"`
	Attachments []string `json:"attachments"`
	Inject      bool     `json:"inject"`
}

// sendTaskMessage serves POST /api/tasks/{id}/messages (steer.go): deliver a
// user message into a running or queued task. The response's status tells the
// surface how it landed — "interrupted" (a live round was aborted, the text
// leads the next one) or "queued" (the note waits for the next round or the
// task's first run). The message itself is durable either way.
func (h *handler) sendTaskMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
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
	status := "queued"
	warning := ""
	if eng := h.currentEngine(); eng != nil {
		res, serr := eng.SendTaskNote(r.Context(), id, req.Text, req.Attachments, req.Inject)
		if serr != nil {
			writeTaskMessageErr(w, serr)
			return
		}
		status, warning = res.Status, res.Warning
	} else {
		if req.Inject {
			writeErr(w, http.StatusServiceUnavailable, errors.New("inject requires the ask engine (set model.base_url to enable)"))
			return
		}
		grade := core.MsgGradeStatus
		nodeID := ""
		if h.cfg != nil {
			nodeID = core.RuntimeNodeID(h.cfg.Node.Name, h.cfg.Node.Kind, h.cfg.Node.EffectiveIdentity())
		}
		if _, err := h.store.PostUserNote(r.Context(), id, req.Text, req.Attachments, grade, nodeID); err != nil {
			writeTaskMessageErr(w, err)
			return
		}
		// No engine means this console is not draining the queue either —
		// its own panel port would fool QueueConsumerAlive, so the row's
		// heartbeat stamp is the honest probe instead. Probe through the
		// store rather than a separately wired DB handle: embedders may pass
		// only the task store, and the row lives in the same database the
		// note was just posted to.
		if nodeID != "" && h.store != nil {
			if t, terr := h.store.Get(r.Context(), id); terr == nil &&
				t.State == core.StateQueued && !h.store.StoreRowHeartbeating(r.Context(), nodeID) {
				warning = i18n.T(i18n.Parse(h.cfg.UI.Locale), "cli.queue.noConsumer")
			}
		}
	}
	writeJSON(w, map[string]string{"task_id": id, "status": status, "warning": warning})
}

// listTaskMessages serves GET /api/tasks/{id}/messages — the read side of the
// send endpoint: everything currently on the task's own board scope, which is
// where queued user notes, steer posts and sibling-task messages live. A send
// that only accepted and never showed would leave the queue invisible; the
// detail view polls this alongside the task row so a posted note renders as
// soon as it lands. Unknown ids answer 404; a live board on an empty scope
// answers an empty list.
func (h *handler) listTaskMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.store.Get(r.Context(), id); err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such task"))
		return
	}
	msgs, err := h.store.TaskMessages(r.Context(), "task:"+id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if msgs == nil {
		msgs = []core.TaskMessage{}
	}
	writeJSON(w, map[string]any{"task_id": id, "messages": msgs})
}

// writeTaskMessageErr maps a message-delivery failure onto the right status:
// unknown task → 404, terminal or remote-executor refusals → 409 (a state
// conflict the user must resolve), anything else → 500.
func writeTaskMessageErr(w http.ResponseWriter, err error) {
	msg := err.Error()
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeErr(w, http.StatusNotFound, errors.New("no such task"))
	case strings.Contains(msg, "no such note"):
		writeErr(w, http.StatusNotFound, errors.New(msg))
	case strings.Contains(msg, "already read"):
		// The round folded this note into a prompt; editing or dropping it now
		// would leave the board disagreeing with what the model was told.
		writeErr(w, http.StatusConflict, errors.New(msg))
	case strings.Contains(msg, "messages reach running or queued work only"),
		strings.Contains(msg, "runs on "):
		writeErr(w, http.StatusConflict, errors.New(msg))
	default:
		writeErr(w, http.StatusInternalServerError, errors.New("send failed: "+msg))
	}
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

// deleteTask serves DELETE /api/tasks/{id} — removes a task and its subtree,
// the web equivalent of `panda task delete`. Deletable states only (queued or
// finished); a task still moving answers 409 so the board can tell the user to
// cancel first instead of silently failing.
func (h *handler) deleteTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	n, err := h.store.Delete(r.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			writeErr(w, http.StatusNotFound, errors.New("no such task"))
		case errors.Is(err, core.ErrTaskActive):
			writeErr(w, http.StatusConflict, errors.New("task is active — cancel it first, then delete"))
		default:
			writeErr(w, http.StatusInternalServerError, errors.New("delete failed"))
		}
		return
	}
	writeJSON(w, map[string]any{"id": id, "deleted": n})
}

// revertTask serves POST /api/tasks/{id}/revert — the web equivalent of
// `panda task revert`: rolls the run's git checkout back to the whole-tree
// baseline the executor saved before the run began (refs/heads/panda-base-<id>).
// The baseline lives on whichever checkout executed the task, so the handler
// probes the task's recorded work dirs before the shared work path — exactly
// like the CLI. 404 when no baseline exists (remote run, already consumed, or
// non-repo work dir).
func (h *handler) revertTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, err := h.store.Get(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such task"))
		return
	}
	var workPath string
	h.readCfg(func(c *config.Config) { workPath = c.Storage.WorkPath })
	seen := map[string]bool{}
	for _, dir := range []string{t.ExecWorkDir, t.WorkDir, workPath} {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		restored, removed, rerr := defense.RollbackGitBaseline(dir, id)
		if errors.Is(rerr, defense.ErrNoGitBaseline) {
			continue
		}
		if rerr != nil {
			writeErr(w, http.StatusInternalServerError, rerr)
			return
		}
		writeJSON(w, map[string]any{
			"id": id, "reverted": true,
			"restored": restored, "removed": removed, "dir": dir,
		})
		return
	}
	writeErr(w, http.StatusNotFound,
		errors.New("no git baseline for this task — it ran remotely, in a non-repo dir, or its baseline was already consumed"))
}

// clearTasks serves DELETE /api/tasks — the board's one-click wipe: cancel
// everything still moving, then delete every task record (the web equivalent
// of `panda queue clear`). Cancels travel through the ask engine when one is
// configured so remote executors hear them too; without an engine the rows are
// still cleared and running work ends on its own lease.
func (h *handler) clearTasks(w http.ResponseWriter, r *http.Request) {
	n, err := h.store.CountByState(r.Context(), "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("count tasks failed"))
		return
	}
	if n == 0 {
		writeJSON(w, map[string]int{"cancelled": 0, "deleted": 0})
		return
	}
	if eng := h.currentEngine(); eng != nil {
		// Only the active set can still be executing — settled rows have
		// nothing left to cancel.
		if active, lerr := h.store.ListActive(r.Context()); lerr == nil {
			for _, t := range active {
				_, _ = eng.CancelTask(r.Context(), t.TaskID)
			}
		}
	}
	cancelled, deleted, err := h.store.ClearQueue(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("clear queue failed"))
		return
	}
	writeJSON(w, map[string]int{"cancelled": cancelled, "deleted": deleted})
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
