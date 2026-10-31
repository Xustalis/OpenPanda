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
	page := string(blob)
	for _, c := range nodecli.Catalog() {
		name := c.Names[0]
		if strings.Contains(name, " ") || name == "(no subcommand)" {
			continue
		}
		if !strings.Contains(page, name) {
			t.Errorf("`panda help` does not list %q (catalog row %q)", name, c.Usage)
		}
	}
}
