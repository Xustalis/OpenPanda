package main

// Shared CLI plumbing: the global --json switch, JSON emission, and the
// CLI→core priority mapping. All panel-facing subcommands honor jsonOutput
// so the CLI can serve scripts exactly like the web API does.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/core"
)

// jsonOutput is set by the global --json flag (stripped in parseSubcommand):
// panel-style commands then emit their JSON wire form instead of text.
var jsonOutput bool

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
