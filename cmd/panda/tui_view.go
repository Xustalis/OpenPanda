//go:build !lite

package main

// View renders the ephemeral region: the live turn (streaming answer or thought
// preview) with its spinner status while asking, the approval card while
// approving, and the bottom rounded input box while idle. Committed turns are
// not re-rendered here — they live in the terminal's scrollback (tea.Println),
// which is why quitting leaves the whole conversation on screen.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	versionpkg "github.com/Xustalis/OpenPanda/internal/version"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func (m tuiModel) View() string {
	if m.quitting {
		return ""
	}
	if m.mode == modeExec {
		var live strings.Builder
		live.WriteString("\n")
		live.WriteString(m.th.accent.Render(i18n.T(m.loc, "tui.exec.running")))
		if output := m.execText.String(); strings.TrimSpace(output) != "" {
			live.WriteString("\n")
			live.WriteString(output)
		}
		live.WriteString("\n")
		live.WriteString(m.th.muted.Render(i18n.T(m.loc, "tui.exec.cancel")))
		return m.mainChatView(live.String())
	}
	switch m.mode {
	case modeSplash:
		return m.splashView()
	case modeList:
		return m.listView()
	case modeModelPanel:
		return m.modelPanelView()
	case modeModelWizard:
		return m.modelWizardView()
	case modeOnboarding:
		return m.onboardingView()
	case modeSkillsHub:
		return m.skillsHubView()
	case modeAsking:
		// Render live region (task progress card or streaming answer + spinner)
		// followed immediately by the interactive input box so the user can type steering
		// ideas or stop the task at any time.
		var live string
		if m.liveTask != nil {
			live = "\n" + m.liveRegion()
		} else if lr := m.liveRegion(); lr != "" {
			live = "\n" + lr + "\n" + m.statusLine()
		} else {
			live = "\n" + m.statusLine()
		}
		return m.mainChatView(live)
	case modeApproving:
		return m.mainChatView("\n" + m.approvalCard())
	default:
		return m.mainChatView("")
	}
}

// mainChatView renders the full-screen chat interface in AltScreen mode:
//   - The welcome banner (OpenPanda ASCII art wordmark, version, node/model, workdir, tips)
//   - Committed conversation history blocks
//   - In-flight live output (streaming response, reasoning, task progress card, spinner)
//   - Vertical padding so the input box stays anchored at the bottom of the screen
//   - Interactive rounded input box and status row
func (m tuiModel) mainChatView(live string) string {
	w := m.width
	h := m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}

	inputView := m.inputView()
	inputLines := strings.Split(inputView, "\n")
	inputHeight := len(inputLines)
	availHeight := h - inputHeight
	if availHeight < 0 {
		availHeight = 0
	}

	var contentLines []string

	// 1. Welcome banner at top
	banner := m.welcome()
	if banner != "" {
		contentLines = append(contentLines, strings.Split(banner, "\n")...)
	}

	// 2. Committed conversation turns
	if m.chatHistory != nil && len(m.chatHistory.blocks) > 0 {
		for _, b := range m.chatHistory.blocks {
			rendered := b.render(m.th, m.textWidth(), m.expandThought)
			contentLines = append(contentLines, "")
			contentLines = append(contentLines, strings.Split(rendered, "\n")...)
		}
	}

	// 3. In-flight live region (streaming answer, reasoning preview, task progress card, spinner)
	if strings.TrimSpace(live) != "" {
		contentLines = append(contentLines, strings.Split(live, "\n")...)
	}

	totalContent := len(contentLines)

	// 4. Pad blank lines so inputView stays anchored at the bottom of the screen
	if totalContent <= availHeight {
		// Nothing overflows, so there is nothing to scroll: republish 0 rather
		// than leaving a taller frame's ceiling behind, which would let a stale
		// limit admit an offset this content cannot honour.
		m.publishScrollLimit(0)
		pad := availHeight - totalContent
		var out []string
		out = append(out, contentLines...)
		for i := 0; i < pad; i++ {
			out = append(out, "")
		}
		out = append(out, inputLines...)
		return clipRendered(strings.Join(out, "\n"), w)
	}

	// 5. If content exceeds available height, apply scrollOffset (0 means anchored at bottom)
	maxScroll := totalContent - availHeight
	m.publishScrollLimit(maxScroll)
	scroll := m.scrollOffset
	if scroll < 0 {
		scroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}

	end := totalContent - scroll
	start := end - availHeight
	if start < 0 {
		start = 0
	}
	visibleLines := contentLines[start:end]

	var out []string
	if w > 10 {
		// Attach a visual vertical scrollbar on the right boundary so users clearly see
		// that history exists and can gauge their scroll position.
		thumbHeight := max(1, availHeight*availHeight/totalContent)
		thumbTop := 0
		if maxScroll > 0 {
			thumbTop = (start * (availHeight - thumbHeight)) / maxScroll
		}
		thumbBottom := thumbTop + thumbHeight
		contentWidth := w - 1
		for i, line := range visibleLines {
			lw := ansi.StringWidth(line)
			if lw > contentWidth {
				line = ansi.Truncate(line, contentWidth, "")
				lw = ansi.StringWidth(line)
			}
			pad := contentWidth - lw
			if pad < 0 {
				pad = 0
			}
			var barChar string
			if i >= thumbTop && i < thumbBottom {
				barChar = m.th.accent.Render(m.th.glyph("█", "#"))
			} else {
				barChar = m.th.muted.Render(m.th.glyph("│", "|"))
			}
			out = append(out, line+strings.Repeat(" ", pad)+barChar)
		}
	} else {
		out = append(out, visibleLines...)
	}
	out = append(out, inputLines...)
	return clipRendered(strings.Join(out, "\n"), w)
}

