package main

import (
	"fmt"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/charmbracelet/lipgloss"
)

// SelectionItem represents one selectable entry in a SelectionList.
type SelectionItem struct {
	ID      string // Internal identifier (e.g. session hash ID, project name, model alias)
	Index   int    // 1-based index (序号)
	Title   string // Main title/name
	Time    string // Formatted timestamp (e.g. 2026-09-06 10:00)
	Turns   int    // Turn count for sessions (-1 if not applicable)
	Snippet string // Message summary or snippet
	WorkDir string // Working directory path
	Badge   string // Status badge (e.g. "[当前]")
	Value   any    // Arbitrary underlying data
}

// SelectionList is a reusable keyboard-navigable list component.
type SelectionList struct {
	Title       string
	Header      string
	Items       []SelectionItem
	Cursor      int
	Top         int
	EmptyText   string
	FooterHints string
	ActionHints string
	Boxed       bool
}

// NewSelectionList creates an initialized SelectionList.
//
// FooterHints is left empty: the renderers localize the legend through the
// theme's locale when the caller does not set one. It used to hold a hardcoded
// Chinese legend here and again as a fallback in each renderer, which meant an
// English or Japanese list showed Chinese unless every call site remembered to
// override it.
func NewSelectionList(title string, items []SelectionItem) SelectionList {
	return SelectionList{
		Title:  title,
		Items:  items,
		Cursor: 0,
		Top:    0,
	}
}

// MoveUp moves the selection highlight up by one item.
func (s *SelectionList) MoveUp() {
	if len(s.Items) == 0 {
		return
	}
	if s.Cursor > 0 {
		s.Cursor--
		if s.Cursor < s.Top {
			s.Top = s.Cursor
		}
	}
}

// MoveDown moves the selection highlight down by one item.
func (s *SelectionList) MoveDown(visibleRows int) {
	if len(s.Items) == 0 {
		return
	}
	if s.Cursor < len(s.Items)-1 {
		s.Cursor++
		if visibleRows > 0 && s.Cursor >= s.Top+visibleRows {
			s.Top = s.Cursor - visibleRows + 1
		}
	}
}

// MovePage moves the highlight by delta rows and re-glues the viewport window to
// it, clamped to the list ends. visibleRows is the caller's render budget (the
// same value it passes to MoveDown), so the window always keeps the cursor on
// screen.
//
// delta is a plain row count, not a fixed page: PgUp/PgDn pass one full page
// (delta == visibleRows) while a wheel notch passes a few rows, so one clamping
// and re-gluing path serves both. The name comes from the paging case, which is
// where it started.
func (s *SelectionList) MovePage(delta, visibleRows int) {
	if len(s.Items) == 0 || visibleRows <= 0 {
		return
	}
	s.Cursor += delta
	if s.Cursor < 0 {
		s.Cursor = 0
	}
	if maxCursor := len(s.Items) - 1; s.Cursor > maxCursor {
		s.Cursor = maxCursor
	}
	if s.Cursor < s.Top {
		s.Top = s.Cursor
	}
	if s.Cursor >= s.Top+visibleRows {
		s.Top = s.Cursor - visibleRows + 1
	}
}

// Selected returns the currently highlighted item, or false if the list is empty.
func (s *SelectionList) Selected() (SelectionItem, bool) {
	if len(s.Items) == 0 || s.Cursor < 0 || s.Cursor >= len(s.Items) {
		return SelectionItem{}, false
	}
	return s.Items[s.Cursor], true
}

// SetItems updates the list items and bounds the cursor.
func (s *SelectionList) SetItems(items []SelectionItem) {
	s.Items = items
	if s.Cursor >= len(items) {
		s.Cursor = max(0, len(items)-1)
	}
	if s.Top > s.Cursor {
		s.Top = s.Cursor
	}
}

// boxedVisibleCap bounds the item window inside the rounded box. The box
// exists to focus a small choice, but provider/model catalogues run a dozen
// rows — capping lower would force scrolling on every terminal.
const boxedVisibleCap = 12

