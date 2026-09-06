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
	"github.com/Xustalis/OpenPanda/internal/providers"
	"github.com/charmbracelet/lipgloss"
)

func (m tuiModel) View() string {
	// modeExec draws nothing on purpose: a foreground command is about to own the
	// terminal, and whatever this frame drew would be stranded above its output.
	if m.quitting || m.mode == modeExec {
		return ""
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

	// 4. Pad blank lines so inputView stays anchored at the bottom of the screen
	if len(contentLines) <= availHeight {
		pad := availHeight - len(contentLines)
		var out []string
		out = append(out, contentLines...)
		for i := 0; i < pad; i++ {
			out = append(out, "")
		}
		out = append(out, inputLines...)
		return strings.Join(out, "\n")
	}

	// 5. If content exceeds available height, scroll to show the latest turns above the input box
	contentLines = contentLines[len(contentLines)-availHeight:]
	var out []string
	out = append(out, contentLines...)
	out = append(out, inputLines...)
	return strings.Join(out, "\n")
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

	dirLabel := "  " + m.th.glyph("▫", "-") + " 工作目录: " + workPath
	blockLines = append(blockLines, m.th.muted.Render(cliui.TruncateTail(dirLabel, max(20, w-8), m.th.unicode)))

	blockLines = append(blockLines, "")
	blockLines = append(blockLines, m.th.muted.Render(cliui.Truncate(i18n.T(m.loc, "tui.welcome.tips"), max(20, w-8), m.th.unicode)))

	centerContent := strings.Join(blockLines, "\n")
	bottomPrompt := m.th.muted.Render("Enter 开始 · Q 退出")

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

// modelPanelView renders the boxed model management panel.
func (m tuiModel) modelPanelView() string {
	return m.selectionList.Render(m.th, m.width, m.height)
}

// modelWizardView renders the step-by-step model setup guide.
func (m tuiModel) modelWizardView() string {
	w := m.width
	h := m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}

	if m.wizardStep == wizardStepProvider {
		return m.selectionList.Render(m.th, w, h)
	}

	var lines []string
	provLabel := m.wizardProvider
	if p, ok := providers.Lookup(m.wizardProvider); ok {
		provLabel = p.Label
	}

	lines = append(lines, m.th.heading.Render(fmt.Sprintf("添加模型 - %s", provLabel)))
	lines = append(lines, "")

	if m.wizardStep == wizardStepAPIKey {
		lines = append(lines, "请输入 API Key:")
		inputDisplay := m.wizardInput
		lines = append(lines, m.th.accent.Render("> ")+inputDisplay+m.th.accent.Render("█"))
		lines = append(lines, "")
		lines = append(lines, m.th.muted.Render("Enter 确认 · Esc 返回"))
	} else if m.wizardStep == wizardStepModelName {
		defModel := ""
		if p, ok := providers.Lookup(m.wizardProvider); ok {
			defModel = p.DefaultModel
		}
		prompt := "请输入模型名称:"
		if defModel != "" {
			prompt = fmt.Sprintf("请输入模型名称 (默认: %s):", defModel)
		}
		lines = append(lines, prompt)
		inputDisplay := m.wizardInput
		lines = append(lines, m.th.accent.Render("> ")+inputDisplay+m.th.accent.Render("█"))
		lines = append(lines, "")
		lines = append(lines, m.th.muted.Render("Enter 确认 · Esc 返回"))
	}

	content := strings.Join(lines, "\n")
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, content)
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
	if ans := m.liveAnswer; strings.TrimSpace(ans) != "" {
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
	rows := []string{
		"", // the blank line every committed block gets above it
	}
	rows = append(rows, boxStyle.Width(m.textWidth()+2).Render(m.ta.View()))
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
// to leave — so the middle two go first, in reverse order of usefulness.
//
// The state half of the row is measured out of the budget before the legend gets
// any of it, and below a floor the legend yields the row entirely: a squeezed
// hint is worth less than the project the next prompt would land in.
func (m tuiModel) hintLine() string {
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
		return strings.Join(hints, " ")
	}
	fits := func() bool {
		return cliui.DisplayWidth(strings.Join(hints, sep)) <= budget
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
		line = cliui.Truncate(line, budget, m.th.unicode)
	}
	return line
}

// hintKeys is the legend's content: what the keys do at this moment. The slash
// menu rebinds enter, tab, the arrows and esc while it is open, so it brings its
// own legend — leaving "ctrl+j 换行" over a list where enter runs the highlighted
// command would describe a keyboard the user does not currently have. Both lists
// are ordered action-first and escape-last, and both shed their middle two under
// the same budget (see hintLine).
func (m tuiModel) hintKeys() []string {
	if m.mode == modeAsking {
		return []string{
			m.th.stopButton().Render(m.th.glyph("⏹", "[x]") + " Esc " + i18n.T(m.loc, "tui.hint.stop")),
			m.th.steerButton().Render(m.th.glyph("⏎", "[>]") + " Enter " + i18n.T(m.loc, "tui.hint.steer")),
			m.th.thoughtButton().Render("⌃O " + i18n.T(m.loc, "tui.hint.thought")),
		}
	}
	if m.menu.active && len(m.menu.items) > 0 {
		return []string{
			i18n.T(m.loc, "tui.hint.menuRun"),
			m.th.glyph("↑↓", "^v") + " " + i18n.T(m.loc, "tui.hint.menuSelect"),
			i18n.T(m.loc, "tui.hint.menuComplete"),
			i18n.T(m.loc, "tui.hint.menuCancel"),
		}
	}
	return []string{
		i18n.T(m.loc, "tui.hint.submit"),
		i18n.T(m.loc, "tui.hint.newline"),
		i18n.T(m.loc, "tui.hint.thought"),
		i18n.T(m.loc, "tui.hint.quit"),
	}
}

// approvalCard renders the tier-2 consent prompt for a parked task: what it
// wants to do, why the executor refused, and the y/n choice. The focused
// choice is accented and marker-prefixed so arrows + Enter read as a picker
// while the [y]/[n] labels keep the hotkeys discoverable.
func (m tuiModel) approvalCard() string {
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
	sb.WriteString("\n\n" + choice(0, "y", i18n.T(m.loc, "tui.approval.yes")))
	sb.WriteString("   " + choice(1, "n", i18n.T(m.loc, "tui.approval.no")))
	sb.WriteString("\n" + m.th.muted.Render(m.th.glyph("↑↓", "^v")+" "+i18n.T(m.loc, "tui.approval.hint")))
	return m.th.approval.Width(m.textWidth()).Render(sb.String())
}

// approvalHit maps a terminal click to an approval choice: 0 = approve,
// 1 = deny, -1 = not on either option. The ephemeral frame is only
// bottom-anchored once the transcript has filled the screen (bubbletea paints
// the view at the cursor and scrollback pushes it down), so the choice row is
// located by re-rendering the card and finding the [y]/[n] line from the
// bottom, and the option cells are measured from the same pieces choice()
// renders. Any click that does not resolve to an option — the transcript, the
// card body, a stale frame after a resize — is ignored: a mis-aimed click may
// be useless, but it must never be able to approve an irreversible task. The
// keyboard path (y/n, arrows + enter) is unaffected.
func (m tuiModel) approvalHit(x, y int) int {
	if m.height <= 0 {
		return -1
	}
	lines := strings.Split(m.approvalCard(), "\n")
	choice := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], "[y]") && strings.Contains(lines[i], "[n]") {
			choice = i
			break
		}
	}
	if choice < 0 || y != m.height-len(lines)+choice {
		return -1
	}
	// choice() renders: 2-col focus prefix, "[k]" badge, a space, the label —
	// inside a border+padding frame whose content starts at column 2. The two
	// options are joined by a 3-space gap.
	const origin = 2 // border + horizontal padding
	optW := func(label string) int { return 2 + 3 + 1 + cliui.DisplayWidth(label) }
	yes := i18n.T(m.loc, "tui.approval.yes")
	no := i18n.T(m.loc, "tui.approval.no")
	yesX0 := origin
	yesX1 := yesX0 + optW(yes) - 1
	noX0 := yesX1 + 1 + 3
	noX1 := noX0 + optW(no) - 1
	switch {
	case x >= yesX0 && x <= yesX1:
		return 0
	case x >= noX0 && x <= noX1:
		return 1
	}
	return -1
}