// publishScrollLimit records the largest useful scroll offset for the frame just
// rendered. View is the only place that knows the content's real height, and it
// is a value method that cannot write the model's offset back — so the ceiling
// travels through a pointer, and scrollTranscript clamps against it. Without
// this the offset grows without bound past the top and the next scroll-downs do
// nothing visible until it unwinds.
func (m tuiModel) publishScrollLimit(maxScroll int) {
	if m.scrollLimit != nil {
		*m.scrollLimit = max(0, maxScroll)
	}
}

// splashView renders the centered full-screen startup overlay.
func (m tuiModel) splashView() string {
	w := m.width
	h := m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}

	var blockLines []string
	if w >= 76 {
		for _, line := range figlet("OpenPanda") {
			blockLines = append(blockLines, m.th.accent.Render(line))
		}
	} else {
		blockLines = append(blockLines, m.th.accent.Render("=== OpenPanda ==="))
	}
	blockLines = append(blockLines, "")
	blockLines = append(blockLines, m.th.heading.Render("  v"+version))

	model := ""
	nodeName := ""
	workPath := ""
	if m.r != nil && m.r.cfg != nil {
		nodeName = m.r.cfg.Node.Name
		workPath = m.r.cfg.Storage.WorkPath
		if m.r.cfg.Model.BaseURL != "" {
			model = m.r.cfg.Model.Model
			if model == "" {
				model = m.r.cfg.Model.BaseURL
			}
		}
	} else {
		workPath, _ = os.Getwd()
	}

	if nodeName != "" || model != "" {
		nodeLabel := "  " + m.th.glyph("▪", "#") + " " + i18n.Tf(m.loc, "repl.banner.node", "node", nodeName, "model", model)
		blockLines = append(blockLines, m.th.muted.Render(cliui.Truncate(nodeLabel, max(20, w-8), m.th.unicode)))
	}

	dirLabel := "  " + m.th.glyph("▫", "-") + " " + i18n.Tf(m.loc, "tui.banner.workdir", "dir", workPath)
	blockLines = append(blockLines, m.th.muted.Render(cliui.TruncateTail(dirLabel, max(20, w-8), m.th.unicode)))

	blockLines = append(blockLines, "")
	blockLines = append(blockLines, m.th.muted.Render(cliui.Truncate(i18n.T(m.loc, "tui.welcome.tips"), max(20, w-8), m.th.unicode)))

	centerContent := strings.Join(blockLines, "\n")
	bottomPrompt := m.th.muted.Render(i18n.T(m.loc, "tui.splash.prompt"))

	centerRendered := lipgloss.PlaceHorizontal(w, lipgloss.Center, centerContent)
	centerLines := strings.Split(centerRendered, "\n")

	padTop := max(0, (h-len(centerLines)-3)/2)
	var out []string
	for i := 0; i < padTop; i++ {
		out = append(out, "")
	}
	out = append(out, centerLines...)
	for len(out) < h-2 {
		out = append(out, "")
	}
	out = append(out, lipgloss.PlaceHorizontal(w, lipgloss.Center, bottomPrompt))
	for len(out) < h {
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}

// listView renders the full-screen selection list for /sessions, /projects, /resume.
func (m tuiModel) listView() string {
	return m.selectionList.Render(m.th, m.width, m.height)
}

// modelWizardView routes the model setup screens: the provider pick stays a
// SelectionList; everything past it is the single-screen form editor (or its
// fetched-models picker overlay) in tui_modelcfg.go.
func (m tuiModel) modelWizardView() string {
	w := m.width
	h := m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	if m.wizardStep == wizardStepProvider || m.formPicking {
		return m.selectionList.Render(m.th, w, h)
	}
	return m.modelFormView()
}

