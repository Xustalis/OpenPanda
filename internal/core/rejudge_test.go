// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

const objectiveSpec = `{"objectives":{"version":1,"revision":1,"dimensions":[` +
	`{"dimension":"visual","weight":0.6,"priority":"MAX","requirement":"cinematic scene",` +
	`"reason":"the user asked for a finished render","criteria":["tree reads as a real tree"]}]}}`

// objectiveSupervisor serves one reviewing-model answer. The text must be a
// JSON string in an Anthropic-shaped content block, the same shape the entry
// client decodes from a real endpoint.
func objectiveSupervisor(t *testing.T, verdict string) *httptest.Server {
	t.Helper()
	return objectiveSupervisorSeq(t, verdict)
}

// objectiveSupervisorSeq serves one answer per call, in order, and repeats the
// last one afterwards — enough to pin a judge that omits the evidence on its
// first answer and supplies it on the corrective second one.
func objectiveSupervisorSeq(t *testing.T, verdicts ...string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	next := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		text := verdicts[len(verdicts)-1]
		if next < len(verdicts) {
			text = verdicts[next]
			next++
		}
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		body, _ := json.Marshal(map[string]any{
			"content": []map[string]string{{"type": "text", "text": text}},
		})
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// parkedObjectiveTask parks a task in review as accept_work with a MAX-objective
// contract and a stored result that carries no acceptance evidence — the exact
// shape an unparsable judge verdict leaves behind.
func parkedObjectiveTask(t *testing.T, c *Core, stdout string) Task {
	t.Helper()
	ctx := context.Background()
	task, err := c.store.Create(ctx, "", "proj", "cinematic tree", c.nodeID, []string{c.nodeID})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.store.SetDetail(ctx, task.TaskID, TaskDetail{Intent: "build a cinematic tree", SpecJSON: objectiveSpec}); err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error { return c.store.SetAuthorized(ctx, task.TaskID, true) },
		func() error { return c.store.Queue(ctx, task.TaskID, c.nodeID) },
		func() error { return c.store.Dispatch(ctx, task.TaskID, c.nodeID, c.nodeID) },
		func() error { return c.store.Accept(ctx, task.TaskID, c.nodeID) },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.store.PauseWithResult(ctx, task.TaskID, c.nodeID, map[string]any{
		"ok": true, "exit_code": 0, "stdout": stdout, "agent": "claude_code",
		"files_changed": []string{"index.html"}, "verdict": "review",
		"verdict_reason": "verdict unparsable: unexpected end of JSON input",
	}); err != nil {
		t.Fatal(err)
	}
	parked, err := c.store.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	return parked
}

// TestObjectiveEvidenceErrorNamesTheBlocker pins the diagnostic the approve
// refusal now carries: a MAX contract without assessments is an error, and the
// same row with assessments is not.
func TestObjectiveEvidenceErrorNamesTheBlocker(t *testing.T) {
	blocked := Task{SpecJSON: objectiveSpec, ResultJSON: `{"ok":true,"stdout":"done"}`}
	err := ObjectiveEvidenceError(blocked)
	if err == nil || !strings.Contains(err.Error(), "lacks independent acceptance evidence") {
		t.Fatalf("ObjectiveEvidenceError = %v, want missing-evidence error", err)
	}
	accepted := Task{SpecJSON: objectiveSpec, ResultJSON: `{"ok":true,"stdout":"done","objective_assessments":[
		{"dimension":"visual","criterion":0,"status":"passed","evidence":"rendered screenshot reviewed"}]}`}
	if err := ObjectiveEvidenceError(accepted); err != nil {
		t.Fatalf("ObjectiveEvidenceError with evidence = %v, want nil", err)
	}
	// No contract at all: nothing to block.
	if err := ObjectiveEvidenceError(Task{ResultJSON: `{"ok":true}`}); err != nil {
		t.Fatalf("ObjectiveEvidenceError without objectives = %v, want nil", err)
	}
}

// TestRejudgeReviewedAcceptsStoredWork is the deadlock's way out: a fresh
// verdict that accepts the work records its assessments and lands the task in
// done, without re-running the agent.
func TestRejudgeReviewedAcceptsStoredWork(t *testing.T) {
	srv := objectiveSupervisor(t, `{"status":"done","reason":"the render meets the bar","followup":"",`+
		`"objective_assessments":[{"dimension":"visual","criterion":0,"status":"passed","evidence":"index.html reviewed: instanced foliage, bloom, depth of field"}]}`)
	c := newCoreWithAgentModel(t, "rejudge-ok", config.ModelConfig{})
	c.AttachSupervisor(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk-test", Model: "deepseek-chat"})
	ctx := context.Background()
	task := parkedObjectiveTask(t, c, "交付完成。index.html 2101 行")

	if err := ObjectiveEvidenceError(task); err == nil {
		t.Fatal("fixture must start blocked by the evidence gate")
	}
	final, result, err := c.RejudgeReviewed(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("rejudge: %v", err)
	}
	if final.State != StateDone || result.State != StateDone {
		t.Fatalf("state = %s / %s, want done", final.State, result.State)
	}
	var stored struct {
		Verdict       string `json:"verdict"`
		Assessments   []any  `json:"objective_assessments"`
		VerdictReason string `json:"verdict_reason"`
	}
	if err := json.Unmarshal([]byte(final.ResultJSON), &stored); err != nil {
		t.Fatalf("decode stored result: %v", err)
	}
	if stored.Verdict != "done" || len(stored.Assessments) != 1 {
		t.Fatalf("stored verdict=%q assessments=%d, want done/1", stored.Verdict, len(stored.Assessments))
	}
	if stored.VerdictReason == "" {
		t.Fatal("re-review must keep the judge's reason on the row")
	}
	if err := ObjectiveEvidenceError(final); err != nil {
		t.Fatalf("accepted row still blocked: %v", err)
	}
}

// TestRejudgeReviewedAsksAgainWhenVerdictOmitsEvidence covers the judge that
// answers "done" but forgets the per-criterion evidence — the shape a live
// judge actually produced. One corrective call must be enough to record it,
// because the human otherwise has no route to done at all.
func TestRejudgeReviewedAsksAgainWhenVerdictOmitsEvidence(t *testing.T) {
	srv := objectiveSupervisorSeq(t,
		`{"status":"done","reason":"交付完整","followup":""}`,
		`{"status":"done","reason":"交付完整","followup":"","objective_assessments":[{"dimension":"visual","criterion":0,"status":"passed","evidence":"index.html 已渲染并逐项核对"}]}`,
	)
	c := newCoreWithAgentModel(t, "rejudge-correct", config.ModelConfig{})
	c.AttachSupervisor(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk-test", Model: "deepseek-chat"})
	ctx := context.Background()
	task := parkedObjectiveTask(t, c, "交付完成。index.html 2101 行")

	final, _, err := c.RejudgeReviewed(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("rejudge after a corrected verdict: %v", err)
	}
	if final.State != StateDone {
		t.Fatalf("state = %s, want done", final.State)
	}
	var stored struct {
		Assessments []any `json:"objective_assessments"`
	}
	if err := json.Unmarshal([]byte(final.ResultJSON), &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Assessments) != 1 {
		t.Fatalf("assessments = %d, want 1", len(stored.Assessments))
	}
}

// TestRejudgeReviewedKeepsReviewWhenVerdictRefuses: a judge that still will not
// accept the work must leave the task parked, with the new reason visible, and
// must not silently promote it to done.
func TestRejudgeReviewedKeepsReviewWhenVerdictRefuses(t *testing.T) {
	srv := objectiveSupervisor(t, `{"status":"continue","reason":"no rendered evidence for the lighting criterion","followup":"capture a screenshot and compare"}`)
	c := newCoreWithAgentModel(t, "rejudge-refuse", config.ModelConfig{})
	c.AttachSupervisor(config.ModelConfig{BaseURL: srv.URL, APIKey: "sk-test", Model: "deepseek-chat"})
	ctx := context.Background()
	task := parkedObjectiveTask(t, c, "交付完成。index.html 2101 行")

	final, _, err := c.RejudgeReviewed(ctx, task.TaskID)
	if err == nil || !strings.Contains(err.Error(), "no rendered evidence") {
		t.Fatalf("rejudge error = %v, want the judge's reason", err)
	}
	if final.State != StateReview {
		t.Fatalf("state = %s, want review", final.State)
	}
	reloaded, gerr := c.store.Get(ctx, task.TaskID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	var stored struct {
		Verdict       string `json:"verdict"`
		VerdictReason string `json:"verdict_reason"`
	}
	if err := json.Unmarshal([]byte(reloaded.ResultJSON), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Verdict != "continue" || !strings.Contains(stored.VerdictReason, "no rendered evidence") {
		t.Fatalf("stored verdict=%q reason=%q, want continue with the new reason", stored.Verdict, stored.VerdictReason)
	}
}
