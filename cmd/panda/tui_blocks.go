package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// A transcript is a list of blocks — the committed conversation history the TUI
// scrolls through, mirroring Claude Code's block tree. Each block renders itself
// to a width; the live (in-flight) answer and thought are separate fields on the
// model and only become blocks once the turn commits, so a redraw never has to
// re-flow the whole stream mid-token.

// blockKind tags a transcript entry for styling and fold behaviour.
type blockKind int

const (
	blockUser    blockKind = iota // the prompt the user typed
	blockAnswer                   // assistant prose
	blockThought                  // chain-of-thought, collapsible (D14 display-only)
	blockNote                     // a progress/status note (routing, running…)
	blockTask                     // a delegated task/subagent result
	blockError                    // a failed ask
	blockInfo                     // slash-command output captured from a handler
)

// block is one committed transcript entry. thoughtSummary/thoughtLines back the
// collapsible thought block: the summary is the one-line fold, the lines the
// full expansion. Other kinds use body.
type block struct {
	kind blockKind
	body string

	// Thought-block state (kind == blockThought).
	thoughtLines []string
	// ok distinguishes a succeeded task (blockTask) from a failed one for tinting.
	ok bool
	// Task-block state (kind == blockTask): what was delegated and the lifecycle
	// trail it went through, so scrollback keeps the evidence the live card showed.
	title    string
	stages   []string
	meta     string
	agent    string
	model    string
	injected bool
}

// render draws the block at the given width using the theme. expandThought
// controls whether thought blocks show their full body or the one-line fold —
// it is a global toggle (Ctrl+O), so every thought block folds together, which
// matches how Claude Code's transcript behaves.
func (b block) render(t theme, width int, expandThought bool) string {
	switch b.kind {
	case blockUser:
		return userText(t, b.body, width)
	case blockAnswer:
		res := answerText(t, b.body, width)
		if b.meta != "" {
			res += "\n" + t.muted.Render("  "+b.meta)
		}
		return res
	case blockThought:
		return b.renderThought(t, expandThought)
	case blockNote:
		return t.muted.Render(t.glyph("•", "*") + " " + b.body)
	case blockTask:
		return b.renderTask(t)
	case blockError:
		return t.danger.Render(t.glyph("✗", "x") + " " + b.body)
	case blockInfo:
		return b.body
	}
	return b.body
}

// userText echoes the submitted prompt under a "❯" so scrolling back reads as a
// dialogue rather than an undifferentiated wall of text. It is dim, and wrapped
// and hung like the answer beneath it: the user's own words are the one thing in
// the transcript they do not need to read back, so they recede and the accent
// colour stays reserved for the app's own voice. Wrapping matters as much as the
// tint — only the answer half of a turn used to be wrapped, so a pasted prompt
// ran past the frame while the reply to it stayed inside.
func userText(t theme, body string, width int) string {
	if width > 4 {
		body = wrap(body, width-2)
	}
	return t.muted.Render(indentLines(body, t.glyph("❯", ">")+" ", "  "))
}

// answerText lays out assistant prose under a marker glyph: wrapped to the
// remaining width and hung under a two-column indent. The user's prompt already
// commits with a "❯", so an unmarked answer left the two halves of a turn looking
// like different kinds of thing — and a one-word reply ("2") was
// indistinguishable from a stray line of terminal output. The live region uses
// this same function, so a streaming answer does not shift when it commits.
func answerText(t theme, body string, width int) string {
	if width > 4 {
		body = wrap(body, width-2)
	}
	return indentLines(body, t.accent.Render(t.glyph("⏺", "*"))+" ", "  ")
}

