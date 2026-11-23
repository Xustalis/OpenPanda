// SPDX-License-Identifier: AGPL-3.0-or-later

package core

// User-message delivery into live tasks — the two send modes users know from
// Claude Code and Codex, mapped onto this kernel's execution model:
//
//	queue  — a durable note on the task's own board scope ("task:<id>"), the
//	         same channel sibling tasks post on. The running agent finishes
//	         its current round and reads the note as context at the next one;
//	         a parked or queued task reads it at its first round. Nothing is
//	         interrupted.
//
//	inject — the same durable note, marked with the steer grade, plus the
//	         abort of the in-flight agent round when one is live. The steer
//	         text LEADS the next round's intent, ahead of the supervisor's
//	         follow-up: the user's words become the round's primary
//	         instruction, not context.
//
// Both modes are board-backed and therefore restart-safe: the note is the
// delivery mechanism, and the round loop's seen-set makes each message land
// exactly once. The only in-memory state is the running round's cancel — the
// handle an inject needs to abort work in flight.
//
// Both modes refuse terminal tasks.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/scheduler"
)

// QueueUserMessage posts a durable advisory note to the task's own board
// scope. The next round (or the first, for a task that has not started)
// folds it into the agent's intent; a finished task refuses.
func (c *Core) QueueUserMessage(ctx context.Context, taskID, text string) error {
	_, err := c.userMessage(ctx, taskID, text, nil, MsgGradeStatus, "queue")
	return err
}

// QueueUserNote is QueueUserMessage carrying files, returning the slot it
// landed so the caller can edit or promote that exact note afterwards.
func (c *Core) QueueUserNote(ctx context.Context, taskID, text string, attachments []string) (TaskMessage, error) {
	return c.userMessage(ctx, taskID, text, attachments, MsgGradeStatus, "queue")
}

// InjectUserMessage posts the steer note AND aborts the running agent round
// when one is live. Returns "interrupted" when a running turn was aborted,
// "queued" when the steer waits for the next round boundary (or the task's
// first run — the note is durable either way).
func (c *Core) InjectUserMessage(ctx context.Context, taskID, text string) (string, error) {
	_, status, err := c.InjectUserNote(ctx, taskID, text, nil)
	return status, err
}

// InjectUserNote is InjectUserMessage carrying files. The slot is returned so
// a surface can keep addressing the note it just sent.
func (c *Core) InjectUserNote(ctx context.Context, taskID, text string, attachments []string) (TaskMessage, string, error) {
	msg, err := c.userMessage(ctx, taskID, text, attachments, MsgGradeSteer, "inject")
	if err != nil {
		return TaskMessage{}, "", err
	}
	return msg, c.interrupt(taskID), nil
}

// PromoteUserNote turns an already-queued note into an inject without posting
// a second row: the same slot is re-written with the steer grade, so the next
// round carries the user's words exactly once, as a directive instead of
// context. Returns "interrupted" when a live round was aborted here,
// "queued" when the steer waits for the round boundary.
//
// This is the second-Enter escalation: a surface that queued a note on the
// first Enter must not send a duplicate on the second.
func (c *Core) PromoteUserNote(ctx context.Context, taskID, sender string) (string, error) {
	if c == nil || c.store == nil {
		return "", fmt.Errorf("task messages require a task store")
	}
	old, err := c.store.userNote(ctx, taskID, sender)
	if err != nil {
		return "", err
	}
	if old.ConsumedAt != 0 {
		return "", fmt.Errorf("note %q was already read by the round — send a new one", sender)
	}
	msg := old
	msg.Grade = MsgGradeSteer
	msg.SenderSeq = old.SenderSeq + 1
	msg.SentAt = time.Now().Unix()
	if err := c.store.PutTaskMessage(ctx, msg); err != nil {
		return "", fmt.Errorf("promote user message: %w", err)
	}
	c.EvTrace(ctx, taskID, EvUserMsg, map[string]any{"mode": "inject", "text": old.Body})
	return c.interrupt(taskID), nil
}

// interrupt aborts the round this process is running for the task, when one
// is live, and reports which promise the caller may make: "interrupted" or
// "queued". The cancel handle is process-local (see registerInterrupt), so a
// task executing in another process always answers "queued" — which is the
// truth, not a downgrade.
func (c *Core) interrupt(taskID string) string {
	c.steerMu.Lock()
	cancel := c.interrupts[taskID]
	c.steerMu.Unlock()
	if cancel == nil {
		return "queued"
	}
	cancel()
	return "interrupted"
}

