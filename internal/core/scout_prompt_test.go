// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// TestBuildAgentPromptCarriesWorkspaceMap pins the integration: the scout
// report reaches the agent prompt for a workspace that has something to
// describe, and stays out of it when there is no workspace at all.
func TestBuildAgentPromptCarriesWorkspaceMap(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("build:\n\ttrue\ntest:\n\ttrue\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Core{}
	prompt, _ := buildAgentPrompt(c, "make the build pass", "", "fix build", dir, 0, i18n.English)
	for _, want := range []string{"[workspace map", "workspace: " + dir, "build: make build", "verify: make test"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("agent prompt missing %q:\n%s", want, prompt)
		}
	}

	// No workspace: no map, and none of the map's labels leak in.
	prompt, _ = buildAgentPrompt(c, "answer only", "", "answer", "", 0, i18n.English)
	if strings.Contains(prompt, "[workspace map") {
		t.Fatalf("map rendered without a workspace:\n%s", prompt)
	}
}
