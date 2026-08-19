package panel

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/core"
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

// summaryTurnLimit caps the result text folded into a session turn — the
// full result stays in the task detail/logs, the transcript only carries
// the digest.
const summaryTurnLimit = 800

// startSessionFinalizer runs the summary sweep until ctx ends (queue
// redesign §5: a finished task solidifies its result as an assistant turn
// in the linked session). No-op pieces when the sessions store is absent.
func (h *handler) startSessionFinalizer(ctx context.Context) {
	if h.sessions == nil {
		return
	}
	go func() {
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
	}()
}

// finalizeSessionSweep appends the summary turn for every terminal task
// linked to a session that has not been summarized yet.
func (h *handler) finalizeSessionSweep(ctx context.Context) {
	tasks, err := h.store.ListByState(ctx, "")
	if err != nil {
		return
	}
	for _, t := range tasks {
		if t.SessionID == "" || !terminalState(t.State) {
			continue
		}
		events, err := h.store.Events(ctx, t.TaskID)
		if err != nil {
			continue
		}
		done := false
		for _, ev := range events {
			if ev.Type == evSessionSummary {
				done = true
				break
			}
		}
		if done {
			continue
		}
		summary := taskSummary(t)
		// Marker first (see evSessionSummary): a crash after the marker but
		// before the turn loses one turn, a crash the other way round would
		// duplicate it on restart.
		if err := h.store.RecordEvent(ctx, t.TaskID, evSessionSummary, map[string]string{
			"session_id": t.SessionID,
			"state":      t.State,
		}); err != nil {
			continue
		}
		if _, err := h.sessions.AppendTurn(t.SessionID, sessions.Turn{
			Role: "assistant",
			Kind: "task",
			Ref:  t.TaskID,
			Text: summary,
		}); err != nil {
			log.From(ctx).Warn("session summary append", "task", t.TaskID, "session", t.SessionID, "err", err)
		}
	}
}

// terminalState reports whether state is one the task never leaves.
func terminalState(state string) bool {
	switch state {
	case core.StateDone, core.StateFailed, core.StateCancelled, core.StateExpired:
		return true
	}
	return false
}

// taskSummary renders the one-turn digest of a finished task: state line
// plus the best-effort result text (the adapter payload is JSON; its
// "result" string is the readable part).
func taskSummary(t core.Task) string {
	var b strings.Builder
	b.WriteString("[" + t.State + "] ")
	if text := extractResultText(t.ResultJSON); text != "" {
		b.WriteString(text)
	} else if t.Risk != "" {
		b.WriteString(t.Risk)
	} else {
		b.WriteString(t.Title)
	}
	return b.String()
}

// extractResultText pulls the human-readable "result" field out of an
// adapter result payload; anything else (or malformed JSON) yields "".
func extractResultText(resultJSON string) string {
	if resultJSON == "" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(resultJSON), &payload); err != nil {
		return ""
	}
	text, _ := payload["result"].(string)
	text = strings.TrimSpace(text)
	if len(text) > summaryTurnLimit {
		text = text[:summaryTurnLimit] + "…"
	}
	return text
}
