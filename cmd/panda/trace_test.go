// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// `panda task <id> --trace` (Track 3): the hop structure derived from the
// event log, plus the delegation_metrics join.

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
)

type traceOut struct {
	TaskID    string   `json:"task_id"`
	Chain     []string `json:"chain"`
	Transport string   `json:"transport"`
	Hops      []struct {
		From   string  `json:"from"`
		To     string  `json:"to"`
		TS     int64   `json:"ts"`
		By     string  `json:"by,omitempty"`
		Events []int64 `json:"event_ids,omitempty"`
	} `json:"hops"`
	Metrics []struct {
		Delegator string `json:"delegator"`
		Executor  string `json:"executor"`
		Success   bool   `json:"success"`
		LatencyMs int64  `json:"latency_ms"`
		Tokens    int64  `json:"tokens,omitempty"`
	} `json:"metrics"`
}

func TestTraceJSONHopsFromEvents(t *testing.T) {
	task := core.Task{
		TaskID:    "t-1",
		OwnerNode: "gpu-box",
		Chain:     []string{"mac", "gpu-box"},
		Transport: "dtn",
	}
	events := []core.Event{
		{ID: 1, TaskID: "t-1", TS: 100, Type: core.EvSubmit, DataJSON: `{}`},
		{ID: 2, TaskID: "t-1", TS: 101, Type: core.EvDelegate, DataJSON: `{"target":"gpu-box","by":"queue"}`},
		{ID: 3, TaskID: "t-1", TS: 102, Type: core.EvAccept, DataJSON: `{"owner":"gpu-box"}`},
		{ID: 4, TaskID: "t-1", TS: 180, Type: core.EvResult, DataJSON: `{"ok":true}`},
	}
	out := traceJSON(task, events, []core.DelegationMetric{
		{TaskID: "t-1", Delegator: "mac", Executor: "gpu-box", Success: true, LatencyMs: 79000, Tokens: sql.NullInt64{Int64: 42, Valid: true}},
	})

	// Decode via JSON: the shape a --json consumer actually sees.
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got traceOut
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Transport != "dtn" || got.TaskID != "t-1" {
		t.Fatalf("head = %+v", got)
	}
	if len(got.Hops) != 1 {
		t.Fatalf("hops = %+v, want 1 hop", got.Hops)
	}
	if got.Hops[0].From != "mac" || got.Hops[0].To != "gpu-box" || got.Hops[0].By != "queue" {
		t.Fatalf("hop = %+v, want mac → gpu-box by queue", got.Hops[0])
	}
	// accept + result belong to the hop; the submit precedes it.
	if len(got.Hops[0].Events) != 2 {
		t.Fatalf("hop events = %v, want the two post-delegate events", got.Hops[0].Events)
	}
	if len(got.Metrics) != 1 || got.Metrics[0].Executor != "gpu-box" || got.Metrics[0].LatencyMs != 79000 || got.Metrics[0].Tokens != 42 {
		t.Fatalf("metrics = %+v", got.Metrics)
	}
}

func TestTraceEventField(t *testing.T) {
	if got := traceEventField(`{"target":"gpu-box"}`, "target"); got != "gpu-box" {
		t.Fatalf("target = %q", got)
	}
	if got := traceEventField(`{"target":3}`, "target"); got != "" {
		t.Fatalf("non-string = %q, want empty", got)
	}
	if got := traceEventField(`not-json`, "target"); got != "" {
		t.Fatalf("malformed = %q, want empty", got)
	}
}
