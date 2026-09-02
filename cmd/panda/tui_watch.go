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
)

// watchMsg carries the report lines from one completed poll, plus the active
// project the same off-loop trip read. proj is authoritative — every poll fills
// it, including a mid-ask one that reports no notes — so Update can assign it
// straight into the status row's cache without a second store trip.
type watchMsg struct {
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
		return watchMsg{notes: r.pollCompletions(context.Background()), proj: proj}
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

// onWatch commits the poll's report lines, adopts the project the poll read, and
// re-arms the watcher.
func (m tuiModel) onWatch(msg watchMsg) (tea.Model, tea.Cmd) {
	m.projName = msg.proj
	cmds := []tea.Cmd{watchTasks(m.r)}
	for _, n := range msg.notes {
		blk := block{kind: blockNote, body: n}
		cmds = append(cmds, m.printBlock(blk))
	}
	return m, tea.Batch(cmds...)
}
