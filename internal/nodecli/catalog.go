// SPDX-License-Identifier: AGPL-3.0-or-later

// Package nodecli is the registry of the `panda` command surface, one
// description per consumer:
//
//   - Catalog is the agent-facing reference injected into task prompts —
//     every subcommand, its primary usage and a one-line purpose;
//   - helpPage is the human `panda help` page, with its own curated order and
//     layout rows;
//   - completionVerbs is the second-level vocabulary shell completion offers.
//
// They live together because they describe one CLI. When the page and the
// completion tables were hand-maintained in cmd/panda (alongside a third list
// for typo suggestions), adding a command meant editing three places and
// forgetting one failed silently; now a subcommand is named once, and tests
// keep the surfaces consistent with the dispatcher and with each other.
//
// The one-line descriptions reuse the `cli.help.d.*` strings, so every
// surface is localized in all five project languages and cannot drift from
// what the others say.
package nodecli

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// Group places a command on the reference page. The order here is the
// rendering order, and mirrors `panda help`'s sections so the two references
// read the same.
type Group string

const (
	GroupRuntime       Group = "runtime"
	GroupSessions      Group = "sessions"
	GroupTasks         Group = "tasks"
	GroupMemory        Group = "memory"
	GroupSettings      Group = "settings"
	GroupObservability Group = "observability"
	GroupSetup         Group = "setup"
)

// groupOrder is the display order of the sections.
var groupOrder = []Group{
	GroupRuntime, GroupSessions, GroupTasks, GroupMemory,
	GroupSettings, GroupObservability, GroupSetup,
}

// groupHelpKey maps a section to its localized heading. The headings are the
// help page's own, so a translator maintains one set of words.
var groupHelpKey = map[Group]string{
	GroupRuntime:       "cli.help.g.runtime",
	GroupSessions:      "cli.help.g.sessions",
	GroupTasks:         "cli.help.g.tasks",
	GroupMemory:        "cli.help.g.memory",
	GroupSettings:      "cli.help.g.settings",
	GroupObservability: "cli.help.g.observability",
	GroupSetup:         "cli.help.g.setup",
}

// Command is one line of the reference.
type Command struct {
	// Names lists every accepted spelling; Names[0] is canonical. Multi-word
	// names ("nodes add") are verb forms of a top-level command.
	Names []string
	// Usage is the command's primary invocation, flags included only where
	// they change what an agent would do. `panda help` carries the rest.
	Usage string
	// HelpKey names the i18n string summarizing the command.
	HelpKey string
	Group   Group
}

