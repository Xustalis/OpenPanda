//go:build !lite

package main

// Out-of-band task notifications for the TUI. Tasks can reach a terminal state
// while the user is not waiting on an ask — queued board work, a web-console
// submission, a delegation arriving from a peer — and the classic loop reports
// those through its line editor (repl_watch.go). The TUI cannot: writing to
// stdout from a goroutine would land inside the frame Bubble Tea is repainting.
//
// So the same single-poll step (repl.pollCompletions) is driven as a Bubble Tea
// command instead: it returns a message, Update commits the lines to scrollback
// as transcript notes, and re-arms the next poll. One poll is ever in flight, and
// only the Update loop touches the model.

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/core"
)

// watchMsg carries the events from one completed poll, plus the active project
// the same off-loop trip read. proj is authoritative — every poll fills it,
// including a mid-ask one that reports nothing — so Update can assign it
// straight into the status row's cache without a second store trip.
type watchMsg struct {
	events []watchEvent
	// notes is the pre-rendered form used by tests and by callers that only
	// have lines to commit; a poll fills events, and onWatch reads both.
	notes []string
	proj  string
}

// watchTasks arms the next store poll. It sleeps first, so the command doubles as
// the ticker: the reply lands one interval from now and Update re-arms it. A poll
// that lands mid-ask reports nothing and leaves the baseline alone — the ask
// prints its own outcome, and absorbBaseline folds it in afterwards.
//
// The active project rides along because this goroutine is already the one
// allowed to touch the store: the pointer can move out of band (the web console
// enters a project, or another `panda` does), and without this the status row
// would keep naming the old one until a slash command happened to refresh it.
func watchTasks(r *repl) tea.Cmd {
	if r == nil || r.store == nil {
		return nil // no store (tests, or a REPL built without one): nothing to watch
	}
	return func() tea.Msg {
		time.Sleep(watchPollInterval)
		proj := r.activeProjectName()
		if r.askingNow() {
			return watchMsg{proj: proj}
		}
		return watchMsg{events: r.pollCompletions(context.Background()), proj: proj}
	}
}

// absorbBaseline re-reads the task states in the background and adopts them, so a
// task this turn just finished is not announced a second time by the watcher. It
// runs off the Update loop because it queries the store.
func absorbBaseline(r *repl) tea.Cmd {
	if r == nil || r.store == nil {
		return nil
	}
	return func() tea.Msg {
		r.resetWatchBaseline()
		return nil
	}
}

// onWatch commits the poll's report lines, adopts the project the poll read,
// raises the approval card for a task that parked in review, and re-arms the
// watcher.
//
// The review case is the point of this path: an approval that arrives out of
// band — a queued task, a web submission, a peer's delegation — must still be
// decided by the user, so the first such task is promoted to the same
// foreground card an inline ask would raise. A line in the scrollback is not a
// decision; nothing else in the system may answer it. Cards are raised only
// while the screen is idle (never over an in-flight ask, another card, or a
// panel), and any further review tasks keep their actionable note so they are
// not lost — the next idle poll cannot re-announce them (the baseline moved),
// but /approve and /reject still reach them.
func (m tuiModel) onWatch(msg watchMsg) (tea.Model, tea.Cmd) {
	m.projName = msg.proj
	cmds := []tea.Cmd{watchTasks(m.r)}
	note := func(body string) {
		cmds = append(cmds, m.printBlock(block{kind: blockNote, body: body}))
	}
	for _, n := range msg.notes {
		note(n)
	}
	raised := false
	for _, ev := range msg.events {
		note(ev.note)
		if !ev.review || raised || m.mode != modeIdle || m.pending != nil {
			continue
		}
		// needs_changed_input cannot be continued by consent alone, so there is
		// nothing for a yes/no card to answer: the note already says what to do.
		if ev.task.ApprovalDisposition == core.ApprovalNeedsChangedInput {
			continue
		}
		m.pending = &askengine.Result{
			Kind:          "task",
			TaskID:        ev.task.TaskID,
			TaskTitle:     ev.task.Title,
			TaskState:     ev.task.State,
			NeedsApproval: true,
			Approval: &askengine.ApprovalRequest{
				TaskID: ev.task.TaskID,
				Title:  ev.task.Title,
				Intent: ev.task.Intent,
				Reason: reviewReason(ev.task),
			},
		}
		m.mode = modeApproving
		m.approvalSel = 1 // arrows + Enter start on deny, the [y/N] safe default
		raised = true
	}
	return m, tea.Batch(cmds...)
}
