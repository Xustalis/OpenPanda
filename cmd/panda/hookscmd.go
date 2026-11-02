// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// `panda hooks` — what this node will run, and a way to try one.
//
// The list is read from the config file, not from a running daemon: hooks are
// static configuration, and asking a daemon whether it is up would make the
// command useless in exactly the case it matters (a hook that is not firing).

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/hooks"
)

func runHooks(args []string) {
	fs := flag.NewFlagSet("hooks", flag.ExitOnError)
	fs.Parse(reorderFlags(fs, args))
	rest := fs.Args()

	if len(rest) == 0 || rest[0] == "list" {
		hooksList()
		return
	}
	switch rest[0] {
	case "test":
		if len(rest) < 2 {
			fail(2, "usage: panda hooks test <event>  (events: "+strings.Join(hooks.Events, ", ")+")")
		}
		hooksTest(rest[1])
	default:
		fail(2, "usage: panda hooks [list | test <event>]")
	}
}

// hooksList prints the configured bindings.
func hooksList() {
	cfg, err := config.Load(cliConfigPath)
	if err != nil {
		fatal("load config", err)
	}
	h := cfg.Hooks
	if !h.Enabled || len(h.On) == 0 {
		fmt.Println("no hooks configured — add a `hooks:` section to the config (see config.example.yaml)")
		return
	}
	fmt.Printf("hooks enabled · default timeout %s · tier-2 %s\n",
		hooks.ParseTimeout(h.TimeoutS).String(), stateWordBool(h.AllowTier2))
	for _, e := range h.On {
		if !hooks.ValidEvent(e.Event) {
			fmt.Printf("  %-16s %s  [unknown event — ignored]\n", e.Event, e.Command)
			continue
		}
		fmt.Printf("  %-16s %s\n", e.Event, e.Command)
	}
	fmt.Println("\nevents: " + strings.Join(hooks.Events, ", "))
}

// stateWordBool renders a switch the way the header line reads.
func stateWordBool(on bool) string {
	if on {
		return "allowed"
	}
	return "refused (set hooks.allow_tier2)"
}

// hooksTest runs every binding for one event, synchronously, with a sample
// payload — the "does my command even work" check.
func hooksTest(event string) {
	if !hooks.ValidEvent(event) {
		fail(2, fmt.Sprintf("unknown event %q (events: %s)", event, strings.Join(hooks.Events, ", ")))
	}
	cfg, err := config.Load(cliConfigPath)
	if err != nil {
		fatal("load config", err)
	}
	if !cfg.Hooks.Enabled {
		fmt.Println("hooks are not enabled (hooks.enabled is false or absent)")
		return
	}
	sample := hooks.Event{
		Name:    event,
		TaskID:  "task-example-0001",
		Title:   "示例任务 / sample task",
		State:   "running",
		Project: "demo",
		Owner:   "this-node",
	}
	ran := 0
	for _, e := range cfg.Hooks.On {
		if e.Event != event {
			continue
		}
		ran++
		h := hooks.Hook{Event: e.Event, Command: e.Command, Timeout: hooks.ParseTimeout(e.TimeoutS)}
		fmt.Printf("→ %s\n", hooks.Expand(h.Command, sample))
		out, err := hooks.RunOne(context.Background(), h, sample, h.Timeout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  failed: %v\n", err)
			if trimmed := strings.TrimSpace(out); trimmed != "" {
				fmt.Fprintln(os.Stderr, "  "+strings.ReplaceAll(trimmed, "\n", "\n  "))
			}
			continue
		}
		if trimmed := strings.TrimSpace(out); trimmed != "" {
			fmt.Println("  " + strings.ReplaceAll(trimmed, "\n", "\n  "))
		} else {
			fmt.Println("  ok")
		}
	}
	if ran == 0 {
		fmt.Printf("no hook is bound to %s\n", event)
	}
}
