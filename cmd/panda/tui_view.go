package main

// View renders the ephemeral region: the live turn (streaming answer or thought
// preview) with its spinner status while asking, the approval card while
// approving, and the bottom rounded input box while idle. Committed turns are
// not re-rendered here — they live in the terminal's scrollback (tea.Println),
// which is why quitting leaves the whole conversation on screen.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

func (m tuiModel) View() string {
	if m.quitting {
		return ""
	}
	switch m.mode {
	case modeAsking:
		// A delegated task's card carries its own spinner, clock and interrupt
		// hint, so the global status line stands down while one is in flight —
		// two competing spinners read as a glitch, not as more information.
		if m.liveTask != nil {
			return m.liveRegion()
		}
		return m.liveRegion() + "\n" + m.statusLine()
	case modeApproving:
		return m.approvalCard()
	default:
		return m.inputView()
	}
}

// liveRegion is what the in-flight turn shows: the streaming answer once prose
// has started, otherwise the live chain-of-thought (folded to a moving one-liner,
// or expanded under Ctrl+O), plus the delegated-task card when the turn was
// classified as a task. Reasoning is display-only (D14).
func (m tuiModel) liveRegion() string {
	var parts []string
	if ans := m.liveAnswer.String(); strings.TrimSpace(ans) != "" {
		parts = append(parts, answerText(m.th, ans, m.textWidth()))
	} else if m.liveTask == nil {
		// Once a task card is up it is the focus; the thought that led to the
		// delegation has already been committed to scrollback.
		if tv := m.thoughtView(); tv != "" {
			parts = append(parts, tv)
		}
	}
	if m.liveTask != nil {
		parts = append(parts, m.liveTask.renderLive(m.th, m.loc, m.sp.View(), time.Now()))
	}
	return strings.Join(parts, "\n")
}

// thoughtView is the live reasoning preview: a dim moving one-liner while folded,
// the full running thought while expanded.
func (m tuiModel) thoughtView() string {
	if len(m.thought) == 0 {
		return ""
	}
	star := m.th.glyph("✻", "*")
	if !m.expandThought {
		return m.th.muted.Render(star+" "+i18n.T(m.loc, "tui.thinking")) +
			m.th.muted.Render(" · "+truncate(firstNonEmptyTail(m.thought), 60))
	}
	var sb strings.Builder
	sb.WriteString(m.th.muted.Render(star + " " + i18n.T(m.loc, "tui.thinking")))
	for _, line := range m.thought {
		sb.WriteString("\n" + m.th.italic.Render("  "+line))
	}
	return sb.String()
}

// statusLine is the spinner row: the animated glyph, the verb, the current phase
// note, the elapsed clock and the interrupt hint — the TUI's equivalent of the
// classic cliui.Status footer.
func (m tuiModel) statusLine() string {
	// The spinner frames carry no trailing space of their own, so the space
	// belongs here — without it the line renders as "⠙思考中".
	parts := []string{m.sp.View() + " " + m.th.accent.Render(statusVerb(m.loc))}
	if m.note != "" {
		parts = append(parts, m.th.muted.Render("· "+m.note))
	}
	parts = append(parts, m.th.muted.Render(fmt.Sprintf("(%s · %s)",
		elapsed(time.Since(m.started)), i18n.T(m.loc, "cli.status.interrupt"))))
	return strings.Join(parts, " ")
}

// inputView is the bottom rounded input box plus the state and key-hint lines
// around it. The slash-command menu sits directly above the box, so the filtered
// command list reads like a completion popup anchored to the prompt.
func (m tuiModel) inputView() string {
	var parts []string
	if ctx := m.contextLine(); ctx != "" {
		parts = append(parts, ctx)
	}
	if menu := m.menu.render(m.th, m.textWidth()); menu != "" {
		parts = append(parts, menu)
	}
	parts = append(parts,
		m.th.inputBox.Width(m.textWidth()+2).Render(m.ta.View()),
		m.hintLine())
	return strings.Join(parts, "\n")
}

// contextLine is the dim state row above the input box: which thread the next
// prompt joins — a /resume'd session, or this run's bare chat — and whether
// tier-2 authorization is standing open. It shows only the state that changes
// what a prompt will do, which is the TUI's read of the classic footer.
func (m tuiModel) contextLine() string {
	if m.r == nil {
		return ""
	}
	sess := i18n.T(m.loc, "tui.ctx.bare")
	switch {
	case m.r.activeSess != "":
		sess = i18n.Tf(m.loc, "tui.ctx.session", "id", shortID(m.r.activeSess))
	case len(m.r.convo)/2 > 0:
		sess = i18n.Tf(m.loc, "tui.ctx.turns", "n", strconv.Itoa(len(m.r.convo)/2))
	}
	parts := []string{sess}
	// Which project the next prompt belongs to is state that changes what an ask
	// does — the task lands in that project and runs in its tree — so it belongs
	// on the same line as the thread.
	if proj := m.r.activeProjectName(); proj != "" {
		parts = append(parts, m.th.glyph("▪", "#")+" "+proj)
	}
	if m.r.authorize {
		parts = append(parts, i18n.T(m.loc, "repl.footer.authz")+":"+i18n.T(m.loc, "repl.footer.authz.on"))
	}
	return m.th.muted.Render(strings.Join(parts, "  "+m.th.glyph("·", "|")+"  "))
}

