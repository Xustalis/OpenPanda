// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

func TestRegressionExpiredChildReleasesFanout(t *testing.T) {
	for _, state := range []string{StateDone, StateFailed, StateCancelled, StateExpired} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			c := newCore(t, "fanout-deep-probe")
			c.SetDelegationLimits(config.DelegationConfig{MaxChildren: 8, FanOut: 1, MaxDepth: 2})
			parent, _, _, err := c.createTask(ctx, TaskInput{Title: "parent", Intent: "coordinate bounded workers"})
			if err != nil {
				t.Fatal(err)
			}
			for _, step := range []func() error{
				func() error { return c.store.Queue(ctx, parent.TaskID, c.nodeID) },
				func() error { return c.store.Dispatch(ctx, parent.TaskID, c.nodeID, c.nodeID) },
				func() error { return c.store.Accept(ctx, parent.TaskID, c.nodeID) },
			} {
				if err := step(); err != nil {
					t.Fatal(err)
				}
			}
			first, err := c.SpawnChildTask(ctx, parent.TaskID, TaskInput{Title: "first", Intent: "first worker"})
			if err != nil {
				t.Fatal(err)
			}
			switch state {
			case StateExpired:
				err = c.store.MarkExpired(ctx, first.TaskID, "worker TTL elapsed")
			case StateCancelled:
				err = c.store.Cancel(ctx, first.TaskID)
			case StateFailed:
				err = c.store.ForceFail(ctx, first.TaskID, "settled failure")
			case StateDone:
				for _, step := range []func() error{
					func() error { return c.store.Queue(ctx, first.TaskID, c.nodeID) },
					func() error { return c.store.Dispatch(ctx, first.TaskID, c.nodeID, c.nodeID) },
					func() error { return c.store.Accept(ctx, first.TaskID, c.nodeID) },
					func() error { return c.store.Complete(ctx, first.TaskID, c.nodeID, map[string]any{"ok": true}) },
				} {
					if err := step(); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			done, err := c.store.Get(ctx, first.TaskID)
			if err != nil || done.State != state {
				t.Fatalf("fixture must be settled: %s %v", done.State, err)
			}
			children, err := c.store.Children(ctx, parent.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			active := 0
			for _, ch := range children {
				if ch.State != StateDone && ch.State != StateFailed && ch.State != StateCancelled && ch.State != StateExpired {
					active++
				}
			}
			if active != 0 {
				t.Fatalf("fixture still active=%d", active)
			}
			next, err := c.SpawnChildTask(ctx, parent.TaskID, TaskInput{Title: "replacement", Intent: "new worker after prior worker ended"})
			t.Logf("prior=%s active=%d replacement=%s error=%v", done.State, active, next.TaskID, err)
			if err != nil {
				t.Errorf("terminal child still consumed fanout slot: %v", err)
			}
			// A queued/submitted replacement still occupies the only slot.
			if _, err := c.SpawnChildTask(ctx, parent.TaskID, TaskInput{Title: "over fanout"}); err == nil {
				t.Fatal("active replacement must occupy fanout")
			}
			if err := c.store.MarkExpired(ctx, next.TaskID, "replacement expired"); err != nil {
				t.Fatal(err)
			}
			// Two simultaneous replacements may consume only one active slot.
			var wg sync.WaitGroup
			created := make(chan Task, 2)
			for range 2 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					child, err := c.SpawnChildTask(ctx, parent.TaskID, TaskInput{Title: "concurrent replacement"})
					if err == nil {
						created <- child
					}
				}()
			}
			wg.Wait()
			close(created)
			if len(created) != 1 {
				t.Fatalf("concurrent fanout admitted %d tasks", len(created))
			}
			last := <-created
			if err := c.store.MarkExpired(ctx, last.TaskID, "finished"); err != nil {
				t.Fatal(err)
			}
			c.SetDelegationLimits(config.DelegationConfig{MaxChildren: 3, FanOut: 1, MaxDepth: 2})
			if _, err := c.SpawnChildTask(ctx, parent.TaskID, TaskInput{Title: "over lifetime limit"}); err == nil {
				t.Fatal("expiry must not refund MaxChildren")
			}
		})
	}
}
