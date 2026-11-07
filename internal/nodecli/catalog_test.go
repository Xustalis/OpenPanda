// SPDX-License-Identifier: AGPL-3.0-or-later

package nodecli

import (
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// TestCatalogShape pins the invariants the reference relies on: every row is
// renderable (a name, a usage, a declared group) and every description
// resolves in every locale — a missing i18n key would otherwise print the key
// itself into an agent's prompt.
func TestCatalogShape(t *testing.T) {
	groups := map[Group]bool{}
	for _, g := range groupOrder {
		groups[g] = true
	}
	for _, c := range Catalog() {
		if len(c.Names) == 0 || c.Names[0] == "" {
			t.Fatalf("command %q has no names", c.Usage)
		}
		if !strings.HasPrefix(c.Usage, "panda") {
			t.Fatalf("%v: usage %q does not start with panda", c.Names, c.Usage)
		}
		if !groups[c.Group] {
			t.Fatalf("%v: group %q has no heading (add it to groupOrder)", c.Names, c.Group)
		}
		for _, loc := range i18n.Locales {
			if got := i18n.T(loc, c.HelpKey); got == c.HelpKey {
				t.Fatalf("%v: help key %q missing in %s", c.Names, c.HelpKey, loc)
			}
		}
	}
}

// TestPromptReferenceCoversWebConsole is the regression this catalog exists
// for: asked to start OpenPanda's web console, the agent must be able to find
// `panda web` in its prompt — the old four-command hint did not mention it,
// and the agent went prospecting for a panda it assumed was not there.
func TestPromptReferenceCoversWebConsole(t *testing.T) {
	for _, loc := range i18n.Locales {
		ref := PromptReference(loc)
		if !strings.Contains(ref, "panda web [--daemon]") {
			t.Fatalf("%s: reference omits the web console command:\n%s", loc, ref)
		}
		if !strings.Contains(ref, "panda daemon") {
			t.Fatalf("%s: reference omits the kernel command", loc)
		}
		if !strings.Contains(ref, "panda queue") {
			t.Fatalf("%s: reference omits the task board", loc)
		}
		// A leaked key is a missing translation; the renderer must never ship
		// one to a model.
		if strings.Contains(ref, "cli.help.") || strings.Contains(ref, "prompt.selfcmd.") {
			t.Fatalf("%s: reference leaks an i18n key:\n%s", loc, ref)
		}
	}
}

// TestResolve covers the spellings callers look up: canonical names, aliases,
// verb forms, the bare invocation, and a name that does not exist.
func TestCompactPromptReference(t *testing.T) {
	for _, loc := range i18n.Locales {
		full := PromptReference(loc)
		compact := PromptReferenceFor(loc, "fix a typo")
		if len(compact)*2 >= len(full) {
			t.Fatalf("%s: insufficient reduction: %d / %d", loc, len(compact), len(full))
		}
		for _, command := range []string{"panda help", "panda <command> --help", "panda status", "panda task"} {
			if !strings.Contains(compact, command) {
				t.Fatalf("%s: missing %s", loc, command)
			}
		}
		for query, command := range map[string]string{"配置模型": "panda model", "panda web": "panda web", "memory": "panda memory", "诊断": "panda doctor"} {
			if !strings.Contains(PromptReferenceFor(loc, query), command) {
				t.Fatalf("%s: query %s omits %s", loc, query, command)
			}
		}
		t.Logf("%s command reference bytes: %d -> %d", loc, len(full), len(compact))
	}
}

func TestResolve(t *testing.T) {
	cases := []struct {
		name string
		want string
		ok   bool
	}{
		{"web", "panda web", true},
		{"sessions", "panda session", true},
		{"nodes add", "panda nodes add", true},
		{"", "panda", true},
		{"(no subcommand)", "panda", true},
		{"statsu", "", false},
	}
	for _, tc := range cases {
		c, ok := Resolve(tc.name)
		if ok != tc.ok {
			t.Fatalf("Resolve(%q) ok = %v, want %v", tc.name, ok, tc.ok)
		}
		if ok && !strings.HasPrefix(c.Usage, tc.want) {
			t.Fatalf("Resolve(%q) = %q, want prefix %q", tc.name, c.Usage, tc.want)
		}
	}
}
