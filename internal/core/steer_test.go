// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

// steerTestTask enqueues one task without a scheduler running, so it sits
// queued — the "message a parked task" case both send modes must support.
func steerTestTask(t *testing.T, c *Core) Task {
	t.Helper()
	tk, err := c.Enqueue(context.Background(), TaskInput{
		Title: "steer target", Intent: "work", Requires: []string{"sys:info"}, Authorized: true,
	}, DefaultQueueSpec())
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	return tk
}

// TestUserMessageModes pins the two send modes end to end at the kernel
// boundary: queue lands a plain board note the round drain folds as context;
// inject lands a steer-grade note the drain skips and the round's takeSteer
// delivers exactly once as the leading directive; a live round's cancel
// fires on inject and is reported as "interrupted".
func TestUserMessageModes(t *testing.T) {
	c := queueTestCore(t)
	ctx := context.Background()
	tk := steerTestTask(t, c)

	// queue: durable note, grade status, no interrupt semantics.
	if err := c.QueueUserMessage(ctx, tk.TaskID, "  also update the README  "); err != nil {
		t.Fatalf("queue message: %v", err)
	}
	// inject with no live round: reported as queued-for-boundary.
	status, err := c.InjectUserMessage(ctx, tk.TaskID, "stop editing; check the logs first")
	if err != nil || status != "queued" {
		t.Fatalf("inject status = %q err=%v, want queued", status, err)
	}
	// Both messages SURVIVE on the board: the per-sender slot would have
	// made the second overwrite the first, so user sends use one slot each.
	msgs, err := c.store.TaskMessages(ctx, "task:"+tk.TaskID)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("board after two sends: %v msgs=%d want 2", err, len(msgs))
	}
	byBody := map[string]TaskMessage{}
	for _, m := range msgs {
		byBody[m.Body] = m
	}
	if q := byBody["also update the README"]; q.Grade != MsgGradeStatus || q.SenderLabel != "user" {
		t.Fatalf("queue note = %+v", q)
	}
	if s := byBody["stop editing; check the logs first"]; s.Grade != MsgGradeSteer || s.SenderLabel != "user" {
		t.Fatalf("steer note = %+v", s)
	}
	// The drain folds the queue note (grade != steer) and the seen-set keeps
	// it once; the steer note is invisible to the drain — the directive path
	// owns it.
	task, _ := c.store.Get(ctx, tk.TaskID)
	seen := msgSeen{}
	lines := c.drainTaskMessages(ctx, task, seen)
	if len(lines) != 1 || !strings.Contains(lines[0], "also update the README") {
		t.Fatalf("drain after queue = %v", lines)
	}
	if again := c.drainTaskMessages(ctx, task, seen); len(again) != 0 {
		t.Fatalf("drain replayed seen note: %v", again)
	}
	if lines := c.drainTaskMessages(ctx, task, seen); len(lines) != 0 {
		t.Fatalf("drain folded a steer note: %v", lines)
	}
	if !c.steerUnseen(ctx, tk.TaskID, seen) {
		t.Fatal("steer note must read unseen before takeSteer")
	}
	// takeSteer delivers it once, as the leading directive.
	if got := c.takeSteer(ctx, tk.TaskID, seen); got != "stop editing; check the logs first" {
		t.Fatalf("takeSteer = %q", got)
	}
	if got := c.takeSteer(ctx, tk.TaskID, seen); got != "" {
		t.Fatalf("takeSteer replayed: %q", got)
	}
	if c.steerUnseen(ctx, tk.TaskID, seen) {
		t.Fatal("steer must read seen after takeSteer")
	}

	// inject with a live round: the registered cancel fires and the caller
	// is told the turn was aborted.
	fired := false
	c.registerInterrupt(tk.TaskID, func() { fired = true })
	status, err = c.InjectUserMessage(ctx, tk.TaskID, "abort and redo")
	if err != nil || status != "interrupted" || !fired {
		t.Fatalf("live inject: status=%q fired=%v err=%v", status, fired, err)
	}
	c.clearInterrupt(tk.TaskID)

	// Every posted note carries a real timestamp — a zero SentAt rendered
	// "~57 years ago" on `panda task board` and broke ordering by time.
	// Re-fetch first: the live inject above posted the third note, and the
	// freshness check (not just > 0) is what catches a wrong unit — a
	// millisecond stamp is positive too.
	msgs, err = c.store.TaskMessages(ctx, "task:"+tk.TaskID)
	if err != nil || len(msgs) != 3 {
		t.Fatalf("board after three sends: %v msgs=%d want 3", err, len(msgs))
	}
	now := time.Now().Unix()
	for _, m := range msgs {
		if age := now - m.SentAt; age < 0 || age > 60 {
			t.Fatalf("user message %q has SentAt=%d (age %ds), want a fresh unix ts", m.Body, m.SentAt, age)
		}
	}

	// A terminal task refuses both modes.
	if _, err := c.store.db.ExecContext(ctx,
		`UPDATE tasks SET state='failed' WHERE task_id=?`, tk.TaskID); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	if err := c.QueueUserMessage(ctx, tk.TaskID, "late"); err == nil {
		t.Fatal("queue must refuse a terminal task")
	}
	if _, err := c.InjectUserMessage(ctx, tk.TaskID, "late"); err == nil {
		t.Fatal("inject must refuse a terminal task")
	}
}

