package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// TestManualTaskParksForReview pins the approval semantics for the manual tier:
// the human has not acted yet when the router returns NeedManual, so the task
// must land in review (待审批) with the notify text preserved as its result —
// never in done, which is reserved for work that met its success definition.
func TestManualTaskParksForReview(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	card := ledger.Card{
		Device:        "pi",
		ResourceClass: "Micro",
		Manual:        []ledger.ManualAbility{{ID: "hw:solder", Notify: "solder the servo header"}},
		Capacity:      ledger.Capacity{CPUCores: 4, RAMGB: 2, MaxConcurrent: 2},
	}
	c := NewCore(db, "pi", card, 5, testLogger(), config.ModelConfig{})

	tk, payload, err := c.SubmitLocal(ctx, TaskInput{
		Title:    "wire up the servo",
		Intent:   "solder header pins",
		Requires: []string{"hw:solder"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if payload.State != StateReview {
		t.Fatalf("result payload state = %s, want %s", payload.State, StateReview)
	}

	got, err := c.store.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != StateReview {
		t.Fatalf("manual task state = %s, want %s (a human has not acted yet)", got.State, StateReview)
	}

	var res map[string]any
	if err := json.Unmarshal([]byte(got.ResultJSON), &res); err != nil {
		t.Fatalf("result_json %q: %v", got.ResultJSON, err)
	}
	if res["manual"] != true {
		t.Fatalf("result_json = %v, want manual marker", res)
	}
	notify, _ := res["notify"].(string)
	if !strings.Contains(notify, "solder the servo header") {
		t.Fatalf("notify = %q, want the ability's notify text preserved", notify)
	}

	// The task is a live approval item: Approve moves it to done, proving the
	// review parking is a real queue entry and not a dead end.
	if err := c.store.Approve(ctx, tk.TaskID); err != nil {
		t.Fatalf("approve: %v", err)
	}
	after, err := c.store.Get(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("get after approve: %v", err)
	}
	if after.State != StateDone {
		t.Fatalf("approved manual task state = %s, want %s", after.State, StateDone)
	}
}
