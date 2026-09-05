package main

// Turn handlers: how the model folds streamed events into the in-flight turn and
// commits the finished turn to scrollback. Kept apart from the keystroke routing
// in tui_update.go so each file reads as one concern.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// onDelta appends one streamed answer chunk. The first answer text also closes
// the thought: reasoning precedes the answer on reasoning models, so once prose
// starts, the thought block is committed to scrollback (folded or expanded per
// the current Ctrl+O state) and the answer streams live below it.
func (m tuiModel) onDelta(chunk string) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	if !m.thoughtDone {
		m.thoughtDone = true
		if len(m.thought) > 0 {
			tb := block{kind: blockThought, thoughtLines: m.thought}
			cmds = append(cmds, m.printBlock(tb))
		}
	}
	m.liveAnswer += chunk
	cmds = append(cmds, waitForActivity(m.stream))
	return m, tea.Batch(cmds...)
}

// onProgress folds one lifecycle event into the turn. A task/plan event opens the
// delegated-task card (committing any thought that led to the delegation, the way
// the first answer delta does); the routing/exec/judge milestones that follow
// advance that card's trail. The note is kept either way, for the status line on
// turns that never delegate.
func (m tuiModel) onProgress(p askengine.Progress) (tea.Model, tea.Cmd) {
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
		m.liveTask = newTaskProgress(p.Name, now)
	default:
		if m.liveTask != nil {
			m.liveTask.advance(label, now)
		}
	}
	cmds = append(cmds, waitForActivity(m.stream))
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
	if msg.stream != nil && msg.stream.detached {
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
			m.r.recordErrorTurn(msg.err)
		}
		return m, tea.Batch(done, m.printBlock(blk))
	}
	out := msg.out
	if out != nil && out.NeedsApproval && out.Approval != nil {
		m.pending = out
		m.mode = modeApproving
		m.approvalSel = 1 // arrows + Enter start on deny, the [y/N] safe default
		// The watcher stays quiet while the card is up: the parked task's own
		// "review" state is what the card is showing.
		return m, nil // the card renders in View; keys handled by onApprovalKey
	}
	return m.commit(out)
}

// onResumed commits the outcome of a ResumeApproved re-run.
func (m tuiModel) onResumed(msg resumedMsg) (tea.Model, tea.Cmd) {
	m.mode = modeIdle
	m.pending = nil
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
	blk := resultBlock(out, m.liveAnswer, m.loc)
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
		// Sub-agent round: the converged report is the reply — it streamed
		// live into this turn's answer region, so the committed body is the
		// model's report with the raw agent output demoted to a pointer
		// line. Without a report (queue-parked, budget-cut, report
		// degraded) the raw output remains the body, as before.
		if report := strings.TrimSpace(out.Answer); report != "" {
			body := report
			if !out.OK {
				body += fmt.Sprintf("\nexit %d: %s", out.ExitCode, strings.TrimSpace(out.Stderr))
			}
			if out.TaskID != "" && out.TaskState != "" {
				meta = i18n.Tf(loc, "repl.ask.taskReport", "id", out.TaskID, "state", out.TaskState)
			}
			return block{kind: blockTask, ok: out.OK, body: body, meta: appendCostMeta(meta), agent: out.Agent, model: out.Model, injected: out.Injected}
		}
		// LLM-generated summary: the dedicated "report after execution" call
		// fills Report so the user sees a human-readable summary instead of
		// raw stdout/stderr.
		if summary := strings.TrimSpace(out.Report); summary != "" {
			return block{kind: blockTask, ok: out.OK, body: summary, meta: appendCostMeta(meta), agent: out.Agent, model: out.Model, injected: out.Injected}
		}
		if out.OK {
			return block{kind: blockTask, ok: true, body: strings.TrimRight(out.Stdout, "\n"), meta: appendCostMeta(meta), agent: out.Agent, model: out.Model, injected: out.Injected}
		}
		return block{kind: blockTask, ok: false, body: fmt.Sprintf("exit %d: %s", out.ExitCode, out.Stderr), meta: appendCostMeta(meta), agent: out.Agent, model: out.Model, injected: out.Injected}
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

func resultCostMeta(out *askengine.Result) string {
	if out == nil {
		return ""
	}
	var parts []string
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

// onApprovalKey handles the tier-2 approval card: y approves (resume the task
// authorized), n/Esc denies and commits a note. The arrows move the focus
// between the two choices and Enter answers the focused one, so the card can
// be answered without leaving the navigation keys — the y/n hotkeys remain.
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

// approvePending answers the approval card with yes: the parked task resumes
// authorized, in the worktree this turn was running in.
func (m tuiModel) approvePending() (tea.Model, tea.Cmd) {
	req := m.pending.Approval
	m.mode = modeAsking
	m.started = time.Now()
	m.lastInterrupt = time.Time{} // the re-run gets its own double-tap window
	// Resume in the tree this turn was running in. A task started inside a
	// /resume'd session must not silently re-run under the engine's default
	// work path — for an irreversible task that is the wrong directory, not
	// merely a cosmetic difference.
	return m, tea.Batch(m.sp.Tick, resumeApproved(m.engine, req.TaskID, m.turnWorkDir))
}

// denyPending answers the approval card with no: the task stays in review and
// a note says how to run it later.
func (m tuiModel) denyPending() (tea.Model, tea.Cmd) {
	id := m.pending.Approval.TaskID
	m.pending = nil
	m.mode = modeIdle
	done := m.turnEnded()
	note := block{kind: blockNote, body: i18n.Tf(m.loc, "repl.approval.denied", "id", id)}
	return m, tea.Batch(done, m.printBlock(note))
}

// resetLive clears the in-flight turn state after a turn commits.
func (m *tuiModel) resetLive() {
	m.liveAnswer = ""
	m.thought = nil
	m.thoughtDone = false
	m.note = ""
	m.pendingPrompt = ""
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