// userMessage is the shared body: land the durable board note (the store
// applies the state and owner guards) and trace the event.
func (c *Core) userMessage(ctx context.Context, taskID, text string, attachments []string, grade, mode string) (TaskMessage, error) {
	if c == nil || c.store == nil {
		return TaskMessage{}, fmt.Errorf("task messages require a task store")
	}
	msg, err := c.store.PostUserNote(ctx, taskID, text, attachments, grade, c.nodeID)
	if err != nil {
		return TaskMessage{}, err
	}
	c.EvTrace(ctx, taskID, EvUserMsg, map[string]any{"mode": mode, "text": msg.Body, "seq": msg.SenderSeq})
	return msg, nil
}

// PostUserMessage lands a user message on the task's own board scope. The
// state guard refuses terminal tasks; the board's per-sender slot keeps the
// newest message, so a user who sends twice carries the second one — the
// same semantics as every other board sender. This is the scheduler-less
// delivery path too: a note needs the store, not the execution core.
//
// localNodeID is the runtime identity of the node this store belongs to; it
// backs the owner guard — a task dispatched to another node reads ITS
// store's board, not ours, so a note landed here would never reach the
// agent. The guard refuses honestly instead of accepting a message nobody
// will read. The comparison is runtime identity, not literal equality: a
// CLI engine's ephemeral id and the daemon's base id resolve to the same
// store-owning node. An empty localNodeID skips the check (callers that
// cannot name the store's owner degrade to the state guard alone).
func (s *TaskStore) PostUserMessage(ctx context.Context, taskID, text, grade, localNodeID string) error {
	_, err := s.PostUserNote(ctx, taskID, text, nil, grade, localNodeID)
	return err
}

