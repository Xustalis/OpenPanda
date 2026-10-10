// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

// The sub-agent round does not end at the dispatch. A task the queue parked —
// its pinned target's link is not live, or the submission asked for the
// asynchronous transport — is not an outcome: converging the conversation on
// "queued" ends the session while the work has not started (the reported
// defect: the sub-agent's task state never loaded, the turn just stopped).
// This file follows the parked task until it settles, so the round reports
// what actually happened.
//
// The wait reads the shared task store rather than a waiter channel, because
// the process that finishes the task is routinely a different one: the
// daemon's queue consumer claims the row when the link comes up, forwards it,
// and writes the executor's result back — all into the same SQLite store this
// process already reads. Polling is the only signal that crosses that
// boundary; the timeline poll rides EventsSince so a long agent transcript is
// read once, not re-read every tick.

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/core"
)

// awaitPollInterval is the settle-poll cadence: fast enough that a task
// finishing between polls is noticed promptly, slow enough to be free (one
// indexed row read plus one incremental event read per tick).
const awaitPollInterval = time.Second

// settledState answers "what happened to my task?" — done/cancelled/expired,
// plus failed and review. Failed is broader than core.Terminal on purpose: a
// failed row can be retried, but for a round waiting on the outcome the
// failure is the answer. Review is the designed wait for a human, and the
// round surfaces it as the outcome (the front end raises the approval card).
func settledState(s string) bool {
	return core.Terminal(s) || s == core.StateFailed || s == core.StateReview
}

// awaitSettled follows a task the queue parked until it reaches a settled
// state, bridging the timeline the consumer writes as progress so the card
// keeps advancing while the work runs elsewhere. It returns the settled row
// and its persisted result payload.
//
// When ctx ends first the last observed row is returned instead — the caller
// stopped watching, and the work keeps its place in the queue (releasing the
// front end never cancelled the task: the whole point of the queue is to run
// the work when its link is live, whether or not anyone is still on screen).
func (e *Engine) awaitSettled(ctx context.Context, sched *core.Core, cur core.Task, res bus.TaskResultPayload, cb StreamCallbacks) (core.Task, bus.TaskResultPayload) {
	store := sched.TaskStore()
	if store == nil {
		return cur, res
	}
	// Adopt the events already on the timeline as the cursor: the submit path
	// recorded them through this process's own store, where the in-flight
	// progress bridge already delivered them. Only what the consumer writes
	// from here on is news.
	cursor := int64(0)
	if evs, err := store.Events(ctx, cur.TaskID); err == nil && len(evs) > 0 {
		cursor = evs[len(evs)-1].ID
	}
	last := cur.State
	e.logger.Debug("askengine: task parked, following it to the outcome", "task", cur.TaskID, "state", last)
	cb.progress(Progress{Kind: ProgressWait, Name: last})
	tick := time.NewTicker(awaitPollInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return cur, res
		case <-tick.C:
		}
		t, err := store.Get(ctx, cur.TaskID)
		if err != nil {
			continue // a row we cannot read is not proof of death
		}
		cur = t
		if t.State != last {
			last = t.State
			// A settled state ends the wait below; announcing it as a
			// "watching" stage first would only add noise to the card.
			if !settledState(t.State) {
				cb.progress(Progress{Kind: ProgressWait, Name: last})
			}
		}
		// Bridge the timeline the consumer wrote since the last pass — before
		// the settle check, so the notes recorded just ahead of the result
		// still land on the card.
		bridgeNewEvents(ctx, store, t.TaskID, &cursor, cb)
		if settledState(t.State) {
			return t, settledPayload(t)
		}
	}
}

// bridgeNewEvents delivers the timeline events recorded since cursor as
// progress, advancing the cursor. Read failures are silent: the poll retries
// next tick, and the timeline bridge is display-only — the settle wait and
// the round's outcome never depend on it.
func bridgeNewEvents(ctx context.Context, store *core.TaskStore, taskID string, cursor *int64, cb StreamCallbacks) {
	evs, err := store.EventsSince(ctx, taskID, *cursor)
	if err != nil {
		return
	}
	for _, ev := range evs {
		*cursor = ev.ID
		var data any
		if json.Unmarshal([]byte(ev.DataJSON), &data) != nil {
			continue
		}
		if p, ok := progressForEvent(ev.Type, data); ok {
			cb.progress(p)
		}
	}
}