// onboardingView renders the initial first-run onboarding steps.
func (m tuiModel) onboardingView() string {
	w := m.width
	h := m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}

	switch m.onboardingStep {
	case onboardingStepLanguage:
		return m.selectionList.Render(m.th, w, h)
	case onboardingStepTerms:
		return m.onboardingTermsView(w, h)
	case onboardingStepApproval:
		return m.selectionList.Render(m.th, w, h)
	case onboardingStepModelChoice:
		return m.selectionList.Render(m.th, w, h)
	case onboardingStepModelWizard:
		return m.modelWizardView()
	default:
		return m.selectionList.Render(m.th, w, h)
	}
}

// onboardingTermsView renders the rich terms of service & license agreement card.
func (m tuiModel) onboardingTermsView(w, h int) string {
	boxWidth := min(max(50, w-4), 86)

	var lines []string
	isZh := m.loc == i18n.ChineseSimp

	if isZh {
		lines = append(lines, m.th.heading.Render("⚖️  OpenPanda 开源许可与服务条款协议"))
		lines = append(lines, m.th.muted.Render(strings.Repeat("─", boxWidth-4)))
		lines = append(lines, "")
		lines = append(lines, m.th.accent.Bold(true).Render("1. MIT 开源许可证声明 (MIT Open Source License)"))
		lines = append(lines, "   OpenPanda 遵循 MIT 协议开源发布。您拥有完全且自由的权利在个人、")
		lines = append(lines, "   学术或商业项目中运行、复制、修改、分发及二次开发本软件。")
		lines = append(lines, "")
		lines = append(lines, m.th.accent.Bold(true).Render("2. 本地优先与数据自治原则 (Local-First & Privacy Autonomy)"))
		lines = append(lines, "   开发者的隐私与数据主权是我们的根本基石。OpenPanda 恪守本地优先原则，")
		lines = append(lines, "   您的源代码、工作区文件、对话历史与 API 密钥均严格保存于本地设备。")
		lines = append(lines, "   绝无任何未经授权的遥测、用户追踪或窃取私有资产的代码逻辑。")
		lines = append(lines, "")
		lines = append(lines, m.th.accent.Bold(true).Render("3. AI 生成内容免责声明 (AI Generation Disclaimer)"))
		lines = append(lines, "   大语言模型生成的所有代码变更、终端命令及分析均基于概率生成，")
		lines = append(lines, "   可能包含逻辑缺陷、幻觉或安全隐患。在应用于生产环境或关键系统前，")
		lines = append(lines, "   请务必进行充分的人工审查与测试验证。运行生成结果的一切风险由使用者承担。")
		lines = append(lines, "")
		lines = append(lines, m.th.accent.Bold(true).Render("4. 命令执行与系统安全须知 (Execution & System Safety)"))
		lines = append(lines, "   OpenPanda 具备通过终端执行系统命令及修改磁盘文件的能力。虽然系统提供")
		lines = append(lines, "   多层级确认机制，在执行不可逆的破坏性命令或修改前，请务必审慎核验。")
		lines = append(lines, "")
		lines = append(lines, m.th.muted.Render(strings.Repeat("─", boxWidth-4)))

		// Options
		var optAgree, optDecline string
		if m.termsCursor == 0 {
			optAgree = m.th.accent.Bold(true).Render("> [Y] 同意并继续 (Agree and Continue)")
			optDecline = m.th.muted.Render("  [N] 拒绝并退出 (Decline and Exit)")
		} else {
			optAgree = m.th.muted.Render("  [Y] 同意并继续 (Agree and Continue)")
			optDecline = m.th.warn.Bold(true).Render("> [N] 拒绝并退出 (Decline and Exit)")
		}
		lines = append(lines, fmt.Sprintf("  %s      %s", optAgree, optDecline))
		lines = append(lines, "")
		lines = append(lines, m.th.muted.Render("  Y 同意 · N/Esc 拒绝 · ↑↓/←→ 选择 · Enter 确认"))
	} else {
		lines = append(lines, m.th.heading.Render("⚖️  OpenPanda Terms of Service & License Agreement"))
		lines = append(lines, m.th.muted.Render(strings.Repeat("─", boxWidth-4)))
		lines = append(lines, "")
		lines = append(lines, m.th.accent.Bold(true).Render("1. MIT Open Source License"))
		lines = append(lines, "   OpenPanda is free software licensed under the MIT License. You have")
		lines = append(lines, "   full rights to run, modify, distribute, and build on it for personal,")
		lines = append(lines, "   academic, or commercial use without royalty fees.")
		lines = append(lines, "")
		lines = append(lines, m.th.accent.Bold(true).Render("2. Local-First Architecture & Privacy Autonomy"))
		lines = append(lines, "   Your privacy and source code confidentiality are foundational. OpenPanda")
		lines = append(lines, "   operates strictly locally. Your source code, workspace contexts, chat")
		lines = append(lines, "   history, and API credentials remain on your device with no unauthorized")
		lines = append(lines, "   telemetry or data exfiltration.")
		lines = append(lines, "")
		lines = append(lines, m.th.accent.Bold(true).Render("3. AI Generation Advisory & Risk Disclaimer"))
		lines = append(lines, "   All code edits, shell commands, and suggestions are probabilistically")
		lines = append(lines, "   generated by AI models and may contain hallucinations, bugs, or flaws.")
		lines = append(lines, "   Always inspect and test generated actions before running them in")
		lines = append(lines, "   production. You assume full responsibility for running AI suggestions.")
		lines = append(lines, "")
		lines = append(lines, m.th.accent.Bold(true).Render("4. System Execution & Operational Safety"))
		lines = append(lines, "   OpenPanda has capabilities to execute terminal commands and modify files.")
		lines = append(lines, "   While approval guardrails are provided, always exercise discretion and")
		lines = append(lines, "   review destructive operations (such as file deletion or system calls).")
		lines = append(lines, "")
		lines = append(lines, m.th.muted.Render(strings.Repeat("─", boxWidth-4)))

		// Options
		var optAgree, optDecline string
		if m.termsCursor == 0 {
			optAgree = m.th.accent.Bold(true).Render("> [Y] Agree and Continue")
			optDecline = m.th.muted.Render("  [N] Decline and Exit")
		} else {
			optAgree = m.th.muted.Render("  [Y] Agree and Continue")
			optDecline = m.th.warn.Bold(true).Render("> [N] Decline and Exit")
		}
		lines = append(lines, fmt.Sprintf("  %s      %s", optAgree, optDecline))
		lines = append(lines, "")
		lines = append(lines, m.th.muted.Render("  Y Agree · N/Esc Decline · ↑↓/←→ Select · Enter Confirm"))
	}

	cardContent := strings.Join(lines, "\n")
	boxed := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("35")).
		Padding(0, 1).
		Width(boxWidth).
		Render(cardContent)

	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, boxed)
}

