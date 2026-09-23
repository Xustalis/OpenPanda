package core

import (
	"context"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

func TestTaskOutboxPersistAndDrop(t *testing.T) {
	ctx := context.Background()
	c := newCore(t, "node-outbox", "")

	p := bus.TaskDelegatePayload{
		TaskID: "task-dtn-1",
		Title:  "offline delegation",
		Intent: "run job",
	}

	// Persist to outbox
	c.taskOutboxPersist(ctx, "target-peer", p, "dtn", 60000)

	// Verify row exists in task_outbox
	var count int
	err := c.db.QueryRowContext(ctx, `SELECT count(*) FROM task_outbox WHERE peer = ? AND task_id = ?`,
		"target-peer", "task-dtn-1").Scan(&count)
	if err != nil {
		t.Fatalf("query task_outbox: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row in task_outbox, got %d", count)
	}

	// Drop from outbox
	c.taskOutboxDrop(ctx, "target-peer", "task-dtn-1")

	err = c.db.QueryRowContext(ctx, `SELECT count(*) FROM task_outbox WHERE peer = ? AND task_id = ?`,
		"target-peer", "task-dtn-1").Scan(&count)
	if err != nil {
		t.Fatalf("query task_outbox after drop: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 rows in task_outbox after drop, got %d", count)
	}
}

func TestTaskOutboxFlushOnHello(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := newCore(t, "node-a", "127.0.0.1:17961")
	b := newCore(t, "node-b", "127.0.0.1:17962")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(a.Register(ctx))
	must(b.Register(ctx))

	go func() { _ = a.Listen(ctx, "127.0.0.1:17961") }()
	go func() { _ = b.Listen(ctx, "127.0.0.1:17962") }()
	time.Sleep(100 * time.Millisecond)

	// Park task in a's outbox for b BEFORE b connects
	taskID := "task-parked-1"
	p := bus.TaskDelegatePayload{
		TaskID: taskID,
		Title:  "delayed task",
		Intent: "dtn delayed delivery",
	}
	a.taskOutboxPersist(ctx, "node-b", p, "dtn", 60000)

	// Now connect a -> b
	must(a.DialPeer(ctx, "127.0.0.1:17962"))
	waitPeer(t, a, "node-b")
	waitPeer(t, b, "node-a")
	time.Sleep(300 * time.Millisecond)

	// Verify task_outbox row in a was flushed and dropped
	var count int
	_ = a.db.QueryRowContext(ctx, `SELECT count(*) FROM task_outbox WHERE peer = ? AND task_id = ?`,
		"node-b", taskID).Scan(&count)
	if count != 0 {
		t.Fatalf("expected task_outbox to be flushed and empty, but got %d", count)
	}
}
