// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

func TestRegressionIntermediateRejudgeProjection(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	parent, err := store.Create(ctx, "", "p", "integrator", "node", []string{"node"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.Create(ctx, parent.TaskID, "p", "intermediate", "node", []string{"node"})
	if err != nil {
		t.Fatal(err)
	}
	spec := `{"objectives":{"version":1,"revision":1,"dimensions":[{"dimension":"visual","weight":1,"priority":"MAX","requirement":"final render","reason":"user request","criteria":["final render verified"]}]}}`
	if err := store.SetDetail(ctx, task.TaskID, core.TaskDetail{SpecJSON: spec}); err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error { return store.Queue(ctx, task.TaskID, "node") },
		func() error { return store.Dispatch(ctx, task.TaskID, "node", "node") },
		func() error { return store.Accept(ctx, task.TaskID, "node") },
		func() error {
			return store.PauseWithResult(ctx, task.TaskID, "node", map[string]any{"ok": true, "objective_assessments": []map[string]any{{"dimension": "visual", "criterion": 0, "status": "deferred", "evidence": "Parent integrator owns final rendering"}}})
		},
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	task, err = store.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if err := core.ObjectiveEvidenceError(task); err != nil {
		t.Fatalf("intermediate deferred evidence should be approvable: %v", err)
	}
	row := taskJSON{State: task.State, ParentID: task.ParentID, ApprovalDisposition: string(core.ApprovalAcceptWork), Spec: task.SpecJSON, Result: task.ResultJSON}
	if needsObjectiveRejudge(row) {
		t.Error("projection forces rejudge even though intermediate evidence passes the real approval gate")
	}
	h := New(Deps{Store: store, Sessions: sessions.NewStore(t.TempDir()), StaticDir: t.TempDir(), Token: testToken})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/tasks/"+task.TaskID+"/rejudge", nil))
	t.Logf("action offered by projection: status=%d body=%s", rr.Code, rr.Body.String())
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "approve it instead") {
		t.Fatalf("expected exact UI/API disagreement; got %d %s", rr.Code, rr.Body.String())
	}
	if err := store.Approve(ctx, task.TaskID); err != nil {
		t.Fatalf("ordinary approval control failed: %v", err)
	}
	approved, err := store.Get(ctx, task.TaskID)
	if err != nil || approved.State != core.StateDone {
		t.Fatalf("approval control: %s %v", approved.State, err)
	}
	t.Log("ordinary approval control completed the same task")
}

func TestRegressionRejudgeProjectionPreservesPlanResponsibility(t *testing.T) {
	spec := `{"objectives":{"version":1,"revision":1,"dimensions":[{"dimension":"visual","weight":1,"priority":"MAX","requirement":"render","reason":"user","criteria":["render verified"]}]}}`
	result := `{"objective_assessments":[{"dimension":"visual","criterion":0,"status":"deferred","evidence":"final stage owns the render"}]}`
	for _, tc := range []struct {
		name, parent, plan string
		final, want        bool
	}{
		{"child", "parent", "", false, false},
		{"plan intermediate", "", "plan", false, false},
		{"plan final", "", "plan", true, true},
		{"standalone final", "", "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := spec
			if tc.final {
				raw = strings.Replace(raw, `{`, `{"plan_final":true,`, 1)
			}
			task := core.Task{State: core.StateReview, ParentID: tc.parent, PlanID: tc.plan, SpecJSON: raw, ResultJSON: result, ApprovalDisposition: core.ApprovalAcceptWork}
			h := &handler{}
			row := h.toTaskJSON(task)
			if got := needsObjectiveRejudge(row); got != tc.want {
				t.Fatalf("needs rejudge=%t want=%t", got, tc.want)
			}
			row.Result = strings.Replace(result, `"deferred"`, `"passed"`, 1)
			if needsObjectiveRejudge(row) {
				t.Fatal("passed evidence must remain approvable")
			}
		})
	}
}
