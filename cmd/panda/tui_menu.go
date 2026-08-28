package main

// The filterable slash-command menu. When the user starts a line with "/" the
// model opens this popup above the input: the full slash-command table filtered
// live by what has been typed, arrow-navigable, Enter/Tab to pick. It reuses the
// exact replCommands table the classic loop dispatches, so the two front ends
// never drift on which commands exist or what they do — the menu is a discovery
// affordance over the same handlers, not a second command registry.

import (
	"sort"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// menuItem is one selectable command: its "/name" and the one-line help shown
// beside it (the same i18n help string the /help screen prints).
type menuItem struct {
	name string // includes the leading slash, e.g. "/tasks"
	desc string
}

// slashMenu is the popup state. all is the full command list built once from
// replCommands; items is the current filtered view; sel indexes items. active
// tracks whether the popup is showing (the input is a bare "/token").
type slashMenu struct {
	active bool
	all    []menuItem
	items  []menuItem
	sel    int
}

// newSlashMenu builds the menu's static command list from the dispatch table,
// resolving each help line in the session locale. Order follows replCommands
// (help-display order), so the menu reads like the /help screen.
func newSlashMenu(loc i18n.Locale) slashMenu {
	items := make([]menuItem, 0, len(replCommands))
	for _, c := range replCommands {
		items = append(items, menuItem{name: "/" + c.name, desc: i18n.T(loc, c.help)})
	}
	return slashMenu{all: items}
}

// menuTrigger reports whether an input line should open the menu: a leading "/"
// with no whitespace yet (the user is still typing the command token, not its
// arguments). Once a space is typed the popup closes and the line is a command
// with args.
func menuTrigger(input string) bool {
	if !strings.HasPrefix(input, "/") {
		return false
	}
	return !strings.ContainsAny(input, " \t")
}

// sync recomputes the filtered list from the current input and clamps the
// selection. The filter is the text after the leading slash; matches are ranked
// prefix-first then substring, so "/ta" surfaces /tasks and /task ahead of a
// command that merely contains "ta". An empty filter shows every command.
func (mn *slashMenu) sync(input string) {
	mn.active = menuTrigger(input)
	if !mn.active {
		mn.items = nil
		mn.sel = 0
		return
	}
	filter := strings.ToLower(strings.TrimPrefix(input, "/"))
	type ranked struct {
		it   menuItem
		rank int // 0 = prefix match, 1 = substring match
		ord  int // original table order, for a stable tie-break
	}
	var out []ranked
	for i, it := range mn.all {
		name := strings.ToLower(strings.TrimPrefix(it.name, "/"))
		switch {
		case filter == "":
			out = append(out, ranked{it, 0, i})
		case strings.HasPrefix(name, filter):
			out = append(out, ranked{it, 0, i})
		case strings.Contains(name, filter):
			out = append(out, ranked{it, 1, i})
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].rank != out[b].rank {
			return out[a].rank < out[b].rank
		}
		return out[a].ord < out[b].ord
	})
	mn.items = mn.items[:0]
	for _, r := range out {
		mn.items = append(mn.items, r.it)
	}
	if mn.sel >= len(mn.items) {
		mn.sel = len(mn.items) - 1
	}
	if mn.sel < 0 {
		mn.sel = 0
	}
}

// move steps the selection by delta, clamped to the filtered list — no wrap, so
// holding a key rests at the ends rather than cycling past them.
func (mn *slashMenu) move(delta int) {
	if len(mn.items) == 0 {
		return
	}
	mn.sel += delta
	if mn.sel < 0 {
		mn.sel = 0
	}
	if mn.sel >= len(mn.items) {
		mn.sel = len(mn.items) - 1
	}
}

// selected returns the highlighted command name (with slash), or "" when the
// filter matched nothing.
func (mn *slashMenu) selected() string {
	if !mn.active || len(mn.items) == 0 || mn.sel < 0 || mn.sel >= len(mn.items) {
		return ""
	}
	return mn.items[mn.sel].name
}

// close dismisses the popup without changing the input.
func (mn *slashMenu) close() {
	mn.active = false
	mn.items = nil
	mn.sel = 0
}

// render draws the popup: one row per matching command, the selected row marked
// and accented, the rest dim, the help text muted. It is capped to a handful of
// rows so a long list never pushes the input off a short terminal.
func (mn *slashMenu) render(t theme, width int) string {
	if !mn.active || len(mn.items) == 0 {
		return ""
	}
	const maxRows = 8
	// Keep the selected row inside the visible window when the list is long.
	start := 0
	if mn.sel >= maxRows {
		start = mn.sel - maxRows + 1
	}
	end := min(start+maxRows, len(mn.items))

	var sb strings.Builder
	for i := start; i < end; i++ {
		it := mn.items[i]
		// Style is applied after width math on the plain text, so the rune counts
		// stay accurate — truncating a string that already holds ANSI escapes would
		// cut mid-sequence and miscount its display width.
		marker := "  "
		name := t.command.Render(it.name)
		if i == mn.sel {
			marker = t.accent.Render(t.glyph("❯", ">") + " ")
			name = t.heading.Render(it.name)
		}
		row := marker + name
		// Fit the help text into whatever the marker and name leave; drop it
		// entirely on a very narrow terminal rather than force a wrap.
		budget := max(20, width) - 2 - len([]rune(it.name)) - 1
		if desc := strings.TrimSpace(it.desc); desc != "" && budget > 4 {
			row += " " + t.muted.Render(truncate(desc, budget))
		}
		sb.WriteString(row)
		if i < end-1 {
			sb.WriteString("\n")
		}
	}
	if len(mn.items) > end-start {
		sb.WriteString("\n" + t.muted.Render(t.glyph("…", "...")))
	}
	return sb.String()
}
