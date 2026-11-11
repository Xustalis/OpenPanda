// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

// Replays the persisted shape of production task 01a1c8f8: the supervisor
// rejected completion, but a later approval used to count it as done.
func TestApproveCannotCompleteKnownUnfinishedWork(t *testing.T) {
	for _, result := range []string{
		`{"ok":true,"needs_followup":true,"verdict":"continue","stdout":"curl returned 000; no server PID"}`,
		`{"ok":true,"verdict":"continue"}`,
	} {
		t.Run(result, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			task := createTask(t, s, "", "start server", "node")
			parkForFollowup(t, s, task, "node")
			if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET result_json=? WHERE task_id=?`, result, task.TaskID); err != nil {
				t.Fatal(err)
			}
			err := s.Approve(ctx, task.TaskID)
			got, readErr := s.Get(ctx, task.TaskID)
			t.Logf("approval_error=%v state=%s result_preserved=%t", err, got.State, got.ResultJSON == result)
			if err == nil || readErr != nil || got.State != StateReview || got.ResultJSON != result {
				t.Fatalf("known unfinished work accepted: task=%+v approve=%v read=%v", got, err, readErr)
			}
		})
	}
}

func TestRemoteUnfinishedResultCannotBeApproved(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	task := createTask(t, s, "", "remote incomplete", "origin")
	// Exercise the actual typed wire envelope rather than retaining a map
	// that accidentally preserves fields missing from the bus contract.
	raw := []byte(`{"ok":true,"state":"review","approval_disposition":"accept_work","needs_followup":true,"verdict":"continue","verdict_reason":"curl 000"}`)
	var payload bus.TaskResultPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if err := s.ReviewFromRemote(ctx, task.TaskID, "origin", payload, ApprovalAcceptWork); err != nil {
		t.Fatal(err)
	}
	if err := s.Approve(ctx, task.TaskID); err == nil {
		t.Fatal("remote incomplete work accepted")
	}
	cur, err := s.Get(ctx, task.TaskID)
	if err != nil || !cur.NeedsFollowup() {
		t.Fatalf("remote completion evidence lost: %+v %v", cur, err)
	}
}

func TestSupervisorExternalBlockStopsWithoutRepeatedExecution(t *testing.T) {
	c := newSuperviseCore(t, "external-block", 1)
	c.SetSuperviseRounds(5)
	c.SetSupervisor(newFakeSupervisor(t, func(int) string {
		return `{"status":"blocked","reason":"external tool sandbox denies socket.bind; curl returned 000","followup":"operator must change the external tool environment"}`
	}))
	var calls atomic.Int32
	c.router.SetAdapterRunner(agentRunner(&calls))
	task, _, err := c.SubmitLocal(context.Background(), TaskInput{Title: "start server", ContextType: "command", Intent: "HTTP 200 and live PID required", Requires: []string{"code:modify"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("execution_rounds=%d state=%s disposition=%s", calls.Load(), task.State, task.ApprovalDisposition)
	if calls.Load() != 1 || task.State != StateReview || task.ApprovalDisposition != ApprovalNeedsChangedInput {
		t.Fatalf("external blocker must preserve a changed-input review: %+v", task)
	}
	if err := c.store.Approve(context.Background(), task.TaskID); err == nil {
		t.Fatal("approval must not pretend to fix an external environment")
	}
}
