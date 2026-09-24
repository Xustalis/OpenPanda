//go:build !lite

package main

// Turn handlers: how the model folds streamed events into the in-flight turn and
// commits the finished turn to scrollback. Kept apart from the keystroke routing
// in tui_update.go so each file reads as one concern.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	projectstore "github.com/Xustalis/OpenPanda/internal/projects"
)

// onDelta appends one streamed answer chunk. The first answer text also closes
// the thought: reasoning precedes the answer on reasoning models, so once prose
// starts, the thought block is committed to scrollback (folded or expanded per
// the current Ctrl+O state) and the answer streams live below it.
func (m tuiModel) onDelta(msg deltaMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	if !m.thoughtDone {
		m.thoughtDone = true
		if len(m.thought) > 0 {
			tb := block{kind: blockThought, thoughtLines: m.thought}
			cmds = append(cmds, m.printBlock(tb))
		}
	}
	if msg.text != "" {
		m.liveAnswerChunks = append(m.liveAnswerChunks, msg.text)
		m.liveAnswerBytes += len(msg.text)
	}
	cmds = append(cmds, waitForActivity(msg.stream))
	return m, tea.Batch(cmds...)
}

// onProgress folds one lifecycle event into the turn. A task/plan event opens the
// delegated-task card (committing any thought that led to the delegation, the way
// the first answer delta does); the routing/exec/judge milestones that follow
// advance that card's trail. The note is kept either way, for the status line on
// turns that never delegate.
func (m tuiModel) onProgress(msg progressMsg) (tea.Model, tea.Cmd) {
	p := msg.progress
	label := progressNote(m.loc, p)
	m.note = label
	now := time.Now()

	var cmds []tea.Cmd
	switch p.Kind {
	case askengine.ProgressTask, askengine.ProgressPlan:
		if !m.thoughtDone {
			m.thoughtDone = true
			if len(m.thought) > 0 {
				tb := block{kind: blockThought, thoughtLines: m.thought}
				cmds = append(cmds, m.printBlock(tb))
			}
		}
		m.liveTask = newTaskProgress(p.Name, now, m.loc)
	case askengine.ProgressTool:
		if m.liveTask != nil {
			m.liveTask.recordTool(label, now)
		}
	default:
		if m.liveTask != nil {
			m.liveTask.advance(label, now)
		}
	}
	cmds = append(cmds, waitForActivity(msg.stream))
	return m, tea.Batch(cmds...)
}

// onDone commits the finished turn. It records the exchange into conversation
// memory, then pushes the answer / task / plan / error block to scrollback and
// clears the live region. A tier-2 task parked for approval switches the model
// into approving mode instead of committing.
func (m tuiModel) onDone(msg doneMsg) (tea.Model, tea.Cmd) {
	// An ask the user stopped waiting on still finishes — releasing the front
	// end never stopped the work. The watcher announces that outcome, since
	// turnEnded re-armed it when the turn was detached; committing here as
	// well would print the same result twice.
	if msg.stream == nil || msg.stream != m.stream || msg.stream.detached {
		return m, nil
	}
	m.mode = modeIdle
	m.stream = nil

	if msg.err != nil {
		// A user-initiated cancel (Esc / Ctrl+C mid-stream) surfaces as a context
		// error; that is a quiet "interrupted" note, not a red failure block.
		done := m.turnEnded()
		m.resetLive()
		if errors.Is(msg.err, context.Canceled) {
			note := block{kind: blockNote, body: i18n.T(m.loc, "repl.interrupted")}
			return m, tea.Batch(done, m.printBlock(note))
		}
		blk := block{kind: blockError, body: msg.err.Error()}
		// Pair the persisted user turn with the failure (same guard as the
		// classic loop): a thread left dangling on a user turn 400s on its
		// every following ask.
		if m.r != nil {
			m.r.recordErrorTurn(m.pendingPrompt, msg.err)
		}
		return m, tea.Batch(done, m.printBlock(blk))
	}
	out := msg.out
	if out != nil && out.NeedsApproval && out.Approval != nil {
		m.pending = out
		m.pendingWorkDir = m.turnWorkDir
		m.mode = modeApproving
		m.approvalSel = 1 // arrows + Enter start on deny, the [y/N] safe default
		m.approvalScope = out.Approval.Scope
		if m.approvalScope == "" {
			m.approvalScope = projectstore.ScopeSession
		}
		// The watcher stays quiet while the card is up: the parked task's own
		// "review" state is what the card is showing.
		return m, nil // the card renders in View; keys handled by onApprovalKey
	}
	return m.commit(out)
}

