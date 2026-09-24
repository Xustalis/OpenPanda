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
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// watchPollInterval is the fingerprint poll cadence: fast enough to feel
// live, slow enough to be free (one indexed query per tick).
const watchPollInterval = 2 * time.Second

// Stall detection bounds. The watcher's second job is noticing when nothing
// transitions at all: a task held in a non-terminal state past its own bound
// means no scheduler is consuming it or no monitor is expiring it — either
// way the user must hear about it rather than the row rotting invisibly.
// Review is the designed wait: it waits on the user, so it re-reminds on a
// slow cadence instead of being announced once and forgotten.
const (
	// watchStallQueued: a submitted/queued row carries no lease, so a frozen
	// updated_at is the only clock it has. Longer than this with no movement
	// means a held resource lock or no consumer process at all.
	watchStallQueued = 10 * time.Minute
	// watchLeaseGrace: an active row already past its lease expiry is overdue
	// — the monitor should have failed it; the grace covers one tick skew.
	watchLeaseGrace = 30 * time.Second
	// watchStallActive: an active row with no lease at all is bounded only by
	// updated_at. The default task lease is 20m and renewals touch updated_at
	// every lease/3, so this fires only when the executor genuinely vanished.
	watchStallActive = 25 * time.Minute
	// watchReviewRemind: how often a still-pending approval is re-announced.
	watchReviewRemind = 30 * time.Minute
)

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
		for _, ev := range r.pollCompletions(ctx) {
			r.notify(ev.note)
		}
	}
}

// watchEvent is one out-of-band task transition the watcher observed: the
// report line to show, plus the task itself. A task parked in review is
// flagged, because a pending approval is not a notification — the front end
// must put the decision in front of the user (the TUI raises its approval
// card; the classic loop prints the /approve · /reject line) instead of
// letting it rot in the queue with nobody asked.
type watchEvent struct {
	note   string
	task   core.Task
	review bool
}

// pollCompletions reads the task store once and returns an event for every task
// that reached a terminal state since the last poll, adopting the new states as
// the baseline. Both front ends share it: the classic loop prints the lines
// through the line editor, the TUI commits them as transcript notes and raises
// the approval card for review events.
func (r *repl) pollCompletions(ctx context.Context) []watchEvent {
	if r.store == nil {
		return nil
	}
	tasks, err := r.store.ListByState(ctx, "")
	if err != nil {
		return nil
	}
	cur := make(map[string]core.Task, len(tasks))
	states := make(map[string]string, len(tasks))
	for _, t := range tasks {
		cur[t.TaskID] = t
		states[t.TaskID] = t.State
	}
	var events []watchEvent
	r.watchMu.Lock()
	if r.stallNoted == nil {
		r.stallNoted = make(map[string]time.Time)
	}
	now := time.Now()
	for id, st := range states {
		prev, seen := r.baseline[id]
		if seen && prev != st && isTerminalState(st) {
			t := cur[id]
			events = append(events, watchEvent{
				note:   r.completionNote(t),
				task:   t,
				review: t.State == core.StateReview,
			})
			// The transition just announced this review — start the reminder
			// clock now so it does not immediately repeat as a "stall".
			if t.State == core.StateReview {
				r.stallNoted[id+"|"+st] = now
			}
		}
	}
	for _, t := range tasks {
		if ev, ok := r.stallEventLocked(t, now); ok {
			events = append(events, ev)
		}
	}
	// Forget stall marks for tasks that left the watched set — terminal,
	// deleted, or moved to a new state (a new key, free to warn again).
	for key := range r.stallNoted {
		id, _, _ := strings.Cut(key, "|")
		t, ok := cur[id]
		if !ok || key != id+"|"+t.State {
			delete(r.stallNoted, key)
		}
	}
	r.baseline = states
	r.watchMu.Unlock()
	return events
}