// askingButtonHit maps a terminal click to an asking footer button:
// 0 = Stop, 1 = Steer/Inject, 2 = Thought, -1 = none.
func (m tuiModel) askingButtonHit(x, y int) int {
	if m.height <= 0 || x < 0 {
		return -1
	}
	if y < m.height-2 || y >= m.height {
		return -1
	}

	hints := m.hintKeys()
	if len(hints) < 3 {
		return -1
	}

	w0 := lipgloss.Width(hints[0])
	w1 := lipgloss.Width(hints[1])
	w2 := lipgloss.Width(hints[2])

	stopLimit := max(22, w0)
	steerLimit := max(45, stopLimit+1+w1)
	thoughtLimit := max(68, steerLimit+1+w2)

	switch {
	case x <= stopLimit:
		return 0
	case x > stopLimit && x <= steerLimit:
		return 1
	case x > steerLimit && x <= thoughtLimit:
		return 2
	}
	return -1
}

// renderWelcomeBanner builds the unified startup/clear banner:
//   - If width >= 76: full 72-col ASCII brand wordmark in brandGreen
//   - If width < 76: compact single-line heading (✻ OpenPanda v...)
//   - Version, Node & Model, Working Directory
//   - Interactive tips (commands, file attach, help guide)
func renderWelcomeBanner(cfg *config.Config, loc i18n.Locale, width int, th theme) string {
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
		sb.WriteString(th.heading.Render("  " + i18n.T(loc, "repl.banner.title") + " v" + version))
	} else {
		sb.WriteString(th.heading.Render(cliui.Truncate(
			"  "+th.glyph("✻", "*")+" "+i18n.T(loc, "repl.banner.title")+" v"+version, width, uni)))
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
	if m.r != nil {
		cfg = m.r.cfg
	}
	return renderWelcomeBanner(cfg, m.loc, w, m.th)
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
