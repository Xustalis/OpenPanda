// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/nodecli"
)

// TestHelpCoversCatalog keeps the human help page from falling behind the
// registry. The page keeps its own layout and prose on purpose, so this is the
// cheap guard rather than a renderer rewrite: a command an agent is told
// exists must also be a command the operator can find in `panda help`.
//
// Only canonical top-level names are checked. Aliases share a help row by
// design (read|view|cat), and verb forms live under their parent command.
func TestHelpCoversCatalog(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "help")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	defer f.Close()
	printUsage(f)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	blob, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read help: %v", err)
	}
	page := strings.Split(string(blob), "\n")
	// A command's row is a line whose command column starts with the name and
	// is followed by a word boundary, a pipe (shared rows like
	// `cancel|approve|…`) or the end of the column. Matching the whole page
	// for the bare word would let a description ("…read it, re-scan…")
	// satisfy the check after the row was deleted.
	hasRow := func(name string) bool {
		for _, ln := range page {
			rest, ok := strings.CutPrefix(ln, "  ")
			if !ok || !strings.HasPrefix(rest, name) {
				continue
			}
			if len(rest) == len(name) || rest[len(name)] == ' ' || rest[len(name)] == '|' {
				return true
			}
		}
		return false
	}
	for _, c := range nodecli.Catalog() {
		name := c.Names[0]
		if strings.Contains(name, " ") || name == "(no subcommand)" {
			continue
		}
		if !hasRow(name) {
			t.Errorf("`panda help` has no row for %q (catalog row %q)", name, c.Usage)
		}
	}
}
