package main

// The delegated-task card. A task classified out of a prompt runs on an agent
// (Claude Code, Codex, a peer node) rather than in the model's own reply, so it
// gets its own block instead of collapsing into the answer's status line: a
// header naming the task, a live trail of lifecycle stages (routed → running →
// reviewing) with the current one spinning, and its own clock. The engine
// reports each stage as an askengine.Progress; this file is only the shape those
// events take on screen.

import (
	"fmt"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// taskStage is one lifecycle milestone. label is already localised (progressNote
// phrases it); at is the offset from the task's start, so the trail can show how
// long each stage took without storing wall-clock times.
type taskStage struct {
	label string
	at    time.Duration
}

// taskProgress is the in-flight delegated task. It is created when the engine
// reports it submitting a task and closed when the turn commits; the committed
// block keeps the finished trail so scrollback records what actually happened.
type taskProgress struct {
	title   string
	started time.Time
	stages  []taskStage
}

// newTaskProgress opens a card for a task the engine just submitted.
func newTaskProgress(title string, now time.Time) *taskProgress {
	return &taskProgress{title: strings.TrimSpace(title), started: now}
}

// advance records a new stage. A repeat of the current stage's label is dropped
// so a chatty executor (several exec events for one run) does not pad the trail
// with duplicates.
func (tp *taskProgress) advance(label string, now time.Time) {
	label = strings.TrimSpace(label)
	if label == "" {
		return
	}
	if n := len(tp.stages); n > 0 && tp.stages[n-1].label == label {
		return
	}
	tp.stages = append(tp.stages, taskStage{label: label, at: now.Sub(tp.started)})
}

// trail renders the finished stages as transcript lines, each with the time it
// took (the next stage's offset, or the total for the last one). These go into
// the committed block, so the scrollback keeps the timing evidence.
func (tp *taskProgress) trail(total time.Duration) []string {
	if tp == nil || len(tp.stages) == 0 {
		return nil
	}
	out := make([]string, 0, len(tp.stages))
	for i, st := range tp.stages {
		end := total
		if i+1 < len(tp.stages) {
			end = tp.stages[i+1].at
		}
		if d := end - st.at; d > 0 {
			out = append(out, fmt.Sprintf("%s · %s", st.label, elapsed(d)))
			continue
		}
		out = append(out, st.label)
	}
	return out
}

// renderLive draws the card while the task runs: the header, every completed
// stage with its duration, the current stage carrying the spinner, and a footer
// with the total clock and the interrupt hint. It owns the only animation on
// screen while a task is in flight — the global status line stands down so the
// user is not watching two spinners disagree.
func (tp *taskProgress) renderLive(t theme, loc i18n.Locale, spin string, now time.Time) string {
	if tp == nil {
		return ""
	}
	arm := t.glyph("⎿", "\\_")
	tick := t.glyph("✓", "v")

	// Determine milestone progress for pipeline visualizer
	hasRoute := false
	hasExec := false
	hasJudge := false
	for _, st := range tp.stages {
		l := strings.ToLower(st.label)
		if strings.Contains(l, "rout") || strings.Contains(l, "路由") {
			hasRoute = true
		}
		if strings.Contains(l, "run") || strings.Contains(l, "exec") || strings.Contains(l, "运行") {
			hasExec = true
		}
		if strings.Contains(l, "review") || strings.Contains(l, "judge") || strings.Contains(l, "评审") {
			hasJudge = true
		}
	}

	var s1, s2, s3, s4 string
	circle := t.glyph("○", "-")
	dot := t.glyph("●", "*")
	pipeArrow := t.glyph(" ══▶ ", " ==> ")

	if hasJudge {
		s1 = t.success.Render(tick + " 分流")
		s2 = t.success.Render(tick + " 调度")
		s3 = t.success.Render(tick + " 执行")
		s4 = t.accent.Render(dot + " 评审")
	} else if hasExec {
		s1 = t.success.Render(tick + " 分流")
		s2 = t.success.Render(tick + " 调度")
		s3 = t.accent.Render(dot + " 执行")
		s4 = t.muted.Render(circle + " 评审")
	} else if hasRoute {
		s1 = t.success.Render(tick + " 分流")
		s2 = t.accent.Render(dot + " 调度")
		s3 = t.muted.Render(circle + " 执行")
		s4 = t.muted.Render(circle + " 评审")
	} else {
		s1 = t.accent.Render(dot + " 分流")
		s2 = t.muted.Render(circle + " 调度")
		s3 = t.muted.Render(circle + " 执行")
		s4 = t.muted.Render(circle + " 评审")
	}

	flow := s1 + pipeArrow + s2 + pipeArrow + s3 + pipeArrow + s4

	total := now.Sub(tp.started)
	var sb strings.Builder

	head := t.glyph("⚡", "*") + " " + i18n.T(loc, "tui.task.head")
	sb.WriteString(t.heading.Render(head))
	if tp.title != "" {
		sb.WriteString(" " + t.glyph("·", "-") + " " + tp.title)
	}
	sb.WriteString("\n  " + flow)

	for i, st := range tp.stages {
		last := i == len(tp.stages)-1
		end := total
		if !last {
			end = tp.stages[i+1].at
		}
		mark := t.success.Render(tick)
		if last {
			mark = spin // the stage still running owns the spinner
		}
		line := fmt.Sprintf("  %s  %s %s", arm, mark, st.label)
		if d := end - st.at; d > 0 {
			line += t.muted.Render(" · " + elapsed(d))
		}
		sb.WriteString("\n" + line)
	}
	if len(tp.stages) == 0 {
		// Submitted but not yet routed: show the spinner on the header's arm so
		// the card never sits inert waiting for the first milestone.
		sb.WriteString(fmt.Sprintf("\n  %s  %s %s", arm, spin, i18n.T(loc, "tui.task.starting")))
	}
	sb.WriteString("\n" + t.muted.Render(fmt.Sprintf("  (%s · %s)",
		elapsed(total), i18n.T(loc, "cli.status.interrupt"))))
	return sb.String()
}
