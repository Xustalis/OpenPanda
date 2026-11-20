// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegressionClosedFailedGoalIsInterrupted(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if err := store.BeginAskRound(ctx, "goal", "owner", "en"); err != nil {
		t.Fatal(err)
	}
	task, err := store.Create(core.WithAskRound(ctx, "goal"), "", "p", "failed execution", "node", []string{"node"})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error { return store.Queue(ctx, task.TaskID, "node") },
		func() error { return store.Dispatch(ctx, task.TaskID, "node", "node") },
		func() error { return store.Accept(ctx, task.TaskID, "node") },
		func() error { return store.ForceFail(ctx, task.TaskID, "adapter permanently unavailable") },
		func() error { return store.CloseAskRound(ctx, "goal", "closed") },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	settled, err := store.SettledAskRounds(ctx)
	if err != nil || len(settled) != 1 {
		t.Fatalf("failed round must already be settled: %d %v", len(settled), err)
	}
	h := New(Deps{Store: store, Sessions: sessions.NewStore(t.TempDir()), StaticDir: t.TempDir(), Token: testToken})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/sessions/owner/rounds/goal", nil))
	t.Logf("settled rounds=%d GET round status=%d body=%s", len(settled), rr.Code, rr.Body.String())
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "interrupted") {
		t.Errorf("settled failed goal is not interrupted: %d %s", rr.Code, rr.Body.String())
	}
	for _, tc := range []struct{ state, want string }{{core.StateRunning, "running"}, {core.StateReview, "review"}, {core.StateDone, "finished"}, {core.StateExpired, "interrupted"}, {core.StateCancelled, "interrupted"}} {
		if got := roundPhase(core.AskRound{Status: "closed", Tasks: []core.Task{{State: tc.state}}}); got != tc.want {
			t.Errorf("control %s: got %s want %s", tc.state, got, tc.want)
		}
	}
}

func TestRegressionRoundWaitsForDurableRecoveryDecision(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.BeginAskRound(ctx, "recovering", "owner", "en"); err != nil {
		t.Fatal(err)
	}
	task, err := s.Create(core.WithAskRound(ctx, "recovering"), "", "p", "worker", "node", []string{"node"})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error { return s.Queue(ctx, task.TaskID, "node") },
		func() error { return s.Dispatch(ctx, task.TaskID, "node", "node") },
		func() error { return s.Accept(ctx, task.TaskID, "node") },
		func() error { return s.RecordEvent(ctx, task.TaskID, core.EvExecAgentStart, nil) },
		func() error { return s.ForceFail(ctx, task.TaskID, "execution ended") },
		func() error { return s.CloseAskRound(ctx, "recovering", "closed") },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	check := func(want string, settled int) {
		t.Helper()
		r, err := s.AskRoundProgress(ctx, "owner", "recovering")
		if err != nil || roundPhase(r) != want {
			t.Fatalf("phase=%s want=%s err=%v", roundPhase(r), want, err)
		}
		rounds, err := s.SettledAskRounds(ctx)
		if err != nil || len(rounds) != settled {
			t.Fatalf("settled=%d want=%d err=%v", len(rounds), settled, err)
		}
	}
	// The write gap between result and recovery assessment is still running.
	check("running", 0)
	for _, strategy := range []string{core.RecoveryBoundedRetry, core.RecoverySessionResume, core.RecoveryStop} {
		if err := s.RecordEvent(ctx, task.TaskID, core.EvRecoveryAssessment, map[string]any{"assessment": core.RecoveryAssessment{Strategy: strategy}}); err != nil {
			t.Fatal(err)
		}
		if strategy == core.RecoveryStop {
			check("interrupted", 1)
		} else {
			check("running", 0)
		}
	}
}
