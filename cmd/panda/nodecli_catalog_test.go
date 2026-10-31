// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/nodecli"
)

// dispatcherCases parses dispatch.go and returns the string literals a
// dispatch function's switch accepts. The switch stays the dispatcher —
// aliases share a case and the shape stays the cheapest read of the CLI's
// surface — so the catalog cannot be derived from it at runtime; reading the
// source is how a case added without a catalog row fails the build instead of
// shipping an agent-visible command that no prompt mentions.
func dispatcherCases(t *testing.T, fn string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "dispatch.go", nil, 0)
	if err != nil {
		t.Fatalf("parse dispatch.go: %v", err)
	}
	var body *ast.BlockStmt
	ast.Inspect(file, func(n ast.Node) bool {
		if fd, ok := n.(*ast.FuncDecl); ok && fd.Name.Name == fn {
			body = fd.Body
			return false
		}
		return true
	})
	if body == nil {
		t.Fatalf("%s not found in dispatch.go", fn)
	}
	var names []string
	ast.Inspect(body, func(n ast.Node) bool {
		cc, ok := n.(*ast.CaseClause)
		if !ok {
			return true
		}
		for _, expr := range cc.List {
			lit, ok := expr.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			name, err := strconv.Unquote(lit.Value)
			if err != nil || strings.HasPrefix(name, "-") {
				continue
			}
			names = append(names, name)
		}
		return true
	})
	if len(names) == 0 {
		t.Fatalf("%s has no string cases", fn)
	}
	return names
}

// TestCatalogCoversDispatcher is the drift guard between the CLI's switch and
// the reference agents are handed. A dispatched name without a nodecli entry
// is a command an agent cannot discover from its prompt — exactly the failure
// that left a task hunting for how to start the web console — and a verb
// without one is the same gap one level down.
func TestCatalogCoversDispatcher(t *testing.T) {
	for _, name := range dispatcherCases(t, "dispatchSub") {
		if _, ok := nodecli.Resolve(name); !ok {
			t.Errorf("subcommand %q has no nodecli catalog entry — add it with a help key", name)
		}
	}
	for _, verb := range dispatcherCases(t, "dispatchNodes") {
		name := "nodes " + verb
		if _, ok := nodecli.Resolve(name); !ok {
			t.Errorf("nodes verb %q has no nodecli catalog entry — add %q with a help key", verb, name)
		}
	}
}
