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
	// The fold is stamped on the row, not only in the in-memory seen-set: a
	// console in another process reads consumed_at to draw "waiting for the
	// next round" apart from "already read", and the seen-set is invisible to
	// it.
	if got := consumedAt(t, c, tk.TaskID, "also update the README"); got == 0 {
		t.Fatal("drained queue note must be stamped consumed")
	}
	if got := consumedAt(t, c, tk.TaskID, "stop editing; check the logs first"); got != 0 {
		t.Fatalf("steer note must stay unread until takeSteer, consumed_at=%d", got)
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
	if got := consumedAt(t, c, tk.TaskID, "stop editing; check the logs first"); got == 0 {
		t.Fatal("taken steer note must be stamped consumed")
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

// consumedAt reads one note's delivery stamp off the board, failing when the
// body is absent — a silent 0 would make the "must be stamped" assertions pass
// for the wrong reason.
func consumedAt(t *testing.T, c *Core, taskID, body string) int64 {
	t.Helper()
	msgs, err := c.store.TaskMessages(context.Background(), "task:"+taskID)
	if err != nil {
		t.Fatalf("read board: %v", err)
	}
	for _, m := range msgs {
		if m.Body == body {
			return m.ConsumedAt
		}
	}
	t.Fatalf("board has no note %q", body)
	return 0
}

// TestPendingNoteCounts pins the badge source both pickers read: unread user
// notes counted per task, and the count dropping to zero once a round folds
// them. A stale count is worse than no count — it tells the user their note is
// still queued when the agent already read it.
func TestPendingNoteCounts(t *testing.T) {
	c := queueTestCore(t)
	ctx := context.Background()
	tk := steerTestTask(t, c)
	if err := c.QueueUserMessage(ctx, tk.TaskID, "first"); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.QueueUserMessage(ctx, tk.TaskID, "second"); err != nil {
		t.Fatalf("queue 2: %v", err)
	}
	counts, err := c.store.PendingNoteCounts(ctx)
	if err != nil {
		t.Fatalf("pending counts: %v", err)
	}
	if got := counts[tk.TaskID]; got != 2 {
		t.Fatalf("pending notes = %d, want 2 (map=%v)", got, counts)
	}
	task, _ := c.store.Get(ctx, tk.TaskID)
	c.drainTaskMessages(ctx, task, msgSeen{})
	counts, err = c.store.PendingNoteCounts(ctx)
	if err != nil {
		t.Fatalf("pending counts after drain: %v", err)
	}
	if got := counts[tk.TaskID]; got != 0 {
		t.Fatalf("pending notes after drain = %d, want 0", got)
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

// TestUserNoteLifecycle pins the store-side lifecycle the console's pending
// strip drives: list → edit → drop, all on the same slot, none of it posting a
// second row.
func TestUserNoteLifecycle(t *testing.T) {
	c := queueTestCore(t)
	ctx := context.Background()
	tk := steerTestTask(t, c)

	msg, err := c.QueueUserNote(ctx, tk.TaskID, "check the tests", []string{"uploads/shot.png"})
	if err != nil {
		t.Fatalf("queue note: %v", err)
	}
	if !strings.HasPrefix(msg.Sender, userSenderPrefix) {
		t.Fatalf("note sender = %q, want a user:<seq> slot", msg.Sender)
	}
	// The agent reads the body; the console reads the prose. Both must be
	// right from one row: a chip list alone would leave the prompt without the
	// path, and the raw body alone would show the attachment header as prose.
	if !strings.Contains(msg.Body, "- @uploads/shot.png") {
		t.Fatalf("body should carry the attachment ref: %q", msg.Body)
	}
	if got := NoteProse(msg.Body); got != "check the tests" {
		t.Fatalf("NoteProse = %q", got)
	}

	notes, err := c.store.UserNotes(ctx, tk.TaskID)
	if err != nil || len(notes) != 1 {
		t.Fatalf("UserNotes = %v err=%v, want 1", notes, err)
	}
	if notes[0].Attachments == nil || notes[0].Attachments[0] != "uploads/shot.png" {
		t.Fatalf("attachments did not round-trip: %+v", notes[0].Attachments)
	}

	// Edit keeps the slot and the grade, bumps the seq (the store's stale-write
	// guard needs it) and drops the attachment the user removed.
	edited, err := c.store.EditUserNote(ctx, tk.TaskID, msg.Sender, "check the tests twice", nil, c.nodeID)
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if edited.Sender != msg.Sender || edited.Grade != MsgGradeStatus {
		t.Fatalf("edit changed identity: %+v", edited)
	}
	if edited.SenderSeq <= msg.SenderSeq {
		t.Fatalf("edit kept seq %d (was %d) — the store would drop it as stale", edited.SenderSeq, msg.SenderSeq)
	}
	if got := NoteProse(edited.Body); got != "check the tests twice" {
		t.Fatalf("edited prose = %q", got)
	}

	if err := c.store.DeleteUserNote(ctx, tk.TaskID, msg.Sender); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if notes, _ := c.store.UserNotes(ctx, tk.TaskID); len(notes) != 0 {
		t.Fatalf("note survived the drop: %+v", notes)
	}
}

// TestPromoteKeepsOneRow is the second-Enter contract: escalation rewrites the
// queued slot as a steer instead of posting a duplicate, so the round reads the
// user's words exactly once.
func TestPromoteKeepsOneRow(t *testing.T) {
	c := queueTestCore(t)
	ctx := context.Background()
	tk := steerTestTask(t, c)

	msg, err := c.QueueUserNote(ctx, tk.TaskID, "use tabs", nil)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	status, err := c.PromoteUserNote(ctx, tk.TaskID, msg.Sender)
	if err != nil || status != "queued" {
		t.Fatalf("promote = %q err=%v, want queued (no running round here)", status, err)
	}
	msgs, err := c.store.TaskMessages(ctx, "task:"+tk.TaskID)
	if err != nil {
		t.Fatalf("board: %v", err)
	}
	var user []TaskMessage
	for _, m := range msgs {
		if isUserSender(m.Sender) {
			user = append(user, m)
		}
	}
	if len(user) != 1 {
		t.Fatalf("promote posted a second row: %+v", user)
	}
	if user[0].Grade != MsgGradeSteer {
		t.Fatalf("promoted grade = %q, want steer", user[0].Grade)
	}
	if user[0].SenderSeq <= msg.SenderSeq {
		t.Fatalf("promoted seq %d did not advance past %d", user[0].SenderSeq, msg.SenderSeq)
	}

	// A live round's cancel is what turns the same call into "interrupted".
	cctx, cancel := context.WithCancel(context.Background())
	c.registerInterrupt(tk.TaskID, cancel)
	msg2, err := c.QueueUserNote(ctx, tk.TaskID, "and run gofmt", nil)
	if err != nil {
		t.Fatalf("queue 2: %v", err)
	}
	status, err = c.PromoteUserNote(ctx, tk.TaskID, msg2.Sender)
	if err != nil || status != "interrupted" {
		t.Fatalf("promote with a live round = %q err=%v, want interrupted", status, err)
	}
	if cctx.Err() == nil {
		t.Fatal("promote did not abort the live round")
	}
	c.clearInterrupt(tk.TaskID)
}

// TestConsumedNoteRefusesEdits keeps the board honest: once a round folded a
// note into its prompt, rewriting or deleting the row would leave the board
// disagreeing with what the model was actually told.
func TestConsumedNoteRefusesEdits(t *testing.T) {
	c := queueTestCore(t)
	ctx := context.Background()
	tk := steerTestTask(t, c)
	msg, err := c.QueueUserNote(ctx, tk.TaskID, "already read", nil)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := c.store.MarkTaskMessageConsumed(ctx, msg.Scope, msg.Sender, msg.SenderSeq); err != nil {
		t.Fatalf("mark consumed: %v", err)
	}
	if _, err := c.store.EditUserNote(ctx, tk.TaskID, msg.Sender, "rewritten", nil, c.nodeID); err == nil ||
		!strings.Contains(err.Error(), "already read") {
		t.Fatalf("edit of a consumed note = %v, want an already-read refusal", err)
	}
	if err := c.store.DeleteUserNote(ctx, tk.TaskID, msg.Sender); err == nil ||
		!strings.Contains(err.Error(), "already read") {
		t.Fatalf("drop of a consumed note = %v, want an already-read refusal", err)
	}
	if _, err := c.store.EditUserNote(ctx, tk.TaskID, "user:999", "ghost", nil, c.nodeID); err == nil ||
		!strings.Contains(err.Error(), "no such note") {
		t.Fatalf("edit of an unknown key = %v, want no-such-note", err)
	}
}

func TestWatchRoundSteerInterruptsForAnotherProcess(t *testing.T) {
	c := queueTestCore(t)
	tk := steerTestTask(t, c)
	roundCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go c.watchRoundSteer(roundCtx, tk.TaskID, msgSeen{}, cancel, done)

	_, status, err := c.InjectUserNote(context.Background(), tk.TaskID, "inspect the screenshot first", nil)
	if err != nil || status != "queued" {
		t.Fatalf("remote inject = %q, %v", status, err)
	}
	select {
	case <-roundCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("running round did not observe the durable inject")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("steer watcher did not stop after cancelling the round")
	}
}