// liveRegion is what the in-flight turn shows: the streaming answer once prose
// has started, otherwise the live chain-of-thought (folded to a moving one-liner,
// or expanded under Ctrl+O), plus the delegated-task card when the turn was
// classified as a task. Reasoning is display-only (D14).
func (m tuiModel) liveRegion() string {
	var parts []string
	if ans := m.liveAnswerText(); strings.TrimSpace(ans) != "" {
		text := answerText(m.th, ans, m.textWidth())
		if m.height > 10 {
			lines := strings.Split(text, "\n")
			maxLines := max(4, m.height-10)
			if len(lines) > maxLines {
				lines = lines[len(lines)-maxLines:]
				text = strings.Join(lines, "\n")
			}
		}
		parts = append(parts, text)
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
	// A slash-mode turn announces its lens next to the verb — "goal mode"
	// makes it obvious why the model is refining rather than doing.
	if m.turnMode != "" {
		parts = append(parts, m.th.warn.Render("["+i18n.T(m.loc, "tui.mode.badge."+m.turnMode)+"]"))
	}
	if m.note != "" {
		parts = append(parts, m.th.muted.Render("· "+m.note))
	}
	parts = append(parts, m.th.muted.Render(fmt.Sprintf("(%s · %s)",
		elapsed(time.Since(m.started)), i18n.T(m.loc, "cli.status.interrupt"))))
	return strings.Join(parts, " ")
}

// inputView is the rounded input box with one dim status row under it and a blank
// line above, so the control reads as separate from the transcript instead of
// abutting the last answer.
func (m tuiModel) inputView() string {
	boxStyle := m.th.inputBox
	if m.mode == modeAsking {
		boxStyle = m.th.inputBoxRunning.BorderForeground(m.th.breathingColor(m.animTick))
	}
	// A border plus horizontal padding needs at least five cells. Below that,
	// render a clipped bare editor and footer: terminal geometry is authoritative.
	if m.textWidth() < 5 {
		return clipRendered(m.ta.View(), m.textWidth()) + "\n" + clipRendered(m.statusRow(), m.textWidth())
	}
	rows := []string{
		"", // the blank line every committed block gets above it
	}
	// Lip Gloss Width includes padding but adds borders afterward. textWidth is the
	// actual terminal-bounded frame width, so reserve the two border cells here.
	rows = append(rows, boxStyle.Width(max(1, m.textWidth()-2)).Render(m.ta.View()))
	// The list is capped to what the window can spare: an inline renderer repaints
	// by counting rows back up from the cursor, so a frame taller than the terminal
	// scrolls its own top away and every later repaint lands in the wrong place.
	if menu := m.menu.render(m.th, m.textWidth(), m.menuRows()); menu != "" {
		rows = append(rows, menu)
	}
	return strings.Join(append(rows, m.statusRow()), "\n")
}

// menuRows is how many command rows the popup may draw: whatever the terminal has
// left under the blank line, the three-row box and the footer, held to a handful
// so the list stays scannable on a tall screen. The floor keeps a very short
// window showing something rather than nothing.
func (m tuiModel) menuRows() int {
	const maxRows = 8
	if m.height <= 0 {
		return maxRows // size not reported yet (see Init)
	}
	return max(3, min(maxRows, m.height-6))
}

// statusRow is the dim footer under the box: the key legend on the left, the
// state that decides what the next prompt does on the right. It never wraps —
// the legend sheds hints until both halves fit, and if even that is not enough
// the state keeps the row, because a hint can be found again in /help and "which
// project am I in" cannot.
func (m tuiModel) statusRow() string {
	w := m.textWidth()
	state, hints := m.contextLine(), m.hintLine()
	switch {
	case state == "":
		if m.mode == modeAsking {
			return hints
		}
		return m.th.muted.Render(hints)
	case hints == "":
		return m.th.muted.Render(cliui.Truncate(state, w, m.th.unicode))
	}
	gap := w - cliui.DisplayWidth(hints) - cliui.DisplayWidth(state)
	if gap < 2 {
		return m.th.muted.Render(cliui.Truncate(state, w, m.th.unicode))
	}
	if m.mode == modeAsking {
		return hints + strings.Repeat(" ", gap) + state
	}
	return m.th.muted.Render(hints + strings.Repeat(" ", gap) + state)
}

// contextLine is the state half of the status row: which thread the next prompt
// joins — a /resume'd session, or this run's bare chat — which project it lands
// in, and whether tier-2 authorization is standing open. It shows only the state
// that changes what a prompt will do, which is the TUI's read of the classic
// footer. It returns plain text; statusRow paints the whole row at once, because
// the row's width arithmetic has to measure columns, not escape sequences.
func (m tuiModel) contextLine() string {
	if m.mode == modeAsking {
		dur := elapsed(time.Since(m.started))
		tag := lipgloss.NewStyle().Bold(true).Foreground(m.th.breathingColor(m.animTick)).
			Render(m.th.glyph("⚡", "[!]") + " " + i18n.T(m.loc, "tui.status.running") + " · " + dur)
		if m.projName != "" {
			return tag + "  " + m.th.glyph("·", "|") + "  " + m.th.muted.Render(m.th.glyph("▪", "#")+" "+m.projName)
		}
		return tag
	}
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
	// on the same line as the thread. The name comes from the cache, not the
	// store: this runs on every frame, including every cursor blink.
	if m.projName != "" {
		parts = append(parts, m.th.glyph("▪", "#")+" "+m.projName)
	}
	if m.r.authorize {
		parts = append(parts, i18n.T(m.loc, "repl.footer.authz")+":"+i18n.T(m.loc, "repl.footer.authz.on"))
	}
	return strings.Join(parts, "  "+m.th.glyph("·", "|")+"  ")
}

// hintLine is the key legend for the status row, in plain text. It sheds hints
// rather than wrapping: the legend used to be printed whole, so a narrow terminal
// cut it mid-word ("… ctrl+o 思维链  ·  ctr") and spent a second screen row doing
// it. Submit and quit are the two a user cannot afford to lose — how to send, how
// to leave — so the others go first, most expendable first: the thought fold,
// then the newline, then the mouse toggle.
//
// The mouse hint outlives the other two not because it matters more but because
// it is the one a user cannot rediscover by guessing: /help lists commands, not
// input devices. The mode change is also announced in the transcript, so the
// hint is a convenience rather than the only way to learn the key.
//
// The state half of the row is measured out of the budget before the legend gets
// any of it, and below a floor the legend yields the row entirely: a squeezed
// hint is worth less than the project the next prompt would land in.
func (m tuiModel) hintLine() string {
	if m.scrollOffset > 0 {
		return m.th.warn.Bold(true).Render(i18n.Tf(m.loc, "tui.scroll.hint", "offset", strconv.Itoa(m.scrollOffset)))
	}
	budget := m.textWidth()
	if state := m.contextLine(); state != "" {
		budget -= cliui.DisplayWidth(state) + 2
	}
	if budget < 12 {
		return ""
	}
	sep := "  " + m.th.glyph("·", "|") + "  "
	hints := m.hintKeys()
	if m.mode == modeAsking {
		return strings.Join(hintTexts(hints), " ")
	}
	hints = shedHints(hints, sep, budget)
	line := strings.Join(hintTexts(hints), sep)
	if cliui.DisplayWidth(line) > budget {
		// Only the protected entries are left and they still do not fit: clip,
		// so the legend can never claim a second row from the input box.
		line = cliui.Truncate(line, budget, m.th.unicode)
	}
	return line
}

// shedHints drops the most expendable entries until the joined legend fits
// budget, never dropping one marked shed 0 (submit and quit — how to send and
// how to leave).
//
// Selection is by priority rather than by position, so the same call describes
// every legend regardless of length. The loop it replaced dropped fixed indices
// sized for the five-entry chat legend; on the four-entry slash-menu legend its
// third drop was skipped by a bounds guard, so the two lists silently ran
// different policies than the comment claimed.
func shedHints(hints []hint, sep string, budget int) []hint {
	fits := func(hs []hint) bool {
		return cliui.DisplayWidth(strings.Join(hintTexts(hs), sep)) <= budget
	}
	for !fits(hints) {
		worst := -1
		for i, h := range hints {
			if h.shed == 0 {
				continue
			}
			if worst < 0 || h.shed > hints[worst].shed {
				worst = i
			}
		}
		if worst < 0 {
			break // only the protected entries remain
		}
		hints = append(hints[:worst], hints[worst+1:]...)
	}
	return hints
}

// hint is one legend entry. shed orders the entries under pressure: the highest
// shed value is dropped first, and 0 means "never drop" — submit and quit are
// the two a user cannot afford to lose.
//
// Ordering by a per-entry priority rather than by list position is what keeps
// the policy honest across legends of different lengths: the chat legend has
// five entries and the slash-menu legend four, so one shared list of indices
// could not describe both (the old loop's third drop was silently skipped for
// the shorter one, leaving the two lists on different policies than the comment
// claimed).
type hint struct {
	text string
	shed int
}

// hintTexts strips the shedding priorities, for callers that only lay the
// entries out.
func hintTexts(hints []hint) []string {
	out := make([]string, len(hints))
	for i, h := range hints {
		out[i] = h.text
	}
	return out
}

// hintKeys is the legend's content: what the keys do at this moment. The slash
// menu rebinds enter, tab, the arrows and esc while it is open, so it brings its
// own legend — leaving "ctrl+j 换行" over a list where enter runs the highlighted
// command would describe a keyboard the user does not currently have. Both lists
// are ordered action-first and escape-last, and both shed their most expendable
// entries first under the same budget (see hintLine).
func (m tuiModel) hintKeys() []hint {
	if m.mode == modeAsking {
		return []hint{
			{text: m.th.stopButton().Render(m.th.glyph("⏹", "[x]") + " Esc " + i18n.T(m.loc, "tui.hint.stop"))},
			{text: m.th.steerButton().Render(m.th.glyph("⏎", "[>]") + " Enter " + i18n.T(m.loc, "tui.hint.steer"))},
			{text: m.th.thoughtButton().Render("⌃O " + i18n.T(m.loc, "tui.hint.thought"))},
		}
	}
	if m.menu.active && len(m.menu.items) > 0 {
		return []hint{
			{text: i18n.T(m.loc, "tui.hint.menuRun")},
			{text: m.th.glyph("↑↓", "^v") + " " + i18n.T(m.loc, "tui.hint.menuSelect"), shed: 1},
			{text: i18n.T(m.loc, "tui.hint.menuComplete"), shed: 2},
			{text: i18n.T(m.loc, "tui.hint.menuCancel")},
		}
	}
	return []hint{
		{text: i18n.T(m.loc, "tui.hint.submit")},
		{text: i18n.T(m.loc, "tui.hint.newline"), shed: 2},
		{text: i18n.T(m.loc, "tui.hint.thought"), shed: 3},
		{text: m.mouseHint(), shed: 1},
		{text: i18n.T(m.loc, "tui.hint.quit")},
	}
}

// mouseHint is the legend entry for ctrl+t. It names what the key does *from
// here*, not what the modes are called: in mouseScroll it offers selection, and
// in mouseSelect (the default) it is the way back to clicking the buttons. It
// sits next to quit because it is the one hint a user cannot find again by
// guessing — /help lists commands, not input devices.
func (m tuiModel) mouseHint() string {
	if m.mouse.captured() {
		return i18n.T(m.loc, "tui.hint.mouseScroll")
	}
	return i18n.T(m.loc, "tui.hint.mouseSelect")
}

// approvalCard renders the tier-2 consent prompt for a parked task: what it
// wants to do, why the executor refused, and the y/n choice. The focused
// choice is accented and marker-prefixed so arrows + Enter read as a picker
// while the [y]/[n] labels keep the hotkeys discoverable.
// approvalCardLayout is the final rendered card plus the exact terminal cells
// occupied by its two choices. Rendering and hit testing consume this one layout,
// so localization, resize, padding, or border changes cannot leave stale hitboxes.
type approvalCardLayout struct {
	rendered string
	yes      tuiRect
	no       tuiRect
}

func (m tuiModel) approvalLayout() approvalCardLayout {
	req := m.pending.Approval
	var sb strings.Builder
	sb.WriteString(m.th.warn.Render(m.th.glyph("⚠", "!") + " " + i18n.T(m.loc, "repl.approval.head")))
	sb.WriteString("\n" + i18n.Tf(m.loc, "repl.approval.task", "title", req.Title))
	if reason := strings.TrimSpace(req.Reason); reason != "" {
		sb.WriteString("\n" + m.th.muted.Render(i18n.Tf(m.loc, "repl.approval.reason", "reason", reason)))
	}
	choice := func(focused int, key, label string) string {
		s := m.th.command.Render("["+key+"]") + " " + label
		if m.approvalSel == focused {
			return m.th.heading.Render(m.th.glyph("❯", ">")+" ") + s
		}
		return "  " + s
	}
	yesLabel := i18n.T(m.loc, "tui.approval.yes")
	noLabel := i18n.T(m.loc, "tui.approval.no")
	yesText := choice(0, "y", yesLabel)
	noText := choice(1, "n", noLabel)
	sb.WriteString("\n\n" + yesText)
	sb.WriteString("   " + noText)
	sb.WriteString("\n" + m.th.muted.Render(m.th.glyph("↑↓", "^v")+" "+i18n.T(m.loc, "tui.approval.hint")))

	rendered := m.th.approval.Width(max(1, m.textWidth()-2)).Render(sb.String())
	lines := strings.Split(rendered, "\n")
	choiceRow := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], "[y]") && strings.Contains(lines[i], "[n]") {
			choiceRow = i
			break
		}
	}
	layout := approvalCardLayout{rendered: rendered}
	if choiceRow < 0 {
		return layout
	}
	// Border + horizontal padding precede the rendered choice content. The
	// choices themselves are measured from the exact strings rendered above.
	const origin = 2
	yesWidth := lipgloss.Width(yesText)
	noWidth := lipgloss.Width(noText)
	layout.yes = tuiRect{x: origin, y: choiceRow, w: yesWidth, h: 1}
	layout.no = tuiRect{x: origin + yesWidth + 3, y: choiceRow, w: noWidth, h: 1}
	return layout
}

