// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/guard"
	"github.com/Xustalis/OpenPanda/internal/i18n"
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
		// duplicate it on restart. The attempt id scopes the marker to the run
		// it reported: a retried task rotates it, and the attempt that finally
		// finished owes the thread a report of its own.
		if err := h.store.RecordEvent(ctx, t.TaskID, evSessionSummary, map[string]string{
			"session_id": t.SessionID,
			"state":      t.State,
			"attempt_id": t.AttemptID,
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

// taskSummary renders the one-turn report of a finished task.
func (h *handler) taskSummary(ctx context.Context, t core.Task) string {
	reason := ""
	if t.State != core.StateDone {
		reason = h.failureReason(ctx, t.TaskID)
	}
	agent := h.taskAgent(ctx, t)
	from, why := h.substitutedFrom(ctx, t.TaskID, agent)
	return summaryText(t, reason, agent, from, why)
}

// substitutedFrom names the agent the plan had chosen when the executor could
// not run it and the fallback chain finished the round instead, together with
// the reason that attempt recorded. Both empty when the agent that answered is
// the one the plan picked.
//
// The swap is deliberate (core/run.go annotateAgentSubstitution explains why
// refusing the whole task serves nobody) and it is recorded twice: as an
// agent_fallback event, and as a note at the head of the result payload. The
// thread keeps neither — it prints one closing paragraph — so a pinned
// "agent:claude_code" answered by deepseek_harness read as a silent
// bait-and-switch: the line named one employee for the job and another for the
// reply. This is where that sentence is read, so this is where it gets said.
//
// The reason is half of the sentence. "claude_code 未执行" tells the reader the
// reply is not the one they asked for but not whether to retry, reinstall or
// raise a turn cap; the fallback event carries the diagnosis the failed attempt
// reported, and it rides along when it is there.
func (h *handler) substitutedFrom(ctx context.Context, taskID, agent string) (string, string) {
	if agent == "" {
		return "", ""
	}
	events, err := h.store.Events(ctx, taskID)
	if err != nil {
		return "", ""
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != "agent_fallback" {
			continue
		}
		var d struct {
			From   string `json:"from"`
			To     string `json:"to"`
			Reason string `json:"reason"`
		}
		if json.Unmarshal([]byte(events[i].DataJSON), &d) != nil {
			continue
		}
		if d.From != "" && d.To == agent {
			return d.From, strings.TrimSpace(d.Reason)
		}
	}
	return "", ""
}

// summaryText is the pure composition of the report, in two parts and no
// more: the orchestration line — state, the employee that ran it, the job it
// was given — and then that employee's own closing paragraph.
//
// The thread is a conversation, not a log. It used to carry the entire
// adapter payload: a 2 000-word agent reply, its "变更 / 验证" sections and
// the code fences inside them, printed whole under a `[done]` tag. What a
// reader wants from the transcript is who did the work and how it ended; the
// play-by-play is one click away on the task page, and replaying the dump
// into the model's own history spent the context window on its own past
// prose. One conclusion paragraph is the summary — the sections above it are
// the evidence behind it.
func summaryText(t core.Task, reason, agent, substituted, substitutedWhy string) string {
	head := "[" + t.State + "] "
	if agent != "" {
		head += "@" + agent + " · "
	}
	head += t.Title
	// The substitution note rides the orchestration line, right after the
	// title: the reader who wonders "did it really test claude_code?" is
	// already reading this line, and the body below belongs to the employee
	// that actually spoke. The leading space is part of the Latin strings —
	// CJK locales set the note in full-width brackets and need none.
	if substituted != "" && agent != "" && substituted != agent {
		if why := clipSubstitutionWhy(substitutedWhy); why != "" {
			head += i18n.Tf(t.GetUserLocale(), "web.session.agentSubstitutedWhy",
				"from", substituted, "to", agent, "why", why)
		} else {
			head += i18n.Tf(t.GetUserLocale(), "web.session.agentSubstituted", "from", substituted, "to", agent)
		}
	}

	// A failed run's own reason is its conclusion: there is no adapter answer
	// to take a closing paragraph from, and the reason is what the reader
	// needs. A done run never shows a stale reason from an earlier attempt.
	body := conclusionOf(extractResultText(t.ResultJSON))
	if body == "" && t.State != core.StateDone {
		body = conclusionOf(reason)
	}
	if body == "" {
		return head
	}
	return head + "\n" + body
}

// maxSubstitutionWhy bounds the cause printed inside the swap note. The note
// sits on the orchestration line, which is a caption — an adapter that failed
// with a 400-character stack trace must not turn the line into a paragraph.
// The full diagnosis stays where it belongs: the task's own event trace.
const maxSubstitutionWhy = 80

// clipSubstitutionWhy shortens a fallback reason for the caption, on runes so a
// CJK diagnosis is not cut at a third of its length.
func clipSubstitutionWhy(why string) string {
	why = strings.TrimSpace(strings.ReplaceAll(why, "\n", " "))
	if utf8.RuneCountInString(why) <= maxSubstitutionWhy {
		return why
	}
	return strings.TrimSpace(string([]rune(why)[:maxSubstitutionWhy])) + "…"
}

// maxConclusionRunes bounds one employee's closing paragraph. A conclusion is
// a paragraph, so the cap is generous — but an agent that answers in one
// 6 000-character block must not put the whole block in the thread.
const maxConclusionRunes = 800

// conclusionOf picks the closing paragraph of an agent's reply — the last
// block that reads as a verdict rather than as more evidence. Walking back a
// few blocks skips what cannot be a conclusion: a fenced code block, a table,
// a bare heading, or a trailing "要我继续吗？" (a question is a prompt for the
// next turn, not the answer to this one).
func conclusionOf(text string) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return ""
	}
	blocks := strings.Split(text, "\n\n")
	// Only the tail can be the conclusion; scanning the whole reply would let
	// an early paragraph win over the ending that supersedes it.
	for i := len(blocks) - 1; i >= 0 && i >= len(blocks)-4; i-- {
		if b := strings.TrimSpace(blocks[i]); isConclusionBlock(b) {
			return clipConclusion(b)
		}
	}
	return clipConclusion(strings.TrimSpace(blocks[len(blocks)-1]))
}