// awaitPlanSettled follows a started plan until every stage settles or one
// parks for a human — the plan-side counterpart of awaitSettled: a plan that
// returned at "started" ended the turn's watch while the pipeline ran on
// (the reported "stops listening" defect). The poll reads the shared store —
// the daemon's consumer and the wire handlers write stage verdicts there —
// and bridges each stage's timeline as progress so the card keeps moving
// while stages run elsewhere.
//
// Settle rule: every stage terminal or failed ends the watch with the final
// board; a stage parked in review ends it sooner — the human gate needs the
// turn's input free (/approve), the same reason a task's review settles its
// wait. ctx ending first returns the last board read: the watch detaches,
// the pipeline keeps its place in the queue.
func (e *Engine) awaitPlanSettled(ctx context.Context, sched *core.Core, planID string, cb StreamCallbacks) []core.Task {
	store := sched.TaskStore()
	if store == nil {
		return nil
	}
	e.logger.Debug("askengine: plan started, following stages to the outcome", "plan", planID)
	cursors := map[string]*int64{} // per-stage timeline cursor for the event bridge
	seen := map[string]string{}    // last announced state per stage
	var board []core.Task
	tick := time.NewTicker(awaitPollInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return board
		case <-tick.C:
		}
		stages, err := store.PlanStages(ctx, planID)
		if err != nil {
			continue // a transient read failure is not proof the plan died
		}
		board = stages
		settled := true
		for _, st := range stages {
			if seen[st.StageID] != st.State {
				seen[st.StageID] = st.State
				if !settledState(st.State) {
					cb.progress(Progress{Kind: ProgressWait, Name: st.StageID + ": " + st.State})
				}
			}
			cur, ok := cursors[st.TaskID]
			if !ok {
				cur = new(int64)
				// Adopt the timeline already written — the stage's own
				// dispatch events preceded this watch and would only replay.
				if evs, evErr := store.Events(ctx, st.TaskID); evErr == nil && len(evs) > 0 {
					*cur = evs[len(evs)-1].ID
				}
				cursors[st.TaskID] = cur
			}
			bridgeNewEvents(ctx, store, st.TaskID, cur, cb)
			switch {
			case st.State == core.StateReview:
				return stages // human gate — free the turn for the approval
			case !settledState(st.State):
				settled = false
			}
		}
		if settled {
			return stages
		}
	}
}

// planBoardVerdict folds the final stage board into the round's outcome: a
// parked stage becomes the caller's approval card (the human gate ends the
// watch, it is not a failure); the first terminal-but-not-done stage is the
// plan's failure; an all-done board is the clean finish. A board still in
// flight — the watch detached — keeps OK and reports nothing final.
func (e *Engine) planBoardVerdict(ctx context.Context, store *core.TaskStore, res *Result, stages []core.Task) {
	for _, st := range stages {
		if st.State != core.StateReview {
			continue
		}
		p := settledPayload(st)
		reason := p.Stderr
		if reason == "" {
			reason = p.Question
		}
		if reason == "" {
			reason = stageEventReason(ctx, store, st)
		}
		res.NeedsApproval = true
		res.Approval = &ApprovalRequest{
			TaskID: st.TaskID, Title: st.Title,
			Reason: reason, Project: st.Project, Scope: "once",
		}
		return
	}
	for _, st := range stages {
		if st.State == core.StateFailed || st.State == core.StateCancelled || st.State == core.StateExpired {
			p := settledPayload(st)
			res.OK, res.ExitCode = false, 1
			res.Stderr = p.Stderr
			if res.Stderr == "" {
				res.Stderr = stageEventReason(ctx, store, st)
			}
			if res.Stderr == "" {
				res.Stderr = "stage " + st.StageID + " " + st.State
			}
			return
		}
	}
}

// stageEventReason recovers a settled stage's reason from its audit trail —
// locally failed rows keep the cause on the EvResult event rather than in
// result_json (Fail writes the event, not the column), the same gap the
// resume path's resumeOutcomeFor closes.
func stageEventReason(ctx context.Context, store *core.TaskStore, st core.Task) string {
	evs, err := store.Events(ctx, st.TaskID)
	if err != nil {
		return ""
	}
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type != core.EvResult && evs[i].Type != core.EvReview {
			continue
		}
		var d struct {
			Failed string `json:"failed"`
			Stderr string `json:"stderr"`
			Reason string `json:"reason"`
		}
		if json.Unmarshal([]byte(evs[i].DataJSON), &d) != nil {
			continue
		}
		if d.Failed != "" {
			return d.Failed
		}
		if d.Stderr != "" {
			return d.Stderr
		}
		if d.Reason != "" {
			return d.Reason
		}
	}
	return ""
}

// settledPayload loads the result a settled task left behind. The payload is
// the executor's persisted result (adapter output, attribution, files
// changed); the identity fields come from the row so a legacy result without
// them still answers with the right task. A row that never ran (cancelled or
// expired while queued) has no result JSON and yields a zero payload — the
// observation reports the state, which is the honest outcome there.
func settledPayload(t core.Task) bus.TaskResultPayload {
	var payload bus.TaskResultPayload
	if strings.TrimSpace(t.ResultJSON) != "" {
		if err := json.Unmarshal([]byte(t.ResultJSON), &payload); err != nil {
			payload = bus.TaskResultPayload{}
		}
	}
	payload.TaskID = t.TaskID
	payload.AttemptID = t.AttemptID
	payload.State = t.State
	return payload
}