// PostUserNote is PostUserMessage returning the slot it landed, so a sender
// can address that exact note afterwards (edit / delete / promote). The
// returned Sender is the row's board id (`user:<seq>`), not "user".
func (s *TaskStore) PostUserNote(ctx context.Context, taskID, text string, attachments []string, grade, localNodeID string) (TaskMessage, error) {
	if s == nil {
		return TaskMessage{}, fmt.Errorf("task messages require a task store")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return TaskMessage{}, fmt.Errorf("message text must not be empty")
	}
	t, err := s.Get(ctx, taskID)
	if err != nil {
		return TaskMessage{}, err
	}
	if terminalState(t.State) {
		return TaskMessage{}, fmt.Errorf("task %s is %s — messages reach running or queued work only", taskID, t.State)
	}
	if localNodeID != "" && t.OwnerNode != "" &&
		!scheduler.SameRuntimeIdentity(t.OwnerNode, localNodeID) {
		return TaskMessage{}, fmt.Errorf("task %s runs on %s — messages deliver to the executor's own store", taskID, t.OwnerNode)
	}
	// The board's slot key is (scope, sender, rcpt) with newest-wins per
	// slot — right for status notes, wrong for user messages, where every
	// send must survive. A per-message sender id (`user:<seq>`) gives each
	// its own slot; SenderLabel keeps the rendered line reading "user". The
	// seq is the max across every user sender on the scope (NextMsgSeq's
	// exact-sender match would miss the prefixed ids and restart at 1).
	scope := "task:" + taskID
	var seq int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(sender_seq),0) FROM task_messages WHERE scope=? AND (sender=? OR sender LIKE ?)`,
		scope, "user", userSenderPrefix+"%").Scan(&seq); err != nil {
		return TaskMessage{}, fmt.Errorf("user message seq: %w", err)
	}
	seq++
	msg := TaskMessage{
		Scope:       scope,
		Sender:      fmt.Sprintf("%s%d", userSenderPrefix, seq),
		SenderLabel: "user",
		Rcpt:        taskID,
		SenderSeq:   seq,
		Grade:       grade,
		Body:        noteBody(text, attachments),
		Attachments: attachments,
		// SentAt went unset once and every user note rendered as "~57 years
		// ago" on `panda task board` — the store writes m.SentAt verbatim.
		SentAt: time.Now().Unix(),
	}
	if err := s.PutTaskMessage(ctx, msg); err != nil {
		return TaskMessage{}, fmt.Errorf("post user message: %w", err)
	}
	return msg, nil
}

// noteBody is the text the agent reads: the user's prose plus one `@path`
// line per attachment, in the same shape the web composer's attachment block
// uses. Keeping the list in the body (as well as the Attachments column) is
// what makes the note self-contained — the round folds bodies, never chips.
func noteBody(text string, attachments []string) string {
	text = strings.TrimSpace(text)
	kept := make([]string, 0, len(attachments))
	for _, a := range attachments {
		if a = strings.TrimSpace(a); a != "" {
			kept = append(kept, a)
		}
	}
	if len(kept) == 0 {
		return text
	}
	var b strings.Builder
	if text != "" {
		b.WriteString(text)
		b.WriteString("\n\n")
	}
	b.WriteString("Attached files (read them from the workspace):\n")
	for _, a := range kept {
		fmt.Fprintf(&b, "- @%s\n", a)
	}
	return strings.TrimRight(b.String(), "\n")
}

// userNote loads one live user slot. A row that expired with its board, or
// that names an agent sender, is "no such note" — editing another sender's
// line is not a user affordance.
func (s *TaskStore) userNote(ctx context.Context, taskID, sender string) (TaskMessage, error) {
	if s == nil {
		return TaskMessage{}, fmt.Errorf("task messages require a task store")
	}
	if !isUserSender(sender) || sender == "user" {
		return TaskMessage{}, fmt.Errorf("no such note %q", sender)
	}
	msgs, err := s.TaskMessages(ctx, "task:"+taskID)
	if err != nil {
		return TaskMessage{}, err
	}
	for _, m := range msgs {
		if m.Sender == sender {
			return m, nil
		}
	}
	return TaskMessage{}, fmt.Errorf("no such note %q", sender)
}

// UserNotes lists the user's unconsumed notes on a task board, oldest first —
// what a console draws as the pending strip above its composer. Consumed
// notes are excluded: the round already folded them into a prompt, so there
// is nothing left to edit, drop, or promote.
func (s *TaskStore) UserNotes(ctx context.Context, taskID string) ([]TaskMessage, error) {
	msgs, err := s.TaskMessages(ctx, "task:"+taskID)
	if err != nil {
		return nil, err
	}
	out := make([]TaskMessage, 0, len(msgs))
	for _, m := range msgs {
		if isUserSender(m.Sender) && m.ConsumedAt == 0 && m.Rcpt == taskID {
			out = append(out, m)
		}
	}
	return out, nil
}

// EditUserNote rewrites a pending note in place, keeping its slot and grade.
// The seq is bumped so the store's stale-write guard accepts the update, and
// SentAt moves to now so the edited note re-sorts to the tail of the board —
// an edit is the user saying "this is what I mean now".
//
// A note the round already read is refused: rewriting history would leave the
// agent's prompt and the board disagreeing about what was said.
func (s *TaskStore) EditUserNote(ctx context.Context, taskID, sender, text string, attachments []string, localNodeID string) (TaskMessage, error) {
	text = strings.TrimSpace(text)
	if text == "" && len(attachments) == 0 {
		return TaskMessage{}, fmt.Errorf("message text must not be empty")
	}
	old, err := s.userNote(ctx, taskID, sender)
	if err != nil {
		return TaskMessage{}, err
	}
	if old.ConsumedAt != 0 {
		return TaskMessage{}, fmt.Errorf("note %q was already read by the round — send a new one", sender)
	}
	t, err := s.Get(ctx, taskID)
	if err != nil {
		return TaskMessage{}, err
	}
	if terminalState(t.State) {
		return TaskMessage{}, fmt.Errorf("task %s is %s — messages reach running or queued work only", taskID, t.State)
	}
	if localNodeID != "" && t.OwnerNode != "" && !scheduler.SameRuntimeIdentity(t.OwnerNode, localNodeID) {
		return TaskMessage{}, fmt.Errorf("task %s runs on %s — messages deliver to the executor's own store", taskID, t.OwnerNode)
	}
	msg := TaskMessage{
		Scope:       old.Scope,
		Sender:      old.Sender,
		SenderLabel: "user",
		Rcpt:        taskID,
		SenderSeq:   old.SenderSeq + 1,
		Grade:       old.Grade,
		Body:        noteBody(text, attachments),
		Attachments: attachments,
		SentAt:      time.Now().Unix(),
	}
	if err := s.PutTaskMessage(ctx, msg); err != nil {
		return TaskMessage{}, fmt.Errorf("edit user message: %w", err)
	}
	return msg, nil
}

// DeleteUserNote drops a pending note from the board. Guarded by the same
// "already read" rule as Edit: a note the round consumed is part of the
// transcript's history, and removing the row would only make the board lie.
func (s *TaskStore) DeleteUserNote(ctx context.Context, taskID, sender string) error {
	old, err := s.userNote(ctx, taskID, sender)
	if err != nil {
		return err
	}
	if old.ConsumedAt != 0 {
		return fmt.Errorf("note %q was already read by the round — nothing left to drop", sender)
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM task_messages WHERE scope=? AND sender=? AND consumed_at=0`,
		old.Scope, old.Sender); err != nil {
		return fmt.Errorf("delete user message: %w", err)
	}
	return nil
}

