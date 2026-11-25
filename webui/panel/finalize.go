// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"strconv"
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

// evSessionSummary is the task_events marker for a persisted summary turn.
// Append the idempotently keyed turn BEFORE the marker: failed writes remain
// eligible for a later sweep, and a crash/marker failure replays without
// duplicating the turn.
const evSessionSummary = "session_summary"

// startSessionFinalizer runs the summary sweep until ctx ends (queue
// rea finished task solidifies its result as an assistant turn
// in the linked session). No-op pieces when the sessions store is absent.
func (h *handler) startSessionFinalizer(ctx context.Context) {
	if h.sessions == nil || h.store == nil {
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
				if !h.finalizeSessionSweep(ctx) {
					// The store is gone: this handler's lifetime is over, and
					// every further tick would only re-log that fact.
					return
				}
			}
		}
	})
}

// errStoreClosed reports the error database/sql returns once its DB is closed.
// The finalizer treats it as a retirement signal rather than a fault: every
// New(deps) starts one for the process lifetime, so a handler whose store was
// torn down under it — a test fixture, or a process past its shutdown — would
// otherwise re-log the same warning on every tick and keep probing a store
// that can never answer again.
func errStoreClosed(err error) bool {
	return err != nil && strings.Contains(err.Error(), "database is closed")
}

// finalizeSessionSweep appends the summary turn for every terminal task
// linked to a session that has not been summarized yet. The candidate set
// comes straight from the store as one probe (terminal + session + no
// marker); the former shape loaded every task row and then every task's full
// event timeline on every pass, so a board of already-summarized tasks was
// re-read forever.
//
// The return value says whether another sweep is worth running: false means
// the store is closed and the caller should stop ticking.
func (h *handler) finalizeSessionSweep(ctx context.Context) bool {
	if h.sessions == nil || h.store == nil {
		return false
	}
	tasks, err := h.store.TerminalSessionTasksWithoutEvent(ctx, evSessionSummary)
	if err != nil {
		if errStoreClosed(err) {
			return false
		}
		return h.finalizeAskRounds(ctx)
	}
	for _, t := range tasks {
		// The judge's sentence is read once and used twice: it leads the
		// replay digest and it rides the turn as Verdict for the console.
		evidence := h.taskDeliveryEvidence(ctx, t)
		judge := evidence.judge
		summary := h.taskSummary(ctx, t, judge)
		delivery, truncated := taskDelivery(t.ResultJSON)
		// A task turn measures the *work*, not the wait. The row's own
		// CreatedAt is the submission second, and a task that queued behind a
		// 12-minute sibling reported that sibling's runtime as its own: a
		// route check that failed in 0.18s printed "424s", and a task
		// cancelled before it ever started printed "124s". The clock starts at
		// the attempt's first accept/exec_agent_start instead; a row that
		// never ran keeps 0 and the console shows no number at all.
		if _, err := h.sessions.AppendTurn(t.SessionID, sessions.Turn{
			Role: "assistant",
			// The outcome is prose, not a second dispatch. As a task turn
			// (kind=task + ref) the thread rendered the orbit and the task
			// card all over again — one card for "dispatched", another for
			// "done", with the digest wedged between them. The dispatch turn
			// already carries the card and the jump link; this one is just
			// what happened. Ref still travels so the report can fold the
			// task's own process under it without drawing a second card.
			Kind:              "answer",
			Ref:               t.TaskID,
			Text:              summary,
			Delivery:          delivery,
			DeliveryTruncated: truncated,
			Verdict:           judge,
			DeliveryAttemptID: t.AttemptID,
			DurationMS:        evidence.executionMS,
			ApprovalWaitMS:    evidence.approvalWaitMS,
			DeliveryStatus:    evidence.status,
			IdempotencyKey:    taskSummaryKey(t),
		}); err != nil {
			log.From(ctx).Warn("session summary append", "task", t.TaskID, "session", t.SessionID, "err", err)
			continue
		}
		if err := h.store.RecordEvent(ctx, t.TaskID, evSessionSummary, map[string]string{
			"session_id": t.SessionID,
			"state":      t.State,
			"attempt_id": t.AttemptID,
		}); err != nil {
			log.From(ctx).Warn("session summary marker", "task", t.TaskID, "session", t.SessionID, "err", err)
		}
	}
	return h.finalizeAskRounds(ctx)
}

// taskSummaryKey is stable across sweeps and process restarts, but rotates
// with a retry so a failed attempt cannot suppress the eventual result.
func taskSummaryKey(t core.Task) string {
	return evSessionSummary + ":" + t.TaskID + ":" + t.AttemptID
}

