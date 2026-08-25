package main

// Shared CLI plumbing: the global --json switch, JSON emission, and the
// CLI→core priority mapping. All panel-facing subcommands honor jsonOutput
// so the CLI can serve scripts exactly like the web API does.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
)

// jsonOutput is set by the global --json flag (stripped in parseSubcommand):
// panel-style commands then emit their JSON wire form instead of text.
var jsonOutput bool

// defaultCardPath discovers a capability card without --card: ./capabilities.yaml
// first (repo-root dev flow), then next to the auto-discovered config file —
// where `panda init` writes the card (the user config dir, or /etc/openpanda
// for system installs; this is what daemon services without explicit flags
// rely on) — then /etc/openpanda/capabilities.yaml directly. Empty means no
// card — answer and tool_call still work, task execution stays off.
func defaultCardPath() string {
	candidates := []string{"capabilities.yaml"}
	if cfgPath := config.ResolvePath(""); cfgPath != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(cfgPath), "capabilities.yaml"))
	}
	candidates = append(candidates, "/etc/openpanda/capabilities.yaml")
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// isLinuxConsole reports whether we are on a bare kernel VT (TERM=linux):
// the console font carries no CJK glyphs, so every non-ASCII rune — Chinese
// text, ·, box drawing — renders as a diamond. Callers degrade to English +
// pure ASCII there.
func isLinuxConsole() bool {
	return os.Getenv("TERM") == "linux"
}

// termSupportsUnicode reports whether the terminal is expected to render
// non-ASCII runes. A bare Linux console never does (font, not encoding); an
// ssh/terminal session does when the locale says UTF-8.
func termSupportsUnicode() bool {
	if isLinuxConsole() {
		return false
	}
	for _, v := range []string{os.Getenv("LC_ALL"), os.Getenv("LC_CTYPE"), os.Getenv("LANG")} {
		if v != "" {
			return strings.Contains(strings.ToUpper(v), "UTF")
		}
	}
	return false
}

// cliStateDir is where CLI state (REPL history) lives: XDG_STATE_HOME when
// set, else ~/.local/state (the stdlib has no UserStateDir).
func cliStateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "openpanda")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "state", "openpanda")
}

// loadConfigQuietly loads config with config.Load's slog chatter muted
// (secrets-in-file advice, 0600 tightening notes). The hardening itself still
// runs; interactive surfaces (REPL banner, ask replies) just stay clean —
// the daemon keeps the warnings in its log.
func loadConfigQuietly(path string) (*config.Config, error) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	defer slog.SetDefault(prev)
	return config.Load(path)
}

// emitJSON prints v as indented JSON on stdout; a marshal failure falls back
// to a plain error line (never worth exiting over).
func emitJSON(v any) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "panda: "+err.Error())
		return
	}
	fmt.Println(string(out))
}

// parseCLIPriority maps the CLI's priority vocabulary onto the core's three
// tiers (core only knows high/normal/low). medium lands on normal and
// critical on high so codex-style inputs work unchanged.
func parseCLIPriority(label string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "critical", "high":
		return core.PriorityHigh, true
	case "normal", "medium":
		return core.PriorityNormal, true
	case "low":
		return core.PriorityLow, true
	}
	return 0, false
}

// priorityName renders a core priority tier for human output.
func priorityName(p int) string {
	switch p {
	case core.PriorityHigh:
		return "high"
	case core.PriorityNormal:
		return "normal"
	case core.PriorityLow:
		return "low"
	}
	return fmt.Sprint(p)
}

// cliPriorities is the accepted vocabulary, shown in usage/help texts.
const cliPriorities = "low|medium|normal|high|critical"

// reorderFlags rewrites argv so every "-…" flag (and the value of the
// known value-carrying ones) precedes the positional words. The std flag
// package stops parsing at the first non-flag argument, which makes the
// natural `panda task <id> --config x` silently swallow the trailing flags
// into the positional text — a trap users hit constantly. Unknown "-…"
// tokens are hoisted too; flag.Parse then reports them as errors rather
// than misinterpreting.
func reorderFlags(args []string, valueFlags map[string]bool) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && a != "-" && a != "--" {
			flags = append(flags, a)
			if eq := strings.IndexByte(a, '='); eq < 0 && valueFlags[a] && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a) // bare "-" or "--" terminator: keep order
			continue
		}
		positional = append(positional, a)
	}
	return append(flags, positional...)
}

// commonValueFlags are the flags every task-ish subcommand accepts with a
// value (--config/--card); --json is global (stripped in parseSubcommand).
var commonValueFlags = map[string]bool{
	"--config": true, "--card": true,
}