// catalog is the full surface, ordered for display. A subcommand added to
// cmd/panda's dispatcher without a row here fails cmd/panda's catalog test.
var catalog = []Command{
	// — runtime —
	{Names: []string{"(no subcommand)"}, Usage: "panda",
		HelpKey: "cli.help.d.nosub", Group: GroupRuntime},
	{Names: []string{"daemon", "serve"},
		Usage:   "panda daemon [--config F] [--card F]",
		HelpKey: "cli.help.d.daemon", Group: GroupRuntime},
	{Names: []string{"web"},
		Usage:   "panda web [--daemon] [--lan] [--no-browser]",
		HelpKey: "cli.help.d.web", Group: GroupRuntime},
	{Names: []string{"ask"},
		Usage:   "panda ask <text>",
		HelpKey: "cli.help.d.ask", Group: GroupRuntime},
	{Names: []string{"repl", "chat"},
		Usage:   "panda repl [--card F] [-y]",
		HelpKey: "cli.help.d.repl", Group: GroupRuntime},
	{Names: []string{"voice"},
		Usage:   "panda voice [--once] [--mute]",
		HelpKey: "cli.help.d.voice", Group: GroupRuntime},
	{Names: []string{"read", "view", "cat", "md", "markdown"},
		Usage:   "panda read <file>",
		HelpKey: "cli.help.d.read", Group: GroupRuntime},
	{Names: []string{"hooks"},
		Usage:   "panda hooks list|test <event>",
		HelpKey: "cli.help.d.hooks", Group: GroupRuntime},
	{Names: []string{"nodes"},
		Usage:   "panda nodes",
		HelpKey: "cli.help.d.nodes", Group: GroupRuntime},
	{Names: []string{"nodes add"},
		Usage:   "panda nodes add <host:port>",
		HelpKey: "cli.help.d.nodes_add", Group: GroupRuntime},
	{Names: []string{"nodes invite"},
		Usage:   "panda nodes invite [--uri]",
		HelpKey: "cli.help.d.nodes_invite", Group: GroupRuntime},
	{Names: []string{"nodes admit"},
		Usage:   "panda nodes admit <id>",
		HelpKey: "cli.help.d.nodes_admit", Group: GroupRuntime},
	{Names: []string{"nodes verify"},
		Usage:   "panda nodes verify <id>",
		HelpKey: "cli.help.d.nodes_verify", Group: GroupRuntime},
	{Names: []string{"nodes drain"},
		Usage:   "panda nodes drain [id] [--off]",
		HelpKey: "cli.help.d.nodes_drain", Group: GroupRuntime},
	{Names: []string{"nodes prune"},
		Usage:   "panda nodes prune [--offline-days N]",
		HelpKey: "cli.help.d.nodes_prune", Group: GroupRuntime},
	{Names: []string{"nodes remove", "nodes rm"},
		Usage:   "panda nodes remove <id>",
		HelpKey: "cli.help.d.nodes_remove", Group: GroupRuntime},
	{Names: []string{"nodes trust"},
		Usage:   "panda nodes trust <id> [mesh|guarded|open]",
		HelpKey: "cli.help.d.nodes_trust", Group: GroupRuntime},
	{Names: []string{"nodes disconnect", "nodes dc"},
		Usage:   "panda nodes disconnect <id>",
		HelpKey: "cli.help.d.nodes_disconnect", Group: GroupRuntime},
	{Names: []string{"pair"},
		Usage:   "panda pair --secret S --peer <host:port>",
		HelpKey: "cli.help.d.pair", Group: GroupRuntime},
	{Names: []string{"join"},
		Usage:   "panda join <uri> [--trust mesh|guarded|open]",
		HelpKey: "cli.help.d.join", Group: GroupRuntime},

	// — sessions —
	{Names: []string{"session", "sessions"},
		Usage:   "panda session list|new|show|rm|ask|diff|merge|fork|tree",
		HelpKey: "cli.help.d.session", Group: GroupSessions},

	// — tasks —
	{Names: []string{"queue"},
		Usage:   "panda queue [--state s] [--project p] [--watch]",
		HelpKey: "cli.help.d.queue", Group: GroupTasks},
	{Names: []string{"task"},
		Usage:   "panda task <id>",
		HelpKey: "cli.help.d.task", Group: GroupTasks},
	{Names: []string{"task add"},
		Usage:   "panda task add --title T [--prompt P] [--wait]",
		HelpKey: "cli.help.d.task_add", Group: GroupTasks},
	{Names: []string{"task priority"},
		Usage:   "panda task priority <id> <level>",
		HelpKey: "cli.help.d.task_priority", Group: GroupTasks},
	{Names: []string{"task move"},
		Usage:   "panda task move <id> <seq>",
		HelpKey: "cli.help.d.task_move", Group: GroupTasks},
	{Names: []string{"task artifact"},
		Usage:   "panda task artifact <id> [--list|--file|--out]",
		HelpKey: "cli.help.d.task_artifact", Group: GroupTasks},
	{Names: []string{"task board"},
		Usage:   "panda task board <id>",
		HelpKey: "cli.help.d.task_board", Group: GroupTasks},
	{Names: []string{"task send"},
		Usage:   "panda task send [--inject] <id> <text>",
		HelpKey: "cli.help.d.task_send", Group: GroupTasks},
	{Names: []string{"cancel", "approve", "reject", "logs"},
		Usage:   "panda cancel|approve|reject|logs <id>",
		HelpKey: "cli.help.d.task_verbs", Group: GroupTasks},
	{Names: []string{"plan"},
		Usage:   "panda plan run <file.yaml> [--dry-run] | panda plan show <id>",
		HelpKey: "cli.help.d.plan_run", Group: GroupTasks},
	{Names: []string{"project"},
		Usage:   "panda project list|create",
		HelpKey: "cli.help.d.project", Group: GroupTasks},
	{Names: []string{"todo", "todos"},
		Usage:   "panda todo list|add|edit|done|rm|move|start",
		HelpKey: "cli.help.d.todo", Group: GroupTasks},

	// — memory —
	{Names: []string{"memory"},
		Usage:   "panda memory list|get|set|rm [name]",
		HelpKey: "cli.help.d.memory", Group: GroupMemory},

	// — settings —
	{Names: []string{"model", "models"},
		Usage:   "panda model [status|list|add|rm|switch|test|discover]",
		HelpKey: "cli.help.d.model", Group: GroupSettings},
	{Names: []string{"config"},
		Usage:   "panda config <section> get|set|test",
		HelpKey: "cli.help.d.config", Group: GroupSettings},
	{Names: []string{"agents"},
		Usage:   "panda agents [test <name>]",
		HelpKey: "cli.help.d.agents", Group: GroupSettings},
	{Names: []string{"reminder"},
		Usage:   "panda reminder list|add|rm",
		HelpKey: "cli.help.d.reminder", Group: GroupSettings},
	{Names: []string{"skill"},
		Usage:   "panda skill list|find|hub|add|reset",
		HelpKey: "cli.help.d.skill", Group: GroupSettings},
	{Names: []string{"mcp"},
		Usage:   "panda mcp",
		HelpKey: "cli.help.d.mcp", Group: GroupSettings},
	{Names: []string{"rpc"},
		Usage:   "panda rpc [--card F]",
		HelpKey: "cli.help.d.rpc", Group: GroupSettings},
	{Names: []string{"auth"},
		Usage:   "panda auth login|status|logout <provider>",
		HelpKey: "cli.help.d.auth", Group: GroupSettings},
	{Names: []string{"self"},
		Usage:   "panda self status|queue|doctor|mint|propose|rebase|rollback|reset",
		HelpKey: "cli.help.d.self", Group: GroupSettings},

	// — observability —
	{Names: []string{"status"},
		Usage:   "panda status [--running]",
		HelpKey: "cli.help.d.status", Group: GroupObservability},
	{Names: []string{"metrics"},
		Usage:   "panda metrics [--csv] [--runtime]",
		HelpKey: "cli.help.d.metrics", Group: GroupObservability},
	{Names: []string{"heatmap"},
		Usage:   "panda heatmap [--weeks N]",
		HelpKey: "cli.help.d.heatmap", Group: GroupObservability},
	{Names: []string{"audit"},
		Usage:   "panda audit verify|entries [--task ID]",
		HelpKey: "cli.help.d.audit_verify", Group: GroupObservability},

	// — setup —
	{Names: []string{"install", "uninstall"},
		Usage:   "panda install [--dir DIR] [--no-path] | panda uninstall [--purge]",
		HelpKey: "cli.help.d.install", Group: GroupSetup},
	{Names: []string{"update", "upgrade"},
		Usage:   "panda update [check|apply] | panda upgrade [--pre]",
		HelpKey: "cli.help.d.update", Group: GroupSetup},
	{Names: []string{"init"},
		Usage:   "panda init [--defaults|--non-interactive] [--force]",
		HelpKey: "cli.help.d.init", Group: GroupSetup},
	{Names: []string{"doctor"},
		Usage:   "panda doctor",
		HelpKey: "cli.help.d.doctor", Group: GroupSetup},
	{Names: []string{"detect"},
		Usage:   "panda detect [-o capabilities.yaml]",
		HelpKey: "cli.help.d.detect", Group: GroupSetup},
	{Names: []string{"card"},
		Usage:   "panda card show|rescan|edit|set|invoke",
		HelpKey: "cli.help.d.card", Group: GroupSetup},
	{Names: []string{"card native", "card agent", "card manual"},
		Usage:   "panda card native|agent|manual add|remove|set",
		HelpKey: "cli.help.d.card_struct", Group: GroupSetup},
	{Names: []string{"version", "help"},
		Usage:   "panda version | panda help",
		HelpKey: "cli.help.d.version", Group: GroupSetup},
	{Names: []string{"completion"},
		Usage:   "panda completion bash|zsh|fish|powershell",
		HelpKey: "cli.help.d.completion", Group: GroupSetup},
}

