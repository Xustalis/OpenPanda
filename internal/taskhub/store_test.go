// SPDX-License-Identifier: AGPL-3.0-or-later

package taskhub

import (
	"context"
	"errors"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/plan"
	"github.com/Xustalis/OpenPanda/internal/testdb"
	"github.com/Xustalis/OpenPanda/internal/todos"
)

type testRunner struct{ c *core.Core }

func (r testRunner) EnqueueWork(ctx context.Context, req core.ExecutionRequest, in core.TaskInput, q core.QueueSpec) (core.EnqueueResult, string, error) {
	x, err := r.c.EnqueueRequest(ctx, req, in, q)
	return x, "", err
}
func (r testRunner) StartWorkPlan(ctx context.Context, req core.ExecutionRequest, p plan.Plan, q core.QueueSpec) (core.PlanRequestResult, string, error) {
	x, err := r.c.StartPlanRequest(ctx, req, p, q)
	return x, "", err
}

func TestLocalPlanHistoryAndIdempotency(t *testing.T) {
	db := testdb.Open(t)
	s := New(db)
	ctx := context.Background()
	todo, err := s.Todos.Create(ctx, todos.CreateInput{Title: "two stages"})
	if err != nil {
		t.Fatal(err)
	}
	c := core.NewCore(db, "node", ledger.Card{Device: "node"}, 1, nil, config.ModelConfig{})
	t.Cleanup(func() { c.Shutdown(context.Background()) })
	p := plan.Plan{Goal: "deliver", Stages: []plan.Stage{{ID: "prepare", Intent: "prepare"}, {ID: "verify", Intent: "verify", Needs: []string{"prepare"}}}}
	opts := RunOptions{RequestID: "run-1", Purpose: "rerun", Plan: &p}
	first, err := s.StartLocal(ctx, testRunner{c}, todo.ID, opts)
	if err != nil || first.PlanID == "" || first.Task.TaskID == "" || first.AlreadyStarted {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	again, err := s.StartLocal(ctx, testRunner{c}, todo.ID, opts)
	if err != nil || !again.AlreadyStarted || again.PlanID != first.PlanID || again.Task.TaskID != first.Task.TaskID {
		t.Fatalf("replay=%+v err=%v", again, err)
	}
	history, err := s.History(ctx, "local", "local/local/local/1")
	if err != nil || len(history) != 2 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	for _, h := range history {
		if h.PlanID != first.PlanID || h.RequestID != "run-1" || h.StageID == "" {
			t.Fatalf("missing stage association: %+v", h)
		}
	}
	sources, err := s.Sources(ctx, []string{history[0].TaskID, history[1].TaskID})
	if err != nil || len(sources) != 2 {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
	for _, source := range sources {
		if source.ConnectionID != "local" || source.TaskKey != "local/local/local/1" || source.RequestID != "run-1" {
			t.Fatal(source)
		}
	}
	p.Stages[0].Intent = "changed execution input"
	if _, err := s.StartLocal(ctx, testRunner{c}, todo.ID, opts); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("changed Plan: %v", err)
	}
	compat, err := s.StartLocal(ctx, nil, todo.ID, RunOptions{})
	if err != nil || !compat.AlreadyStarted || compat.PlanID != first.PlanID {
		t.Fatalf("legacy start=%+v err=%v", compat, err)
	}
}
