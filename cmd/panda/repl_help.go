package main

// `/help` — the REPL's orientation page.
//
// It used to pipe a flat 24-line list through $PAGER: the screen was taken
// over, the banner and the conversation scrolled away, and quitting less left
// the user wondering whether anything had happened. A command list is not a
// document — it is a glance. So it prints inline, grouped by what the user is
// trying to do (talk, drive tasks, inspect memory, run the node), tinted the
// same way `panda help` is, and ends with the keys and prefixes that are
// otherwise invisible: @file, !command, !!, Ctrl-R.

import (
	"fmt"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// helpGroups is the display order of the command groups, paired with the i18n
// key of each heading. A command's group comes from replCmd.group; anything
// with an unknown group falls into the last one, so a new command is listed
// even if its author forgets to classify it.
var helpGroups = []struct{ group, key string }{
	{"chat", "repl.help.groups.chat"},
	{"tasks", "repl.help.groups.tasks"},
	{"memory", "repl.help.groups.memory"},
	{"system", "repl.help.groups.system"},
}

// cmdHelp prints the grouped command reference plus the shortcut cheat sheet.
func (r *repl) cmdHelp(arg string) {
	p := pal()
	// A specific command was named (`/help tasks`): print just that line.
	if name := strings.TrimPrefix(strings.TrimSpace(arg), "/"); name != "" {
		for _, c := range replCommands {
			if c.name == name {
				fmt.Println("  " + p.Command("/"+c.name) + "  " + i18n.T(r.loc, c.help))
				return
			}
		}
		if s := suggest(name, commandNames()); s != "" {
			fmt.Println(i18n.Tf(r.loc, "repl.didyoumean", "cmd", "/"+s))
			return
		}
		fmt.Println(i18n.Tf(r.loc, "repl.unknown", "cmd", "/"+name))
		return
	}

	// The command column is sized once for the whole page so descriptions line
	// up across groups — a ragged right edge is what made the flat list hard to
	// scan in the first place.
	width := 0
	for _, c := range replCommands {
		if n := cliui.DisplayWidth(c.name) + 1; n > width {
			width = n
		}
	}
	fmt.Println()
	fmt.Println(p.Heading(i18n.T(r.loc, "repl.help") + ":"))
	for i, g := range helpGroups {
		last := i == len(helpGroups)-1
		var lines []string
		for _, c := range replCommands {
			if c.group == g.group || (last && !knownHelpGroup(c.group)) {
				lines = append(lines, "    "+p.Command(pad("/"+c.name, width))+"  "+i18n.T(r.loc, c.help))
			}
		}
		if len(lines) == 0 {
			continue
		}
		fmt.Println()
		fmt.Println("  " + p.Bold(i18n.T(r.loc, g.key)))
		for _, l := range lines {
			fmt.Println(l)
		}
	}

	fmt.Println()
	fmt.Println("  " + p.Bold(i18n.T(r.loc, "repl.help.shortcuts")))
	for _, key := range []string{"repl.help.at", "repl.help.bang", "repl.help.bangbang"} {
		fmt.Println("    " + i18n.T(r.loc, key))
	}
	fmt.Println()
	fmt.Println("  " + p.Bold(i18n.T(r.loc, "repl.help.keys")))
	for _, key := range []string{"repl.help.tab", "repl.help.ctrlr", "repl.help.esc", "repl.help.ctrlc", "repl.help.ctrlc2"} {
		fmt.Println("    " + i18n.T(r.loc, key))
	}
	fmt.Println()
}

// knownHelpGroup reports whether g is one of the declared groups.
func knownHelpGroup(g string) bool {
	for _, h := range helpGroups {
		if h.group == g {
			return true
		}
	}
	return false
}

// pad right-pads s to n display columns (plain text only — a styled string
// would have its escape bytes counted; style after padding, not before).
func pad(s string, n int) string {
	if d := n - cliui.DisplayWidth(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// commandNames lists the slash command names without their slash, for suggest.
func commandNames() []string {
	names := make([]string, 0, len(replCommands))
	for _, c := range replCommands {
		names = append(names, c.name)
	}
	return names
}
