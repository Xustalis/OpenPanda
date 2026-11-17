// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/taskproviders"
)

func preparationTest(t *testing.T) (*Service, *executionRuntime, *managedExecution, flowRun, core.Task) {
	t.Helper()
	s, e, c, _, wire := automationTest(t, true)
	f := discoverTestFlow(t, s, e)
	automationServer(t, s, c, wire, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(leaseRecord(f))
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := &managedExecution{runtime: e, id: f.ID, owner: "owner", ctx: ctx, cancel: cancel}
	if err := m.acquire(); err != nil {
		t.Fatal(err)
	}
	f, err := s.flow(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	task, err := core.NewTaskStore(s.db, nil).Create(ctx, "", "", "Prepare draft", "owner", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE tasks SET state='running' WHERE task_id=?`, task.TaskID); err != nil {
		t.Fatal(err)
	}
	return s, e, m, f, task
}

func TestPreparationFailurePersistsBeforeCoordinatorObservation(t *testing.T) {
	providerErr := errors.New("provider failed with private details")
	for _, tc := range []struct {
		name, response, code string
		cause                error
		cancel               bool
	}{
		{name: "empty", code: "preparation_empty"},
		{name: "whitespace", response: " \n\t", code: "preparation_empty"},
		{name: "provider", cause: providerErr, code: "preparation_failed"},
		{name: "cancelled", response: "late result", cancel: true, code: "preparation_interrupted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, e, m, f, task := preparationTest(t)
			model := &auxiliaryTestModel{response: &tc.response, err: tc.cause}
			if tc.cancel {
				model.call = m.cancel
			}
			e.options.Model = func() AuxiliaryModel { return model }
			if _, err := e.prepare(m.ctx, m, f, task); err == nil || tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("unexpected result: %v", err)
			}
			assertFailure := func() {
				t.Helper()
				current, err := s.flow(context.Background(), f.ID)
				if err != nil {
					t.Fatal(err)
				}
				if current.State != "needs_reconciliation" || current.Error != tc.code || current.Prepared != "" || current.PrepareAttempt != task.AttemptID {
					t.Fatalf("failure lost: state=%s code=%s attempt=%s", current.State, current.Error, current.PrepareAttempt)
				}
			}
			assertFailure()
			// The coordinator sees a failed task, not the original model error.
			m.lose(errors.New("preparation did not complete"))
			assertFailure()
			if err := e.recover(context.Background(), "owner"); err != nil {
				t.Fatal(err)
			}
			assertFailure()
			if scalar(t, s.db, `SELECT state FROM research_briefs`) != "failed" {
				t.Fatal("failed invocation still appears in flight")
			}
			if scalar(t, s.db, `SELECT reserved_tokens>0 FROM research_briefs`) != "1" {
				t.Fatal("failure refunded an ambiguous invocation")
			}
			_, _ = e.prepare(context.Background(), m, f, task)
			if model.calls.Load() != 1 {
				t.Fatal("failed preparation automatically replayed")
			}
		})
	}
}

func TestPreparationFailurePreservesConcurrentDecision(t *testing.T) {
	for _, change := range []string{`state='paused',error_code='user_paused',lease_owner='',deadline=0`, `prepare_attempt='new-attempt',error_code='new_attempt'`, `lease_owner='new-owner',error_code='new_owner'`, `prepared_json='newer result',error_code='new_result'`} {
		t.Run(change, func(t *testing.T) {
			s, e, m, f, task := preparationTest(t)
			empty := ""
			model := &auxiliaryTestModel{response: &empty, call: func() {
				if _, err := s.db.Exec(`UPDATE task_flow_runs SET `+change+` WHERE request_id=?`, f.ID); err != nil {
					t.Fatal(err)
				}
			}}
			e.options.Model = func() AuxiliaryModel { return model }
			if _, err := e.prepare(m.ctx, m, f, task); !errors.Is(err, ErrEmptyPreparation) {
				t.Fatal(err)
			}
			if scalar(t, s.db, `SELECT error_code FROM task_flow_runs`) == "preparation_empty" {
				t.Fatal("late failure replaced a newer decision")
			}
		})
	}
}

func TestPreparationRejectsSupersededKernelAttempt(t *testing.T) {
	for _, change := range []string{`attempt_id='new-attempt'`, `state='cancelled'`, `state='failed'`} {
		t.Run(change, func(t *testing.T) {
			s, e, m, f, task := preparationTest(t)
			model := &auxiliaryTestModel{call: func() {
				if _, err := s.db.Exec(`UPDATE tasks SET `+change+` WHERE task_id=?`, task.TaskID); err != nil {
					t.Fatal(err)
				}
			}}
			e.options.Model = func() AuxiliaryModel { return model }
			if _, err := e.prepare(m.ctx, m, f, task); err == nil {
				t.Fatal("superseded task published a model result")
			}
			if scalar(t, s.db, `SELECT prepared_json FROM task_flow_runs`) != "" {
				t.Fatal("superseded output became a deliverable")
			}
		})
	}
}

func TestExecutionAuthorityRejectsStoppedAndUnresolvedRuns(t *testing.T) {
	for _, state := range []string{"paused", "failed", "cancelled", "superseded", "succeeded", "awaiting_submit", "awaiting_acceptance", "needs_reconciliation", "unknown_state"} {
		t.Run(state, func(t *testing.T) {
			s, _, m, f, _ := preparationTest(t)
			if _, err := s.db.Exec(`UPDATE task_flow_runs SET state=? WHERE request_id=?`, state, f.ID); err != nil {
				t.Fatal(err)
			}
			if _, _, err := m.authority(m.ctx, false); !errors.Is(err, taskproviders.ErrAuthorizationChanged) {
				t.Fatalf("state %s still grants authority: %v", state, err)
			}
		})
	}
}

func TestLeaseResponseCannotResumeUnresolvedPreparation(t *testing.T) {
	s, _, m, f, _ := preparationTest(t)
	if _, err := s.db.Exec(`UPDATE task_flow_runs SET state='needs_reconciliation',error_code='preparation_failed',deadline=0,lease_owner='' WHERE request_id=?`, f.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.persistLease(m.ctx, f, leaseRecord(f), 0); err == nil {
		t.Fatal("late lease response resumed unresolved preparation")
	}
	if scalar(t, s.db, `SELECT state FROM task_flow_runs`) != "needs_reconciliation" {
		t.Fatal("late lease response erased the stop")
	}
}

func TestExecutionAuthorityAllowsRecoveryWithRecordedResult(t *testing.T) {
	for _, state := range []string{"claiming", "leased", "running", "recovering", "lease_lost", "needs_reconciliation"} {
		t.Run(state, func(t *testing.T) {
			s, _, m, f, _ := preparationTest(t)
			if _, err := s.db.Exec(`UPDATE task_flow_runs SET state=?,prepared_json='recorded result' WHERE request_id=?`, state, f.ID); err != nil {
				t.Fatal(err)
			}
			if _, _, err := m.authority(m.ctx, false); err != nil {
				t.Fatal(err)
			}
			if err := m.persistLease(m.ctx, f, leaseRecord(f), time.Millisecond); err != nil {
				t.Fatalf("recorded result cannot recover: %v", err)
			}
		})
	}
}

func TestPreparationFailurePersistenceIsAtomic(t *testing.T) {
	s, e, m, f, task := preparationTest(t)
	providerErr := errors.New("model failed")
	model := &auxiliaryTestModel{err: providerErr}
	e.options.Model = func() AuxiliaryModel { return model }
	if _, err := s.db.Exec(`CREATE TRIGGER reject_failure BEFORE UPDATE ON research_briefs WHEN NEW.state='failed' BEGIN SELECT RAISE(ABORT,'failure persistence unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.prepare(m.ctx, m, f, task); !errors.Is(err, providerErr) || !strings.Contains(err.Error(), "record preparation failure") {
		t.Fatalf("persistence error hid the original model error: %v", err)
	}
	if scalar(t, s.db, `SELECT state FROM task_flow_runs`) != "running" || scalar(t, s.db, `SELECT state FROM research_briefs`) != "invoking" {
		t.Fatal("failure evidence was partially committed")
	}
	_, _ = e.prepare(context.Background(), m, f, task)
	if model.calls.Load() != 1 {
		t.Fatal("failed evidence write authorized another invocation")
	}
}
