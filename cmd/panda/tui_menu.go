package main

// The filterable slash-command menu. When the user starts a line with "/" the
// model opens this popup under the input: the full slash-command table filtered
// live by what has been typed, arrow-navigable, Enter/Tab to pick. It reuses the
// exact replCommands table the classic loop dispatches, so the two front ends
// never drift on which commands exist or what they do — the menu is a discovery
// affordance over the same handlers, not a second command registry.
//
// Once a space follows the command ("/lang "), the same popup switches to the
// argument position: the candidates the repl's argResolver offers for the token
// under the cursor (locale codes, task ids, session ids, config enums), the
// same arrows + Enter/Tab to pick. The classic editor had Tab-only completion
// for arguments; the popup makes them visible and selectable instead of
// something to know about.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/providers"
)

// menuItem is one selectable row: a command ("/tasks") or an argument
// candidate ("zh-CN"), with the one-line help shown beside it when there is
// one (commands always have one; arguments usually do not — a task id needs
// no gloss, a locale gets its endonym).
type menuItem struct {
	name string
	desc string
}

// slashMenu is the popup state. all is the full command list built once from
// replCommands; items is the current filtered view; sel indexes items. active
// tracks whether the popup is showing (a bare "/token" or a "/cmd arg…"
// position with candidates). argMode marks the argument position, where rows
// are candidates rather than commands and picking one rewrites the token
// under the cursor; prefix holds the line up to that token for the rewrite.
type slashMenu struct {
	active  bool
	argMode bool
	prefix  string
	all     []menuItem
	items   []menuItem
	sel     int
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
// selection. In command mode (a bare "/token") the filter is the text after
// the leading slash; matches are ranked prefix-first then substring, so "/ta"
// surfaces /tasks and /task ahead of a command that merely contains "ta". An
// empty filter shows every command.
//
// Past the first space the menu asks resolve — the repl's argCandidates —
// for the candidates of the token under the cursor and lists those instead.
// resolve may be nil (tests, a repl without stores); the menu then stays
// closed at the argument position, exactly its old behaviour.
func (mn *slashMenu) sync(input string, resolve argResolver) {
	prevArg, sel := mn.argMode, mn.sel
	mn.active = false
	mn.argMode = false
	mn.prefix = ""
	mn.items = nil
	if !strings.HasPrefix(input, "/") || strings.Contains(input, "\n") {
		mn.sel = 0
		return
	}
	if menuTrigger(input) {
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
		for _, r := range out {
			mn.items = append(mn.items, r.it)
		}
		mn.active = true
	} else {
		// Argument position: list what the resolver offers for the token being
		// typed. argCandidatesFor does the parsing (which command, which slot,
		// which partial token); an empty list leaves the popup closed.
		token, cands := argCandidatesFor(input, resolve)
		if len(cands) > 0 {
			cmd := ""
			if sp := strings.IndexAny(input, " \t"); sp > 0 {
				cmd = strings.TrimPrefix(input[:sp], "/")
			}
			mn.active = true
			mn.argMode = true
			mn.prefix = input[:len(input)-len(token)]
			for _, c := range cands {
				mn.items = append(mn.items, menuItem{name: c, desc: argItemDesc(cmd, c)})
			}
		}
	}
	// Keep the selection where it was when the list merely narrowed around it;
	// crossing between the command and argument lists restarts at the top.
	if prevArg != mn.argMode {
		sel = 0
	}
	if sel >= len(mn.items) {
		sel = len(mn.items) - 1
	}
	if sel < 0 {
		sel = 0
	}
	mn.sel = sel
}

// argItemDesc glosses one argument candidate where a gloss helps: a locale
// code gets its endonym, model subcommands get explanations, providers get
// their human labels, and registered models show their model ID & context size.
func argItemDesc(cmd, cand string) string {
	if cmd == "lang" {
		return i18n.LocaleNames[i18n.Locale(cand)]
	}
	if cmd == "model" {
		switch cand {
		case "list":
			return "查看内置供应商 · list providers"
		case "add":
			return "注册供应商或模型 · register provider/model"
		case "remove", "rm", "del":
			return "移除注册模型 · drop model"
		case "fetch", "models":
			return "拉取远端模型列表 · list remote models"
		case "test":
			return "测试连通性 · test connectivity"
		}
		if p, ok := providers.Lookup(cand); ok {
			return p.Label + " · 默认: " + p.DefaultModel
		}
		if cfg, _ := config.Load(""); cfg != nil {
			for _, m := range cfg.Models {
				if m.Alias() == cand {
					desc := effectiveModel(m)
					if cw := effectiveContextWindow(m); cw > 0 {
						desc += fmt.Sprintf(" (%dk)", cw/1000)
					}
					if m.Alias() == cfg.Model.Alias() {
						desc += " ★"
					}
					return desc
				}
			}
			if cfg.Model.Alias() == cand && (cfg.Model.Model != "" || cfg.Model.Provider != "") {
				desc := effectiveModel(cfg.Model)
				if cw := effectiveContextWindow(cfg.Model); cw > 0 {
					desc += fmt.Sprintf(" (%dk)", cw/1000)
				}
				desc += " ★"
				return desc
			}
		}
	}
	return ""
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
// filter matched nothing. It is the command-mode query; argument mode uses
// fill, which rewrites the line instead of naming a command.
func (mn *slashMenu) selected() string {
	if !mn.active || mn.argMode || len(mn.items) == 0 || mn.sel < 0 || mn.sel >= len(mn.items) {
		return ""
	}
	return mn.items[mn.sel].name
}

// fill returns the completed input line for the highlighted row: the command
// plus a trailing space in command mode, or the line with the token under the
// cursor replaced by the highlighted candidate — trailing space again, so the
// next argument of a multi-argument command (/config set …) flows straight on.
func (mn *slashMenu) fill() string {
	if !mn.active || len(mn.items) == 0 || mn.sel < 0 || mn.sel >= len(mn.items) {
		return ""
	}
	if mn.argMode {
		return mn.prefix + mn.items[mn.sel].name + " "
	}
	return mn.items[mn.sel].name + " "
}

// close dismisses the popup without changing the input.
func (mn *slashMenu) close() {
	mn.active = false
	mn.argMode = false
	mn.prefix = ""
	mn.items = nil
	mn.sel = 0
}

// render draws the popup: one row per matching command, the selected row marked
// and accented, the rest dim, the help text muted. rows is the caller's ceiling on
// how many commands may show (the window's spare height, see menuRows), so a long
// list scrolls inside its window instead of outgrowing the terminal.
func (mn *slashMenu) render(t theme, width, rows int) string {
	if !mn.active || len(mn.items) == 0 || rows < 1 {
		return ""
	}
	// Keep the selected row inside the visible window when the list is long.
	start := 0
	if mn.sel >= rows {
		start = mn.sel - rows + 1
	}
	end := min(start+rows, len(mn.items))

	// The help text lines up in a column of its own: the longest visible command
	// sets the gutter, so the list reads as two columns instead of a ragged edge
	// where every description starts wherever its name happened to end.
	gutter := 0
	for i := start; i < end; i++ {
		if n := len([]rune(mn.items[i].name)); n > gutter {
			gutter = n
		}
	}

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
		row := marker + name + strings.Repeat(" ", gutter-len([]rune(it.name)))
		// Fit the help text into whatever the marker and gutter leave; drop it
		// entirely on a very narrow terminal rather than force a wrap.
		budget := max(20, width) - 2 - gutter - 2
		if desc := strings.TrimSpace(it.desc); desc != "" && budget > 4 {
			row += "  " + t.muted.Render(truncate(desc, budget))
		}
		sb.WriteString(strings.TrimRight(row, " "))
		if i < end-1 {
			sb.WriteString("\n")
		}
	}
	if rest := len(mn.items) - (end - start); rest > 0 {
		// Say how many are out of view rather than printing a bare ellipsis: the
		// count is what tells the user whether to keep typing or keep arrowing.
		sb.WriteString("\n" + t.muted.Render("  "+t.glyph("…", "...")+" "+
			i18n.Tf(t.loc, "tui.menu.more", "n", strconv.Itoa(rest))))
	}
	return sb.String()
}