// renderThought draws the collapsible chain-of-thought. Folded, it is a single
// dim line: the label, a one-line teaser, and how many lines are folded away.
// Expanded, the full reasoning prints dim+italic under the header. It is never
// part of the answer (D14) — this is a display affordance over text the engine
// already keeps out of history.
//
// The fold reports its line count rather than advertising "ctrl+o". This front
// end runs inline, so a committed block has already been written into the
// terminal's scrollback and can never be redrawn: the toggle governs the
// thought being streamed and every thought committed after it flips, not the
// blocks already above the cursor. Printing a key hint on a block that key
// cannot touch would be a promise the display cannot keep.
func (b block) renderThought(t theme, expand bool) string {
	star := t.glyph("✻", "*")
	label := i18n.T(t.loc, "tui.thought.head")
	if !expand {
		head := t.muted.Render(star + " " + label)
		if summary := firstThoughtLine(b.thoughtLines); summary != "" {
			head += t.muted.Render(" · " + truncate(summary, 60))
		}
		if n := len(b.thoughtLines); n > 1 {
			head += t.muted.Render(" · " + i18n.Tf(t.loc, "tui.thought.lines", "n", strconv.Itoa(n)))
		}
		return head
	}
	var sb strings.Builder
	sb.WriteString(t.muted.Render(star + " " + label))
	for _, line := range b.thoughtLines {
		sb.WriteString("\n")
		sb.WriteString(t.italic.Render("  " + line))
	}
	return sb.String()
}

// renderTask draws a delegated-task result: a header naming the task and its
// outcome, one arm per lifecycle stage it went through, and a final arm holding
// the task state pointer. Substantive answer/report prose is separated from the
// task subtree and rendered in clean, normal prose below the card.
func (b block) renderTask(t theme) string {
	arm := t.glyph("⎿", "\\_")
	head := t.glyph("●", "*") + " " + i18n.T(t.loc, "tui.task.head")
	if b.ok {
		head = t.success.Render(head)
	} else {
		head = t.danger.Render(head)
	}
	if b.title != "" {
		head += " " + t.glyph("·", "-") + " " + b.title
	}
	var sb strings.Builder
	sb.WriteString(head)
	for _, st := range b.stages {
		sb.WriteString("\n" + t.muted.Render("  "+arm+"  "+st))
	}
	if b.agent != "" {
		execNote := b.agent
		if b.model != "" {
			execNote += fmt.Sprintf(" (%s)", b.model)
		}
		if b.injected {
			execNote += " · " + i18n.T(t.loc, "tui.task.injected")
		}
		sb.WriteString("\n" + t.muted.Render("  "+arm+"  "+i18n.Tf(t.loc, "tui.task.execBy", "exec", execNote)))
	}
	if b.meta != "" {
		sb.WriteString("\n" + t.muted.Render("  "+arm+"  "+b.meta))
	}
	if b.body != "" {
		if b.ok {
			sb.WriteString("\n\n" + b.body)
		} else {
			sb.WriteString("\n\n" + t.danger.Render(b.body))
		}
	}
	return sb.String()
}

// firstThoughtLine returns the first non-empty line of a slice, for the fold
// summary.
func firstThoughtLine(lines []string) string {
	for _, l := range lines {
		if s := strings.TrimSpace(l); s != "" {
			return s
		}
	}
	return ""
}

// truncate clips s to n runes, appending an ellipsis when it had to cut.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// indentLines prefixes the first line with first and the rest with cont, so a
// multi-line block reads as one nested subtree.
func indentLines(s, first, cont string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if i == 0 {
			lines[i] = first + l
		} else {
			lines[i] = cont + l
		}
	}
	return strings.Join(lines, "\n")
}

// wrap hard-wraps s to width columns using lipgloss's width-aware wrapper, so
// answer prose reflows to the terminal instead of running off the edge. A
// non-positive width is a no-op (the model has not learned its size yet).
//
// lipgloss pads every wrapped line out to the block width; that padding is
// stripped here. A committed block is written into the terminal's own scrollback,
// so the spaces would survive in anything the user copies out of it — and the
// live region is repainted by Bubble Tea, which clears its own lines.
func wrap(s string, width int) string {
	if width <= 0 {
		return s
	}
	lines := strings.Split(lipgloss.NewStyle().Width(width).Render(s), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
}