// VisibleRows is how many item rows Render draws at the given terminal
// height. Navigation (MoveDown / MovePage callers) MUST budget with the same
// value: when the two disagree the cursor walks past the rendered window and
// the list appears frozen — the bug this exists to prevent. Keep the two
// branch formulas in sync with renderPlain's and renderBoxed's chrome.
func (s SelectionList) VisibleRows(height int) int {
	if height <= 0 {
		height = 24
	}
	if s.Boxed {
		return max(3, min(boxedVisibleCap, height-10))
	}
	headerH := 0
	if s.Title != "" {
		headerH++
	}
	if s.Header != "" {
		headerH++
	}
	if headerH > 0 {
		headerH++ // blank separator line
	}
	footerH := 3 // blank + footer hints + bottom margin
	if s.ActionHints != "" {
		footerH += 2
	}
	return max(3, height-headerH-footerH)
}

// Render draws the selection list to the given width and height.
func (s SelectionList) Render(th theme, width, height int) string {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}

	if s.Boxed {
		return s.renderBoxed(th, width, height)
	}
	return s.renderPlain(th, width, height)
}

// renderPlain draws the list in full-screen unboxed mode (used for /sessions, /projects, /resume, provider list).
func (s SelectionList) renderPlain(th theme, width, height int) string {
	var lines []string

	// Title
	if s.Title != "" {
		lines = append(lines, th.heading.Render(s.Title))
	}
	if s.Header != "" {
		lines = append(lines, th.muted.Render(s.Header))
	}
	if s.Title != "" || s.Header != "" {
		lines = append(lines, "")
	}

	// Visible item budget shared with navigation via VisibleRows.
	visibleRows := s.VisibleRows(height)
	footerHeight := 3 // blank line + footer hints + bottom margin
	if s.ActionHints != "" {
		footerHeight += 2
	}

	if len(s.Items) == 0 {
		emptyMsg := s.EmptyText
		if emptyMsg == "" {
			emptyMsg = i18n.T(th.loc, "tui.list.empty")
		}
		lines = append(lines, "  "+th.muted.Render(emptyMsg))
	} else {
		top := s.Top
		if top < 0 {
			top = 0
		}
		end := min(len(s.Items), top+visibleRows)
		for i := top; i < end; i++ {
			item := s.Items[i]
			isCursor := i == s.Cursor
			line := s.formatItemLine(th, item, isCursor, width)
			lines = append(lines, line)
		}
	}

	// Add spacing before footer
	for len(lines) < height-footerHeight {
		lines = append(lines, "")
	}

	if s.ActionHints != "" {
		lines = append(lines, "")
		lines = append(lines, "  "+th.accent.Render(s.ActionHints))
	}

	lines = append(lines, "")
	footer := s.FooterHints
	if footer == "" {
		footer = i18n.T(th.loc, "tui.list.footerHints")
	}
	lines = append(lines, th.muted.Render("  "+footer))

	return strings.Join(lines, "\n")
}

