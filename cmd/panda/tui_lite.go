//go:build lite

package main

import (
	"fmt"

	versionpkg "github.com/Xustalis/OpenPanda/internal/version"
)

// Lite build: the Bubble Tea front end (tui_*.go — the charmbracelet stack,
// the binary's largest optional dependency chain) is compiled out entirely
// for constrained devices where a TUI would never run anyway. shouldUseTUI
// is always false so an interactive session falls through to the classic
// line loop; runTUI exists only so repl.go's call site compiles.
func shouldUseTUI(r *repl) bool { return false }

func runTUI(r *repl) {}

// printBanner is the lite greeting: no figlet/lipgloss theme, just the facts
// the banner carried. (In the full build this method lives on repl.go's
// TUI-adjacent helpers.)
func (r *repl) printBanner() {
	fmt.Printf("OpenPanda %s (%s) — node %s (lite build)\n", version, versionpkg.Codename, r.cfg.Node.Name)
}
