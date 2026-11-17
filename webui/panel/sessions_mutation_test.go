// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// TestDeleteSessionStopsRunningAsk covers both delete entry points: a thread
// that is mid-answer must not outlive its own row. Deleting used to remove the
// stored thread and its worktree while the model call kept streaming — the
// console had already dropped the row, so the Stop button was gone and nothing
// named the request any more.
func TestDeleteSessionStopsRunningAsk(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		name := "single"
		if bulk {
			name = "bulk"
		}
		t.Run(name, func(t *testing.T) {
			store := sessions.NewStore(t.TempDir())
			sess, err := store.Create("running thread", "")
			if err != nil {
				t.Fatal(err)
			}
			h := &handler{sessions: store, activeAsks: map[string]sessionOperation{}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			op, err := h.registerSessionAsk(sess.ID, cancel)
			if err != nil {
				t.Fatal(err)
			}
			// Stands in for the ask goroutine: it returns only when the turn's
			// own work is done, and unregistering is the last thing it does.
			release := make(chan struct{})
			unwound := make(chan struct{})
			go func() {
				<-ctx.Done()
				<-release
				h.unregisterSessionAsk(sess.ID, op)
				close(unwound)
			}()

			deleted := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				rr := httptest.NewRecorder()
				if bulk {
					body := `{"ids":["` + sess.ID + `"]}`
					h.bulkDeleteSession(rr, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
				} else {
					req := httptest.NewRequest(http.MethodDelete, "/", nil)
					req.SetPathValue("id", sess.ID)
					h.deleteSession(rr, req)
				}
				deleted <- rr
			}()

			// The cancel fires at once; the delete then waits for the run to
			// unwind so its worktree is not pulled out from under it.
			select {
			case rr := <-deleted:
				t.Fatalf("delete returned while the ask was still unwinding: %d %s", rr.Code, rr.Body.String())
			case <-time.After(150 * time.Millisecond):
			}
			close(release)

			var rr *httptest.ResponseRecorder
			select {
			case rr = <-deleted:
			case <-time.After(5 * time.Second):
				t.Fatal("delete never returned after the ask unwound")
			}
			if rr.Code != http.StatusOK {
				t.Fatalf("delete failed: %d %s", rr.Code, rr.Body.String())
			}
			if _, err := store.Get(sess.ID); err == nil {
				t.Fatal("thread still stored after a 200 delete")
			}
			select {
			case <-ctx.Done():
			default:
				t.Fatal("deleted thread's ask was never cancelled")
			}
			select {
			case <-unwound:
			default:
				t.Fatal("delete returned before the cancelled ask unwound")
			}
			if h.hasActiveAsk(sess.ID) {
				t.Fatal("cancelled ask still holds the session's slot")
			}
		})
	}
}

// TestDeleteSessionStopsSideAsk: the conversation beside a thread has its own
// operation slot, so deleting the thread has to end that run too.
func TestDeleteSessionStopsSideAsk(t *testing.T) {
	store := sessions.NewStore(t.TempDir())
	sess, err := store.Create("thread with a side chat", "")
	if err != nil {
		t.Fatal(err)
	}
	h := &handler{sessions: store, activeAsks: map[string]sessionOperation{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	op, err := h.registerSessionAsk(sess.ID+sideOperationSuffix, cancel)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		<-ctx.Done()
		h.unregisterSessionAsk(sess.ID+sideOperationSuffix, op)
	}()
	req := httptest.NewRequest(http.MethodDelete, "/", nil)
	req.SetPathValue("id", sess.ID)
	rr := httptest.NewRecorder()
	h.deleteSession(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete failed: %d %s", rr.Code, rr.Body.String())
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("deleted thread's side ask was never cancelled")
	}
}

// TestPatchSessionIsAllOrNothing: the body is one document, so a 400 has to
// mean "nothing changed". The handler used to apply each field as it was
// decoded, which meant a bad permission_mode at the end of the request
// returned 400 with the title it arrived with already saved.
func TestPatchSessionIsAllOrNothing(t *testing.T) {
	store := sessions.NewStore(t.TempDir())
	sess, err := store.Create("original title", "")
	if err != nil {
		t.Fatal(err)
	}
	h := &handler{sessions: store}

	for _, body := range []string{
		// A valid field first, the rejected one last: the old order saved the
		// title before the mode was even looked at.
		`{"title":"changed despite 400","pinned":true,"permission_mode":"definitely-invalid"}`,
		// Rejection first in decode order, a valid field after it.
		`{"team":["not a real agent"],"title":"also changed despite 400"}`,
	} {
		req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
		req.SetPathValue("id", sess.ID)
		rr := httptest.NewRecorder()
		h.patchSession(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("body %s: expected 400, got %d %s", body, rr.Code, rr.Body.String())
		}
		after, err := store.Get(sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Title != "original title" || after.Pinned || after.PermissionMode != "" || len(after.Team) != 0 {
			t.Fatalf("body %s: rejected PATCH still moved the thread: %+v", body, after)
		}
	}

	// The same fields, all valid, land together.
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(
		`{"title":"renamed","pinned":true,"permission_mode":"plan"}`))
	req.SetPathValue("id", sess.ID)
	rr := httptest.NewRecorder()
	h.patchSession(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("valid PATCH: %d %s", rr.Code, rr.Body.String())
	}
	got, err := store.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "renamed" || !got.Pinned || got.PermissionMode != "plan" {
		t.Fatalf("valid PATCH did not land every field: %+v", got)
	}
	// "auto" is stored as "no policy chosen", the same value SetPermissionMode
	// writes — the console must not be able to tell the two paths apart.
	req = httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(`{"permission_mode":"auto"}`))
	req.SetPathValue("id", sess.ID)
	rr = httptest.NewRecorder()
	h.patchSession(rr, req)
	got, err = store.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rr.Code != http.StatusOK || got.PermissionMode != "" {
		t.Fatalf("explicit auto: code=%d mode=%q", rr.Code, got.PermissionMode)
	}
}