// onResumed commits the outcome of a ResumeApproved re-run.
func (m tuiModel) onResumed(msg resumedMsg) (tea.Model, tea.Cmd) {
	if msg.stream == nil || msg.stream != m.stream || msg.stream.detached {
		return m, nil
	}
	m.mode = modeIdle
	m.stream = nil
	m.pending = nil
	m.pendingWorkDir = ""
	return m.commit(msg.out)
}

// turnEnded releases the out-of-band watcher and hands back the command that
// absorbs this turn's terminal states into its baseline, so a task the turn just
// finished is reported once — by the turn — and not again by the watcher.
func (m tuiModel) turnEnded() tea.Cmd {
	if m.r == nil {
		return nil
	}
	m.r.setAsking(false)
	return absorbBaseline(m.r)
}

// commit records the turn into conversation memory and pushes its result block
// to scrollback, clearing the live region. Persistence goes through the shared
// repl helper, so a turn taken inside a /resume'd session lands in that thread
// (and a spawned task is bound to it) exactly as the classic loop would.
func (m tuiModel) commit(out *askengine.Result) (tea.Model, tea.Cmd) {
	done := m.turnEnded()
	if out == nil {
		m.resetLive()
		return m, done
	}
	if m.r != nil {
		m.r.recordOutcome(context.Background(), m.pendingPrompt, out)
	}
	blk := resultBlock(out, m.liveAnswerText(), m.loc)
	// A delegated turn carries its card's title and stage trail into scrollback,
	// so the committed block records the same route/exec/judge evidence the live
	// card showed rather than just the final output.
	if blk.kind == blockTask && m.liveTask != nil {
		blk.title = m.liveTask.title
		blk.stages = m.liveTask.trail(time.Since(m.liveTask.started))
	}
	m.resetLive()
	if blk.body == "" && blk.kind == blockAnswer {
		return m, done
	}
	return m, tea.Batch(done, m.printBlock(blk))
}

// resultBlock turns an engine Result into the transcript block for its kind.
// liveAnswer is the streamed text already accumulated (used for answers so the
// committed block matches exactly what streamed).
func resultBlock(out *askengine.Result, liveAnswer string, loc i18n.Locale) block {
	switch out.Kind {
	case "task":
		meta := ""
		if out.TaskID != "" && out.TaskState != "" {
			meta = i18n.Tf(loc, "repl.ask.task", "id", out.TaskID, "state", out.TaskState)
		}
		appendCostMeta := func(base string) string {
			cm := resultCostMeta(out)
			if cm == "" {
				return base
			}
			if base == "" {
				return cm
			}
			return base + " · " + cm
		}
		if report := strings.TrimSpace(out.Answer); report != "" {
			body := report
			if !out.OK {
				body += fmt.Sprintf("\nexit %d: %s", out.ExitCode, strings.TrimSpace(out.Stderr))
			}
			if out.TaskID != "" && out.TaskState != "" {
				meta = i18n.Tf(loc, "repl.ask.taskReport", "id", out.TaskID, "state", out.TaskState)
			}
			return block{kind: blockTask, ok: out.OK, body: body, meta: appendCostMeta(meta), agent: out.Agent, model: out.Model, injected: out.Injected, executor: out.Executor}
		}
		if summary := strings.TrimSpace(out.Report); summary != "" {
			// The LLM summary is the whole display, matching the classic REPL:
			// it prints the summary and stops. Appending the raw stdout here
			// buried the readable report under a wall of execution log.
			return block{kind: blockTask, ok: out.OK, body: summary, meta: appendCostMeta(meta), agent: out.Agent, model: out.Model, injected: out.Injected, executor: out.Executor}
		}
		if out.OK {
			// When no LLM summary was generated (queue-parked, budget-cut, summarizer
			// degraded), fall back to the cleaned agent output so the user sees the
			// actual work result rather than a blank note.
			if log := cleanedTaskLog(loc, out.Stdout); log != "" {
				return block{kind: blockTask, ok: true, body: log, meta: appendCostMeta(meta), agent: out.Agent, model: out.Model, injected: out.Injected, executor: out.Executor}
			}
			body := i18n.T(loc, "tui.task.noSummary")
			if out.TaskID != "" {
				body += " " + i18n.Tf(loc, "tui.task.rawLogHint", "id", out.TaskID)
			}
			return block{kind: blockTask, ok: true, body: body, meta: appendCostMeta(meta), agent: out.Agent, model: out.Model, injected: out.Injected, executor: out.Executor}
		}
		// Failure keeps its exit evidence, with a runaway stderr tail-capped
		// so a noisy command cannot flood the transcript.
		return block{kind: blockTask, ok: false, body: fmt.Sprintf("exit %d: %s", out.ExitCode, cleanedTaskLog(loc, out.Stderr)), meta: appendCostMeta(meta), agent: out.Agent, model: out.Model, injected: out.Injected, executor: out.Executor}
	case "plan":
		// A plan that failed to start has no board to follow and no stages, so
		// its summary line would read "plan  · 0 stages" — a failure rendered
		// as a success. Surface it as the error it is, the way the classic loop
		// does.
		if !out.OK {
			return block{kind: blockError, body: i18n.Tf(loc, "cli.plan.failed", "err", out.Stderr)}
		}
		return block{kind: blockInfo, body: planSummaryLine(out)}
	default: // answer
		body := strings.TrimSpace(liveAnswer)
		if body == "" {
			body = strings.TrimSpace(out.Answer)
		}
		if note := strings.TrimSpace(out.Note); note != "" {
			body = note + "\n" + body
		}
		return block{kind: blockAnswer, body: body, meta: resultCostMeta(out)}
	}
}

