// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// `panda completion bash|zsh|fish` prints a static completion script to
// stdout — `source <(panda completion zsh)` or install it into the shell's
// completion dir (scripts/install.sh offers to). Static word lists, no
// runtime probing: a completer must never block the prompt on the network
// or the store.
//
// SYNC WARNING: the tables below are the dispatch switches in main.go and
// the runXxx verb routers written out as data. subcommandNames() has the
// same warning for the same reason — add a command in three places, or the
// new verb silently stops completing.

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

// completionVerbs maps a top-level command to its second-level verbs. Only
// commands with real subverbs get an entry — single-word commands (ask,
// queue, init) complete flags and files instead.
var completionVerbs = map[string][]string{
	"nodes":    {"add", "disconnect", "invite", "remove", "prune", "verify", "admit", "drain"},
	"task":     {"add", "priority", "move", "approve", "reject", "cancel", "delete", "logs", "show"},
	"plan":     {"run", "show", "example"},
	"session":  {"list", "new", "show", "mv", "rm", "fork", "tree", "ask", "diff", "merge"},
	"skill":    {"list", "approve", "reject", "reset", "find", "import", "hub", "install"},
	"model":    {"status", "list", "add", "remove", "switch", "fetch", "test"},
	"config":   {"get", "set", "test"},
	"memory":   {"list", "get", "set", "rm"},
	"project":  {"list", "new", "show", "enter", "exit", "rename", "rm", "approval"},
	"reminder": {"list", "add", "rm"},
	"card":     {"show", "rescan", "edit", "set", "native", "agent", "manual", "invoke", "path"},
	"agents":   {"test", "install"},
	"auth":     {"login", "logout", "status"},
}

// completionFlags holds the flags worth offering per context — key is the
// command line shape ("task add"), value the flag names. Flag *values* for
// the closed-set ones live in completionFlagValues below. Deeper positional
// completion (config sections, aliases, ids) is deliberately absent: a wrong
// suggestion is worse than none.
var completionFlags = map[string][]string{
	"ask":      {"--authorize", "--continue", "--project", "--output-format", "--config", "--card", "--mcp"},
	"daemon":   {"--config", "--card"},
	"queue":    {"--state", "--project", "--watch", "--json"},
	"task add": {"--title", "--prompt", "--priority", "--project", "--authorize", "--requires", "--agents", "--mode", "--preferred", "--nodes", "--action-spec", "--wait", "--wait-timeout", "--json"},
	"init":     {"--defaults", "--non-interactive", "--force", "--config", "--card"},
	"web":      {"--config", "--card"},
	"plan run": {"--dry-run", "--config", "--card"},
	"status":   {"--config"},
	"doctor":   {"--config"},
	"repl":     {"--config", "--card", "--mcp"},
}

// completionFlagValues maps a flag name to its closed value set — offered
// when the previous word is that flag.
var completionFlagValues = map[string][]string{
	"--state":         {"submitted", "queued", "dispatched", "waiting_context", "running", "review", "done", "failed", "cancelled", "expired"},
	"--priority":      {"low", "medium", "normal", "high", "critical"},
	"--mode":          {"parallel", "serial"},
	"--output-format": {"json", "stream-json"},
}

func runCompletion(args []string) {
	fs := flag.NewFlagSet("completion", flag.ExitOnError)
	fs.Parse(args)
	shell := ""
	if fs.NArg() > 0 {
		shell = strings.ToLower(fs.Arg(0))
	}
	var out string
	switch shell {
	case "bash":
		out = bashCompletion()
	case "zsh":
		out = zshCompletion()
	case "fish":
		out = fishCompletion()
	default:
		fmt.Fprintln(os.Stderr, "usage: panda completion bash|zsh|fish")
		fmt.Fprintln(os.Stderr, "  prints the completion script — e.g. `source <(panda completion zsh)`")
		os.Exit(2)
	}
	fmt.Print(out)
}

// completionWordLists renders the tables once per script: the top-level
// commands (subcommandNames is the source of truth) plus per-command verbs.
func completionTopWords() string {
	return strings.Join(subcommandNames(), " ")
}

func completionVerbsFor(cmd string) string {
	return strings.Join(completionVerbs[cmd], " ")
}

func completionFlagValueWords(flagName string) string {
	return strings.Join(completionFlagValues[flagName], " ")
}

func completionFlagsFor(key string) string {
	return strings.Join(completionFlags[key], " ")
}

// completionFlagValueArns emits one line per flag→value-set pair in the
// shell's own syntax: "<indent><flag><sep><values><close>".
func completionFlagValueArns(indent, sep, close string) string {
	var b strings.Builder
	for _, f := range []string{"--state", "--priority", "--mode", "--output-format"} {
		fmt.Fprintf(&b, "%s%s%s%s%s\n", indent, f, sep, completionFlagValueWords(f), close)
	}
	return b.String()
}

