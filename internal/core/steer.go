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
	return c.userMessage(ctx, taskID, text, MsgGradeStatus, "queue")
}

// InjectUserMessage posts the steer note AND aborts the running agent round
// when one is live. Returns "interrupted" when a running turn was aborted,
// "queued" when the steer waits for the next round boundary (or the task's
// first run — the note is durable either way).
func (c *Core) InjectUserMessage(ctx context.Context, taskID, text string) (string, error) {
	if err := c.userMessage(ctx, taskID, text, MsgGradeSteer, "inject"); err != nil {
		return "", err
	}
	c.steerMu.Lock()
	cancel := c.interrupts[taskID]
	c.steerMu.Unlock()
	if cancel != nil {
		cancel()
		return "interrupted", nil
	}
	return "queued", nil
}

// userMessage is the shared body: land the durable board note (the store
// applies the state and owner guards) and trace the event.
func (c *Core) userMessage(ctx context.Context, taskID, text, grade, mode string) error {
	if c == nil || c.store == nil {
		return fmt.Errorf("task messages require a task store")
	}
	if err := c.store.PostUserMessage(ctx, taskID, text, grade, c.nodeID); err != nil {
		return err
	}
	c.EvTrace(ctx, taskID, EvUserMsg, map[string]any{"mode": mode, "text": text})
	return nil
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
	if s == nil {
		return fmt.Errorf("task messages require a task store")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("message text must not be empty")
	}
	t, err := s.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if terminalState(t.State) {
		return fmt.Errorf("task %s is %s — messages reach running or queued work only", taskID, t.State)
	}
	if localNodeID != "" && t.OwnerNode != "" &&
		!scheduler.SameRuntimeIdentity(t.OwnerNode, localNodeID) {
		return fmt.Errorf("task %s runs on %s — messages deliver to the executor's own store", taskID, t.OwnerNode)
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
		return fmt.Errorf("user message seq: %w", err)
	}
	seq++
	if err := s.PutTaskMessage(ctx, TaskMessage{
		Scope:       scope,
		Sender:      fmt.Sprintf("%s%d", userSenderPrefix, seq),
		SenderLabel: "user",
		Rcpt:        taskID,
		SenderSeq:   seq,
		Grade:       grade,
		Body:        text,
		// SentAt went unset once and every user note rendered as "~57 years
		// ago" on `panda task board` — the store writes m.SentAt verbatim.
		SentAt: time.Now().Unix(),
	}); err != nil {
		return fmt.Errorf("post user message: %w", err)
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
	if c == nil || c.store == nil {
		return ""
	}
	msgs, err := c.store.TaskMessages(ctx, "task:"+taskID)
	if err != nil {
		return ""
	}
	for _, m := range msgs {
		if !isUserSender(m.Sender) || m.Grade != MsgGradeSteer {
			continue
		}
		k := msgSlotKey(m)
		if seen[k] >= m.SenderSeq {
			continue
		}
		seen[k] = m.SenderSeq
		return strings.TrimSpace(m.Body)
	}
	return ""
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
		if isUserSender(m.Sender) && m.Grade == MsgGradeSteer && seen[msgSlotKey(m)] < m.SenderSeq {
			return true
		}
	}
	return false
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
