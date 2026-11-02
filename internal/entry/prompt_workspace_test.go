// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/contract"
)

func TestBuildPromptCarriesWorkspaceMap(t *testing.T) {
	po := PromptOptions{
		WorkspaceMap: "[workspace map]\nroot: /repo\nbuild: make build",
	}
	out := BuildPrompt(po)
	if !strings.Contains(out, "═══ Workspace ═══") || !strings.Contains(out, "root: /repo") {
		t.Fatalf("workspace section missing:\n%s", out)
	}
	if !strings.Contains(out, "spec.scope") || !strings.Contains(out, "duration_hint") {
		t.Fatalf("workspace guidance missing:\n%s", out)
	}

	if got := BuildPrompt(PromptOptions{}); strings.Contains(got, "═══ Workspace ═══") {
		t.Fatal("workspace section rendered without a map")
	}
}

// TestClassifyCacheKeyTracksWorkspace pins the cache boundary: the workspace
// map and the acceptance shortlist are prompt content, so two conversations
// with identical history in different trees must not share a classification.
func TestClassifyCacheKeyTracksWorkspace(t *testing.T) {
	base := PromptOptions{History: []Turn{{Role: "user", Content: "fix the build"}}}
	k0, _ := classifyCacheKey(base, "model", nil)

	withMap := base
	withMap.WorkspaceMap = "[workspace map]\nroot: /other"
	if k1, _ := classifyCacheKey(withMap, "model", nil); k1 == k0 {
		t.Fatal("workspace map does not participate in the classification cache key")
	}

	withCands := base
	withCands.AcceptanceCandidates = []contract.Candidate{{ID: "test", Run: "make test"}}
	if k2, _ := classifyCacheKey(withCands, "model", nil); k2 == k0 {
		t.Fatal("acceptance candidates do not participate in the classification cache key")
	}
}