// taskAttemptEvents excludes earlier attempts. Retries (including lease
// reroutes) append EvRetry atomically with rotating the attempt; legacy
// timelines without a submit/retry marker retain their existing events.
func (h *handler) taskAttemptEvents(ctx context.Context, taskID string) ([]core.Event, error) {
	events, err := h.store.Events(ctx, taskID)
	if err != nil {
		return nil, err
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == core.EvSubmit || events[i].Type == core.EvRetry {
			return events[i+1:], nil
		}
	}
	return events, nil
}

// taskExecutionStart returns the unix second this attempt first left the queue:
// its earliest accept or exec_agent_start. Zero means it never ran — a row
// cancelled while queued, or one the router refused before dispatch — and the
// caller must not invent a duration for it.
//
// The task row itself carries only created_at/updated_at, and their difference
// is queue wait plus execution. The trace events are the only place the
// handoff second is recorded.
func (h *handler) taskExecutionStart(ctx context.Context, taskID string) int64 {
	events, err := h.taskAttemptEvents(ctx, taskID)
	if err != nil {
		return 0
	}
	for _, ev := range events {
		if ev.Type == core.EvAccept || ev.Type == core.EvExecAgentStart {
			return ev.TS
		}
	}
	return 0
}

// taskSummary renders the one-turn report of a finished task. judge is the
// supervision verdict already read for the turn (empty when the task never
// reached a judge).
func (h *handler) taskSummary(ctx context.Context, t core.Task, judge string) string {
	reason := ""
	if t.State != core.StateDone {
		reason = h.failureReason(ctx, t.TaskID)
	}
	agent := h.taskAgent(ctx, t)
	from, why := h.substitutedFrom(ctx, t.TaskID, agent)
	return summaryText(t, reason, agent, from, why, h.reviewVerdict(ctx, t.TaskID), judge)
}

// taskVerdict returns panda's own sentence about the finished work: the last
// supervision round's reason, which is the one paragraph in the trace that was
// written by the judge rather than by the agent.
//
// It exists because the report used to be the agent's own closing paragraph,
// printed under a `[done] @agent` line. A reader could not tell where the
// employee stopped and the house started speaking: claude_code's "结论：成功"
// arrived as if panda had certified it, and a task the supervisor passed with
// reservations read exactly like one it checked. The judge already writes that
// sentence — this is where it becomes the report.
func (h *handler) taskVerdict(ctx context.Context, taskID string) string {
	events, err := h.taskAttemptEvents(ctx, taskID)
	if err != nil {
		return ""
	}
	return attemptVerdict(events)
}

func attemptVerdict(events []core.Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != core.EvSupervise {
			continue
		}
		var d struct {
			Reason string `json:"reason"`
			Status string `json:"status"`
		}
		if json.Unmarshal([]byte(events[i].DataJSON), &d) != nil {
			continue
		}
		if s := strings.TrimSpace(d.Reason); s != "" {
			return s
		}
	}
	return ""
}