// Catalog returns every command in display order. The rows and their Names
// slices are copies: a caller may sort or rewrite the result without
// disturbing the registry every other caller reads.
func Catalog() []Command {
	out := make([]Command, len(catalog))
	for i, c := range catalog {
		c.Names = append([]string(nil), c.Names...)
		out[i] = c
	}
	return out
}

// Names lists every accepted top-level subcommand spelling, sorted. It is the
// vocabulary cmd/panda hands to typo suggestions and shell completion
// (subcommandNames), so a new command is named once and every consumer sees
// it.
func Names() []string {
	var out []string
	for _, c := range catalog {
		for _, n := range c.Names {
			if !strings.Contains(n, " ") {
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Resolve finds the command a name names — a top-level subcommand or a verb
// form ("nodes add"). The empty string resolves to the bare `panda` entry.
func Resolve(name string) (Command, bool) {
	name = strings.TrimSpace(name)
	if name == "" || name == "(no subcommand)" {
		for _, c := range catalog {
			if c.Names[0] == "(no subcommand)" {
				return c, true
			}
		}
		return Command{}, false
	}
	for _, c := range catalog {
		for _, n := range c.Names {
			if n == name {
				return c, true
			}
		}
	}
	return Command{}, false
}

// joinWrapped flattens a help description's terminal hard wraps back into one
// line. The help page wraps for a terminal column; a prompt has no such
// column, and a description cut at its wrap point reads as a broken sentence
// ("…the card is auto-detected;" with the rest missing). CJK segments join
// without a space — the full-width punctuation already separates them.
func joinWrapped(desc string) string {
	lines := strings.Split(desc, "\n")
	out := strings.TrimSpace(lines[0])
	for _, next := range lines[1:] {
		next = strings.TrimSpace(next)
		if next == "" {
			continue
		}
		sep := " "
		if last, _ := utf8.DecodeLastRuneInString(out); last > utf8.RuneSelf {
			sep = ""
		}
		if first, _ := utf8.DecodeRuneInString(next); first > utf8.RuneSelf {
			sep = ""
		}
		out += sep + next
	}
	return out
}

// PromptReference renders the catalog as the compact, grouped block an agent
// prompt carries. Descriptions are the localized `panda help` one-liners,
// with the help page's terminal hard wraps flattened back into one line, and
// every usage aligned to a single column so the block scans as a table.
//
// Sections render in groupOrder rather than in catalog order: the heading is
// therefore emitted exactly once per group even if a future edit files a
// command out of order, and a group added without a row in groupOrder fails
// the catalog test instead of disappearing from the reference.
func PromptReference(loc i18n.Locale) string {
	width := 0
	for _, c := range catalog {
		if n := utf8.RuneCountInString(c.Usage); n > width {
			width = n
		}
	}
	var b strings.Builder
	b.WriteString("\n\n" + i18n.T(loc, "prompt.selfcmd.title"))
	for i, g := range groupOrder {
		var lines []string
		for _, c := range catalog {
			if c.Group != g {
				continue
			}
			desc := joinWrapped(i18n.T(loc, c.HelpKey))
			lines = append(lines, fmt.Sprintf("  %-*s  %s", width, c.Usage, desc))
		}
		if len(lines) == 0 {
			continue
		}
		if i > 0 && b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("\n" + i18n.T(loc, groupHelpKey[g]))
		for _, l := range lines {
			b.WriteString("\n" + l)
		}
	}
	return b.String()
}
