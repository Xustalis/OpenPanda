// SPDX-License-Identifier: AGPL-3.0-or-later

package nodecli

// The human help page and the shell-completion vocabulary live here for the
// same reason the agent reference does: they describe the same CLI, and when
// they were hand-maintained in cmd/panda the page, the completion scripts and
// the dispatcher could drift apart silently. cmd/panda now renders this data
// and keeps only the layout code.

// HelpRow is one line of `panda help`. Cmd is the text in the command column,
// without the page's two-space indent (the renderer adds it); an empty Cmd
// continues the previous entry's column, which is how the page breaks a long
// spelling over two lines. An empty HelpKey prints the command line alone,
// with its description on the following row.
type HelpRow struct {
	Cmd     string
	HelpKey string
}

// HelpSection is one titled block of the page.
type HelpSection struct {
	Group Group
	// Col is the description column: the page uses a narrow column for the
	// runtime block and a wide one wherever command spellings run long.
	Col  int
	Rows []HelpRow
}

// helpCol mirrors printUsage's two layout widths. The narrow column keeps the
// common entries scannable; long spellings get one space after them and the
// wider column so their descriptions still line up.
const (
	helpColNarrow = 25
	helpColWide   = 44
)

// helpPage is the page in print order. The order is curated for a human
// reading top to bottom (kernel and mesh first, then sessions and tasks,
// settings, observability, setup); it deliberately differs from the catalog's
// agent-facing order.
var helpPage = []HelpSection{
	{Group: GroupRuntime, Col: helpColNarrow, Rows: []HelpRow{
		{"(no subcommand)", "cli.help.d.nosub"},
		{"daemon", "cli.help.d.daemon"},
		{"nodes", "cli.help.d.nodes"},
		{"nodes add <host:port>", "cli.help.d.nodes_add"},
		{"nodes invite", "cli.help.d.nodes_invite"},
		{"nodes admit <id>", "cli.help.d.nodes_admit"},
		{"nodes verify <id>", "cli.help.d.nodes_verify"},
		{"nodes drain [id]", "cli.help.d.nodes_drain"},
		{"nodes prune", "cli.help.d.nodes_prune"},
		{"nodes remove <id>", "cli.help.d.nodes_remove"},
		{"nodes trust <id>", "cli.help.d.nodes_trust"},
		{"nodes disconnect <a>", "cli.help.d.nodes_disconnect"},
		{"pair --secret S --peer <host:port>", ""},
		{"", "cli.help.d.pair"},
		{"join <uri>", "cli.help.d.join"},
		{"ask <text>", "cli.help.d.ask"},
		{"repl", "cli.help.d.repl"},
		{"web", "cli.help.d.web"},
		{"     --lan", "cli.help.d.web_lan"},
		{"voice [--once] [--mute]", "cli.help.d.voice"},
		{"read <file> | <file.md>", "cli.help.d.read"},
		{"hooks [list | test <event>]", "cli.help.d.hooks"},
	}},
	{Group: GroupSessions, Col: helpColWide, Rows: []HelpRow{
		{"session list|new|show|rm|ask|diff|merge", ""},
		{"        |fork|tree", "cli.help.d.session"},
	}},
	{Group: GroupTasks, Col: helpColWide, Rows: []HelpRow{
		{"queue [--state s] [--project p] [--watch]", "cli.help.d.queue"},
		{"task <id>", "cli.help.d.task"},
		{"task add --title T [--prompt P] [--priority low|medium|normal|high|critical]", ""},
		{"         [--project p] [--authorize]", "cli.help.d.task_add"},
		{"         [--wait [--wait-timeout 30m]]", "cli.help.d.task_wait"},
		{"         [--agents a,b] [--mode parallel|serial]", ""},
		{"", "cli.help.d.task_agents"},
		{"task priority <id> <level>", "cli.help.d.task_priority"},
		{"task move <id> <seq>", "cli.help.d.task_move"},
		{"task artifact <id> [--list|--file|--out]", "cli.help.d.task_artifact"},
		{"task board <id>", "cli.help.d.task_board"},
		{"task send [--inject] <id> <text>", "cli.help.d.task_send"},
		{"cancel|approve|reject|logs <id>", "cli.help.d.task_verbs"},
		{"plan run <file.yaml> [--dry-run]", "cli.help.d.plan_run"},
		{"plan show <plan-id>", "cli.help.d.plan_show"},
		{"project list|create", "cli.help.d.project"},
	}},
	{Group: GroupMemory, Col: helpColWide, Rows: []HelpRow{
		{"memory list|get|set|rm [name]", "cli.help.d.memory"},
	}},
	{Group: GroupSettings, Col: helpColWide, Rows: []HelpRow{
		{"model [status|list|add|rm|switch|test]", "cli.help.d.model"},
		{"config model|mcp|limits|routing|injection|approval get|set|test", ""},
		{"", "cli.help.d.config"},
		{"agents [test <name>]", "cli.help.d.agents"},
		{"reminder list|add|rm", "cli.help.d.reminder"},
		{"todo <list|add|edit|done|rm|move|start>", "cli.help.d.todo"},
		{"skill list|find|hub|add|reset", "cli.help.d.skill"},
		{"mcp", "cli.help.d.mcp"},
		{"rpc", "cli.help.d.rpc"},
		{"auth login|status|logout <provider>", "cli.help.d.auth"},
		{"self status|queue|doctor|mint|propose|rebase|rollback|reset", ""},
		{"", "cli.help.d.self"},
	}},
	{Group: GroupObservability, Col: helpColWide, Rows: []HelpRow{
		{"status", "cli.help.d.status"},
		{"metrics [--csv|--runtime]", "cli.help.d.metrics"},
		{"heatmap [--weeks N]", "cli.help.d.heatmap"},
		{"audit verify [--task id]", "cli.help.d.audit_verify"},
		{"audit entries [--task id]", "cli.help.d.audit_entries"},
	}},
	{Group: GroupSetup, Col: helpColWide, Rows: []HelpRow{
		{"install|uninstall", "cli.help.d.install"},
		{"update [check|apply] | upgrade", "cli.help.d.update"},
		{"init [--defaults|--non-interactive]", "cli.help.d.init"},
		{"doctor", "cli.help.d.doctor"},
		{"detect", "cli.help.d.detect"},
		{"card show|rescan|edit|set", "cli.help.d.card"},
		{"card native|agent|manual add|remove|set", "cli.help.d.card_struct"},
		{"version|help", "cli.help.d.version"},
		{"completion bash|zsh|fish|powershell", "cli.help.d.completion"},
	}},
}