// TestUserMessageEmptyAndMissing pins the guards: empty text and unknown ids
// are refused before any board write.
func TestUserMessageEmptyAndMissing(t *testing.T) {
	c := queueTestCore(t)
	ctx := context.Background()
	if err := c.QueueUserMessage(ctx, "no-such-task", "hi"); err == nil {
		t.Fatal("unknown task must be refused")
	}
	tk := steerTestTask(t, c)
	if err := c.QueueUserMessage(ctx, tk.TaskID, "   "); err == nil {
		t.Fatal("empty text must be refused")
	}
	if _, err := c.InjectUserMessage(ctx, tk.TaskID, ""); err == nil {
		t.Fatal("empty inject must be refused")
	}
}

// TestPostUserMessageOwnerGuard pins the store-level owner check every
// delivery path now shares — including the scheduler-less fallbacks (panel
// without engine, REPL without engine, scheduler-less ask engine). A task
// owned by another node reads that node's board: a note landed here would
// silently never deliver, so the store refuses it.
func TestPostUserMessageOwnerGuard(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	mine := createTask(t, s, "", "local work", "local-node")
	remote := createTask(t, s, "", "remote work", "local-node")
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET owner_node=? WHERE task_id=?`, "peer-node", remote.TaskID); err != nil {
		t.Fatalf("reassign owner: %v", err)
	}
	if err := s.PostUserMessage(ctx, remote.TaskID, "hi", MsgGradeStatus, "local-node"); err == nil ||
		!strings.Contains(err.Error(), "runs on peer-node") {
		t.Fatalf("remote-owned task must refuse with executor hint, got %v", err)
	}
	if err := s.PostUserMessage(ctx, mine.TaskID, "hi", MsgGradeStatus, "local-node"); err != nil {
		t.Fatalf("local task must accept: %v", err)
	}
	// An ephemeral sibling of the owning node is the same runtime identity.
	if err := s.PostUserMessage(ctx, mine.TaskID, "hi", MsgGradeStatus, "local-node-1a2b3c4d"); err != nil {
		t.Fatalf("ephemeral sibling must accept: %v", err)
	}
	// A caller that cannot name the store owner degrades to no owner check.
	if err := s.PostUserMessage(ctx, remote.TaskID, "hi", MsgGradeStatus, ""); err != nil {
		t.Fatalf("empty local id skips the guard: %v", err)
	}
}