func (m tuiModel) approvalCard() string {
	if m.pending == nil || m.pending.Approval == nil {
		return ""
	}
	return m.approvalLayout().rendered
}

// tuiRect is a half-open rectangle in terminal cells.
type tuiRect struct {
	x, y int
	w, h int
}

func (r tuiRect) contains(x, y int) bool {
	return r.w > 0 && r.h > 0 && x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

// approvalHit maps a terminal click to an approval choice: 0 = approve,
// 1 = deny, -1 = not on either option. The card is bottom-anchored by
// mainChatView; approvalLayout supplies option cells relative to the rendered
// card, and this function only translates them into screen coordinates.
func (m tuiModel) approvalHit(x, y int) int {
	if m.height <= 0 || m.pending == nil || m.pending.Approval == nil {
		return -1
	}
	layout := m.approvalLayout()
	cardHeight := lipgloss.Height(layout.rendered)
	originY := max(0, m.height-cardHeight)
	yes := layout.yes
	no := layout.no
	yes.y += originY
	no.y += originY
	switch {
	case yes.contains(x, y):
		return 0
	case no.contains(x, y):
		return 1
	default:
		return -1
	}
}

// askingButtonRects derives each clickable footer cell from the final status row
// rendered by inputView. Missing or truncated buttons have no rectangle.
func (m tuiModel) askingButtonRects() []tuiRect {
	if m.mode != modeAsking || m.height <= 0 {
		return nil
	}
	hints := hintTexts(m.hintKeys())
	if len(hints) < 3 {
		return nil
	}
	row := m.statusRow()
	rowWidth := lipgloss.Width(row)
	if rowWidth == 0 {
		return nil
	}
	plain := ansi.Strip(row)
	buttons := make([]tuiRect, 3)
	searchByte := 0
	searchCol := 0
	for i, hint := range hints[:3] {
		needle := ansi.Strip(hint)
		rel := strings.Index(plain[searchByte:], needle)
		if rel < 0 {
			continue
		}
		prefix := plain[searchByte : searchByte+rel]
		x := searchCol + lipgloss.Width(prefix)
		w := lipgloss.Width(needle)
		buttons[i] = tuiRect{x: x, y: m.height - 1, w: w, h: 1}
		searchByte += rel + len(needle)
		searchCol = x + w
	}
	return buttons
}

// askingButtonHit maps a terminal click to an asking footer button:
// 0 = Stop, 1 = Steer/Inject, 2 = Thought, -1 = none.
func (m tuiModel) askingButtonHit(x, y int) int {
	for i, rect := range m.askingButtonRects() {
		if rect.contains(x, y) {
			return i
		}
	}
	return -1
}

// renderWelcomeBanner builds the unified startup/clear banner:
//   - If width >= 76: full 72-col ASCII brand wordmark in brandGreen
//   - If width < 76: compact single-line heading (✻ OpenPanda v...)
//   - Version, Node & Model, Working Directory
//   - Interactive tips (commands, file attach, help guide)
//   - A year of task activity as a heatmap, when a store was reachable —
//     the empty screen is the one place a grid can sit without competing
//     with a conversation. nil activity skips the section entirely.
func renderWelcomeBanner(cfg *config.Config, loc i18n.Locale, width int, th theme, activity map[string]int) string {
	if width <= 0 {
		width = 80
	}
	uni := th.unicode

	model := i18n.T(loc, "repl.banner.noModel")
	nodeName := ""
	workPath := ""
	if cfg != nil {
		nodeName = cfg.Node.Name
		workPath = cfg.Storage.WorkPath
		if cfg.Model.BaseURL != "" {
			model = cfg.Model.Model
			if model == "" {
				model = cfg.Model.BaseURL
			}
			if strings.TrimSpace(cfg.Model.APIKey) == "" {
				model += " · " + i18n.T(loc, "repl.banner.noKey")
			}
		}
	}

	contentWidth := max(20, width-2)
	var sb strings.Builder

	if width >= 76 {
		for i, line := range figlet("OpenPanda") {
			if i > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(th.accent.Render(line))
		}
		sb.WriteString("\n\n")
		sb.WriteString(th.heading.Render("  " + i18n.T(loc, "repl.banner.title") + " v" + version + " (" + versionpkg.Codename + ")"))
	} else {
		sb.WriteString(th.heading.Render(cliui.Truncate(
			"  "+th.glyph("✻", "*")+" "+i18n.T(loc, "repl.banner.title")+" v"+version+" ("+versionpkg.Codename+")", width, uni)))
	}

	sb.WriteString("\n")
	sb.WriteString(th.muted.Render("  " + cliui.Truncate(
		i18n.Tf(loc, "repl.banner.node", "node", nodeName, "model", model), contentWidth, uni)))
	sb.WriteString("\n")
	sb.WriteString(th.muted.Render("  " + cliui.TruncateTail(
		i18n.Tf(loc, "repl.banner.dir", "dir", workPath), contentWidth, uni)))
	sb.WriteString("\n\n")
	sb.WriteString(th.muted.Render("  " + cliui.Truncate(
		i18n.T(loc, "tui.welcome.tips"), contentWidth, uni)))

	if activity != nil {
		var hb strings.Builder
		renderTaskHeatmap(&hb, loc, activity, time.Now(), heatmapMaxWeek, contentWidth, pal())
		sb.WriteString("\n")
		for _, line := range strings.Split(strings.TrimRight(hb.String(), "\n"), "\n") {
			sb.WriteString("\n  " + line)
		}
	}

	if isLinuxConsole() {
		sb.WriteString("\n" + th.warn.Render("  ! bare console font has no CJK glyphs; answers are forced to English."))
		sb.WriteString("\n" + th.warn.Render("    for Chinese on this screen: sudo apt install fbterm fonts-wqy-zenhei && fbterm"))
	}

	return sb.String()
}

// welcome is the startup banner pushed into scrollback: the wordmark, version,
// node/model, working directory and orientation tips.
func (m tuiModel) welcome() string {
	w := m.width
	if w <= 0 {
		w = 80
	}
	cfg := (*config.Config)(nil)
	var activity map[string]int
	if m.r != nil {
		cfg = m.r.cfg
		activity = m.activity.get(m.r)
	}
	return renderWelcomeBanner(cfg, m.loc, w, m.th, activity)
}

// textWidth is the terminal-bounded width available to top-level TUI rows. A
// reported tiny width is authoritative: decoration must shed or clip rather than
// inventing columns beyond the physical screen.
func (m tuiModel) textWidth() int {
	if m.width <= 0 {
		return 76
	}
	return max(1, m.width)
}

func clipRendered(s string, width int) string {
	if width <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "")
	}
	return strings.Join(lines, "\n")
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
