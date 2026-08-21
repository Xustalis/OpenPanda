package main

// The REPL's task watcher: a background goroutine that polls the task
// store's state fingerprint and reports out-of-band completions — tasks
// that reached a terminal state while the user was NOT waiting on an ask
// (queued board tasks, web-console submissions, delegated work arriving
// from peers). Inline asks suppress it: they print their own outcome, and
// resetWatchBaseline absorbs the task so it is not announced twice.
//
// Delivery goes through the terminal layer's notify channel when the line
// editor is active (the message is interleaved without losing the user's
// in-progress buffer), and plain stdout otherwise.

import (
	"context"
	"fmt"
	"time"

	"github.com/Xustalis/OpenPanda/internal/core"
)

// watchPollInterval is the fingerprint poll cadence: fast enough to feel
// live, slow enough to be free (one indexed query per tick).
const watchPollInterval = 2 * time.Second

// watchTasks polls the task store until ctx ends. Terminal transitions are
// announced; everything else just updates the baseline.
func (r *repl) watchTasks(ctx context.Context) {
	r.resetWatchBaseline()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(watchPollInterval):
		}
		if r.askingNow() {
			continue // an inline ask is running; it reports its own outcome
		}
		tasks, err := r.store.ListByState(ctx, "")
		if err != nil {
			continue
		}
		cur := make(map[string]core.Task, len(tasks))
		states := make(map[string]string, len(tasks))
		for _, t := range tasks {
			cur[t.TaskID] = t
			states[t.TaskID] = t.State
		}
		var notes []string
		r.watchMu.Lock()
		for id, st := range states {
			prev, seen := r.baseline[id]
			if seen && prev != st && isTerminalState(st) {
				notes = append(notes, r.completionNote(cur[id]))
			}
		}
		r.baseline = states
		r.watchMu.Unlock()
		for _, n := range notes {
			r.notify(n)
		}
	}
}

// completionNote renders one finished/failed task as a single report line.
func (r *repl) completionNote(t core.Task) string {
	title := t.Title
	if len([]rune(title)) > 48 {
		title = string([]rune(title)[:48]) + "…"
	}
	mark, code := "✓", "32"
	if t.State != core.StateDone {
		mark = "✗"
		code = "31"
	}
	line := fmt.Sprintf("%s %s (%s) — /task %s", mark, title, t.State, shortID(t.TaskID))
	if stdoutIsTTY() {
		return "\x1b[" + code + "m" + line + "\x1b[0m"
	}
	return line
}

// notify delivers one watcher line: through the terminal layer when the
// line editor owns the screen (it redraws the prompt+buffer around the
// message), otherwise straight to stdout.
func (r *repl) notify(line string) {
	if r.term != nil && r.term.deliver(line) {
		return
	}
	fmt.Println(line)
}

// setAsking marks an inline ask in flight; the watcher stays silent then.
func (r *repl) setAsking(v bool) {
	r.watchMu.Lock()
	r.asking = v
	r.watchMu.Unlock()
}

func (r *repl) askingNow() bool {
	r.watchMu.Lock()
	defer r.watchMu.Unlock()
	return r.asking
}

// resetWatchBaseline re-reads the current task states and adopts them as
// the seen baseline, so already-finished tasks are never announced.
func (r *repl) resetWatchBaseline() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tasks, err := r.store.ListByState(ctx, "")
	if err != nil {
		return
	}
	states := make(map[string]string, len(tasks))
	for _, t := range tasks {
		states[t.TaskID] = t.State
	}
	r.watchMu.Lock()
	r.baseline = states
	r.watchMu.Unlock()
}

// isTerminalState reports whether a task state is final for the watcher's
// purpose (no further transition worth announcing).
func isTerminalState(s string) bool {
	switch s {
	case core.StateDone, core.StateFailed, core.StateCancelled, core.StateExpired, "review":
		return true
	}
	return false
}
