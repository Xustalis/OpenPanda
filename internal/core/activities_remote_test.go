// SPDX-License-Identifier: AGPL-3.0-or-later
package core

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

// Real WebSocket delivery exercises each custodian's independent cursor and
// terminal catchup: a leaf's durable history must survive a relay, pagination,
// and repeated advertisements after the task has already completed.
func TestRemoteActivityThreeNodeTerminalCatchup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root, mid, leaf := startChain(t, ctx)
	task, result, err := root.Submit(ctx, TaskInput{Title: "activity relay", Intent: "read gpio", Requires: []string{"gpio:read"}})
	if err != nil || !result.OK {
		t.Fatalf("submit: %+v, %v", result, err)
	}
	for i := range 70 {
		if err := leaf.store.RecordEvent(ctx, task.TaskID, EvAgentEvent, map[string]any{
			"ev": "tool_use", "name": "shell", "attempt_id": task.AttemptID,
			"input": map[string]any{"command": fmt.Sprintf("step %d", i)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	leaf.advertisePeerActivities(ctx, mid.nodeID)
	var origin string
	wait := func(c *Core) {
		t.Helper()
		for {
			events, err := c.store.Events(ctx, task.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, event := range events {
				if event.Type != EvAgentEvent {
					continue
				}
				var data map[string]any
				_ = json.Unmarshal([]byte(event.DataJSON), &data)
				if data["executor_id"] != leaf.nodeID || data["remote"] != true {
					t.Fatalf("lost source on %s: %s", c.nodeID, event.DataJSON)
				}
				if n == 0 {
					id, _ := data["activity_id"].(string)
					if origin == "" {
						origin = id
					} else if id != origin {
						t.Fatalf("relay changed identity: %s vs %s", id, origin)
					}
				}
				n++
			}
			if n == 70 {
				return
			}
			if n > 70 {
				t.Fatalf("duplicate activities on %s: %d", c.nodeID, n)
			}
			select {
			case <-ctx.Done():
				t.Fatalf("only %d activities on %s: %v", n, c.nodeID, ctx.Err())
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	wait(mid)
	wait(root)
	for range 2 {
		leaf.advertisePeerActivities(ctx, mid.nodeID)
		mid.advertisePeerActivities(ctx, root.nodeID)
	}
	wait(mid)
	wait(root)
	if stored, err := root.store.Get(ctx, task.TaskID); err != nil || stored.State != StateDone {
		t.Fatalf("catchup reopened terminal task: %+v, %v", stored, err)
	}
}

func TestRemoteActivityPersistenceAndDeduplication(t *testing.T) {
	c, task, _ := runningRPCParent(t)
	ctx := context.Background()
	p := bus.TaskActivitiesPayload{TaskID: task.TaskID, AttemptID: task.AttemptID, Cursor: 99, Activities: []bus.TaskActivity{{ID: "worker:42", Executor: "worker", TS: 123, Type: EvAgentEvent, Data: json.RawMessage(`{"ev":"tool_use","name":"shell","input":{"command":"go test ./..."}}`)}}}
	for range 2 {
		if err := c.store.importActivities(ctx, p, "relay"); err != nil {
			t.Fatal(err)
		}
	}
	if got := c.store.receivedActivityCursor(ctx, task.TaskID, task.AttemptID, "relay"); got != 99 {
		t.Fatalf("cursor %d", got)
	}
	events, _ := c.store.Events(ctx, task.TaskID)
	n := 0
	for _, event := range events {
		if event.Type == EvAgentEvent {
			n++
			var d map[string]any
			_ = json.Unmarshal([]byte(event.DataJSON), &d)
			if d["executor_id"] != "worker" || d["activity_id"] != "worker:42" {
				t.Fatalf("lost origin: %s", event.DataJSON)
			}
		}
	}
	if n != 1 {
		t.Fatalf("replayed activity count %d", n)
	}
	p.AttemptID = "old"
	if err := c.store.importActivities(ctx, p, "relay"); err == nil {
		t.Fatal("stale attempt imported")
	}
}

func TestRemoteActivityRejectsInvalidBatchAtomically(t *testing.T) {
	c, task, _ := runningRPCParent(t)
	ctx := context.Background()
	p := bus.TaskActivitiesPayload{TaskID: task.TaskID, AttemptID: task.AttemptID, Cursor: 5, Activities: []bus.TaskActivity{{ID: "x", Executor: "worker", Type: EvAgentEvent, Data: json.RawMessage(`{}`)}, {ID: "y", Executor: "worker", Type: EvUserMsg, Data: json.RawMessage(`{}`)}}}
	if err := c.store.importActivities(ctx, p, "worker"); err == nil {
		t.Fatal("control event accepted as activity")
	}
	if got := c.store.receivedActivityCursor(ctx, task.TaskID, task.AttemptID, "worker"); got != 0 {
		t.Fatalf("invalid cursor committed: %d", got)
	}
	if got := c.store.activityCursor(ctx, task.TaskID); got != 0 {
		t.Fatalf("partial activity committed: %d", got)
	}
}
