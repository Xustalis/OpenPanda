// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !lite

package main

// View renders the whole frame: the welcome banner, the committed transcript
// from chatHistory, the live turn (streaming answer, thought preview or task
// card) with its spinner while asking, the approval card while approving, and
// the bottom rounded input box. The frame is repainted in the alternate screen,
// which has no scrollback of its own; quitting restores the shell's previous
// screen.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/nodeidentity"
	versionpkg "github.com/Xustalis/OpenPanda/internal/version"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func (m tuiModel) View() string {
	if m.quitting {
		return ""
	}
	// The side channel replaces the frame while it is open; see tui_side.go.
	if m.sideShowing() {
		return m.sideView()
	}
	if m.mode == modeExec {
		var live strings.Builder
		live.WriteString("\n")
		live.WriteString(m.th.accent.Render(i18n.T(m.loc, "tui.exec.running")))
		// Strip terminal control sequences from the live stream: a handler's
		// clear-screen or color bytes inside the frame would corrupt the paint
		// Bubble Tea is about to do. The text lives on the exec itself — a
		// strings.Builder field on the value-copied model panics on write.
		var output string
		if m.exec != nil {
			output = ansi.Strip(latestFrame(m.exec.text()))
		}
		// Clip the stream to the window: a command that has already produced
		// more lines than fit would otherwise scroll the running header off
		// the top — leaving a full-screen wall of output with no visible
		// "running" affordance at all (the exact frozen-screen complaint this
		// frame exists to fix). The tail is what a reader watches; the header
		// and the cancel hint are chrome and must survive any stream length.
		lines := strings.Split(output, "\n")
		budget := m.height - 8 // chrome + input box; min height falls back below
		if budget < 3 {
			budget = 3
		}
		if len(lines) > budget {
			lines = lines[len(lines)-budget:]
		}
		if strings.TrimSpace(strings.Join(lines, "\n")) != "" {
			// The same muted bar the committed blockInfo wears, so a finishing
			// exec does not visibly shift its output when it commits to the
			// transcript.
			bar := m.th.muted.Render(m.th.glyph("│", "|")) + " "
			for i, l := range lines {
				lines[i] = bar + l
			}
			live.WriteString("\n")
			live.WriteString(strings.Join(lines, "\n"))
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
	case modeClarifying:
		return m.mainChatView("\n" + m.clarifyCard())
	case modeConfirm:
		return m.mainChatView("\n" + m.confirmCard())
	default:
		return m.mainChatView("")
	}
}

// mainChatView renders the full-screen chat interface in AltScreen mode:
//   - The welcome banner (OpenPanda ASCII art wordmark, version, workdir, tips)
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
	// Paint one row short of the screen. A frame of exactly Height rows leaves
	// the cursor on the last line, and a terminal that scrolls there shifts every
	// later repaint up by a row: Bubble Tea then moves up "lines rendered" from a
	// position that has drifted, so the previous frame's tail is never erased and
	// the composer piles up down the screen, one copy per repaint.
	h = max(1, h-1)

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
	if m.layout != nil {
		m.layout.blockStart = m.layout.blockStart[:0]
	}
	if m.chatHistory != nil && len(m.chatHistory.blocks) > 0 {
		for i := range m.chatHistory.blocks {
			contentLines = append(contentLines, "")
			if m.layout != nil {
				m.layout.blockStart = append(m.layout.blockStart, len(contentLines))
			}
			contentLines = append(contentLines, m.chatHistory.renderedAt(i, m.th, m.textWidth(), m.expandThought, m.expandHints)...)
		}
	}

	// 3. In-flight live region (streaming answer, reasoning preview, task progress card, spinner)
	liveLines := 0
	if strings.TrimSpace(live) != "" {
		split := strings.Split(live, "\n")
		liveLines = len(split)
		contentLines = append(contentLines, split...)
	}

	totalContent := len(contentLines)
	// liveStart is the content-line index where the live region begins; the
	// branches below translate it into a screen row for the click handlers.
	liveStart := totalContent - liveLines
	// The rail and the locate action both need where things landed; the model
	// shares this through a pointer the same way it shares the scroll ceiling
	// (View is a value method and can only publish).
	if m.layout != nil {
		m.layout.content, m.layout.avail = totalContent, availHeight
	}

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
		liveTop := -1
		if liveLines > 0 {
			liveTop = liveStart
		}
		m.publishFrameGeom(tuiFrameGeom{rows: len(out), liveTop: liveTop})
		return clipRendered(m.withSidebar(out), w)
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
	// The scrollbar sits on the transcript's own right edge, which is the left
	// side of the rail when it is open — hence the content width, not the
	// terminal's.
	if mw := m.textWidth(); mw > 10 {
		// Attach a visual vertical scrollbar on the right boundary so users clearly see
		// that history exists and can gauge their scroll position.
		thumbHeight := max(1, availHeight*availHeight/totalContent)
		thumbTop := 0
		if maxScroll > 0 {
			thumbTop = (start * (availHeight - thumbHeight)) / maxScroll
		}
		thumbBottom := thumbTop + thumbHeight
		contentWidth := mw - 1
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
	liveTop := -1
	if liveLines > 0 {
		liveTop = liveStart - start
	}
	m.publishFrameGeom(tuiFrameGeom{rows: len(out), liveTop: liveTop})
	return clipRendered(m.withSidebar(out), w)
}

// withSidebar attaches the task rail to the right of a finished frame. The
// frame's own lines were already clipped to textWidth, so this only pads them
// out to the separator and appends the rail's column — the rail never re-lays
// out the transcript.
func (m tuiModel) withSidebar(out []string) string {
	railW := m.sidebarWidth()
	if railW <= 0 {
		return strings.Join(out, "\n")
	}
	rail := m.sidebarLines(len(out), railW)
	contentW := m.textWidth()
	sep := m.th.muted.Render(m.th.glyph("│", "|"))
	joined := make([]string, len(out))
	for i, line := range out {
		pad := contentW - ansi.StringWidth(line)
		if pad < 0 {
			pad = 0
		}
		row := ""
		if i < len(rail) {
			row = rail[i]
		}
		joined[i] = line + strings.Repeat(" ", pad) + sep + row
	}
	return strings.Join(joined, "\n")
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

// tuiFrameGeom is where the frame mainChatView just emitted actually placed
// things on screen. Click handlers read it to translate mouse coordinates —
// the frame's own row count is authoritative because it does not always fill
// the terminal (it deliberately stays one row short, #44), and the live
// region's origin depends on how much history sat above it and whether the
// transcript is scrolled.
type tuiFrameGeom struct {
	// rows is the number of lines the frame emitted; the status row and its
	// clickable footer cells sit on the last one.
	rows int
	// liveTop is the screen row the live region's first line landed on, or
	// -1 when the frame carried no live region. It can be negative when the
	// transcript is scrolled back far enough to push the live region above
	// the visible window.
	liveTop int
}

// publishFrameGeom records the geometry of the frame just rendered, using the
// same shared-pointer trick as publishScrollLimit.
func (m tuiModel) publishFrameGeom(g tuiFrameGeom) {
	if m.frameGeom != nil {
		*m.frameGeom = g
	}
}

// frameRows returns the number of lines the last frame emitted. Before the
// first render it predicts what the next frame will emit — one row short of
// the terminal height — so hitboxes built ahead of the first View still land.
func (m tuiModel) frameRows() int {
	if m.frameGeom != nil && m.frameGeom.rows > 0 {
		return m.frameGeom.rows
	}
	return max(1, m.height-1)
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
	blockLines = append(blockLines, m.th.heading.Render("  "+versionpkg.Display()))

	// The splash shows only the session's own facts: version and work dir.
	// Node name and model are deliberately absent — a fresh install would
	// display the built-in defaults (or the hostname probe) as if they were
	// the user's own configuration. /context and the footer carry them.
	workPath := ""
	if m.r != nil && m.r.cfg != nil {
		m.r.readConfig(func(c *config.Config) { workPath = c.Storage.WorkPath })
	} else {
		workPath, _ = os.Getwd()
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

// termsBoxWidth is the terms card's outer width: never narrower than 50 columns
// so the clauses stay readable, never wider than 86 so the lines do not run away
// on a wide terminal. The frame adds a border and a padding column on each side,
// so it renders 2 columns wider than the value returned here — the last clamp
// keeps it on screen when the terminal is narrower than the 50-column floor.
func termsBoxWidth(w int) int {
	if w <= 0 {
		w = 80
	}
	return min(min(86, max(50, w-4)), max(24, w-2))
}

// termsCard lays the terms card out as three row groups: everything above the
// clause body, the clause body itself, and everything below it (the prevails
// note, the answer row, the key hints). Rendering windows the body to the frame
// and the scroll handlers page by a screenful of it, so both go through here
// instead of each deriving the geometry.
//
// scrollNote is the trailing "PgUp/PgDn  18/80" position line. It occupies a row
// of its own whether or not the document overflows, so the card's geometry never
// depends on the position it reports; callers that only need the row counts pass
// "".
func (m tuiModel) termsCard(tt termsText, boxWidth int, scrollNote string) (head, body, foot []string) {
	// lipgloss counts a style's Width as padding plus content, so the frame's
	// one-column padding on each side comes off it; clause prose sits in a
	// three-column gutter, which comes off again.
	inner := boxWidth - 2
	const gutter = 3
	rule := m.th.muted.Render(strings.Repeat("─", inner))

	// Every fixed row is wrapped here rather than left to the frame: the frame
	// wraps whatever overflows, and a row that silently becomes two would make
	// the card taller than the window it was sized for.
	if m.termsReconsent {
		// The license-change banner leads the card so an upgrade from the MIT
		// era explains itself rather than looking like a repeat.
		for _, line := range wrapTermsText(i18n.T(m.loc, "tui.onboard.termsUpdated"), inner) {
			head = append(head, m.th.warn.Render(line))
		}
		head = append(head, "")
	}
	for _, line := range wrapTermsText(tt.heading, inner) {
		head = append(head, m.th.heading.Render(line))
	}
	head = append(head, rule, "")

	for i, sec := range tt.sections {
		if i > 0 {
			body = append(body, "")
		}
		for _, line := range wrapTermsText(sec.title, inner) {
			body = append(body, m.th.accent.Bold(true).Render(line))
		}
		for _, line := range wrapTermsText(sec.body, inner-gutter) {
			body = append(body, strings.Repeat(" ", gutter)+line)
		}
	}

	// Answer row: the cursor decides which option is highlighted and marked, so
	// the styling happens here; answerStacks only picks the layout.
	optAgree := m.th.muted.Render("  " + tt.agree)
	optDecline := m.th.muted.Render("  " + tt.decline)
	if m.termsCursor == 0 {
		optAgree = m.th.accent.Bold(true).Render("> " + tt.agree)
	} else {
		optDecline = m.th.warn.Bold(true).Render("> " + tt.decline)
	}

	// Everything below the clause window. The prevails note is built apart from
	// the rest because it is the one row the card gives up on a terminal too
	// short to hold it: it restates the pointer to LICENSE and TERMS.md that
	// clause one already ends with, so the clauses are what must survive.
	var prevails []string
	for _, line := range wrapTermsText(tt.prevails, inner) {
		prevails = append(prevails, m.th.muted.Render(line))
	}

	tail := []string{"", rule}
	if tt.answerStacks(inner) {
		tail = append(tail, "  "+optAgree, "  "+optDecline)
	} else {
		tail = append(tail, fmt.Sprintf("  %s      %s", optAgree, optDecline))
	}
	tail = append(tail, "")
	for _, line := range wrapTermsText("  "+tt.hints, inner) {
		tail = append(tail, m.th.muted.Render(line))
	}
	tail = append(tail, m.th.muted.Render(scrollNote))

	foot = tail
	if m.termsBodyRows(len(head), len(tail)+len(prevails)+1) >= minTermsClauseRows {
		foot = append(append([]string{""}, prevails...), tail...)
	}
	return head, body, foot
}

// minTermsClauseRows is how many clause rows the card insists on before it
// spends rows on the prevails note instead. Below this the note is dropped and
// the window takes the space.
const minTermsClauseRows = 3

// termsBodyRows is how many clause rows the frame leaves for the body at the
// current terminal size, once its fixed head, foot and two border rows are
// accounted for. It floors at one row rather than at a comfortable number: a
// short terminal has to give something up, and giving up clause rows is what
// the scroll window is for — overflowing the frame is not.
func (m tuiModel) termsBodyRows(headRows, footRows int) int {
	h := m.height
	if h <= 0 {
		h = 24
	}
	return max(1, h-headRows-footRows-2)
}

// termsPageRows is one screenful of the terms card's clause window — the step
// PgUp and PgDn move by.
func (m tuiModel) termsPageRows() int {
	head, _, foot := m.termsCard(termsCardText(m.loc), termsBoxWidth(m.width), "")
	return m.termsBodyRows(len(head), len(foot))
}

// scrollTerms moves the terms card's clause window by delta rows, clamped to the
// document.
func (m *tuiModel) scrollTerms(delta int) {
	head, body, foot := m.termsCard(termsCardText(m.loc), termsBoxWidth(m.width), "")
	rows := m.termsBodyRows(len(head), len(foot))
	m.termsScroll = min(max(0, m.termsScroll+delta), max(0, len(body)-rows))
}

// onboardingTermsView renders the terms of service & license agreement card.
//
// The ten clauses are long prose in five languages — seventy-odd wrapped rows
// against the twenty-four a default terminal offers — so the clause body is
// windowed to the frame and scrolled with PgUp/PgDn, Home/End or the wheel. The
// answer row sits below the window, outside it: wherever the user has scrolled
// to, agree and decline are on screen.
func (m tuiModel) onboardingTermsView(w, h int) string {
	boxWidth := termsBoxWidth(w)
	tt := termsCardText(m.loc)
	head, body, foot := m.termsCard(tt, boxWidth, "")

	rows := m.termsBodyRows(len(head), len(foot))
	scroll := min(max(0, m.termsScroll), max(0, len(body)-rows))
	end := min(len(body), scroll+rows)

	if len(body) > rows {
		// Lay out again with the position line. It holds a row of its own either
		// way, so the geometry computed above still stands.
		_, _, foot = m.termsCard(tt, boxWidth, fmt.Sprintf("  PgUp/PgDn   %d/%d", end, len(body)))
	}

	lines := append([]string{}, head...)
	lines = append(lines, body[scroll:end]...)
	lines = append(lines, foot...)

	boxed := m.th.listBox.Width(boxWidth).Render(strings.Join(lines, "\n"))

	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, boxed)
}

// Line-breaking prohibitions the card honours. Chinese and Japanese typography
// does not let a line end on an opening bracket or open on a closing one, and a
// Latin token embedded in CJK prose is one word: a hard break inside it reads as
// a typo rather than as a wrap.
const (
	noLineStart = "）】》」』〉〕］｝、。，．；：！？…·”’)]}>,.;:!?%‰℃"
	noLineEnd   = "（【《「『〈〔［｛“‘([{<"
)

// asciiWordRune reports whether r belongs to a Latin or numeric token. Only
// ASCII is considered: CJK letters are word runes to unicode.IsLetter, and
// treating them as tokens would pull every break back to the start of the
// sentence.
func asciiWordRune(r rune) bool {
	switch {
	case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		return true
	case r == '-', r == '_', r == '.', r == '+', r == '@', r == '/':
		return true
	}
	return false
}

// pullBackBreak walks a hard break back while either side of it breaks one of
// those rules, so the line before never ends on an opening bracket and the line
// after never opens on a closing one or on half a Latin token. cur is the line
// being filled and next is the rune that overflowed it; at is where the break
// currently sits, and the rune following the break is next when at is the end of
// cur.
func pullBackBreak(cur []rune, next rune, at int) int {
	start := at
	for at > 0 {
		first := next
		if at < len(cur) {
			first = cur[at]
		}
		last := cur[at-1]
		switch {
		case strings.ContainsRune(noLineStart, first), strings.ContainsRune(noLineEnd, last):
			at--
		case asciiWordRune(first) && asciiWordRune(last):
			// Protecting the token must not push the break past its start: a
			// token too long for one line has to be split somewhere, and moving
			// it whole onto the next line only moves the wall.
			if tokenStartsLine(cur, at-1) {
				return at
			}
			at--
		default:
			return at
		}
	}
	// A pathological run — a whole line of closing punctuation — must still
	// break somewhere rather than be carried forward wholesale.
	return start
}

// tokenStartsLine reports whether the word run ending at index i also begins the
// line, i.e. the token has nothing in front of it to break after.
func tokenStartsLine(cur []rune, i int) bool {
	for i > 0 && asciiWordRune(cur[i-1]) {
		i--
	}
	return i == 0
}

// wrapTermsText greedily wraps s to width display columns.
//
// lipgloss's own wrapper breaks on spaces, which is right for Latin prose but
// leaves a short line whenever a long unbroken CJK run follows one: the whole
// run moves to the next line and is then broken per rune. The clauses are prose
// in five languages, so the card wraps here instead.
//
// A space is preferred as the break point so Latin words survive, but only when
// it does not waste much of the line. In CJK prose the rare spaces usually
// follow an embedded Latin token and sit far back — honouring one there would
// leave a ragged column and push the text onto many more rows than it needs.
// Whatever break wins is then walked back past the prohibitions above.
func wrapTermsText(s string, width int) []string {
	if width < 8 {
		width = 8
	}
	var (
		out            []string
		cur            []rune
		curWidth       int
		lastSpace      = -1
		lastSpaceWidth int
	)
	flush := func() {
		// An empty line can only come from a break pulled all the way back;
		// dropping it keeps the caller's row count honest.
		if line := strings.TrimRight(string(cur), " "); line != "" {
			out = append(out, line)
		}
		cur, curWidth, lastSpace, lastSpaceWidth = cur[:0], 0, -1, 0
	}
	for _, r := range s {
		if r == '\n' {
			flush()
			continue
		}
		if r == ' ' {
			// A line never opens with a space, and a space that would itself
			// overflow simply ends the line.
			if len(cur) == 0 {
				continue
			}
			if curWidth+1 > width {
				flush()
				continue
			}
			lastSpace, lastSpaceWidth = len(cur), curWidth
			cur = append(cur, r)
			curWidth++
			continue
		}
		rw := cliui.RuneWidth(r)
		if curWidth+rw > width && len(cur) > 0 {
			at := len(cur)
			if lastSpace >= 0 && curWidth-lastSpaceWidth <= width/4 {
				at = lastSpace
			}
			at = pullBackBreak(cur, r, at)
			// Carry what falls past the break onto the new line, dropping the
			// space the break consumed.
			carry := append([]rune(nil), cur[at:]...)
			cur = cur[:at]
			flush()
			for _, c := range carry {
				if c == ' ' && len(cur) == 0 {
					continue
				}
				cur = append(cur, c)
				curWidth += cliui.RuneWidth(c)
			}
		}
		cur = append(cur, r)
		curWidth += rw
	}
	if len(cur) > 0 {
		flush()
	}
	return out
}

// termsSection is one numbered clause of the onboarding terms card. body is a
// single paragraph: the card wraps it to its own width at render time, so the
// text does not have to be hand-broken per language.
type termsSection struct {
	title string
	body  string
}

// termsText is everything the terms card renders in the user's language.
type termsText struct {
	heading  string
	sections []termsSection
	prevails string
	agree    string
	decline  string
	hints    string
}

// answerStacks reports whether the two answer options have to go one per line:
// they do not fit side by side in German or Spanish on a narrow terminal.
// Keeping the row's line count exact matters, because the card sizes its clause
// window from the number of fixed rows around it.
func (tt termsText) answerStacks(inner int) bool {
	// The row renders as "  " + marker(2) + agree + gap(6) + marker(2) + decline.
	// Both markers are 2 columns whether or not the option is the cursor's.
	return cliui.DisplayWidth(tt.agree)+cliui.DisplayWidth(tt.decline)+12 > inner
}

// termsCardText localizes the license/terms card. It carries the same ten
// clauses the web console shows in its onboarding wizard — the two surfaces are
// the same notice and must not drift — with the clause prose left unwrapped so
// the card can reflow it to whatever width the terminal gives it.
func termsCardText(loc i18n.Locale) termsText {
	switch loc {
	case i18n.ChineseSimp:
		return termsText{
			heading: "⚖️  OpenPanda 开源许可与服务条款协议",
			sections: []termsSection{
				{`1. 开源许可（AGPL-3.0）`, `OpenPanda 是基于 GNU Affero 通用公共许可证 3.0 或更高版本（AGPL-3.0-or-later）发布的自由开源软件。你可以运行、研究、修改和再分发（包括商业用途），但衍生作品必须同样以 AGPL 发布；若将修改版通过网络提供给他人使用，必须同时提供其源代码。闭源集成或托管用途可购买商业授权（见 COMMERCIAL.md）。完整许可与条款见随附的 LICENSE 与 TERMS.zh-CN.md 文件，如与本摘要不一致，以二者为准。`},
				{`2. 自托管软件，而非托管服务`, `OpenPanda 是你自行安装、配置并在自己的硬件上运行的软件。作者不提供托管服务、服务水平承诺、正常运行时间保证、支持义务或数据保管。安装、运行、维护、访问控制、监控及部署环境的安全姿态全部由你自行负责。`},
				{`3. 本地优先与隐私自主`, `你的源代码、工作区文件、对话记录、记忆库与 API 凭据默认保留在你自己的设备上。软件不含遥测、跟踪或私人数据收集。仅在你主动配置模型服务方或对等节点接收时数据才会离开本机——一旦离开，适用接收方的隐私实践而非 OpenPanda 的。`},
				{`4. 第三方模型服务`, `AI 功能使用你自行提供的凭据接入你选择的模型服务商（云端 API 或本地端点）。发送给服务商的提示词、工作区上下文与文件仅受该服务商的条款、隐私政策、数据留存与计费规则约束。OpenPanda 与任何服务商均无隶属、背书或责任关系；相关争议、超额费用与数据处理属于你和服务商之间的事项。`},
				{`5. 点对点委派与网络信任`, `OpenPanda 可以在经配对授权的设备之间委派任务。任务负载、能力卡片、文件与结果经由认证信道在节点间传输——其中可能包括你与之配对的他人设备，其行为不受作者控制。请只与信任的机器配对，并将节点监听端口与 Web 控制台视为私有端点：暴露给不受信网络的风险完全由你自行承担。`},
				{`6. AI 生成内容`, `模型生成的代码、命令、分析及其他输出具有概率性，可能存在缺陷、幻觉、安全隐患、无法运行，或受第三方权利约束——且不构成任何形式的专业建议（包括法律、医疗、金融或安全建议）。使用前请自行审阅、核查许可并测试；依据生成输出采取的行动风险由你独自承担。`},
				{`7. 执行与系统安全`, `OpenPanda 可以执行 shell 命令、修改文件并改变系统状态，包括在远端节点上。审批分级、沙箱与审计日志是缓解措施而非保证：软件缺陷、误配置、提示注入或被入侵的对等节点仍可能造成损害。批准破坏性、不可逆操作前务必核实，并为重要数据保留独立备份。`},
				{`8. 合规与可接受使用`, `你有责任以合法方式使用 OpenPanda，并遵守适用于你的全部义务——数据保护法规、出口管制与制裁规定、所在单位的设备与网络政策，以及你通过本软件接触的任何服务、仓库或网络的条款。不得将其用于侵犯他人权利、未经授权访问系统，或处理你无权处理的数据。本条不改变亦不转移你的责任。`},
				{`9. 无担保与责任限制`, `本软件按"现状"及"可用性"提供，不附带任何明示、默示或法定的担保——包括适销性、特定用途适用性、安全性及不侵权担保。在法律允许的最大范围内，作者与贡献者不对因本软件、其输出或其使用引起的任何直接、间接、附带、特殊、后果性或惩罚性损害负责——包括数据丢失、利润损失、停机、替代服务成本或第三方索赔——即使已被告知存在此类可能性。在法律不允许完全排除责任的情形下，责任以法律要求的最低限度为限。`},
				{`10. 赔偿、条款变更与可分割性`, `因你使用本软件、你的配置、你下达的任务与委派，或你违反本条款、违反法律或第三方权利而引起的索赔、损害与费用（包括律师费），你同意为作者与贡献者抗辩、使其免受损害并予以赔偿。条款可能随软件版本更新，以你所运行版本附带的条款为准。如任何条款被认定不可执行，其余条款仍然完全有效。`},
			},
			prevails: "本卡为摘要；权威文本为随附 LICENSE 与 TERMS.zh-CN.md 文件，如有出入以二者为准。",
			agree:    "[Y] 同意并继续 (Agree and Continue)",
			decline:  "[N] 拒绝并退出 (Decline and Exit)",
			hints:    "Y 同意 · N/Esc 拒绝 · ↑↓/←→ 选择 · Enter 确认",
		}
	case i18n.Japanese:
		return termsText{
			heading: "⚖️  OpenPanda 利用規約とライセンス",
			sections: []termsSection{
				{`1. オープンソースライセンス（AGPL-3.0）`, `OpenPanda は GNU Affero General Public License v3.0 以降（AGPL-3.0-or-later）の下で公開されているフリーかつオープンソースのソフトウェアです。商用を含め、実行・研究・改変・再配布が可能ですが、派生作品は同じ AGPL 条件で公開する必要があり、改変版をネットワーク越しにユーザーへ提供する場合はそのソースコードも提供しなければなりません。クローズドソース利用には商用ライセンスをご利用ください（COMMERCIAL.md 参照）。完全なライセンス文と利用規約は同梱の LICENSE と TERMS.md に記載されており、本要約と矛盾する場合はそちらが優先されます。`},
				{`2. セルフホスト型ソフトウェア（マネージドサービスではありません）`, `OpenPanda は利用者自身のハードウェアにインストール・設定・運用するソフトウェアです。作者はホスティング、サービスレベル、稼働率の約束、サポート義務、データ管理を一切提供しません。インストール、運用、保守、アクセス制御、監視、デプロイ環境のセキュリティはすべて利用者の責任です。`},
				{`3. ローカルファーストとプライバシー自治`, `ソースコード、ワークスペースのファイル、会話履歴、メモリストア、API 認証情報は既定で利用者のデバイス上に保持されます。テレメトリ、トラッキング、個人データの収集はありません。データがノード外へ出るのは、利用者がモデルプロバイダーまたはピアを明示的に設定した場合のみです。送信後は OpenPanda ではなく受信者側の運用方針が適用されます。`},
				{`4. サードパーティのモデルプロバイダー`, `AI 機能は、利用者が選択したプロバイダー（ホスト型 API またはローカルエンドポイント）の認証情報で動作します。プロバイダーへ送信されるプロンプト、ワークスペースコンテキスト、ファイルは、当該プロバイダーの規約・プライバシーポリシー・保存方針・料金のみに従います。OpenPanda はいかなるプロバイダーとも提携・推薦関係になく、責任も負いません。紛争、超過料金、データ取扱いは利用者とプロバイダー間の問題です。`},
				{`5. P2P 委譲とネットワークの信頼`, `OpenPanda は、ペアリングで承認されたデバイス間でタスクを委譲できます。ペイロード、能力カード、ファイル、結果は認証済みチャネル経由でノード間を移動します——ペアリング相手が他人の運営するノードである場合を含み、その行為は作者の管理外です。信頼できるマシンのみとペアリングし、ノードリスナーと Web コンソールは非公開エンドポイントとして扱ってください。信頼できないネットワークへの公開リスクはすべて利用者にあります。`},
				{`6. AI 生成物`, `モデルが生成するコード、コマンド、分析などの出力は確率的です。欠陥、ハルシネーション、セキュリティ上の問題、動作不良、第三者の権利に抵触する内容を含む可能性があり、法律・医療・金融・セキュリティなどの専門的助言を構成しません。使用前に必ずレビュー、ライセンス確認、テストを行ってください。生成物に基づく行動のリスクは利用者が単独で負います。`},
				{`7. 実行とシステム安全`, `OpenPanda はシェルコマンドの実行、ファイル変更、システム状態の変更を行えます（リモートノードを含む）。承認階層、サンドボックス、監査ログは緩和策であり保証ではありません。不具合、設定ミス、プロンプトインジェクション、侵害されたピアによって損害が生じる可能性があります。破壊的・不可逆な操作は承認前に必ず確認し、重要なデータは独立したバックアップを保持してください。`},
				{`8. コンプライアンスと利用規範`, `OpenPanda の利用を適法に行い、適用されるすべての義務——データ保護法、輸出・制裁規制、所属組織のデバイス・ネットワークポリシー、および本ソフトウェア経由でアクセスするサービス・リポジトリ・ネットワークの規約——を遵守する責任は利用者にのみあります。権利侵害、権限のないシステムへのアクセス、処理権限のないデータの取扱いには使用しないでください。本条はその責任を移転しません。`},
				{`9. 無保証と責任の制限`, `本ソフトウェアは「現状のまま」かつ「提供可能な範囲で」提供され、明示・黙示・法定を問わず、商品性、特定目的適合性、安全性、非侵害を含め一切の保証を伴いません。法律で認められる最大限の範囲において、作者および貢献者は、本ソフトウェア、その出力、またはその使用に起因する直接的・間接的・付随的・特別・結果的・懲罰的損害——データ損失、逸失利益、ダウンタイム、代替サービスの費用、第三者からの請求を含む——について、可能性を知らされていた場合でも責任を負いません。責任の完全な免責が認められない場合、法律が求める最小限度に限定されます。`},
				{`10. 免責・補償、変更および分離条項`, `本ソフトウェアの利用、設定、発行したタスクや委譲、本規約・法律・第三者の権利への違反に起因する請求、損害、費用（弁護士費用を含む）について、利用者は作者および貢献者を防御し、免責し、補償することに同意します。本規約はソフトウェアとともに更新されることがあり、実行中のビルドに同梱される規約が適用されます。いずれかの条項が執行不能と判断された場合でも、残りの条項は完全に有効です。`},
			},
			prevails: "本カードは要約です。正式文書は LICENSE と TERMS.md にあり、食い違いはそちらが優先されます。",
			agree:    "[Y] 同意して続行",
			decline:  "[N] 拒否して終了",
			hints:    "Y 同意 · N/Esc 拒否 · ↑↓/←→ 選択 · Enter 確定",
		}
	case i18n.Spanish:
		return termsText{
			heading: "⚖️  Términos de servicio y licencia de OpenPanda",
			sections: []termsSection{
				{`1. Licencia de código abierto (AGPL-3.0)`, `OpenPanda es software libre y de código abierto publicado bajo la GNU Affero General Public License v3.0 o posterior (AGPL-3.0-or-later). Puedes ejecutarlo, estudiarlo, modificarlo y redistribuirlo —incluido el uso comercial— siempre que las obras derivadas permanezcan bajo la AGPL y cualquier versión modificada ofrecida a usuarios a través de una red les ofrezca también su código fuente. Existe una licencia comercial para uso en código cerrado; consulta COMMERCIAL.md. La licencia y los términos completos se encuentran en los archivos LICENSE y TERMS.md y prevalecen sobre este resumen.`},
				{`2. Software autoalojado, no un servicio gestionado`, `OpenPanda es software que instalas, configuras y operas en tu propio hardware. Los autores no ofrecen alojamiento, niveles de servicio, compromisos de disponibilidad, obligaciones de soporte ni custodia de datos. Tú eres el único responsable de la instalación, operación, mantenimiento, control de acceso, monitorización y seguridad de tu despliegue.`},
				{`3. Local-first y autonomía de privacidad`, `Tu código fuente, archivos de trabajo, historial de conversaciones, memorias y credenciales de API permanecen en tus dispositivos de forma predeterminada. El software no incluye telemetría, rastreo ni recopilación de datos privados. Los datos solo salen de un nodo cuando configuras explícitamente un proveedor de modelos o un par para recibirlos — y una vez fuera, rigen las prácticas del destinatario, no las de OpenPanda.`},
				{`4. Proveedores de modelos de terceros`, `Las funciones de IA usan credenciales que tú aportas del proveedor que elijas (API alojadas o endpoints locales). Los prompts, el contexto del espacio de trabajo y los archivos enviados a un proveedor se rigen exclusivamente por sus términos, política de privacidad, retención y precios. OpenPanda no está afiliado a ningún proveedor ni responde por él; disputas, cargos por exceso y tratamiento de datos son asuntos estrictamente entre tú y el proveedor.`},
				{`5. Delegación P2P y confianza de red`, `OpenPanda puede delegar tareas entre dispositivos autorizados mediante emparejamiento. Las cargas, tarjetas de capacidad, archivos y resultados viajan entre nodos por canales autenticados — incluidos nodos operados por otras personas con quienes te emparejes, cuyas acciones escapan al control de los autores. Empareja solo con máquinas de confianza y trata el listener del nodo y la consola web como endpoints privados: exponerlos a redes no confiables es riesgo exclusivamente tuyo.`},
				{`6. Resultados generados por IA`, `El código, los comandos, los análisis y demás salidas del modelo son probabilísticos. Pueden contener defectos, alucinaciones, inseguridad, fallos de funcionamiento o material sujeto a derechos de terceros — y no constituyen asesoramiento profesional de ningún tipo (legal, médico, financiero ni de seguridad). Revisa, verifica licencias y prueba todo antes de usarlo; actuar sobre los resultados generados es bajo tu exclusiva responsabilidad.`},
				{`7. Ejecución y seguridad del sistema`, `OpenPanda puede ejecutar comandos de shell, modificar archivos y alterar el estado del sistema, incluso en nodos remotos. Los niveles de aprobación, el sandboxing y los registros de auditoría son mitigaciones, no garantías: errores, configuraciones incorrectas, inyección de prompts o un par comprometido pueden causar daños. Verifica las acciones destructivas o irreversibles antes de aprobarlas y conserva copias de seguridad independientes de lo importante.`},
				{`8. Cumplimiento y uso aceptable`, `Eres el único responsable de usar OpenPanda legalmente y conforme a todas las obligaciones que te aplican: legislación de protección de datos, controles de exportación y sanciones, políticas de dispositivos y redes de tu organización, y los términos de cualquier servicio, repositorio o red a los que accedas a través de él. No lo uses para infringir derechos, acceder a sistemas sin autorización ni procesar datos que no tienes derecho a tratar. Nada de esto transfiere esa responsabilidad.`},
				{`9. Sin garantía y limitación de responsabilidad`, `El software se proporciona "tal cual" y "según disponibilidad", sin garantía alguna, expresa, implícita o legal — incluidas comerciabilidad, idoneidad para un fin, seguridad y no infracción. En la máxima medida permitida por la ley, los autores y contribuidores no responden por daños directos, indirectos, incidentales, especiales, consecuentes o punitivos — incluida pérdida de datos, lucro cesante, tiempo de inactividad, coste de servicios sustitutos o reclamaciones de terceros — derivados del software, sus salidas o su uso, aun advirtiendo la posibilidad. Donde la responsabilidad no pueda excluirse por completo, queda limitada al mínimo que la ley exija.`},
				{`10. Indemnización, cambios y divisibilidad`, `Aceptas indemnizar, defender y mantener indemnes a los autores y contribuidores frente a reclamaciones, daños y gastos (incluidos honorarios legales) derivados de tu uso del software, tu configuración, las tareas y delegaciones que emites, o tu infracción de estos términos, de la ley o de derechos de terceros. Los términos pueden actualizarse junto con el software; se aplican los incluidos en la compilación que ejecutas. Si alguna disposición resulta inaplicable, el resto permanece en pleno vigor.`},
			},
			prevails: "Esta tarjeta es un resumen; los textos completos y vinculantes son LICENSE y TERMS.md, que prevalecen ante cualquier discrepancia.",
			agree:    "[Y] Aceptar y continuar",
			decline:  "[N] Rechazar y salir",
			hints:    "Y aceptar · N/Esc rechazar · ↑↓/←→ elegir · Enter confirmar",
		}
	case i18n.German:
		return termsText{
			heading: "⚖️  OpenPanda Nutzungsbedingungen & Lizenz",
			sections: []termsSection{
				{`1. Open-Source-Lizenz (AGPL-3.0)`, `OpenPanda ist freie quelloffene Software unter der GNU Affero General Public License v3.0 oder später (AGPL-3.0-or-later). Du darfst sie ausführen, untersuchen, ändern und weiterverbreiten — auch kommerziell —, solange abgeleitete Werke unter der AGPL bleiben und jede über ein Netzwerk angebotene geänderte Version den Nutzern ihren Quellcode anbietet. Für die Nutzung in geschlossenem Code ist eine kommerzielle Lizenz erhältlich (siehe COMMERCIAL.md). Der vollständige Lizenztext und die Nutzungsbedingungen liegen in den Dateien LICENSE und TERMS.md bei und gehen dieser Zusammenfassung vor.`},
				{`2. Selbst gehostete Software, kein Managed Service`, `OpenPanda ist Software, die du auf eigener Hardware installierst, konfigurierst und betreibst. Die Autoren bieten kein Hosting, keine Servicelevels, keine Verfügbarkeitszusagen, keine Supportpflichten und keine Datenverwahrung. Installation, Betrieb, Wartung, Zugriffskontrolle, Monitoring und die Sicherheit deiner Bereitstellung liegen allein in deiner Verantwortung.`},
				{`3. Local-First & Datensouveränität`, `Quellcode, Arbeitsdateien, Gesprächsverläufe, Gedächtnisspeicher und API-Zugangsdaten verbleiben standardmäßig auf deinen Geräten. Die Software enthält keine Telemetrie, kein Tracking und keine Erhebung privater Daten. Daten verlassen einen Knoten nur dort, wo du ausdrücklich einen Modellanbieter oder Peer dafür konfigurierst — und ab dann gelten die Praktiken des Empfängers, nicht die von OpenPanda.`},
				{`4. Modellanbieter von Drittanbietern`, `KI-Funktionen laufen über Zugangsdaten, die du für einen selbst gewählten Anbieter bereitstellst (gehostete APIs oder lokale Endpunkte). Prompts, Arbeitskontext und Dateien, die an einen Anbieter gesendet werden, unterliegen ausschließlich dessen Bedingungen, Datenschutzrichtlinie, Aufbewahrung und Preisen. OpenPanda steht mit keinem Anbieter in Verbindung, wird von keinem unterstützt und übernimmt keine Verantwortung; Streitigkeiten, Mehrkosten und Datenverarbeitung sind ausschließlich Sache zwischen dir und dem Anbieter.`},
				{`5. P2P-Delegation & Netzwerkvertrauen`, `OpenPanda kann Aufgaben an durch Kopplung autorisierte Geräte delegieren. Payloads, Fähigkeitskarten, Dateien und Ergebnisse wandern über authentifizierte Kanäle zwischen Knoten — einschließlich Knoten, die von anderen Personen betrieben werden, mit denen du dich koppelst und deren Handlungen außerhalb der Kontrolle der Autoren liegen. Kopple nur mit vertrauenswürdigen Maschinen und behandle Node-Listener und Web-Konsole als private Endpunkte: Sie gegenüber nicht vertrauenswürdigen Netzen offenzulegen geschieht ausschließlich auf dein eigenes Risiko.`},
				{`6. KI-generierte Ausgaben`, `Code, Befehle, Analysen und andere Modellausgaben sind probabilistisch. Sie können fehlerhaft, halluziniert, unsicher, nicht funktionsfähig oder durch Rechte Dritter belastet sein — und stellen keine professionelle Beratung dar (weder rechtlich, medizinisch, finanziell noch in Sicherheitsfragen). Prüfe jede Ausgabe auf Inhalt und Lizenz und teste sie vor der Verwendung; Handlungen auf Grundlage generierter Ausgaben erfolgen auf dein alleiniges Risiko.`},
				{`7. Ausführung & Systemsicherheit`, `OpenPanda kann Shell-Befehle ausführen, Dateien verändern und den Systemzustand ändern, auch auf entfernten Knoten. Genehmigungsstufen, Sandboxing und Audit-Logs sind Risikominderung, keine Garantie: Fehler, Fehlkonfigurationen, Prompt-Injection oder ein kompromittierter Peer können weiterhin Schaden anrichten. Prüfe destruktive oder irreversible Aktionen vor der Freigabe und halte unabhängige Backups wichtiger Daten vor.`},
				{`8. Compliance & zulässige Nutzung`, `Du bist allein dafür verantwortlich, OpenPanda rechtmäßig und im Einklang mit allen für dich geltenden Pflichten zu nutzen — Datenschutzrecht, Export- und Sanktionskontrollen, Geräte- und Netzwerkrichtlinien deiner Organisation sowie die Bedingungen jeglicher Dienste, Repositories oder Netze, die du darüber erreichst. Nutze die Software nicht zur Rechtsverletzung, zum unbefugten Zugriff auf Systeme oder zur Verarbeitung von Daten, zu deren Handhabung du nicht berechtigt bist. Nichts hiervon überträgt diese Verantwortung.`},
				{`9. Gewährleistungsausschluss & Haftungsbeschränkung`, `Die Software wird „wie besehen" und „wie verfügbar" ohne jegliche ausdrückliche, stillschweigende oder gesetzliche Gewährleistung bereitgestellt — einschließlich Marktgängigkeit, Eignung für einen bestimmten Zweck, Sicherheit und Nichtverletzung. Soweit gesetzlich zulässig, haften die Autoren und Mitwirkenden nicht für direkte, indirekte, zufällige, besondere, Folge- oder Strafschäden — einschließlich Datenverlust, entgangener Gewinne, Ausfallzeiten, Kosten für Ersatzdienste oder Ansprüche Dritter — die aus der Software, ihren Ausgaben oder ihrer Nutzung entstehen, selbst wenn auf die Möglichkeit hingewiesen wurde. Wo eine Haftung nicht vollständig ausgeschlossen werden kann, ist sie auf das gesetzlich geforderte Mindestmaß beschränkt.`},
				{`10. Freistellung, Änderungen & salvatorische Klausel`, `Du stimmst zu, die Autoren und Mitwirkenden zu verteidigen, schadlos zu halten und von Ansprüchen, Schäden und Aufwendungen (einschließlich Anwaltskosten) freizustellen, die aus deiner Nutzung der Software, deiner Konfiguration, den von dir ausgelösten Aufgaben und Delegationen oder deinem Verstoß gegen diese Bedingungen, gegen Gesetze oder Rechte Dritter entstehen. Die Bedingungen können zusammen mit der Software aktualisiert werden; es gelten die Bedingungen des Builds, den du ausführst. Sollte eine Bestimmung undurchsetzbar sein, bleiben die übrigen uneingeschränkt wirksam.`},
			},
			prevails: "Diese Karte ist eine Zusammenfassung; maßgeblich sind die vollständigen Texte in den Dateien LICENSE und TERMS.md.",
			agree:    "[Y] Zustimmen und fortfahren",
			decline:  "[N] Ablehnen und beenden",
			hints:    "Y zustimmen · N/Esc ablehnen · ↑↓/←→ wählen · Enter bestätigen",
		}
	default:
		return termsText{
			heading: "⚖️  OpenPanda Terms of Service & License Agreement",
			sections: []termsSection{
				{`1. Open-Source License (AGPL-3.0)`, `OpenPanda is free and open-source software released under the GNU Affero General Public License v3.0 or later. You may run, study, modify and redistribute it — including commercially — provided derivative works remain under the AGPL, and any modified version offered to users over a network also offers them its source code. A commercial license is available for closed-source use; see COMMERCIAL.md. The complete license and terms ship in the LICENSE and TERMS.md files and prevail over this summary.`},
				{`2. Self-Hosted Software, Not a Managed Service`, `OpenPanda is software you install, configure and operate on your own hardware. The authors provide no hosting, service levels, uptime commitments, support obligations or data stewardship. You alone are responsible for the installation, operation, maintenance, access control, monitoring and security posture of your deployment.`},
				{`3. Local-First & Privacy Autonomy`, `Your source code, workspace files, conversation history, memory stores and API credentials stay on your devices by default. The software ships no telemetry, tracking or private-data collection. Data leaves a node only where you configure a model provider or peer to receive it — and once it does, the recipient's own practices govern, not OpenPanda's.`},
				{`4. Third-Party Model Providers`, `AI features run on credentials you supply for providers you choose (hosted APIs or local endpoints). Prompts, workspace context and files sent to a provider are governed exclusively by that provider's terms, privacy policy, retention and pricing. OpenPanda is not affiliated with, endorsed by or responsible for any provider; disputes, overage charges and data handling are strictly between you and them.`},
				{`5. Peer-to-Peer Delegation & Network Trust`, `OpenPanda can delegate tasks across devices authorized through pairing. Payloads, capability cards, files and results travel between nodes over authenticated channels — including nodes operated by other people you pair with, whose actions are outside the authors' control. Pair only with machines you trust and treat the node listener and web console as private endpoints: exposing them to untrusted networks is entirely your own risk.`},
				{`6. AI-Generated Output`, `Code, commands, analysis and other model output is probabilistic. It may be defective, hallucinated, insecure, non-functional or subject to third-party rights — and it is not professional advice of any kind (including legal, medical, financial or security advice). Review, license-check and test everything before use; you act on generated output at your sole risk.`},
				{`7. Execution & System Safety`, `OpenPanda can run shell commands, modify files and alter system state, including on remote nodes. Approval tiers, sandboxing and audit logs are mitigations, not guarantees: bugs, misconfiguration, prompt injection or a compromised peer can still cause damage. Verify destructive or irreversible actions before approving them, and keep independent backups of anything that matters.`},
				{`8. Compliance & Acceptable Use`, `You are solely responsible for using OpenPanda lawfully and in line with every obligation that applies to you — data-protection law, export and sanctions controls, workplace device and network policies, and the terms of any service, repository or network you reach through it. Do not use it to infringe rights, access systems without authorization, or process data you are not entitled to handle. Nothing here transfers that responsibility.`},
				{`9. No Warranty & Limitation of Liability`, `The software is provided "as is" and "as available" without warranty of any kind, express, implied or statutory — including merchantability, fitness for a particular purpose, security and non-infringement. To the maximum extent permitted by law, the authors and contributors are not liable for any direct, indirect, incidental, special, consequential or punitive damages — including lost data, lost profits, downtime, cost of substitute services or third-party claims — arising from the software, its outputs or its use, even if advised of the possibility. Where liability cannot be excluded entirely, it is limited to the minimum the law requires.`},
				{`10. Indemnification, Changes & Severability`, `You agree to indemnify and hold the authors and contributors harmless from claims, damages and expenses (including legal fees) arising out of your use of the software, your configuration, the tasks and delegations you issue, or your violation of these terms, of any law or of third-party rights. Terms may be updated together with the software; the build you run carries the terms that apply to it. If any provision is held unenforceable, the remainder stays in full force.`},
			},
			prevails: "This card is a summary; the authoritative texts ship in the LICENSE and TERMS.md files and prevail over it.",
			agree:    "[Y] Agree and Continue",
			decline:  "[N] Decline and Exit",
			hints:    "Y Agree · N/Esc Decline · ↑↓/←→ Select · Enter Confirm",
		}
	}
}

// liveRegion is what the in-flight turn shows: the streaming answer once prose
// has started, otherwise the live chain-of-thought (folded to a moving one-liner,
// or expanded under Ctrl+O), plus the delegated-task card when the turn was
// classified as a task. Reasoning is display-only (D14).
func (m tuiModel) liveRegion() string {
	var parts []string
	if ans := m.liveAnswerText(); strings.TrimSpace(ans) != "" {
		text := m.streamAnswerText(ans)
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
		// delegation has already been committed to the transcript.
		if tv := m.thoughtView(); tv != "" {
			parts = append(parts, tv)
		}
	}
	if m.liveTask != nil {
		parts = append(parts, m.liveTask.renderLive(m.th, m.loc, m.sp.View(), time.Now()))
	}
	return strings.Join(parts, "\n")
}

// liveRenderMinInterval caps how often the streaming answer's markdown is
// re-laid-out. Each delta changes the source, so without a cap every token
// triggers a full glamour pass over the accumulated text — O(answer²) over a
// stream. Between renders the previous frame is served; the spinner already
// repaints at ~10fps, so the lag is imperceptible, and the final commit always
// renders the complete text exactly.
const liveRenderMinInterval = 90 * time.Millisecond

// liveRenderCache memoizes the live answer's markdown render. src tracks the
// source text out was built from, so identical frames (spinner ticks,
// keystrokes, mouse motion between deltas) reuse out with a string compare
// instead of a full render.
type liveRenderCache struct {
	src     string
	out     string
	width   int
	color   bool
	unicode bool
	at      time.Time
}

// streamAnswerText is answerText throttled for the live region: identical
// sources return the memoized render, and new deltas re-render at most once
// per liveRenderMinInterval. A nil cache (a zero-value model in a test)
// renders straight through.
func (m tuiModel) streamAnswerText(ans string) string {
	c := m.liveRC
	w := m.textWidth()
	if c == nil {
		return answerText(m.th, ans, w)
	}
	if c.src == ans && c.width == w && c.color == m.th.color && c.unicode == m.th.unicode {
		return c.out
	}
	if c.out != "" && c.width == w && c.color == m.th.color && c.unicode == m.th.unicode &&
		time.Since(c.at) < liveRenderMinInterval {
		return c.out
	}
	out := answerText(m.th, ans, w)
	c.src, c.out, c.width, c.color, c.unicode, c.at = ans, out, w, m.th.color, m.th.unicode, time.Now()
	return out
}

// thoughtPreviewLines is how many of the newest reasoning lines stay visible
// while the chain is folded. Three is enough to show the thread of thought
// without pushing the answer or the input box off a short terminal.
const thoughtPreviewLines = 3

// thoughtView is the live reasoning preview: a dim moving one-liner while folded,
// the full running thought while expanded.
func (m tuiModel) thoughtView() string {
	if len(m.thought) == 0 {
		return ""
	}
	star := m.th.glyph("✻", "*")
	if !m.expandThought {
		// The line count is the cheapest honest evidence that reasoning is
		// accumulating: a single tail sentence reads the same whether the model
		// wrote three words or three hundred, and "thinking" with nothing moving
		// is what a stalled turn looks like. The last few lines stay visible
		// below it, so the turn shows what it is reasoning about rather than
		// only that it is; Ctrl+O still unfolds the whole chain.
		meta := i18n.Tf(m.loc, "tui.thought.lines", "n", strconv.Itoa(len(m.thought)))
		if !m.thoughtStarted.IsZero() {
			if d := time.Since(m.thoughtStarted); d >= time.Second {
				meta += " · " + elapsed(d)
			}
		}
		var sb strings.Builder
		sb.WriteString(m.th.muted.Render(star + " " + i18n.T(m.loc, "tui.thinking")))
		sb.WriteString(m.th.muted.Render(" · " + meta + " · " + i18n.T(m.loc, "tui.thought.expand")))
		lines := m.thought
		if len(lines) > thoughtPreviewLines {
			lines = lines[len(lines)-thoughtPreviewLines:]
		}
		for _, line := range lines {
			sb.WriteString("\n" + m.th.italic.Render("  "+truncate(line, 100)))
		}
		return sb.String()
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
		m.statusMeta(time.Now()), i18n.T(m.loc, "cli.status.interrupt"))))
	return strings.Join(parts, " ")
}

// How long a turn may stay quiet before the status row says so. Before any
// event has arrived the wait is the provider's prefill window, which can run
// to ResponseHeaderTimeout; after activity starts, a gap means a slow model or
// a stalled link. The two get different wording because they are different
// problems.
const (
	quietAwaitAfter = 3 * time.Second
	quietIdleAfter  = 5 * time.Second
)

// statusMeta is the parenthesised tail of the status row: elapsed clock, live
// token consumption, and how long the turn has been quiet. Token counts come
// from the engine's ProgressUsage events, so the number moves while the model
// writes instead of appearing only in the closing result line.
func (m tuiModel) statusMeta(now time.Time) string {
	meta := elapsed(now.Sub(m.started))
	if tok := m.liveInputTokens + m.liveOutputTokens; tok > 0 {
		label := cliui.HumanCount(tok)
		if m.liveTokensEst {
			label = "~" + label
		}
		meta += " · " + label + " tokens"
	}
	if quiet := m.quietNote(now); quiet != "" {
		meta += " · " + quiet
	}
	return meta
}

// quietNote phrases the turn's current silence. No event yet reads as "waiting
// for the model", which is what the prefill gap actually is; silence after
// activity reads as "quiet for Ns", which is the honest way to show a stream
// that has stopped without claiming it failed.
func (m tuiModel) quietNote(now time.Time) string {
	seen := m.lastActivity.After(m.started)
	if !seen {
		idle := now.Sub(m.started)
		if idle < quietAwaitAfter {
			return ""
		}
		return i18n.Tf(m.loc, "tui.status.awaitModel", "d", elapsed(idle))
	}
	idle := now.Sub(m.lastActivity)
	if idle < quietIdleAfter {
		return ""
	}
	return i18n.Tf(m.loc, "tui.status.idle", "d", elapsed(idle))
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
	// It wears the menuBox card — the same rounded frame as every other panel,
	// in the quieter grey — with the rows sized for the four columns its border
	// and padding take.
	if menu := m.menu.render(m.th, max(1, m.textWidth()-4), m.menuRows()); menu != "" {
		rows = append(rows, m.th.menuBox.Width(max(1, m.textWidth()-2)).Render(menu))
	}
	return strings.Join(append(rows, m.statusRow()), "\n")
}

// menuRows is how many command rows the popup may draw: whatever the terminal has
// left under the blank line, the three-row box, the popup card's two border rows
// and the footer, held to a handful so the list stays scannable on a tall screen.
// The floor keeps a very short window showing something rather than nothing.
func (m tuiModel) menuRows() int {
	const maxRows = 8
	if m.height <= 0 {
		return maxRows // size not reported yet (see Init)
	}
	return max(3, min(maxRows, m.height-8))
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
	pendingTag := ""
	if n := len(m.pendingInputs); n > 0 {
		pendingTag = "  " + m.th.glyph("·", "|") + "  " +
			m.th.accent.Render(i18n.Tf(m.loc, "tui.queue.n", "n", strconv.Itoa(n)))
	}
	if m.mode == modeAsking {
		dur := elapsed(time.Since(m.started))
		tag := lipgloss.NewStyle().Bold(true).Foreground(m.th.breathingColor(m.animTick)).
			Render(m.th.glyph("⚡", "[!]") + " " + i18n.T(m.loc, "tui.status.running") + " · " + dur)
		if m.projName != "" {
			return tag + "  " + m.th.glyph("·", "|") + "  " + m.th.muted.Render(m.th.glyph("▪", "#")+" "+m.projName) + pendingTag
		}
		return tag + pendingTag
	}
	if m.r == nil {
		return pendingTag
	}
	sess := i18n.T(m.loc, "tui.ctx.bare")
	sessID, convoLen := m.r.sessID(), m.r.convoLen()
	switch {
	case sessID != "":
		sess = i18n.Tf(m.loc, "tui.ctx.session", "id", shortID(sessID))
	case convoLen/2 > 0:
		sess = i18n.Tf(m.loc, "tui.ctx.turns", "n", strconv.Itoa(convoLen/2))
	}
	parts := []string{sess}
	// Which project the next prompt belongs to is state that changes what an ask
	// does — the task lands in that project and runs in its tree — so it belongs
	// on the same line as the thread. The name comes from the cache, not the
	// store: this runs on every frame, including every cursor blink.
	if m.projName != "" {
		parts = append(parts, m.th.glyph("▪", "#")+" "+m.projName)
	}
	// The roster and the pending node pin are state: they change who may take
	// the next turn's work and where it runs. Quiet when neither is set.
	if len(m.team) > 0 {
		parts = append(parts, i18n.Tf(m.loc, "tui.ctx.team", "names", teamNamesShort(m.team)))
	}
	if m.nextNode != "" {
		parts = append(parts, i18n.Tf(m.loc, "tui.ctx.node", "node", m.nextNode))
	}
	if m.r.authorized() {
		parts = append(parts, i18n.T(m.loc, "repl.footer.authz")+":"+i18n.T(m.loc, "repl.footer.authz.on"))
	}
	return strings.Join(parts, "  "+m.th.glyph("·", "|")+"  ") + pendingTag
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
		// One slot describes the third key the user has right now. It is the
		// thought toggle normally; while lines are parked it is ctrl+d, because
		// "how do I take it back" is the question a visible queue raises, and a
		// key nobody is told about is the same as no key at all. Swapping in
		// place — rather than appending a fourth entry — keeps the legend at
		// the width the status row already budgets for: this list is joined
		// without shedding (see hintLine).
		//
		// ctrl+x (inject) takes that slot only when the queue is empty, because
		// drop would be a no-op message there; with lines parked, drop is the
		// actionable one. Enter always queues mid-turn, so its label is fixed.
		var third string
		if len(m.pendingInputs) > 0 {
			third = m.th.thoughtButton().Render("⌃D " + i18n.T(m.loc, "tui.hint.drop"))
		} else {
			third = m.th.steerButton().Render("⌃X " + i18n.T(m.loc, "tui.hint.inject"))
		}
		return []hint{
			{text: m.th.stopButton().Render(m.th.glyph("⏹", "[x]") + " Esc " + i18n.T(m.loc, "tui.hint.stop"))},
			{text: m.th.steerButton().Render(m.th.glyph("⏎", "[>]") + " Enter " + i18n.T(m.loc, "tui.hint.queue"))},
			{text: third},
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
// occupied by its choices and scope options. Rendering and hit testing consume
// this one layout, so localization, resize, padding, or border changes cannot
// leave stale hitboxes.
type approvalCardLayout struct {
	rendered string
	yes      tuiRect
	no       tuiRect
	scopes   [3]tuiRect
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
	scopeLabels := [3]string{
		i18n.T(m.loc, "tui.approval.scopeOnce"),
		i18n.T(m.loc, "tui.approval.scopeSession"),
		i18n.T(m.loc, "tui.approval.scopeProject"),
	}
	scopePrefix := i18n.T(m.loc, "tui.approval.scope") + " "
	var scopeTexts [3]string
	for i, sc := range approvalScopes {
		key := fmt.Sprint(i + 1)
		if m.approvalScope == sc {
			scopeTexts[i] = m.th.command.Render("["+key+"]") + " " + m.th.accent.Render(scopeLabels[i])
		} else {
			scopeTexts[i] = m.th.muted.Render("[" + key + "] " + scopeLabels[i])
		}
	}
	scopeRow := m.th.muted.Render(scopePrefix) + strings.Join(scopeTexts[:], "  ")
	sb.WriteString("\n\n" + yesText)
	sb.WriteString("   " + noText)
	sb.WriteString("\n" + scopeRow)
	sb.WriteString("\n" + m.th.muted.Render(m.th.glyph("↑↓", "^v")+" "+i18n.T(m.loc, "tui.approval.hint")))

	rendered := m.th.approval.Width(max(1, m.textWidth()-2)).Render(sb.String())
	lines := strings.Split(rendered, "\n")
	choiceRow, scopeRowIdx := -1, -1
	for i := len(lines) - 1; i >= 0; i-- {
		if choiceRow < 0 && strings.Contains(lines[i], "[y]") && strings.Contains(lines[i], "[n]") {
			choiceRow = i
			continue
		}
		if scopeRowIdx < 0 && strings.Contains(lines[i], "[1]") && strings.Contains(lines[i], "[3]") {
			scopeRowIdx = i
		}
	}
	layout := approvalCardLayout{rendered: rendered}
	// Border + horizontal padding precede the rendered choice content. The
	// choices themselves are measured from the exact strings rendered above.
	const origin = 2
	if choiceRow >= 0 {
		yesWidth := lipgloss.Width(yesText)
		noWidth := lipgloss.Width(noText)
		layout.yes = tuiRect{x: origin, y: choiceRow, w: yesWidth, h: 1}
		layout.no = tuiRect{x: origin + yesWidth + 3, y: choiceRow, w: noWidth, h: 1}
	}
	if scopeRowIdx >= 0 {
		x := origin + lipgloss.Width(scopePrefix)
		for i, st := range scopeTexts {
			layout.scopes[i] = tuiRect{x: x, y: scopeRowIdx, w: lipgloss.Width(st), h: 1}
			x += lipgloss.Width(st) + 2
		}
	}
	return layout
}

func (m tuiModel) approvalCard() string {
	if m.pending == nil || m.pending.Approval == nil {
		return ""
	}
	return m.approvalLayout().rendered
}

// confirmCard renders the generic yes/no prompt (modeConfirm) — the same
// boxed warning shape as the approval card, minus the scope row: what is about
// to happen, then the [y]/[n] answers. Any key that is not y declines.
func (m tuiModel) confirmCard() string {
	var sb strings.Builder
	sb.WriteString(m.th.warn.Render(m.th.glyph("⚠", "!") + " " + m.confirmText))
	yes := m.th.command.Render("[y]") + " " + i18n.T(m.loc, "tui.approval.yes")
	no := m.th.command.Render("[n]") + " " + i18n.T(m.loc, "tui.approval.no")
	sb.WriteString("\n\n  " + yes + "   " + no)
	sb.WriteString("\n" + m.th.muted.Render(i18n.T(m.loc, "tui.confirm.hint")))
	return m.th.approval.Width(max(1, m.textWidth()-2)).Render(sb.String())
}

// tuiRect is a half-open rectangle in terminal cells.
type tuiRect struct {
	x, y int
	w, h int
}

func (r tuiRect) contains(x, y int) bool {
	return r.w > 0 && r.h > 0 && x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

// approvalHit maps a terminal click to an approval choice and a scope option:
// choice is 0 = approve, 1 = deny, -1 = not on either option; scope is the
// index into approvalScopes or -1. The card is part of the live region — in
// modeApproving the live string is "\n" + card, so the card begins one row
// below the region's published top. approvalLayout supplies option cells
// relative to the rendered card, and this function only translates them into
// screen coordinates; a frame that never published a live region has no card
// to hit.
func (m tuiModel) approvalHit(x, y int) (choice, scope int) {
	choice, scope = -1, -1
	if m.pending == nil || m.pending.Approval == nil || m.frameGeom == nil || m.frameGeom.liveTop < 0 {
		return choice, scope
	}
	layout := m.approvalLayout()
	originY := m.frameGeom.liveTop + 1
	yes := layout.yes
	no := layout.no
	yes.y += originY
	no.y += originY
	switch {
	case yes.contains(x, y):
		choice = 0
	case no.contains(x, y):
		choice = 1
	}
	for i, sc := range layout.scopes {
		sc.y += originY
		if sc.contains(x, y) {
			scope = i
		}
	}
	return choice, scope
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
		buttons[i] = tuiRect{x: x, y: m.frameRows() - 1, w: w, h: 1}
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
//   - Version, Working Directory
//   - Interactive tips (commands, file attach, help guide)
//   - A year of task activity as a heatmap, when a store was reachable —
//     the empty screen is the one place a grid can sit without competing
//     with a conversation. nil activity skips the section entirely.
//
// Node name and model are deliberately not shown: on a fresh install they
// would be the built-in defaults / hostname probe parading as the user's
// own configuration. They remain available via /context and the footer.
func renderWelcomeBanner(cfg *config.Config, loc i18n.Locale, width int, th theme, activity map[string]int) string {
	if width <= 0 {
		width = 80
	}
	uni := th.unicode

	workPath := ""
	if cfg != nil {
		workPath = cfg.Storage.WorkPath
	}

	// Every line is truncated to the terminal width itself — an overlong
	// line would wrap and grow the banner a row per wrap.
	contentWidth := max(20, width)
	var sb strings.Builder

	if width >= 76 {
		for i, line := range figlet("OpenPanda") {
			if i > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(th.accent.Render(line))
		}
		sb.WriteString("\n\n")
		// The figlet row above already spells the wordmark — repeat only the
		// version/codename, not the name a second time.
		sb.WriteString(th.heading.Render("  " + versionpkg.Display()))
	} else {
		sb.WriteString(th.heading.Render(cliui.Truncate(
			"  "+th.glyph("✻", "*")+" "+i18n.T(loc, "repl.banner.title")+" "+versionpkg.Display(), contentWidth, uni)))
	}

	// Every text line carries its own two-column indent inside the truncation
	// budget — the indent is part of the line, not free columns on top of it.
	sb.WriteString("\n")
	sb.WriteString(th.muted.Render(cliui.TruncateTail(
		"  "+i18n.Tf(loc, "repl.banner.dir", "dir", workPath), contentWidth, uni)))
	sb.WriteString("\n\n")
	sb.WriteString(th.muted.Render(cliui.Truncate(
		"  "+i18n.T(loc, "tui.welcome.tips"), contentWidth, uni)))

	// The REPL does not listen: a configured-to-mesh node (a shared secret
	// exists) whose identity lock is free has no daemon running, so peers can
	// neither reach it nor see its discovery beacon. This is the Windows
	// double-click trap — bare panda.exe looks "running" while the node is
	// offline to the fleet. A standalone install (no secret) is a legitimate
	// single-machine setup and stays quiet.
	if cfg != nil && cfg.Network.SharedSecret != "" {
		if held, err := nodeidentity.Held(cfg.Node.Kind, cfg.Node.EffectiveIdentity()); err == nil && !held {
			sb.WriteString("\n" + th.warn.Render(cliui.Truncate("  "+i18n.T(loc, "repl.banner.noDaemon"), contentWidth, uni)))
		}
	}

	if activity != nil {
		var hb strings.Builder
		// The grid is inset by the same two columns every text line carries.
		renderTaskHeatmap(&hb, loc, activity, time.Now(), heatmapMaxWeek, max(10, contentWidth-2), pal())
		sb.WriteString("\n")
		for _, line := range strings.Split(strings.TrimRight(hb.String(), "\n"), "\n") {
			sb.WriteString("\n  " + line)
		}
	}

	if isLinuxConsole() {
		sb.WriteString("\n" + th.warn.Render(cliui.Truncate("  ! bare console font has no CJK glyphs; answers are forced to English.", contentWidth, uni)))
		sb.WriteString("\n" + th.warn.Render(cliui.Truncate("    for Chinese on this screen: sudo apt install fbterm fonts-wqy-zenhei && fbterm", contentWidth, uni)))
	}

	return strings.TrimRight(sb.String(), "\n")
}

// welcomeCache memoizes the rendered banner. The banner is static between
// config edits, activity refreshes (30s TTL) and terminal resizes, so the
// figlet + year-heatmap + daemon-lock probe it used to build every frame —
// including a nodeidentity.Held file-lock syscall — now costs a key compare.
type welcomeCache struct {
	key string
	out string
}

// welcome is the startup banner rendered at the top of the frame: the wordmark,
// version, working directory and orientation tips.
func (m tuiModel) welcome() string {
	w := m.width
	if w <= 0 {
		w = 80
	}
	if m.r == nil {
		return renderWelcomeBanner(nil, m.loc, w, m.th, nil)
	}
	activity := m.activity.get(m.r)
	day := time.Now().Format("2006-01-02") // the heatmap anchors to today
	var cfgKey string
	// Fingerprint only the cfg fields the banner reads — work path, shared
	// secret presence, node identity — so a /project or /model edit invalidates
	// while an unrelated toggle keeps the memo.
	m.r.readConfig(func(c *config.Config) {
		cfgKey = c.Storage.WorkPath + "|" + c.Node.Kind + "|" +
			c.Node.EffectiveIdentity() + "|" + strconv.FormatBool(c.Network.SharedSecret != "")
	})
	var fetched int64
	if m.activity != nil {
		fetched = m.activity.fetched.Unix()
	}
	key := fmt.Sprintf("%d|%s|%s|%s|%d|%t", w, m.loc, cfgKey, day, fetched, m.th.unicode)
	if m.welcomeC != nil && m.welcomeC.key == key {
		return m.welcomeC.out
	}
	var banner string
	m.r.readConfig(func(c *config.Config) { banner = renderWelcomeBanner(c, m.loc, w, m.th, activity) })
	if m.welcomeC != nil {
		m.welcomeC.key, m.welcomeC.out = key, banner
	}
	return banner
}

// textWidth is the terminal-bounded width available to top-level TUI rows. A
// reported tiny width is authoritative: decoration must shed or clip rather than
// inventing columns beyond the physical screen.
//
// With the task rail open it is the width LEFT of the rail, minus the one column
// that separates them: the transcript and the composer sit in that column, and a
// line that ran under the rail would be a line nobody can read.
func (m tuiModel) textWidth() int {
	if m.width <= 0 {
		return 76
	}
	if w := m.sidebarWidth(); w > 0 {
		return max(1, m.width-w-1)
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