// renderBoxed draws the list enclosed in a stylish rounded border box.
func (s SelectionList) renderBoxed(th theme, width, height int) string {
	boxWidth := min(max(40, width-8), 64)

	var innerLines []string
	innerLines = append(innerLines, "") // breathing room

	visibleRows := s.VisibleRows(height)
	if len(s.Items) == 0 {
		emptyMsg := s.EmptyText
		if emptyMsg == "" {
			emptyMsg = i18n.T(th.loc, "tui.list.empty")
		}
		innerLines = append(innerLines, "  "+th.muted.Render(emptyMsg))
	} else {
		top := s.Top
		if top < 0 {
			top = 0
		}
		end := min(len(s.Items), top+visibleRows)
		innerW := boxWidth - 6 // borders + padding + row prefix
		for i := top; i < end; i++ {
			item := s.Items[i]
			isCursor := i == s.Cursor
			prefix := "  "
			if isCursor {
				prefix = th.accent.Render("> ")
			}
			title := item.Title
			badge := ""
			if item.Badge != "" {
				badge = " " + th.accent.Render(item.Badge)
			}
			line := prefix + title + badge
			if item.Snippet != "" {
				used := cliui.DisplayWidth(item.Title) + cliui.DisplayWidth(item.Badge)
				if snipW := innerW - used - 2; snipW >= 8 {
					line += "  " + th.muted.Render(cliui.Truncate(item.Snippet, snipW, th.unicode))
				}
			}
			innerLines = append(innerLines, line)
		}
	}

	innerLines = append(innerLines, "")
	if s.ActionHints != "" {
		innerLines = append(innerLines, "  "+th.command.Render(s.ActionHints))
		innerLines = append(innerLines, "")
	}

	footer := s.FooterHints
	if footer == "" {
		footer = i18n.T(th.loc, "tui.model.footerHints")
	}
	// Overflowing lists get a position counter so a scrolled window is
	// obviously a window, not the whole catalogue.
	if len(s.Items) > visibleRows {
		footer += fmt.Sprintf("   %d/%d", s.Cursor+1, len(s.Items))
	}
	innerLines = append(innerLines, "  "+th.muted.Render(footer))
	innerLines = append(innerLines, "")

	content := strings.Join(innerLines, "\n")
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("35")).
		Padding(0, 1).
		Width(boxWidth)

	// Add custom title to border
	renderedBox := boxStyle.Render(content)
	if s.Title != "" {
		lines := strings.Split(renderedBox, "\n")
		if len(lines) > 0 {
			titleText := " " + s.Title + " "
			topBorder := lines[0]
			if len(topBorder) > len(titleText)+4 {
				runes := []rune(topBorder)
				titleRunes := []rune(titleText)
				copy(runes[2:], titleRunes)
				lines[0] = string(runes)
				renderedBox = strings.Join(lines, "\n")
			}
		}
	}

	// Center horizontally and vertically
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, renderedBox)
}

// formatItemLine formats a single item for plain list rendering.
func (s SelectionList) formatItemLine(th theme, item SelectionItem, isCursor bool, termWidth int) string {
	cursorPrefix := "  "
	if isCursor {
		cursorPrefix = th.accent.Render("> ")
	}

	idxStr := ""
	if item.Index > 0 {
		idxStr = fmt.Sprintf("%d. ", item.Index)
	}

	badge := ""
	if item.Badge != "" {
		badge = " " + th.accent.Render(item.Badge)
	}

	// Project format: Index, Name, WorkDir, [Badge]
	if item.WorkDir != "" {
		var parts []string
		parts = append(parts, idxStr+item.Title+badge)
		dirW := max(20, termWidth-40)
		parts = append(parts, th.muted.Render(cliui.TruncateTail(item.WorkDir, dirW, th.unicode)))
		content := strings.Join(parts, "  ")
		if isCursor {
			return cursorPrefix + lipgloss.NewStyle().Bold(true).Render(content)
		}
		return cursorPrefix + content
	}

	// Session format: Index, Time, Turns, Snippet, [Badge], Truncated ID
	if item.Time != "" || item.Snippet != "" {
		var parts []string
		parts = append(parts, idxStr+item.Title+badge)
		if item.Time != "" {
			parts = append(parts, th.muted.Render(item.Time))
		}
		if item.Turns >= 0 {
			parts = append(parts, th.muted.Render(fmt.Sprintf("%d turns", item.Turns)))
		}
		if item.Snippet != "" {
			// Limit snippet length so line fits comfortably
			snippet := item.Snippet
			maxSnippetW := max(15, termWidth-55)
			snippet = cliui.Truncate(snippet, maxSnippetW, th.unicode)
			parts = append(parts, `"`+snippet+`"`)
		}
		if item.ID != "" && len(item.ID) > 6 {
			parts = append(parts, th.muted.Render(item.ID[:6]+"..."))
		}
		content := strings.Join(parts, "  ")
		if isCursor {
			return cursorPrefix + lipgloss.NewStyle().Bold(true).Render(content)
		}
		return cursorPrefix + content
	}

	// Default simple format
	content := idxStr + item.Title + badge
	if isCursor {
		return cursorPrefix + lipgloss.NewStyle().Bold(true).Render(content)
	}
	return cursorPrefix + content
}
