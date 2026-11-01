// SPDX-License-Identifier: AGPL-3.0-or-later

package nodecli

import (
	"slices"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// TestHelpPageShape pins what the renderer relies on: known groups in page
// order, one non-empty section per group, a positive description column, and
// no row that would print as a blank line. Every description must resolve in
// every locale — a missing key would print the key into `panda help`.
func TestHelpPageShape(t *testing.T) {
	allowed := map[Group]bool{}
	for _, g := range groupOrder {
		allowed[g] = true
	}
	sections := HelpPage()
	if len(sections) != len(groupOrder) {
		t.Fatalf("page has %d sections, want one per group (%d)", len(sections), len(groupOrder))
	}
	seen := map[Group]bool{}
	for i, sec := range sections {
		if !allowed[sec.Group] {
			t.Fatalf("section %d: unknown group %q", i, sec.Group)
		}
		if seen[sec.Group] {
			t.Fatalf("group %q appears twice", sec.Group)
		}
		seen[sec.Group] = true
		if groupOrder[i] != sec.Group {
			t.Fatalf("section %d = %q, want %q (page order follows groupOrder)", i, sec.Group, groupOrder[i])
		}
		if sec.Col <= 0 {
			t.Fatalf("%s: description column %d", sec.Group, sec.Col)
		}
		if len(sec.Rows) == 0 {
			t.Fatalf("%s: empty section", sec.Group)
		}
		for _, row := range sec.Rows {
			if row.Cmd == "" && row.HelpKey == "" {
				t.Fatalf("%s: blank row", sec.Group)
			}
			if row.HelpKey == "" {
				continue
			}
			for _, loc := range i18n.Locales {
				if got := i18n.T(loc, row.HelpKey); got == row.HelpKey {
					t.Fatalf("%s: help key %q missing in %s", sec.Group, row.HelpKey, loc)
				}
			}
		}
	}
}

// TestCompletionVerbsAreKnownCommands catches a completion vocabulary that
// names a command the CLI does not dispatch — the generated scripts would
// suggest a dead word.
func TestCompletionVerbsAreKnownCommands(t *testing.T) {
	names := map[string]bool{}
	for _, n := range Names() {
		names[n] = true
	}
	for cmd := range CompletionVerbs() {
		if !names[cmd] {
			t.Errorf("completion verbs for %q, which is not a subcommand", cmd)
		}
	}
}

// TestCatalogVerbsComplete keeps the two registries in step: every verb form
// the agent reference names ("nodes add") must also be a completion verb for
// its parent, or typing `panda nodes <Tab>` stops offering a command the
// agent knows exists.
func TestCatalogVerbsComplete(t *testing.T) {
	verbs := CompletionVerbs()
	for _, c := range Catalog() {
		if strings.HasPrefix(c.Names[0], "(") {
			continue // the bare invocation's pseudo-name is not a command path
		}
		parent, verb, ok := strings.Cut(c.Names[0], " ")
		if !ok {
			continue
		}
		if !slices.Contains(verbs[parent], verb) {
			t.Errorf("catalog verb form %q is missing from completion verbs for %q", c.Names[0], parent)
		}
	}
}