// deleteRace is the shared fixture for the delete-vs-ask races: a real handler
// with an SSE model stub that reports when a call starts and when its context is
// cancelled. Every ask in these tests reaches the stub, so "the provider was
// stopped" is observed rather than inferred from a map.
type deleteRace struct {
	h           *handler
	sess        *sessions.Session
	entered     chan struct{}
	stopped     chan struct{}
	stopService func()
}

func newDeleteRace(t *testing.T) *deleteRace {
	t.Helper()
	engine, _ := newApprovalQueueEngine(t)
	serviceCtx, stopService := context.WithCancel(context.Background())
	entered := make(chan struct{})
	stopped := make(chan struct{})
	var enteredOnce, stoppedOnce sync.Once
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"still running"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		enteredOnce.Do(func() { close(entered) })
		<-r.Context().Done()
		stoppedOnce.Do(func() { close(stopped) })
	}))
	t.Cleanup(model.Close)
	t.Cleanup(stopService)
	if err := engine.SetModel(config.ModelConfig{
		APIType: config.APITypeOpenAI, BaseURL: model.URL, Model: "test", NoAuth: true,
	}); err != nil {
		t.Fatal(err)
	}
	store := sessions.NewStore(t.TempDir())
	sess, err := store.Create("delete race")
	if err != nil {
		t.Fatal(err)
	}
	h := &handler{store: engine.TaskStore(), engine: engine, sessions: store,
		cfgMu: &sync.RWMutex{}, serviceCtx: serviceCtx}
	return &deleteRace{h: h, sess: sess, entered: entered, stopped: stopped, stopService: stopService}
}

// delete runs one delete request (single or bulk) in the background.
func (f *deleteRace) delete(bulk bool) <-chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		if bulk {
			body := `{"ids":["` + f.sess.ID + `"]}`
			f.h.bulkDeleteSession(rr, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
		} else {
			req := httptest.NewRequest(http.MethodDelete, "/", nil)
			req.SetPathValue("id", f.sess.ID)
			f.h.deleteSession(rr, req)
		}
		done <- rr
	}()
	return done
}