// taskLogMaxLines caps the degraded raw-output fallback: without an LLM
// summary the transcript shows the log's tail, which is where the outcome
// lands, rather than every line the agent ever printed.
const taskLogMaxLines = 40

// cleanedTaskLog makes a raw agent log presentable for the transcript: ANSI
// artefacts stripped, trailing blank lines dropped, and an over-long log cut
// to its last taskLogMaxLines lines behind a note counting the elided ones.
// The note is localized, so it takes the turn's locale.
func cleanedTaskLog(loc i18n.Locale, stdout string) string {
	text := ansi.Strip(strings.TrimRight(stdout, "\n"))
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) <= taskLogMaxLines {
		return text
	}
	elided := len(lines) - taskLogMaxLines
	note := i18n.Tf(loc, "tui.task.logElided",
		"total", strconv.Itoa(len(lines)), "shown", strconv.Itoa(taskLogMaxLines))
	return note + "\n" + strings.Join(lines[elided:], "\n")
}

func resultCostMeta(out *askengine.Result) string {
	if out == nil {
		return ""
	}
	var parts []string
	// The meta line leads with the serving model — on an answer it is the
	// model that wrote the text; on a task it is the entry model that
	// classified it (the agent's own model rides the execBy arm instead).
	if out.EntryModel != "" {
		parts = append(parts, out.EntryModel)
	}
	if out.Latency > 0 {
		parts = append(parts, cliui.HumanDuration(out.Latency))
	}
	tok := out.Tokens()
	if tok > 0 {
		parts = append(parts, cliui.HumanCount(tok)+" tokens")
	}
	if out.Cost > 0 {
		parts = append(parts, fmt.Sprintf("($%.4f)", out.Cost))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

// planSummaryLine is the one-line commit for a started plan: a plan runs
// asynchronously, so the transcript records that it started and how to follow it.
func planSummaryLine(out *askengine.Result) string {
	return fmt.Sprintf("plan %s · %d stages · %s", out.PlanID, len(out.PlanStages), out.PlanGoal)
}

// approvalScopes is the card's remember-scope axis in display order: the
// scope row renders them left to right, and the 1/2/3 hotkeys index it.
var approvalScopes = []string{
	projectstore.ScopeOnce,
	projectstore.ScopeSession,
	projectstore.ScopeProject,
}

// onApprovalKey handles the tier-2 approval card: y approves (resume the task
// authorized), n/Esc denies and commits a note. The arrows move the focus
// between the two choices and Enter answers the focused one, so the card can
// be answered without leaving the navigation keys — the y/n hotkeys remain.
// 1/2/3 (or o/s/p) pick the remember scope the answer is stored under.
func (m tuiModel) onApprovalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// The card only renders while pending is set, but Update sees every
	// keystroke, so guard the dereference instead of trusting the mode flag to
	// stay in step with the field.
	if m.pending == nil || m.pending.Approval == nil {
		m.mode = modeIdle
		return m, nil
	}
	switch strings.ToLower(msg.String()) {
	case "y":
		return m.approvePending()
	case "n", "esc":
		return m.denyPending()
	case "1", "o":
		m.approvalScope = projectstore.ScopeOnce
		return m, nil
	case "2", "s":
		m.approvalScope = projectstore.ScopeSession
		return m, nil
	case "3", "p":
		m.approvalScope = projectstore.ScopeProject
		return m, nil
	case "up", "down", "left", "right":
		// Two choices: any arrow hops to the other one. Holding a key
		// toggles between them, which is the honest reading of a
		// two-option picker.
		m.approvalSel = 1 - m.approvalSel
		return m, nil
	case "enter":
		if m.approvalSel == 0 {
			return m.approvePending()
		}
		return m.denyPending()
	}
	return m, nil
}

// rememberApproval writes the card's answer into the selected scope and
// returns a transcript note when one was stored ("" for "once" or no engine).
// A project-scope answer without a project context degrades to the session
// scope rather than silently writing nothing.
func (m tuiModel) rememberApproval(approved bool) string {
	if m.engine == nil || m.pending == nil || m.pending.Approval == nil {
		return ""
	}
	req := m.pending.Approval
	scope := m.approvalScope
	if scope == "" {
		scope = projectstore.ScopeSession
	}
	if scope == projectstore.ScopeOnce {
		return ""
	}
	if scope == projectstore.ScopeProject && req.Project == "" {
		scope = projectstore.ScopeSession
	}
	decision := projectstore.DecisionDeny
	if approved {
		decision = projectstore.DecisionApprove
	}
	sess := ""
	if m.r != nil {
		sess = m.r.activeSess
	}
	if err := m.engine.RememberApproval(sess, req.Project, scope, decision); err != nil {
		return ""
	}
	return i18n.Tf(m.loc, "repl.approval.remembered", "decision", decision, "scope", scope)
}

// approvePending answers the approval card with yes: the parked task resumes
// authorized, in the worktree this turn was running in.
func (m tuiModel) approvePending() (tea.Model, tea.Cmd) {
	req := m.pending.Approval
	note := m.rememberApproval(true)
	m.mode = modeAsking
	m.started = time.Now()
	m.lastInterrupt = time.Time{} // the re-run gets its own double-tap window
	// Resume in the tree the card was raised for. A task started inside a
	// /resume'd session must not silently re-run under the engine's default
	// work path — for an irreversible task that is the wrong directory, not
	// merely a cosmetic difference. An out-of-band task (watcher-raised card)
	// carries no session tree, and an empty workDir keeps its persisted one.
	stream, pump := startResume(m.engine, req.TaskID, m.pendingWorkDir)
	m.stream = stream
	cmds := []tea.Cmd{m.sp.Tick, pump}
	if note != "" {
		cmds = append(cmds, m.printBlock(block{kind: blockNote, body: note}))
	}
	return m, tea.Batch(cmds...)
}

// denyPending answers the approval card with no: the task stays in review and
// a note says how to run it later.
func (m tuiModel) denyPending() (tea.Model, tea.Cmd) {
	id := m.pending.Approval.TaskID
	note := m.rememberApproval(false)
	m.pending = nil
	m.pendingWorkDir = ""
	m.mode = modeIdle
	done := m.turnEnded()
	cmds := []tea.Cmd{done, m.printBlock(block{kind: blockNote, body: i18n.Tf(m.loc, "repl.approval.denied", "id", id)})}
	if note != "" {
		cmds = append(cmds, m.printBlock(block{kind: blockNote, body: note}))
	}
	return m, tea.Batch(cmds...)
}

// liveAnswerText materializes the streamed answer only at render/commit boundaries.
// Chunks make appending O(1) amortized instead of copying the complete answer for
// every delta.
func (m tuiModel) liveAnswerText() string {
	if len(m.liveAnswerChunks) == 0 {
		return ""
	}
	var b strings.Builder
	b.Grow(m.liveAnswerBytes)
	for _, chunk := range m.liveAnswerChunks {
		b.WriteString(chunk)
	}
	return b.String()
}

// resetLive clears the in-flight turn state after a turn commits.
func (m *tuiModel) resetLive() {
	m.liveAnswerChunks = nil
	m.liveAnswerBytes = 0
	m.thought = nil
	m.thoughtDone = false
	m.note = ""
	m.pendingPrompt = ""
	m.turnMode = "" // the slash-mode lens is per-turn, never sticky
	m.liveTask = nil
	m.ta.Placeholder = i18n.T(m.loc, "tui.input.placeholder")
}

// appendReasoning folds a reasoning chunk into the running thought lines: it
// keeps whole lines, so a chunk that splits mid-line extends the last line
// rather than starting a new one. Display-only (D14).
func appendReasoning(lines []string, chunk string) []string {
	if chunk == "" {
		return lines
	}
	parts := strings.Split(chunk, "\n")
	if len(lines) == 0 {
		lines = append(lines, "")
	}
	lines[len(lines)-1] += parts[0]
	for _, p := range parts[1:] {
		lines = append(lines, p)
	}
	return lines
}
