// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !lite

package main

import (
	"context"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/sessions"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

func TestDeepTerminalExhaustedBudgetResumeIsAtomic(t *testing.T) {
	m, _, _ := newNoteTUI(t, true)
	r := m.r
	r.sessionsSt = sessions.NewStore(t.TempDir())
	sess, err := r.sessionsSt.Create("terminal budget fixture")
	if err != nil {
		t.Fatal(err)
	}
	r.setSess(sess)
	service := r.engine.Load().Conversations(r.sessionsSt)
	service.Close()
	if _, _, err = r.modeCommand("plan", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err = r.modeCommand("goal", "--tokens 10 preserve every original requirement"); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(r.cfg.Storage.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("UPDATE conversation_goals SET tokens=10,status='budget_limited' WHERE session_id=?", sess.ID); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"resume", "--tokens 9 resume", "--tokens 10 resume"} {
		if _, _, err = r.modeCommand("goal", command); err == nil {
			t.Fatalf("terminal accepted exhausted resume %q", command)
		}
		state, err := service.State(context.Background(), sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !state.Planning || state.Goal.Status != "budget_limited" || *state.Goal.TokenBudget != 10 {
			t.Fatalf("rejection mutated mode/goal: %+v %+v", state, state.Goal)
		}
	}
	if _, _, err = r.modeCommand("goal", "--tokens 11 resume"); err != nil {
		t.Fatal(err)
	}
	state, err := service.State(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Planning || state.Goal.Status != "active" || *state.Goal.TokenBudget != 11 || state.Goal.Tokens != 10 {
		t.Fatalf("valid resume: %+v %+v", state, state.Goal)
	}
}
