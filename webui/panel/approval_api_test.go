package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/core"
	projectstore "github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// waitTaskState polls until the task reaches the wanted state — queue work is
// async by design, so a test that slept a fixed time would flake.
func waitTaskState(t *testing.T, store *core.TaskStore, id, want string) core.Task {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		task, err := store.Get(context.Background(), id)
		if err == nil && task.State == want {
			return task
		}
		if time.Now().After(deadline) {
			last, _ := store.Get(context.Background(), id)
			t.Fatalf("task %s never reached %s (last %+v, err %v)", id, want, last, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// createBoardTask POSTs the board's new-task form and returns the task id.
func createBoardTask(t *testing.T, h http.Handler, project string) string {
	t.Helper()
	body := fmt.Sprintf(`{"title":"run once","prompt":"run once","project":%q,"requires":["test:once"],"authorize":false}`, project)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks", strings.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("create task: %d %s", rr.Code, rr.Body.String())
	}
	var res struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil || res.TaskID == "" {
		t.Fatalf("decode create result: %+v, %v", res, err)
	}
	return res.TaskID
}

// TestApproveScopeProjectAutoConsentsNextTask is the feature end to end: a
// tier-2 board task parks in review, an approve remembered at project scope
// writes the standing consent onto the project row, and the next task for the
// same project runs without ever parking — while a task for a different
// project still parks.
func TestApproveScopeProjectAutoConsentsNextTask(t *testing.T) {
	engine, counter := newApprovalQueueEngine(t)
	store := engine.TaskStore()
	h := New(Deps{Store: store, Engine: engine, StaticDir: t.TempDir(), Token: testToken})

	first := createBoardTask(t, h, "gate")
	waitTaskState(t, store, first, core.StateReview)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks/"+first+"/approve",
		strings.NewReader(`{"scope":"project"}`)))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("approve: %d %s", rr.Code, rr.Body.String())
	}
	waitTaskState(t, store, first, core.StateDone)

	// The remember is visible on the project row, in the session-less bucket
	// every later session of the project reads.
	st := engine.ApprovalState("", "gate")
	if st.Decision != projectstore.DecisionApprove || st.DecisionScope != projectstore.ScopeProject {
		t.Fatalf("ApprovalState = %+v, want approve/project", st)
	}

	// The remembered approve answers the next task's gate: it never parks.
	second := createBoardTask(t, h, "gate")
	waitTaskState(t, store, second, core.StateDone)
	if data, err := os.ReadFile(counter); err != nil || string(data) != "x\nx\n" {
		t.Fatalf("counter = %q, %v — want exactly two runs", data, err)
	}

	// A different project does not inherit it — its tier-2 task still parks.
	other := createBoardTask(t, h, "other")
	waitTaskState(t, store, other, core.StateReview)
}

// TestApproveScopeSessionStaysInItsSession pins the narrower remember: an
// approve remembered at session scope consents for that conversation only —
// a second board task (which always lands in a fresh session) parks again.
func TestApproveScopeSessionStaysInItsSession(t *testing.T) {
	engine, _ := newApprovalQueueEngine(t)
	store := engine.TaskStore()
	sessStore := sessions.NewStore(t.TempDir())
	h := New(Deps{Store: store, Engine: engine, Sessions: sessStore, StaticDir: t.TempDir(), Token: testToken})

	first := createBoardTask(t, h, "gate")
	waitTaskState(t, store, first, core.StateReview)
	task, err := store.Get(context.Background(), first)
	if err != nil || task.SessionID == "" {
		t.Fatalf("task has no linked session: %+v, %v", task, err)
	}

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks/"+first+"/approve",
		strings.NewReader(`{"scope":"session"}`)))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("approve: %d %s", rr.Code, rr.Body.String())
	}
	waitTaskState(t, store, first, core.StateDone)

	// Session-scoped and keyed by (session, project): visible to that session…
	st := engine.ApprovalState(task.SessionID, "gate")
	if st.Decision != projectstore.DecisionApprove || st.DecisionScope != projectstore.ScopeSession {
		t.Fatalf("ApprovalState = %+v, want approve/session", st)
	}
	// …invisible to the next session, so the next board task parks again.
	second := createBoardTask(t, h, "gate")
	waitTaskState(t, store, second, core.StateReview)
}