// HelpPage returns the page's sections in print order.
func HelpPage() []HelpSection {
	out := make([]HelpSection, len(helpPage))
	for i, sec := range helpPage {
		rows := make([]HelpRow, len(sec.Rows))
		copy(rows, sec.Rows)
		sec.Rows = rows
		out[i] = sec
	}
	return out
}

// GroupHeadingKey names the i18n key for a section's title.
func GroupHeadingKey(g Group) string { return groupHelpKey[g] }

// completionVerbs is the second-level vocabulary the completion scripts offer.
// Keys are canonical top-level command names; a verb whose command has no
// entry here completes flags and files instead.
var completionVerbs = map[string][]string{
	"nodes":    {"add", "disconnect", "invite", "remove", "prune", "verify", "admit", "drain", "trust"},
	"task":     {"add", "priority", "move", "approve", "reject", "cancel", "delete", "revert", "followup", "continue", "send", "board", "artifact", "logs", "show"},
	"plan":     {"run", "show", "example"},
	"session":  {"list", "new", "show", "mv", "rm", "fork", "tree", "ask", "diff", "merge", "revert"},
	"skill":    {"list", "approve", "reject", "reset", "find", "import", "hub", "install"},
	"model":    {"status", "list", "add", "remove", "switch", "fetch", "test"},
	"config":   {"get", "set", "test"},
	"memory":   {"list", "get", "set", "rm"},
	"project":  {"list", "new", "show", "enter", "exit", "rename", "rm", "approval"},
	"reminder": {"list", "add", "rm"},
	"todo":     {"list", "add", "edit", "show", "done", "reopen", "pause", "running", "rm", "move", "start"},
	"card":     {"show", "rescan", "edit", "set", "native", "agent", "manual", "invoke", "path"},
	"agents":   {"test", "install"},
	"auth":     {"login", "logout", "status"},
	"hooks":    {"list", "test"},
}

// CompletionVerbs returns the completion vocabulary. The map is a copy: a
// caller may drop entries without changing what the next caller sees.
func CompletionVerbs() map[string][]string {
	out := make(map[string][]string, len(completionVerbs))
	for cmd, verbs := range completionVerbs {
		out[cmd] = append([]string(nil), verbs...)
	}
	return out
}