// ask starts one real turn through the HTTP handler.
func (f *deleteRace) ask(prompt string) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"prompt":"`+prompt+`"}`))
		req.SetPathValue("id", f.sess.ID)
		f.h.sessionAsk(httptest.NewRecorder(), req)
	}()
	return done
}

// TestDeleteSessionCancelsAskAdmittedDuringDrain: the drain window stays open
// for as long as a cancelled run takes to unwind, and the row is still
// readable during it, so a second client can legitimately start a turn. The
// delete has to end that one too — one scan of the active map is not a
// contract, because the next registration lands after it.
func TestDeleteSessionCancelsAskAdmittedDuringDrain(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		name := "single"
		if bulk {
			name = "bulk"
		}
		t.Run(name, func(t *testing.T) {
			f := newDeleteRace(t)
			// The first generation is a latch: cancelled, but still unwinding
			// while the delete waits for it.
			oldCtx, oldCancel := context.WithCancel(context.Background())
			defer oldCancel()
			oldOp, err := f.h.registerSessionAsk(f.sess.ID, oldCancel)
			if err != nil {
				t.Fatal(err)
			}
			deleted := f.delete(bulk)
			select {
			case <-oldCtx.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("delete did not cancel the running ask")
			}
			askDone := f.ask("new request during delete")
			select {
			case <-f.entered:
			case <-time.After(2 * time.Second):
				f.h.unregisterSessionAsk(f.sess.ID, oldOp)
				t.Fatal("the second ask never reached the model")
			}
			f.h.unregisterSessionAsk(f.sess.ID, oldOp)
			rr := waitDelete(t, deleted)
			if rr.Code != http.StatusOK {
				t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
			}
			if _, err := f.h.sessions.Get(f.sess.ID); err == nil {
				t.Fatal("thread still stored after a 200 delete")
			}
			if f.h.hasActiveAsk(f.sess.ID) {
				t.Fatal("an ask admitted during the drain still holds the session")
			}
			select {
			case <-f.stopped:
			case <-time.After(3 * time.Second):
				t.Fatal("the second model call was not cancelled")
			}
			f.stopService()
			select {
			case <-askDone:
			case <-time.After(4 * time.Second):
				t.Fatal("ask did not exit after the service context was cancelled")
			}
		})
	}
}

// TestDeleteSessionDeadlineStillCancelsLateAsk: when a run refuses to unwind,
// the deadline lets the delete finish anyway — but it must still cancel the
// asks that registered while it was waiting. Sealing alone leaves a live model
// call under a row that is already gone.
func TestDeleteSessionDeadlineStillCancelsLateAsk(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		name := "single"
		if bulk {
			name = "bulk"
		}
		t.Run(name, func(t *testing.T) {
			f := newDeleteRace(t)
			// Never released: the delete has to run out its budget.
			oldCtx, oldCancel := context.WithCancel(context.Background())
			defer oldCancel()
			oldOp, err := f.h.registerSessionAsk(f.sess.ID, oldCancel)
			if err != nil {
				t.Fatal(err)
			}
			deleted := f.delete(bulk)
			select {
			case <-oldCtx.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("delete did not cancel the running ask")
			}
			askDone := f.ask("new request during the deadline")
			select {
			case <-f.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("the second ask never reached the model")
			}
			rr := waitDelete(t, deleted)
			if rr.Code != http.StatusOK {
				t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
			}
			if _, err := f.h.sessions.Get(f.sess.ID); err == nil {
				t.Fatal("thread still stored after a 200 delete")
			}
			if f.h.hasActiveAsk(f.sess.ID) {
				t.Fatal("the timeout path left an ask registered under a deleted row")
			}
			select {
			case <-f.stopped:
			case <-time.After(3 * time.Second):
				t.Fatal("the ask admitted during the wait was never cancelled")
			}
			f.stopService()
			select {
			case <-askDone:
			case <-time.After(4 * time.Second):
				t.Fatal("ask did not exit after the service context was cancelled")
			}
			f.h.unregisterSessionAsk(f.sess.ID, oldOp)
		})
	}
}

// waitDelete waits for the delete request to finish.
func waitDelete(t *testing.T, deleted <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case rr := <-deleted:
		return rr
	case <-time.After(10 * time.Second):
		t.Fatal("delete did not finish")
		return nil
	}
}

func TestRegisterSessionAskRefusedWhileDeleting(t *testing.T) {
	h := &handler{sessions: sessions.NewStore(t.TempDir()), activeAsks: map[string]sessionOperation{}}
	h.askMu.Lock()
	h.sealSessionDeleteLocked("sealed")
	h.askMu.Unlock()
	if _, err := h.registerSessionAsk("sealed", func() {}); !errors.Is(err, errSessionDeleting) {
		t.Fatalf("register on a sealed session: err = %v, want errSessionDeleting", err)
	}
	if _, err := h.registerSessionAsk("sealed"+sideOperationSuffix, func() {}); !errors.Is(err, errSessionDeleting) {
		t.Fatalf("side register on a sealed session: err = %v, want errSessionDeleting", err)
	}
	// A delete that failed has to leave the thread usable.
	h.finishSessionDelete("sealed")
	if _, err := h.registerSessionAsk("sealed", func() {}); err != nil {
		t.Fatalf("register after a failed delete: %v", err)
	}
}
