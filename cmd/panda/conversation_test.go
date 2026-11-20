// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !lite

package main

import (
	"context"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	"strings"
	"testing"
)

func TestTerminalCommandsSharePersistentConversationService(t *testing.T) {
	m, _, _ := newNoteTUI(t, true)
	r := m.r
	r.sessionsSt = sessions.NewStore(t.TempDir())
	sess, err := r.sessionsSt.Create("terminal conversation fixture")
	if err != nil {
		t.Fatal(err)
	}
	r.setSess(sess)
	service := r.engine.Load().Conversations(r.sessionsSt)
	service.Close() // Controls are real; this fixture authorizes no model execution.
	command := func(name, arg string) string {
		t.Helper()
		_, prompt, err := r.modeCommand(name, arg)
		if err != nil {
			t.Fatal(err)
		}
		return prompt
	}
	if got := command("plan", "inspect the complete fixture"); got != "inspect the complete fixture" {
		t.Fatalf("prompt=%q", got)
	}
	command("goal", "--tokens 2000 complete the original objective")
	state, err := service.State(context.Background(), sess.ID)
	if err != nil || !state.Planning || state.Goal.Status != "paused" || state.Goal.TokenBudget == nil || *state.Goal.TokenBudget != 2000 {
		t.Fatalf("state=%+v goal=%+v err=%v", state, state.Goal, err)
	}
	command("plan", "")
	state, _ = service.State(context.Background(), sess.ID)
	if state.Planning || state.Goal.Status != "paused" {
		t.Fatal("Plan exit resumed the Goal")
	}
	command("goal", "resume")
	command("goal", "pause")
	command("goal", "edit preserve all requirements and verify results")
	state, _ = service.State(context.Background(), sess.ID)
	if state.Goal.Objective != "preserve all requirements and verify results" {
		t.Fatalf("edit lost: %+v", state.Goal)
	}
	if !strings.Contains(r.conversationBadge(), state.Goal.Objective) {
		t.Fatal("terminal state badge stale")
	}
	command("goal", "clear")
	state, _ = service.State(context.Background(), sess.ID)
	if state.Goal != nil || r.conversationBadge() != "" {
		t.Fatal("clear left a current goal")
	}
}

func TestTerminalDurableTurnsDeliveredOncePerSession(t *testing.T) {
	r, st := newThreadRepl(t)
	a, _ := st.Create("first")
	b, _ := st.Create("second")
	r.setSess(a)
	_, err := st.AppendTurn(a.ID, sessions.Turn{Role: "assistant", Text: "Verified remaining goal work.", IdempotencyKey: "goal:fixture-round"})
	if err != nil {
		t.Fatal(err)
	}
	events := r.pollConversation(context.Background())
	if len(events) != 1 || events[0].note != "Verified remaining goal work." {
		t.Fatalf("events=%+v", events)
	}
	if len(r.pollConversation(context.Background())) != 0 {
		t.Fatal("duplicate notification")
	}
	r.setSess(b)
	if len(r.pollConversation(context.Background())) != 0 {
		t.Fatal("cross-session notification")
	}
	reopened, err := st.Get(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	r.setSess(reopened)
	if len(r.pollConversation(context.Background())) != 0 {
		t.Fatal("old transcript replayed")
	}
}

func TestTerminalGoalCommandKeepsObjectivesAndPendingNotifications(t *testing.T) {
	m, _, _ := newNoteTUI(t, true)
	r := m.r
	r.sessionsSt = sessions.NewStore(t.TempDir())
	sess, err := r.sessionsSt.Create("goal fixture")
	if err != nil {
		t.Fatal(err)
	}
	r.setSess(sess)
	service := r.engine.Load().Conversations(r.sessionsSt)
	service.Close()
	ctx := context.Background()
	if err = service.SetPlanning(ctx, sess.ID, true); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ arg, want string }{
		{"stop the bleeding in CI", "stop the bleeding in CI"},
		{"PAUSE\tall background jobs", "PAUSE\tall background jobs"},
		{"--tokens 9 -- stop", "stop"},
		{"edit preserve  internal whitespace", "preserve  internal whitespace"},
	} {
		if _, _, err = r.modeCommand("goal", tc.arg); err != nil {
			t.Fatalf("/goal %s: %v", tc.arg, err)
		}
		state, err := service.State(ctx, sess.ID)
		if err != nil || state.Goal == nil || state.Goal.Objective != tc.want {
			t.Fatalf("/goal %s: state=%+v, err=%v", tc.arg, state.Goal, err)
		}
	}
	for _, key := range []string{"goal:pending", "plan:assistant:pending"} {
		if _, err = r.sessionsSt.AppendTurn(sess.ID, sessions.Turn{Role: "assistant", Text: key, IdempotencyKey: key}); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range []string{"goal", "plan"} {
		if _, _, err = r.modeCommand(command, ""); err != nil {
			t.Fatal(err)
		}
	}
	events := r.pollConversation(ctx)
	if len(events) != 2 || events[0].note != "goal:pending" || events[1].note != "plan:assistant:pending" {
		t.Fatalf("pending autonomous turns were lost: %+v", events)
	}
	m.pendingInputs = []string{"queue fixture"}
	line := m.contextLine()
	if !strings.Contains(line, "preserve  internal whitespace") || !strings.Contains(line, "queued") {
		t.Fatalf("status line lost goal or queue: %q", line)
	}
}
