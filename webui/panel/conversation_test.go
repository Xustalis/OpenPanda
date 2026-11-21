// SPDX-License-Identifier: AGPL-3.0-or-later
package panel

import (
	"context"
	"encoding/json"
	"github.com/Xustalis/OpenPanda/internal/conversation"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	"github.com/Xustalis/OpenPanda/internal/storage"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestConversationControlAPISharedStateAndVersionConflict(t *testing.T) {
	engine, counter := newApprovalQueueEngine(t)
	st := sessions.NewStore(t.TempDir())
	sess, err := st.Create("control fixture")
	if err != nil {
		t.Fatal(err)
	}
	service := engine.Conversations(st)
	service.Close()
	db, err := storage.Open(filepath.Join(filepath.Dir(counter), "panda.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stateStore := &conversation.Store{DB: db}
	h := New(Deps{Store: engine.TaskStore(), Engine: engine, Sessions: st, DB: db, Token: testToken, StaticDir: t.TempDir()})
	call := func(body string, want int) conversation.State {
		t.Helper()
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/sessions/"+sess.ID+"/control", strings.NewReader(body)))
		if rr.Code != want {
			t.Fatalf("%s: %d %s", body, rr.Code, rr.Body.String())
		}
		var state conversation.State
		if want == 200 && json.Unmarshal(rr.Body.Bytes(), &state) != nil {
			t.Fatal(rr.Body.String())
		}
		return state
	}
	call(`{"action":"mode","planning":true}`, 200)
	call(`{"action":"mode"}`, http.StatusBadRequest)
	call(`{"action":"create","objective":" "}`, http.StatusBadRequest)
	call(`{"action":"create","objective":"goal","token_budget":0}`, http.StatusBadRequest)
	goal := call(`{"action":"create","objective":"preserve the full fixture objective","token_budget":100}`, 200).Goal
	if goal == nil || goal.Status != "paused" || !goal.PausedByPlan {
		t.Fatalf("planning goal: %+v", goal)
	}
	v, err := stateStore.SaveProposal(context.Background(), sess.ID, "version one")
	if err != nil {
		t.Fatal(err)
	}
	_, err = stateStore.SaveProposal(context.Background(), sess.ID, "version two")
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"action": "confirm", "version": v})
	call(string(payload), 409)
	call(`{"action":"confirm","version":2}`, 200)
	call(`{"action":"confirm","version":2}`, 409)
	terminalState, err := service.State(context.Background(), sess.ID)
	if err != nil || terminalState.Planning || terminalState.Goal.ID != goal.ID {
		t.Fatalf("shared state: %+v %v", terminalState, err)
	}
	payload, _ = json.Marshal(map[string]any{"action": "clear", "goal_id": goal.ID})
	cleared := call(string(payload), 200)
	if cleared.Goal != nil {
		t.Fatal("clear retained current goal")
	}
	var runStatus string
	_ = db.QueryRow("SELECT status FROM conversation_plan_runs WHERE session_id=?", sess.ID).Scan(&runStatus)
	if runStatus != "cancelled" {
		t.Fatalf("clear left implementation runnable: %s", runStatus)
	}
	if _, err := db.Exec(`DROP TABLE conversation_events`); err != nil {
		t.Fatal(err)
	}
	call(`{"action":"create","objective":"simulate a storage write failure"}`, http.StatusInternalServerError)
	if state, err := service.State(context.Background(), sess.ID); err != nil || state.Goal != nil {
		t.Fatalf("failed control did not roll back: %+v, %v", state, err)
	}
	rr := httptest.NewRecorder()
	writeConversationError(rr, conversation.ErrWaiting)
	if rr.Code != http.StatusConflict {
		t.Fatalf("the unsettled-work conflict returned %d: %s", rr.Code, rr.Body.String())
	}
}

// A controller closes its dispatch round as soon as it queues the plan.
// Reopening the conversation must follow the durable implementation status.
func TestReloadConfirmedPlanAfterDispatch(t *testing.T) {
	engine, counter := newApprovalQueueEngine(t)
	st := sessions.NewStore(t.TempDir())
	sess, err := st.Create("refresh after dispatch")
	if err != nil {
		t.Fatal(err)
	}
	engine.Conversations(st).Close()
	db, err := storage.Open(filepath.Join(filepath.Dir(counter), "panda.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	plans := conversation.Store{DB: db}
	if err := plans.SetPlanning(ctx, sess.ID, true); err != nil {
		t.Fatal(err)
	}
	version, err := plans.SaveProposal(ctx, sess.ID, "run stages")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = plans.Confirm(ctx, sess.ID, version); err != nil {
		t.Fatal(err)
	}
	const round = "dispatch-closed"
	if err = engine.TaskStore().BeginAskRound(ctx, round, sess.ID, "en"); err != nil {
		t.Fatal(err)
	}
	if err = engine.TaskStore().CloseAskRound(ctx, round, "closed"); err != nil {
		t.Fatal(err)
	}
	h := New(Deps{Store: engine.TaskStore(), Engine: engine, Sessions: st, DB: db, Token: testToken, StaticDir: t.TempDir()})
	for _, tc := range []struct{ run, operation string }{{"running", "running"}, {"done", "done"}, {"failed", "error"}, {"cancelled", "cancelled"}, {"waiting_input", "done"}} {
		t.Run(tc.run, func(t *testing.T) {
			if _, err := db.Exec("UPDATE conversation_plan_runs SET round_id=?, status=? WHERE session_id=? AND version=?", round, tc.run, sess.ID, version); err != nil {
				t.Fatal(err)
			}
			if err := st.StartOperation(sess.ID, sessions.Operation{ID: round, Status: "running"}); err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/sessions/"+sess.ID, nil))
			var got sessions.Session
			if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &got) != nil || got.Operation == nil || got.Operation.Status != tc.operation || len(got.Turns) != 0 {
				t.Fatalf("refresh for %s: %d %s", tc.run, rr.Code, rr.Body.String())
			}
		})
	}
}

func TestConversationStopConfirmedImplementationThroughOrdinaryStop(t *testing.T) {
	engine, counter := newApprovalQueueEngine(t)
	st := sessions.NewStore(t.TempDir())
	service := engine.Conversations(st)
	service.Close()
	db, err := storage.Open(filepath.Join(filepath.Dir(counter), "panda.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stateStore := &conversation.Store{DB: db}
	h := New(Deps{Store: engine.TaskStore(), Engine: engine, Sessions: st, DB: db, Token: testToken, StaticDir: t.TempDir()})
	for _, linked := range []bool{false, true} {
		sess, err := st.Create("confirmed stop fixture")
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if err = service.SetPlanning(ctx, sess.ID, true); err != nil {
			t.Fatal(err)
		}
		if linked {
			if _, err = service.Create(ctx, sess.ID, "full fixture objective", nil); err != nil {
				t.Fatal(err)
			}
		}
		version, err := stateStore.SaveProposal(ctx, sess.ID, "confirmed fixture")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = service.ConfirmPlan(ctx, sess.ID, version); err != nil {
			t.Fatal(err)
		}
		round := "fixture-" + sess.ID
		if err = engine.TaskStore().BeginAskRound(ctx, round, sess.ID, "en"); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec("UPDATE conversation_plan_runs SET status='running',round_id=? WHERE session_id=?", round, sess.ID); err != nil {
			t.Fatal(err)
		}
		if err = st.StartOperation(sess.ID, sessions.Operation{ID: round, Status: "running"}); err != nil {
			t.Fatal(err)
		}
		read := httptest.NewRecorder()
		h.ServeHTTP(read, authedReq(http.MethodGet, "/api/sessions/"+sess.ID, nil))
		var reopened sessions.Session
		if read.Code != 200 || json.Unmarshal(read.Body.Bytes(), &reopened) != nil || reopened.Operation == nil || reopened.Operation.Status != "running" {
			t.Fatalf("live controller operation was marked orphaned: %d %s", read.Code, read.Body.String())
		}
		wantRound := "stopped"
		if !linked {
			if err := engine.TaskStore().CloseAskRound(ctx, round, "closed"); err != nil {
				t.Fatal(err)
			}
			wantRound = "closed"
		}
		rr := httptest.NewRecorder()
		body, _ := json.Marshal(map[string]string{"operation_id": round})
		h.ServeHTTP(rr, authedReq(http.MethodPost, "/api/sessions/"+sess.ID+"/cancel", strings.NewReader(string(body))))
		if rr.Code != 200 {
			t.Fatalf("linked=%t status=%d %s", linked, rr.Code, rr.Body.String())
		}
		var runStatus, roundStatus string
		_ = db.QueryRow("SELECT status FROM conversation_plan_runs WHERE session_id=?", sess.ID).Scan(&runStatus)
		_ = db.QueryRow("SELECT status FROM ask_rounds WHERE round_id=?", round).Scan(&roundStatus)
		state, _ := service.State(ctx, sess.ID)
		stopped, err := st.Get(sess.ID)
		if err != nil || stopped.Operation == nil || stopped.Operation.Status != "cancelled" {
			t.Fatalf("stop did not settle the stored operation: %+v %v", stopped, err)
		}
		if runStatus != "cancelled" || roundStatus != wantRound || (linked && (state.Goal.Generation != 2 || state.Goal.Status != "paused")) {
			t.Fatalf("run=%s round=%s goal=%+v", runStatus, roundStatus, state.Goal)
		}
		// A reload may arrive after sealing the database round but before
		// the runner or the stop request saves its terminal session snapshot.
		if err = st.StartOperation(sess.ID, sessions.Operation{ID: round, Status: "running"}); err != nil {
			t.Fatal(err)
		}
		read = httptest.NewRecorder()
		h.ServeHTTP(read, authedReq(http.MethodGet, "/api/sessions/"+sess.ID, nil))
		if read.Code != 200 || json.Unmarshal(read.Body.Bytes(), &reopened) != nil || reopened.Operation == nil || reopened.Operation.Status != "cancelled" || reopened.Operation.Error != "" {
			t.Fatalf("a reload mislabelled the stopped round: %d %s", read.Code, read.Body.String())
		}
	}
}