// stallEventLocked decides whether one task has been stuck long enough to
// warrant a foreground note. It runs under watchMu. States that legitimately
// wait — a review parked on the user, a plan stage held by its graph — get a
// reminder cadence or an exemption rather than an alarm; everything else that
// has not moved past its own bound is a genuine silent stall.
func (r *repl) stallEventLocked(t core.Task, now time.Time) (watchEvent, bool) {
	key := t.TaskID + "|" + t.State
	notedAt, noted := r.stallNoted[key]
	mark := func() { r.stallNoted[key] = now }
	p := pal()
	short := shortID(t.TaskID)
	title := t.Title
	if len([]rune(title)) > 48 {
		title = string([]rune(title)[:48]) + "…"
	}
	frozen := now.Sub(time.Unix(t.UpdatedAt, 0))

	switch t.State {
	case core.StateReview:
		// Waiting on a human is the designed state, but it must stay in the
		// foreground: re-remind on a slow cadence so a pending approval can
		// never rot silently behind an older scroll line.
		if noted && now.Sub(notedAt) < watchReviewRemind {
			return watchEvent{}, false
		}
		mark()
		if t.ApprovalDisposition == core.ApprovalNeedsChangedInput {
			return watchEvent{
				note: p.Warn(fmt.Sprintf("%s %s — %s", p.MarkBullet(), title,
					i18n.Tf(r.loc, "repl.watch.reviewBlocked", "id", short))),
				task: t,
			}, true
		}
		return watchEvent{
			note: p.Warn(fmt.Sprintf("%s %s — %s", p.MarkBullet(), title,
				i18n.Tf(r.loc, "repl.watch.reviewRemind", "id", short))),
			task:   t,
			review: true,
		}, true

	case core.StateSubmitted:
		// An unreleased plan stage parks in submitted behind its dependency
		// graph — the plan sweep owns that wait and propagates failures, so
		// it is not a stall. A plain submitted row has no such owner.
		if t.PlanID != "" || noted || frozen < watchStallQueued {
			return watchEvent{}, false
		}
		mark()
		return watchEvent{
			note: p.Warn(fmt.Sprintf("%s %s — %s", p.MarkBullet(), title,
				i18n.Tf(r.loc, "repl.watch.stalledWait", "state", t.State,
					"mins", fmt.Sprintf("%d", int(frozen.Minutes())), "id", short))),
			task: t,
		}, true

	case core.StateQueued:
		if noted || frozen < watchStallQueued {
			return watchEvent{}, false
		}
		mark()
		return watchEvent{
			note: p.Warn(fmt.Sprintf("%s %s — %s", p.MarkBullet(), title,
				i18n.Tf(r.loc, "repl.watch.stalledWait", "state", t.State,
					"mins", fmt.Sprintf("%d", int(frozen.Minutes())), "id", short))),
			task: t,
		}, true

	case core.StateDispatched, core.StateWaitingCtx, core.StateRunning:
		if noted {
			return watchEvent{}, false
		}
		switch {
		case t.LeaseExpires > 0 && now.Unix() > t.LeaseExpires+int64(watchLeaseGrace/time.Second):
			// Past its lease yet still active: the monitor that should have
			// failed it is not running, and the executor may be gone.
			mark()
			return watchEvent{
				note: p.Warn(fmt.Sprintf("%s %s — %s", p.MarkBullet(), title,
					i18n.Tf(r.loc, "repl.watch.stalledLease", "state", t.State, "id", short))),
				task: t,
			}, true
		case t.LeaseExpires == 0 && frozen > watchStallActive:
			mark()
			return watchEvent{
				note: p.Warn(fmt.Sprintf("%s %s — %s", p.MarkBullet(), title,
					i18n.Tf(r.loc, "repl.watch.stalledFrozen", "state", t.State,
						"mins", fmt.Sprintf("%d", int(frozen.Minutes())), "id", short))),
				task: t,
			}, true
		}
	}
	return watchEvent{}, false
}

// completionNote renders one finished/failed task as a single report line. A
// review task gets the actionable form instead: it is waiting on the user, so
// the line names the two decisions rather than only where to look.
func (r *repl) completionNote(t core.Task) string {
	title := t.Title
	if len([]rune(title)) > 48 {
		title = string([]rune(title)[:48]) + "…"
	}
	p := pal()
	if t.State == core.StateReview {
		short := shortID(t.TaskID)
		if t.ApprovalDisposition == core.ApprovalNeedsChangedInput {
			return p.Warn(fmt.Sprintf("%s %s — %s", p.MarkBullet(), title,
				i18n.Tf(r.loc, "repl.watch.reviewBlocked", "id", short)))
		}
		return p.Warn(fmt.Sprintf("%s %s — %s", p.MarkBullet(), title,
			i18n.Tf(r.loc, "repl.watch.reviewHint", "id", short)))
	}
	mark, tint := p.MarkOK(), p.Success
	if t.State != core.StateDone {
		mark, tint = p.MarkFail(), p.Danger
	}
	return tint(fmt.Sprintf("%s %s (%s) — /task %s", mark, title, t.State, shortID(t.TaskID)))
}

// reviewReason digs the executor's refusal/park reason out of a review task's
// persisted result so the approval surface can say what is being approved. An
// empty reason is fine: the card still names the task and its intent.
func reviewReason(t core.Task) string {
	if t.ResultJSON == "" {
		return ""
	}
	var res bus.TaskResultPayload
	if err := json.Unmarshal([]byte(t.ResultJSON), &res); err != nil {
		return ""
	}
	if s := strings.TrimSpace(res.Stderr); s != "" {
		return s
	}
	return strings.TrimSpace(res.Stdout)
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
	if r.store == nil {
		return
	}
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