// userSenderPrefix marks the sender ids of user messages (steer.go): each
// message gets its own slot via `user:<seq>`, and the prefix is what the
// round loop's steer/drain matching keys on.
const userSenderPrefix = "user:"

// isUserSender reports whether a board sender id is a user message.
func isUserSender(sender string) bool {
	return sender == "user" || strings.HasPrefix(sender, userSenderPrefix)
}

// takeSteer consumes the pending steer note, if any — called once at the top
// of each round so the directive leads that round's intent exactly once. The
// seen-set is the round loop's board cursor: a note the loop already folded
// (or delivered as a directive) never re-fires.
func (c *Core) takeSteer(ctx context.Context, taskID string, seen msgSeen) string {
	body, _ := c.takeRoundSteer(ctx, taskID, seen)
	return body
}

func (c *Core) takeRoundSteer(ctx context.Context, taskID string, seen msgSeen) (string, string) {
	if c == nil || c.store == nil {
		return "", ""
	}
	msgs, err := c.store.TaskMessages(ctx, "task:"+taskID)
	if err != nil {
		return "", ""
	}
	for _, m := range msgs {
		if !pendingRoundSteer(m, taskID, seen) {
			continue
		}
		k := msgSlotKey(m)
		seen[k] = m.SenderSeq
		// Write the delivery back to the row. The in-memory seen-set is the
		// round loop's own cursor and nothing else can read it, so a console in
		// another process — the web panel, a remote daemon — would otherwise
		// keep drawing this note as "waiting for the next round" forever.
		// Best-effort by design: the note has already been read here, and a
		// failed stamp costs a stale badge, never a message.
		if err := c.store.MarkTaskMessageConsumed(ctx, m.Scope, m.Sender, m.SenderSeq); err != nil && c.logger != nil {
			c.logger.Debug("mark steer consumed", "task", taskID, "err", err)
		}
		if strings.HasPrefix(m.Sender, planSenderPrefix) {
			c.EvTrace(ctx, taskID, EvPlanAdjustmentRead, map[string]any{"decision_id": strings.TrimPrefix(m.Sender, planSenderPrefix), "instruction": m.Body})
			return strings.TrimSpace(m.Body), "[Panda plan adjustment]"
		}
		c.EvTrace(ctx, taskID, EvUserMsgConsumed, map[string]any{
			"mode": "inject", "seq": m.SenderSeq, "text": m.Body,
		})
		return strings.TrimSpace(m.Body), "[user directive]"
	}
	return "", ""
}

func pendingRoundSteer(m TaskMessage, taskID string, seen msgSeen) bool {
	if m.Grade != MsgGradeSteer || m.ConsumedAt != 0 || seen[msgSlotKey(m)] >= m.SenderSeq {
		return false
	}
	return isUserSender(m.Sender) || (m.Rcpt == taskID && strings.HasPrefix(m.Sender, planSenderPrefix) && m.ConsumedAt == 0)
}

// steerUnseen reports whether an undelivered steer note is waiting — the
// round loop's interrupt and done-verdict guards key on this so the message
// is never closed over unread.
func (c *Core) steerUnseen(ctx context.Context, taskID string, seen msgSeen) bool {
	if c == nil || c.store == nil {
		return false
	}
	msgs, err := c.store.TaskMessages(ctx, "task:"+taskID)
	if err != nil {
		return false
	}
	for _, m := range msgs {
		if pendingRoundSteer(m, taskID, seen) {
			return true
		}
	}
	return false
}

// watchRoundSteer wakes the executor when another local process posts a directive.
func (c *Core) watchRoundSteer(ctx context.Context, taskID string, seen msgSeen, cancel context.CancelFunc, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if c.steerUnseen(ctx, taskID, seen) {
				cancel()
				return
			}
		}
	}
}

// registerInterrupt publishes the running round's cancel so an inject can
// abort the in-flight agent turn. Paired with clearInterrupt on every exit
// path of the round.
func (c *Core) registerInterrupt(taskID string, cancel context.CancelFunc) {
	c.steerMu.Lock()
	if c.interrupts == nil {
		c.interrupts = map[string]context.CancelFunc{}
	}
	c.interrupts[taskID] = cancel
	c.steerMu.Unlock()
}

func (c *Core) clearInterrupt(taskID string) {
	c.steerMu.Lock()
	delete(c.interrupts, taskID)
	c.steerMu.Unlock()
}

// terminalState reports whether a task state ends the row's life.
func terminalState(state string) bool {
	switch state {
	case StateDone, StateFailed, StateCancelled, StateExpired:
		return true
	}
	return false
}
