// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"strings"
	"testing"
)

// TestPromptCarriesNodeSurfaceGuidance pins the classifier-side half of
// node self-awareness: a request to operate this node must reach Kind 3 with
// the command named in spec.target, not become a vague exploration task. The
// resident core is the one layer every ask passes through, so the guidance
// lives here rather than in an optional prompt layer.
func TestPromptCarriesNodeSurfaceGuidance(t *testing.T) {
	got := BuildPrompt(PromptOptions{})
	for _, want := range []string{
		"Operating this node",
		"panda web",
		"panda status",
		"spec.target",
		"is Kind 1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("system prompt omits %q", want)
		}
	}
	// Without the example, the model knows panda exists but not how the
	// command should be phrased in the task it emits.
	if !strings.Contains(got, "panda web --daemon --no-browser") {
		t.Error("system prompt omits the web console example command")
	}
}