// isConclusionBlock reports whether one paragraph of a reply can stand as its
// conclusion. The thresholds are deliberately low: "已完成。" is a real
// verdict, and refusing it would push the reader back into the log.
func isConclusionBlock(b string) bool {
	if b == "" {
		return false
	}
	// Code and tables are evidence, never the verdict.
	if strings.HasPrefix(b, "```") || strings.HasPrefix(b, "|") || strings.HasPrefix(b, "    ") {
		return false
	}
	// A heading with nothing under it names a section, it does not conclude.
	if strings.HasPrefix(b, "#") && !strings.Contains(b, "\n") {
		return false
	}
	if utf8.RuneCountInString(b) < 4 {
		return false
	}
	// A trailing question is the agent asking what to do next.
	last := strings.TrimSpace(b)
	if (strings.HasSuffix(last, "?") || strings.HasSuffix(last, "？")) && !strings.ContainsAny(last, "。！.!") {
		return false
	}
	return true
}

// clipConclusion bounds a paragraph without cutting it mid-sentence: the cut
// falls back to the last sentence break inside the budget when there is one,
// so a truncated conclusion ends on a full stop and an ellipsis rather than
// on half a word. The cut is on runes, never bytes — a byte cap used to slice
// Chinese replies at a third of their length.
func clipConclusion(s string) string {
	if utf8.RuneCountInString(s) <= maxConclusionRunes {
		return s
	}
	runes := []rune(s)
	cut := maxConclusionRunes
	// Look for a sentence end in the last quarter of the budget.
	for i := cut - 1; i >= cut-maxConclusionRunes/4 && i > 0; i-- {
		switch runes[i] {
		case '。', '！', '？', '.', '!', '?', '\n':
			return strings.TrimSpace(string(runes[:i+1])) + "…"
		}
	}
	return strings.TrimSpace(string(runes[:cut])) + "…"
}

// taskAgent names the employee that actually ran the task. A remote or
// delegated result carries it in the payload; a local run reports the adapter
// on its timeline instead (exec_agent_start) and leaves the payload without an
// agent field, so the events are read as the fallback — otherwise the thread
// says "done" without ever saying who did it.
//
// A task that never reached an adapter has neither: it failed at routing, or
// was cancelled while queued. Its row still carries the pin it was submitted
// with (`agent:deepseek_harness`), and that pin is the employee the user named
// — so the report says the job belongs to them instead of falling through to
// the headless form. Without this the one report a user is most likely to be
// staring at ("why did deepseek_harness not answer?") was the only one spoken
// by the panda, which reads as the console answering in the employee's place.
func (h *handler) taskAgent(ctx context.Context, t core.Task) string {
	// A result that travelled (delegated, remote) names the agent it was run
	// by; that is the authoritative answer and needs no timeline read.
	if t.ResultJSON != "" {
		var payload struct {
			Agent string `json:"agent"`
		}
		if json.Unmarshal([]byte(t.ResultJSON), &payload) == nil {
			if a := strings.TrimSpace(payload.Agent); a != "" {
				return a
			}
		}
	}
	events, err := h.store.Events(ctx, t.TaskID)
	if err != nil {
		return pinnedAgent(t.Requires)
	}
	for i := len(events) - 1; i >= 0; i-- {
		switch events[i].Type {
		case "exec_agent_start", "agent_usage", "supervision_round":
		default:
			continue
		}
		var d struct {
			Agent string `json:"agent"`
		}
		if json.Unmarshal([]byte(events[i].DataJSON), &d) == nil && strings.TrimSpace(d.Agent) != "" {
			return strings.TrimSpace(d.Agent)
		}
	}
	return pinnedAgent(t.Requires)
}

// pinnedAgent returns the employee named by an `agent:<name>` requirement, or
// "" when the task named none. Only the prefix is stripped: the pin is stored
// exactly as submitted, and the registry key is what the view resolves a face
// and a display name from.
func pinnedAgent(requires []string) string {
	for _, req := range requires {
		if name, ok := strings.CutPrefix(strings.TrimSpace(req), "agent:"); ok {
			if name = strings.TrimSpace(name); name != "" {
				return name
			}
		}
	}
	return ""
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
// The text comes back whole — conclusionOf is what shortens it for the
// thread, and it does so on purpose, at a paragraph boundary. The bounds that
// keep a pathological task from writing an unbounded row sit upstream, on the
// process output itself: executil's capture cap (8 MiB) and the bus clamp for
// remote results (512 KiB per field).
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