// reviewVerdict reports whether a human already decided the newest review on
// this task: "approved", "rejected", or "" when the newest review is still the
// park itself. A settled row's report must not ask for a decision that has been
// made — a scope drift accepted into `done` is exactly the case that used to
// come back as "needs your attention / approve or reject".
//
// Key presence, not truthiness: a rejection with an empty reason still records
// {"rejected": ""}, and that is a decision.
func (h *handler) reviewVerdict(ctx context.Context, taskID string) string {
	events, err := h.taskAttemptEvents(ctx, taskID)
	if err != nil {
		return ""
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != core.EvReview {
			continue
		}
		var payload map[string]json.RawMessage
		if json.Unmarshal([]byte(events[i].DataJSON), &payload) != nil {
			return ""
		}
		if _, ok := payload["rejected"]; ok {
			return "rejected"
		}
		if raw, ok := payload["approved"]; ok {
			var approved bool
			if json.Unmarshal(raw, &approved) == nil && approved {
				return "approved"
			}
		}
		// The newest review is the park itself: nothing has answered it yet.
		return ""
	}
	return ""
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

// taskDelivery preserves the actual handoff separately from the replay digest.
// Bound the display copy, not the underlying task result: very large outputs
// remain accessible on the task page without growing every session replay.
const maxDeliveryRunes = 24000

func taskDelivery(resultJSON string) (string, bool) {
	text := extractResultText(resultJSON)
	if utf8.RuneCountInString(text) <= maxDeliveryRunes {
		return text, false
	}
	return string([]rune(text)[:maxDeliveryRunes]), true
}

// summaryText is the pure composition of the report, in two parts and no
// more: the orchestration line — state, the employee that ran it, the job it
// was given — and then what happened, in two voices: panda's own verdict on
// the work, then the executor's closing paragraph, quoted.
//
// The thread is a conversation, not a log. It used to carry the entire
// adapter payload: a 2 000-word agent reply, its "变更 / 验证" sections and
// the code fences inside them, printed whole under a `[done]` tag. What a
// reader wants from the transcript is who did the work and how it ended; the
// play-by-play is one click away on the task page, and replaying the dump
// into the model's own history spent the context window on its own past
// prose. One conclusion paragraph is the summary — the sections above it are
// the evidence behind it.
//
// The verdict is not optional decoration. With only the agent's paragraph the
// report read as if the employee had graded its own homework: "结论：成功"
// under a `[done]` line is indistinguishable from panda having checked it. The
// judge writes one sentence per task already; it goes first, and the employee's
// paragraph follows in a blockquote so the voices never blur.
//
// This short version is replayed to the model. The full display report is
// stored in Turn.Delivery, so preserving changes, verification and open items
// for the user no longer consumes the next request's context window.
func summaryText(t core.Task, reason, agent, substituted, substitutedWhy, verdict, judge string) string {
	// A failed run's own reason is its conclusion: there is no adapter answer
	// to take a closing paragraph from, and the reason is what the reader
	// needs. A done run never shows a stale reason from an earlier attempt.
	loc := t.GetUserLocale()
	conclusion := conclusionOf(extractResultText(t.ResultJSON))
	if conclusion == "" && t.State != core.StateDone {
		conclusion = conclusionOf(reason)
	}
	rawBody := conclusion
	conclusion = compactScopeDrift(loc, conclusion, verdict)
	body := composeReport(judge, conclusion)
	// A review always needs a person. So does a drift nothing has answered —
	// the supervisor closing a row whose own report says it overstepped is not
	// a decision about the drift. A drift a human already accepted or rejected
	// is neither: the report owes the reader the outcome, not the question.
	undecidedDrift := conclusion != rawBody && verdict == ""
	needsAttention := t.State == core.StateReview || undecidedDrift
	stateLabel := t.State
	if undecidedDrift && stateLabel == core.StateDone {
		stateLabel = core.StateReview
	}

	head := "[" + stateLabel + "] "
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
			head += i18n.Tf(loc, "web.session.agentSubstitutedWhy",
				"from", substituted, "to", agent, "why", why)
		} else {
			head += i18n.Tf(loc, "web.session.agentSubstituted", "from", substituted, "to", agent)
		}
	}

	if needsAttention {
		attention := i18n.T(loc, "web.session.reviewNeedsAttention")
		if body == "" {
			return head + "\n\n" + attention
		}
		return head + "\n\n" + attention + "\n\n" + body
	}
	if body == "" {
		return head
	}
	return head + "\n" + body
}

// compactScopeDrift turns the scope intercept's raw file list into a summary
// a person can read in a chat bubble. The full list stays on the task's
// execution chain: the transcript owes the reader the count, the first few
// paths, and — depending on who closed the row — either what to do next or
// what was decided. An undecided drift keeps the "approve or reject"
// instruction; an accepted or rejected one must not, because by the time this
// report is written the decision already happened.
// agentConclusionRunes bounds the executor's quoted paragraph. The full text
// is in Turn.Delivery (and on the task page); this copy is what the next
// request replays, so it stays one short paragraph.
const agentConclusionRunes = 600

// composeReport puts panda's verdict first and the executor's closing
// paragraph after it as a blockquote.
//
// The blockquote is the whole point: it marks a change of speaker without a
// locale-specific label, so a reader (and a model replaying the thread) can
// tell the house's sentence from the employee's. An empty verdict falls back
// to the old shape — the conclusion alone — because a task the supervisor
// never judged (an adapter that failed before the first round) has no house
// sentence to print.
func composeReport(judge, conclusion string) string {
	judge = strings.TrimSpace(judge)
	conclusion = strings.TrimSpace(conclusion)
	switch {
	case judge == "":
		return conclusion
	case conclusion == "":
		return judge
	}
	quoted := clipRunes(conclusion, agentConclusionRunes)
	return judge + "\n\n> " + strings.ReplaceAll(quoted, "\n", "\n> ")
}

// clipRunes cuts s to at most n runes, marking the cut. Runes, not bytes: a
// CJK report halved by a byte offset ends in mojibake.
func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return strings.TrimRight(string([]rune(s)[:n]), " \t\r\n") + "…"
}

func compactScopeDrift(loc i18n.Locale, body, verdict string) string {
	const prefix = "scope drift: agent changed files outside declared scope: "
	if !strings.HasPrefix(body, prefix) {
		return body
	}
	var paths []string
	for _, p := range strings.Split(strings.TrimPrefix(body, prefix), ",") {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return body
	}
	shown := paths
	if len(shown) > 3 {
		shown = shown[:3]
	}
	list := strings.Join(shown, ", ")
	if len(paths) > len(shown) {
		list += " …"
	}
	key := "web.session.scopeDrift"
	switch verdict {
	case "approved":
		key = "web.session.scopeDriftAccepted"
	case "rejected":
		key = "web.session.scopeDriftRejected"
	}
	return i18n.Tf(loc, key, "n", strconv.Itoa(len(paths)), "paths", list)
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