// hintLine is the dim key legend under the input box. It sheds hints rather than
// wrapping: the legend used to be printed whole, so a narrow terminal cut it
// mid-word ("… ctrl+o 思维链  ·  ctr") and spent a second screen row doing it.
// Submit and quit are the two a user cannot afford to lose — how to send, how to
// leave — so the middle two go first, in reverse order of usefulness.
func (m tuiModel) hintLine() string {
	sep := "  " + m.th.glyph("·", "|") + "  "
	hints := []string{
		i18n.T(m.loc, "tui.hint.submit"),
		i18n.T(m.loc, "tui.hint.newline"),
		i18n.T(m.loc, "tui.hint.thought"),
		i18n.T(m.loc, "tui.hint.quit"),
	}
	fits := func() bool {
		return cliui.DisplayWidth(strings.Join(hints, sep)) <= m.textWidth()
	}
	for _, drop := range []int{2, 1} { // thought, then newline
		if fits() {
			break
		}
		hints = append(hints[:drop], hints[drop+1:]...)
	}
	line := strings.Join(hints, sep)
	if !fits() {
		// Two hints and still too narrow: clip, so the legend can never claim a
		// second row from the input box.
		line = cliui.Truncate(line, m.textWidth(), m.th.unicode)
	}
	return m.th.muted.Render(line)
}

// approvalCard renders the tier-2 consent prompt for a parked task: what it
// wants to do, why the executor refused, and the y/n choice.
func (m tuiModel) approvalCard() string {
	req := m.pending.Approval
	var sb strings.Builder
	sb.WriteString(m.th.warn.Render(m.th.glyph("⚠", "!") + " " + i18n.T(m.loc, "repl.approval.head")))
	sb.WriteString("\n" + i18n.Tf(m.loc, "repl.approval.task", "title", req.Title))
	if reason := strings.TrimSpace(req.Reason); reason != "" {
		sb.WriteString("\n" + m.th.muted.Render(i18n.Tf(m.loc, "repl.approval.reason", "reason", reason)))
	}
	sb.WriteString("\n\n" + m.th.command.Render("[y]") + " " + i18n.T(m.loc, "tui.approval.yes") +
		"   " + m.th.command.Render("[n]") + " " + i18n.T(m.loc, "tui.approval.no"))
	return m.th.approval.Width(m.textWidth()).Render(sb.String())
}

// welcome is the startup banner pushed into scrollback: the wordmark, version,
// node/model, working directory and orientation tips, framed in brand green.
func (m tuiModel) welcome() string {
	model := i18n.T(m.loc, "repl.banner.noModel")
	if m.r.cfg.Model.BaseURL != "" {
		model = m.r.cfg.Model.Model
		if model == "" {
			model = m.r.cfg.Model.BaseURL
		}
	}
	// Each line is clipped to the frame rather than left to wrap: a wrapped
	// banner grows the box a row at a time as the terminal narrows, and a path
	// broken across two lines is harder to read than an elided one. The working
	// directory keeps its tail — the last segments are what identifies it.
	w := m.textWidth()
	uni := m.th.unicode
	var sb strings.Builder
	sb.WriteString(m.th.heading.Render(cliui.Truncate(i18n.T(m.loc, "repl.banner.title")+" v"+version, w, uni)))
	sb.WriteString("\n" + m.th.muted.Render(cliui.Truncate(
		i18n.Tf(m.loc, "repl.banner.node", "node", m.r.cfg.Node.Name, "model", model), w, uni)))
	sb.WriteString("\n" + m.th.muted.Render(cliui.TruncateTail(
		i18n.Tf(m.loc, "repl.banner.dir", "dir", m.r.cfg.Storage.WorkPath), w, uni)))
	sb.WriteString("\n" + m.th.muted.Render(cliui.Truncate(i18n.T(m.loc, "tui.welcome.tips"), w, uni)))
	return m.th.welcome.Width(w + 2).Render(sb.String())
}

// textWidth is the usable content width inside the frame (terminal minus the
// border+padding), floored so a very narrow terminal still renders.
func (m tuiModel) textWidth() int {
	if m.width <= 0 {
		return 76
	}
	return max(20, m.width-4)
}

// elapsed formats a duration as a compact clock for the status line. A
// sub-second duration prints "<1s" rather than "0s": routing decisions finish
// in milliseconds, and "0s" reads as a broken timer instead of a fast stage.
func elapsed(d time.Duration) string {
	s := int(d.Seconds())
	if s < 1 {
		return "<1s"
	}
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm%02ds", s/60, s%60)
}

// firstNonEmptyTail returns the last non-empty thought line — the thought still
// being written — for the folded live preview.
func firstNonEmptyTail(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return s
		}
	}
	return ""
}
