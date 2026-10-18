// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"strings"
	"testing"
)

// The completion tables are the dispatch switches written out as data —
// this test is the guardrail that fails when a command is added in main.go
// but forgotten here.
func TestCompletionVerbsSyncWithCommands(t *testing.T) {
	names := map[string]bool{}
	for _, n := range subcommandNames() {
		names[n] = true
	}
	for cmd := range completionVerbs {
		if !names[cmd] {
			t.Errorf("completionVerbs has %q, which is not a subcommand", cmd)
		}
	}
	if !names["completion"] {
		t.Error("completion itself must be in subcommandNames or it stops completing")
	}
}

func TestCompletionScriptsContainCommands(t *testing.T) {
	for _, shell := range []struct {
		name string
		gen  func() string
	}{
		{"bash", bashCompletion},
		{"zsh", zshCompletion},
		{"fish", fishCompletion},
		{"powershell", powershellCompletion},
	} {
		out := shell.gen()
		for _, want := range []string{"task", "daemon", "ask", "queue", "init"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s script missing command %q", shell.name, want)
			}
		}
		for _, want := range []string{"approve", "reject", "priority"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s script missing task verb %q", shell.name, want)
			}
		}
	}
}

// completionFlags keys are "cmd" or "cmd verb" shapes — a renamed command or
// verb leaves the flag list unreachable in every generated script.
func TestCompletionFlagsSyncWithVerbs(t *testing.T) {
	names := map[string]bool{}
	for _, n := range subcommandNames() {
		names[n] = true
	}
	for key := range completionFlags {
		cmd, verb, twoPart := strings.Cut(key, " ")
		if !names[cmd] {
			t.Errorf("completionFlags key %q starts with unknown command %q", key, cmd)
			continue
		}
		if twoPart {
			ok := false
			for _, v := range completionVerbs[cmd] {
				if v == verb {
					ok = true
					break
				}
			}
			if !ok {
				t.Errorf("completionFlags key %q names a verb %q not in completionVerbs[%q]", key, verb, cmd)
			}
		}
	}
}

func TestBashCompletionNoAssocArrays(t *testing.T) {
	// macOS still ships bash 3.2: declare -A does not exist there and the
	// generated script must stay POSIX-enough for it.
	if strings.Contains(bashCompletion(), "declare -A") {
		t.Error("bash script must not use assoc arrays (bash 3.2 compat)")
	}
}