// TestRejectScopeProjectDeniesNextTask: a remembered deny refuses the gate
// before a row exists — the next create for the project answers 409, and the
// session approval endpoint reports the standing "no".
func TestRejectScopeProjectDeniesNextTask(t *testing.T) {
	engine, counter := newApprovalQueueEngine(t)
	store := engine.TaskStore()
	h := New(Deps{Store: store, Engine: engine, StaticDir: t.TempDir(), Token: testToken})

	first := createBoardTask(t, h, "gate")
	waitTaskState(t, store, first, core.StateReview)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks/"+first+"/reject",
		strings.NewReader(`{"scope":"project","reason":"no"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("reject: %d %s", rr.Code, rr.Body.String())
	}
	st := engine.ApprovalState("", "gate")
	if st.Decision != projectstore.DecisionDeny || st.DecisionScope != projectstore.ScopeProject {
		t.Fatalf("ApprovalState = %+v, want deny/project", st)
	}

	// The remembered deny answers before the row exists: create refuses.
	body := `{"title":"run","prompt":"run","project":"gate","requires":["test:once"],"authorize":false}`
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks", strings.NewReader(body)))
	if rr.Code != http.StatusConflict {
		t.Fatalf("denied create = %d %s, want 409", rr.Code, rr.Body.String())
	}
	if data, err := os.ReadFile(counter); err == nil && len(data) != 0 {
		t.Fatalf("denied scope still ran: %q", data)
	}
}

// TestApproveScopeBadValue fails closed: an unrecognized scope is a 400, not a
// silently-broader remember.
func TestApproveScopeBadValue(t *testing.T) {
	engine, _ := newApprovalQueueEngine(t)
	store := engine.TaskStore()
	h := New(Deps{Store: store, Engine: engine, StaticDir: t.TempDir(), Token: testToken})

	task := createBoardTask(t, h, "gate")
	waitTaskState(t, store, task, core.StateReview)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks/"+task+"/approve",
		strings.NewReader(`{"scope":"forever"}`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad scope = %d %s, want 400", rr.Code, rr.Body.String())
	}
	if st := engine.ApprovalState("", "gate"); st.Decision != "" {
		t.Fatalf("bad scope still remembered: %+v", st)
	}
}

// TestSessionApprovalEndpoints exercises GET/DELETE on the session's resolved
// policy: defaults, a remembered decision surfacing, and the clear returning
// the now-clean state. Unknown sessions are 404; no engine is 503.
func TestSessionApprovalEndpoints(t *testing.T) {
	engine := newQueueEngine(t)
	sessStore := sessions.NewStore(t.TempDir())
	h := New(Deps{Store: engine.TaskStore(), Engine: engine, Sessions: sessStore, StaticDir: t.TempDir(), Token: testToken})
	sess, err := sessStore.Create("chat", "gate")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	get := func() (int, askengine.ApprovalState) {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/sessions/"+sess.ID+"/approval", nil))
		var st askengine.ApprovalState
		_ = json.Unmarshal(rr.Body.Bytes(), &st)
		return rr.Code, st
	}
	code, st := get()
	if code != http.StatusOK || st.Mode != "on-request" || st.Scope != projectstore.ScopeSession || st.Decision != "" {
		t.Fatalf("fresh state = %d %+v", code, st)
	}

	if err := engine.RememberApproval(sess.ID, "gate", projectstore.ScopeSession, projectstore.DecisionApprove); err != nil {
		t.Fatalf("remember: %v", err)
	}
	code, st = get()
	if code != http.StatusOK || st.Decision != projectstore.DecisionApprove || st.DecisionScope != projectstore.ScopeSession {
		t.Fatalf("remembered state = %d %+v", code, st)
	}

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodDelete, "/api/sessions/"+sess.ID+"/approval", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("clear = %d %s", rr.Code, rr.Body.String())
	}
	code, st = get()
	if code != http.StatusOK || st.Decision != "" {
		t.Fatalf("cleared state = %d %+v", code, st)
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/sessions/nope/approval", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown session = %d, want 404", rr.Code)
	}

	noEng := New(Deps{Store: newTestStore(t), Sessions: sessStore, StaticDir: t.TempDir(), Token: testToken})
	rr = httptest.NewRecorder()
	noEng.ServeHTTP(rr, authedReq(http.MethodGet, "/api/sessions/"+sess.ID+"/approval", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("engine-less approval = %d, want 503", rr.Code)
	}
}

// TestPatchProjectApprovalPolicy covers the project's own gate configuration
// over PATCH: mode/scope write through with validation, "inherit" clears the
// override, and DELETE /api/projects/{name}/approval forgets the row's
// remembered decision.
func TestPatchProjectApprovalPolicy(t *testing.T) {
	h, ps, _, _ := projectHandler(t)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/projects", strings.NewReader(`{"name":"gate"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("create = %d %s", rr.Code, rr.Body.String())
	}

	patch := func(body string) (int, projectView) {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, authedReq(http.MethodPatch, "/api/projects/gate", strings.NewReader(body)))
		var v projectView
		_ = json.Unmarshal(rr.Body.Bytes(), &v)
		return rr.Code, v
	}

	code, v := patch(`{"approval_mode":"always","approval_scope":"project"}`)
	if code != http.StatusOK || v.ApprovalMode != "always" || v.ApprovalScope != projectstore.ScopeProject {
		t.Fatalf("patch policy = %d %+v", code, v)
	}
	if code, _ := patch(`{"approval_mode":"bogus"}`); code != http.StatusBadRequest {
		t.Fatalf("bad mode = %d, want 400", code)
	}
	if code, _ := patch(`{"approval_scope":"bogus"}`); code != http.StatusBadRequest {
		t.Fatalf("bad scope = %d, want 400", code)
	}
	code, v = patch(`{"approval_mode":"inherit"}`)
	if code != http.StatusOK || v.ApprovalMode != "" || v.ApprovalScope != projectstore.ScopeProject {
		t.Fatalf("inherit patch = %d %+v, want mode cleared, scope kept", code, v)
	}

	// The remembered decision round-trips through the row and clears on DELETE.
	if err := ps.SetApprovalDecision("gate", projectstore.DecisionDeny); err != nil {
		t.Fatalf("seed decision: %v", err)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodDelete, "/api/projects/gate/approval", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("clear approval = %d %s", rr.Code, rr.Body.String())
	}
	p, err := ps.Get("gate")
	if err != nil || p.ApprovalDecision != "" {
		t.Fatalf("decision after clear = %q, %v", p.ApprovalDecision, err)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodDelete, "/api/projects/nope/approval", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown project clear = %d, want 404", rr.Code)
	}
}
