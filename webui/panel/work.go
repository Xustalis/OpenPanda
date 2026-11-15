// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/taskflow"
	"github.com/Xustalis/OpenPanda/internal/taskhub"
	"github.com/Xustalis/OpenPanda/internal/taskproviders"
	"github.com/Xustalis/OpenPanda/internal/todos"
)

var errWorkDecision = errors.New("invalid work decision")

func writeWorkError(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	switch {
	case errors.Is(err, todos.ErrNotFound), errors.Is(err, sql.ErrNoRows):
		code = 404
	case errors.Is(err, core.ErrConflict), errors.Is(err, taskproviders.ErrConflict), errors.Is(err, askengine.ErrApprovalDenied):
		code = 409
	case errors.Is(err, taskflow.ErrStale), errors.Is(err, taskflow.ErrRuntimeHeld):
		code = 409
	case errors.Is(err, core.ErrExecutionUnavailable):
		code = 410
	case errors.Is(err, taskproviders.ErrReconnect):
		code = 401
	case errors.Is(err, taskproviders.ErrAuthorizationChanged):
		code = 403
	case errors.Is(err, taskproviders.ErrUnsupported):
		code = 501
	case errors.Is(err, taskproviders.ErrPrecondition):
		code = 428
	}
	writeErr(w, code, err)
}
func (h *handler) workStore() *taskhub.Store { return taskhub.New(h.db) }

func (h *handler) attachWorkSources(ctx context.Context, tasks []taskJSON) {
	if h.db == nil || len(tasks) == 0 {
		return
	}
	ids := make([]string, len(tasks))
	for i := range tasks {
		ids[i] = tasks[i].ID
	}
	sources, err := h.workStore().Sources(ctx, ids)
	if err != nil {
		return
	}
	for i := range tasks {
		if source, ok := sources[tasks[i].ID]; ok {
			tasks[i].WorkSource = &source
		}
	}
}

func decodeWorkJSON(w http.ResponseWriter, r *http.Request, v any, allowEmpty bool) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	var raw json.RawMessage
	if err := d.Decode(&raw); err != nil {
		if allowEmpty && errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	if err := d.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("expected one JSON object")
	}
	if len(raw) == 0 || raw[0] != '{' {
		return errors.New("expected one JSON object")
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	return strict.Decode(v)
}
func (h *handler) listWork(w http.ResponseWriter, r *http.Request) {
	ts, err := h.workStore().List(r.Context(), r.URL.Query().Get("connection"))
	if err != nil {
		writeWorkError(w, err)
		return
	}
	writeJSON(w, ts)
}
func (h *handler) getWork(w http.ResponseWriter, r *http.Request) {
	t, err := h.workStore().Get(r.Context(), r.URL.Query().Get("connection"), r.PathValue("key"))
	if err != nil {
		writeWorkError(w, err)
		return
	}
	w.Header().Set("ETag", `"`+t.Snapshot.TaskRevision+`"`)
	writeJSON(w, t)
}
func (h *handler) workHistory(w http.ResponseWriter, r *http.Request) {
	x, err := h.workStore().History(r.Context(), r.URL.Query().Get("connection"), r.PathValue("key"))
	if err != nil {
		writeWorkError(w, err)
		return
	}
	writeJSON(w, x)
}
func (h *handler) runWork(w http.ResponseWriter, r *http.Request) {
	key, err := taskproviders.ParseKey(r.PathValue("key"))
	if err != nil {
		writeWorkError(w, err)
		return
	}
	id, err := taskproviders.LocalID(key)
	if err != nil {
		writeWorkError(w, taskproviders.ErrUnsupported)
		return
	}
	var opts taskhub.RunOptions
	if err = decodeWorkJSON(w, r, &opts, true); err != nil {
		writeWorkError(w, err)
		return
	}
	if opts.Purpose == "" {
		opts.Purpose = "rerun"
	}
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		if opts.RequestID != "" && opts.RequestID != key {
			writeWorkError(w, taskproviders.ErrConflict)
			return
		}
		opts.RequestID = key
	}
	if opts.RequestID == "" {
		writeWorkError(w, taskproviders.ErrPrecondition)
		return
	}
	eng := h.currentEngine()
	if eng == nil {
		writeErr(w, 503, errors.New("task execution is not configured"))
		return
	}
	result, err := h.workStore().StartLocal(r.Context(), eng, id, opts)
	if err != nil {
		writeWorkError(w, err)
		return
	}
	writeJSON(w, result)
}
func (h *handler) createWork(w http.ResponseWriter, r *http.Request) {
	var in todos.CreateInput
	if err := decodeWorkJSON(w, r, &in, false); err != nil {
		writeWorkError(w, err)
		return
	}
	t, err := (taskproviders.Local{Store: h.workStore().Todos}).CreateLocal(r.Context(), in, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeWorkError(w, err)
		return
	}
	w.Header().Set("ETag", `"`+taskproviders.LocalSnapshot(t).TaskRevision+`"`)
	writeJSON(w, taskhub.Task{ConnectionID: "local", AccountID: "local", Snapshot: taskproviders.LocalSnapshot(t), Local: &t})
}
func (h *handler) patchWork(w http.ResponseWriter, r *http.Request) {
	k, err := taskproviders.ParseKey(r.PathValue("key"))
	if err != nil {
		writeWorkError(w, err)
		return
	}
	id, err := taskproviders.LocalID(k)
	if err != nil {
		writeWorkError(w, taskproviders.ErrUnsupported)
		return
	}
	var in struct {
		Title       *string   `json:"title"`
		Description *string   `json:"description"`
		Status      *string   `json:"status"`
		Priority    *string   `json:"priority"`
		Project     *string   `json:"project"`
		DueAt       *int64    `json:"due_at"`
		Tags        *[]string `json:"tags"`
	}
	if err = decodeWorkJSON(w, r, &in, false); err != nil {
		writeWorkError(w, err)
		return
	}
	revision := r.Header.Get("If-Match")
	if revision != "" && (!strings.HasPrefix(revision, `"`) || !strings.HasSuffix(revision, `"`) || strings.ContainsAny(strings.Trim(revision, `"`), `",`)) {
		writeWorkError(w, taskproviders.ErrConflict)
		return
	}
	revision = strings.Trim(revision, `"`)
	t, err := (taskproviders.Local{Store: h.workStore().Todos}).PatchLocal(r.Context(), id, revision, todos.Patch{Title: in.Title, Description: in.Description, Status: in.Status, Priority: in.Priority, Project: in.Project, DueAt: in.DueAt, Tags: in.Tags}, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeWorkError(w, err)
		return
	}
	w.Header().Set("ETag", `"`+taskproviders.LocalSnapshot(t).TaskRevision+`"`)
	writeJSON(w, taskhub.Task{ConnectionID: "local", AccountID: "local", Snapshot: taskproviders.LocalSnapshot(t), Local: &t})
}