func bashCompletion() string {
	var b strings.Builder
	b.WriteString(`# bash completion for panda — source this file or drop it into
# ~/.local/share/bash-completion/completions/panda (or /etc/bash_completion.d/).
_panda() {
    local cur prev
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"

    # flag values: a closed set where the flag knows its vocabulary
    case "$prev" in
`)
	b.WriteString(completionFlagValueArns("        ", ") COMPREPLY=( $(compgen -W \"", "\" -- \"$cur\") ); return ;;"))
	b.WriteString(`        --config|-config|--card|-card) COMPREPLY=( $(compgen -f -- "$cur") ); return ;;
    esac

    # bare words so far (flags skipped): 0 → complete commands, 1 → the
    # command's verbs or flags, 2+ → flags of the "cmd verb" shape
    local -a bare=()
    local i
    for ((i=1; i<COMP_CWORD; i++)); do
        case "${COMP_WORDS[i]}" in
            -*) ;;
            *) bare+=("${COMP_WORDS[i]}") ;;
        esac
    done
    local sub="${bare[0]:-}"

    if [[ ${#bare[@]} -eq 0 ]]; then
        COMPREPLY=( $(compgen -W "` + completionTopWords() + `" -- "$cur") )
        return
    fi
    if [[ "$cur" == -* ]]; then
        local key="$sub"
        [[ ${#bare[@]} -ge 2 ]] && key="$sub ${bare[1]}"
        local fl="--help"
        case "$key" in
`)
	for _, key := range []string{"task add", "plan run", "ask", "daemon", "queue", "init", "web", "status", "doctor", "repl"} {
		fmt.Fprintf(&b, "            \"%s\") fl=\"%s\" ;;\n", key, completionFlagsFor(key))
	}
	b.WriteString(`        esac
        COMPREPLY=( $(compgen -W "$fl" -- "$cur") )
        return
    fi
    case "$sub" in
`)
	for _, cmd := range []string{"nodes", "task", "plan", "session", "sessions", "skill", "model", "models", "config", "memory", "project", "reminder", "card", "agents", "auth", "completion"} {
		words := completionVerbsFor(cmd)
		if cmd == "sessions" {
			words = completionVerbsFor("session")
		}
		if cmd == "models" {
			words = completionVerbsFor("model")
		}
		if cmd == "completion" {
			words = "bash zsh fish"
		}
		fmt.Fprintf(&b, "        %s) COMPREPLY=( $(compgen -W \"%s\" -- \"$cur\") ) ;;\n", cmd, words)
	}
	b.WriteString(`        *) COMPREPLY=( $(compgen -f -- "$cur") ) ;;
    esac
}

complete -F _panda panda
`)
	return b.String()
}

func zshCompletion() string {
	var b strings.Builder
	b.WriteString(`#compdef panda
# zsh completion for panda — drop into a fpath dir as _panda, or
# ` + "`source <(panda completion zsh)`" + `.
_panda() {
    local -a bare
    integer i
    for ((i=2; i<CURRENT; i++)); do
        [[ "${words[i]}" == -* ]] || bare+="${words[i]}"
    done

    if (( ${#bare[@]} == 0 )); then
        local -a cmds
        cmds=(` + completionTopWords() + `)
        _describe 'panda command' cmds
        return
    fi

    if [[ "${words[CURRENT]}" == -* ]]; then
        local -a fl
        case "${bare[1]} ${bare[2]:-}" in
`)
	for _, key := range []string{"task add", "plan run"} {
		fmt.Fprintf(&b, "            \"%s\") fl=(%s) ;;\n", key, completionFlagsFor(key))
	}
	b.WriteString(`            *) case "${bare[1]}" in
`)
	for _, key := range []string{"ask", "daemon", "queue", "init", "web", "status", "doctor", "repl"} {
		fmt.Fprintf(&b, "                %s) fl=(%s) ;;\n", key, completionFlagsFor(key))
	}
	b.WriteString(`            esac ;;
        esac
        (( ${#fl[@]} )) && _describe 'flags' fl
        return
    fi

    local -a verbs
    case "${bare[1]}" in
`)
	for _, cmd := range []string{"nodes", "task", "plan", "session", "sessions", "skill", "model", "models", "config", "memory", "project", "reminder", "card", "agents", "auth", "completion"} {
		words := completionVerbsFor(cmd)
		switch cmd {
		case "sessions":
			words = completionVerbsFor("session")
		case "models":
			words = completionVerbsFor("model")
		case "completion":
			words = "bash zsh fish"
		}
		fmt.Fprintf(&b, "        %s) verbs=(%s) ;;\n", cmd, words)
	}
	b.WriteString(`    esac
    (( ${#verbs[@]} )) && _describe 'verb' verbs
}
_panda "$@"
`)
	return b.String()
}

func fishCompletion() string {
	var b strings.Builder
	b.WriteString(`# fish completion for panda — drop into ~/.config/fish/completions/panda.fish
# or ` + "`panda completion fish | source`" + `.

function __fish_panda_bare_count
    # how many non-flag words sit on the command line before the cursor
    set -l bare 0
    for w in (commandline -opc)[2..-1]
        switch $w
            case '-\*'
            case '*'
                set bare (math $bare + 1)
        end
    end
    echo $bare
end

complete -c panda -f -n 'test (__fish_panda_bare_count) -eq 0' -a '` + completionTopWords() + `' -d 'panda command'
`)
	for _, cmd := range []string{"nodes", "task", "plan", "session", "sessions", "skill", "model", "models", "config", "memory", "project", "reminder", "card", "agents", "auth", "completion"} {
		words := completionVerbsFor(cmd)
		switch cmd {
		case "sessions":
			words = completionVerbsFor("session")
		case "models":
			words = completionVerbsFor("model")
		case "completion":
			words = "bash zsh fish"
		}
		fmt.Fprintf(&b, "complete -c panda -f -n 'test (__fish_panda_bare_count) -eq 1; and __fish_seen_subcommand_from %s' -a '%s' -d '%s verb'\n", cmd, words, cmd)
	}
	for flagName, vals := range map[string][]string{
		"--state":         completionFlagValues["--state"],
		"--priority":      completionFlagValues["--priority"],
		"--mode":          completionFlagValues["--mode"],
		"--output-format": completionFlagValues["--output-format"],
	} {
		fmt.Fprintf(&b, "complete -c panda -f -n '__fish_seen_argument -l %s' -a '%s'\n",
			strings.TrimLeft(flagName, "-"), strings.Join(vals, " "))
	}
	return b.String()
}
