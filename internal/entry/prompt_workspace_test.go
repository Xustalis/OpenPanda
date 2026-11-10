// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"context"
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

func TestWorkspaceClassificationsBypassPathOnlyCache(t *testing.T) {
	c, calls, mu := startCountingAnswerServer(t)
	c.SetDiskCache(NewDiskCache(newCacheDB(t)))
	reg := NewRegistry()
	reg.Register(Tool{Name: "workspace_read"})
	for i := 0; i < 2; i++ {
		_, err := ClassifyTurnsWithTools(context.Background(), c, nil, "", []Turn{{Role: "user", Content: "Explain FastTriage"}}, reg)
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := serverCalls(calls, mu); got != 2 {
		t.Fatalf("workspace cached stale answer: calls=%d", got)
	}
	if !liveWorkspaceClassification(PromptOptions{WorkspaceMap: "same paths, different bytes"}, nil) {
		t.Fatal("workspace-map-only classification was cacheable")
	}
}
