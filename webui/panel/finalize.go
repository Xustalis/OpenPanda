// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/guard"
	"github.com/Xustalis/OpenPanda/internal/log"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// sessionFinalizeInterval is how often the finalizer sweeps for finished
// session-linked tasks. A few seconds keeps the transcript's summary turn
// close behind the task's terminal transition without busy-polling.
const sessionFinalizeInterval = 5 * time.Second

// evSessionSummary is the task_events marker making the summary turn
// exactly-once: written before the turn is appended so a crash between the
// two steps can at worst lose the turn, never duplicate it.
const evSessionSummary = "session_summary"

// startSessionFinalizer runs the summary sweep until ctx ends (queue
// redesign §5: a finished task solidifies its result as an assistant turn
// in the linked session). No-op pieces when the sessions store is absent.
func (h *handler) startSessionFinalizer(ctx context.Context) {
	if h.sessions == nil {
		return
	}
	guard.Go(nil, "panel: session finalizer", nil, func() {
		tick := time.NewTicker(sessionFinalizeInterval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				h.finalizeSessionSweep(ctx)
			}
		}
	})
}

// finalizeSessionSweep appends the summary turn for every terminal task
// linked to a session that has not been summarized yet. The candidate set
// comes straight from the store as one probe (terminal + session + no
// marker); the former shape loaded every task row and then every task's full
// event timeline on every pass, so a board of already-summarized tasks was
// re-read forever.
func (h *handler) finalizeSessionSweep(ctx context.Context) {
	tasks, err := h.store.TerminalSessionTasksWithoutEvent(ctx, evSessionSummary)
	if err != nil {
		return
	}
	for _, t := range tasks {
		summary := h.taskSummary(ctx, t)
		// Marker first (see evSessionSummary): a crash after the marker but
		// before the turn loses one turn, a crash the other way round would
		// duplicate it on restart.
		if err := h.store.RecordEvent(ctx, t.TaskID, evSessionSummary, map[string]string{
			"session_id": t.SessionID,
			"state":      t.State,
		}); err != nil {
			continue
		}
		// A task turn measures the task, not the dispatch: the row's own
		// timestamps are the only clock that spans queue wait plus execution,
		// and the console prints the number above the summary. Guarded because
		// legacy rows can carry a zero (or backwards) pair.
		var durationMS int64
		if t.UpdatedAt > t.CreatedAt {
			durationMS = (t.UpdatedAt - t.CreatedAt) * 1000
		}
		if _, err := h.sessions.AppendTurn(t.SessionID, sessions.Turn{
			Role: "assistant",
			// The outcome is prose, not a second dispatch. As a task turn
			// (kind=task + ref) the thread rendered the orbit and the task
			// card all over again — one card for "dispatched", another for
			// "done", with the digest wedged between them. The dispatch turn
			// already carries the card and the jump link; this one is just
			// what happened.
			Kind:       "answer",
			Text:       summary,
			DurationMS: durationMS,
		}); err != nil {
			log.From(ctx).Warn("session summary append", "task", t.TaskID, "session", t.SessionID, "err", err)
		}
	}
}

// taskSummary renders the one-turn digest of a finished task: state line
// plus the best-effort result text (the adapter payload is JSON; see
// extractResultText for where the readable part lives). reason is the task's
// own failure explanation, read from its event history by the caller.
//
// The order matters: a digest that says "[failed] low" (the risk level, which
// is not a fact about what happened) told the reader nothing, and "[done]
// <title>" is no better than the task card it sits under.
func (h *handler) taskSummary(ctx context.Context, t core.Task) string {
	reason := ""
	if t.State != core.StateDone {
		reason = h.failureReason(ctx, t.TaskID)
	}
	return summaryText(t, reason)
}

// summaryText is the pure composition of the digest: adapter payload, then the
// task's own failure reason, then the title, and only as a last resort the
// risk level — which says something about the work, never about what happened.
func summaryText(t core.Task, reason string) string {
	var b strings.Builder
	b.WriteString("[" + t.State + "] ")
	if text := extractResultText(t.ResultJSON); text != "" {
		b.WriteString(text)
		return b.String()
	}
	if t.State != core.StateDone && reason != "" {
		b.WriteString(reason)
		return b.String()
	}
	b.WriteString(t.Title)
	return b.String()
}

// failureReason reads the newest result/review event's own explanation. A task
// that died before an adapter payload existed — lease expired, a spawn error,
// a rejected park — has an empty result_json, and the event is the only place
// the reason was ever written down.
func (h *handler) failureReason(ctx context.Context, taskID string) string {
	events, err := h.store.Events(ctx, taskID)
	if err != nil {
		return ""
	}
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Type != "result" && ev.Type != "review" {
			continue
		}
		var payload map[string]any
		if json.Unmarshal([]byte(ev.DataJSON), &payload) != nil {
			continue
		}
		for _, key := range []string{"failed", "reason", "stderr"} {
			if text, _ := payload[key].(string); strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
	}
	return ""
}

// extractResultText pulls the human-readable text out of an adapter result
// payload; anything else (or malformed JSON) yields "".
//
// Adapters disagree on where that text lives: a structured run reports the
// answer as "result" or "answer", a plain agent run writes it to stdout, and
// a failure explains itself in "stderr" or "failed". Reading only "result"
// left every task without that key with an empty digest, so the summary fell
// through to the risk level and the thread showed things like "[done] low".
//
// The text comes back whole. It used to be capped at 800 bytes — compared with
// len(), not a rune count, so a Chinese result was cut at roughly a third of
// that and the thread ended mid-sentence on an ellipsis the reader had no way
// to expand ("背景含渐变天空、…" under a task that had in fact finished).
// Nothing here needs the cap: the transcript is local storage and AppendTurn
// only appends, so the one real budget is the replay window, which
// sessions.DefaultHistoryBudget already enforces when the thread is compacted.
// The bounds that keep a pathological task from writing an unbounded row sit
// upstream, on the process output itself: executil's capture cap (8 MiB) and
// the bus clamp for remote results (512 KiB per field).
func extractResultText(resultJSON string) string {
	if resultJSON == "" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(resultJSON), &payload); err != nil {
		return ""
	}
	for _, key := range []string{"result", "answer", "stdout", "stderr", "failed"} {
		text, _ := payload[key].(string)
		if text = strings.TrimSpace(text); text != "" {
			return text
		}
	}
	return ""
}
