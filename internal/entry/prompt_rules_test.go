// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"strings"
	"testing"
)

// TestCoreRulesLayOutEnumerations guards the answer-formatting rule: a reply
// that folds "1）…；2）…" into one Hanzi sentence arrives at the console as a
// single sixty-character paragraph, which the reading measure cannot rescue.
// The rule is one line of prose in the prompt kernel and nothing in the code
// depends on it, so without this guard it can be edited away unnoticed.
func TestCoreRulesLayOutEnumerations(t *testing.T) {
	for _, want := range []string{"own line", "1）…；2）…", "separate paragraphs", "numbered list"} {
		if !strings.Contains(coreRules, want) {
			t.Errorf("coreRules does not lay out enumerations (%q missing)", want)
		}
	}
}

// TestPromptStatesApprovalPolicy guards the other half of the same problem: the
// model used to close every dispatch with "是否按这个思路开始执行？", asking the
// user to authorize what the composer's permission switch had already answered.
// The prompt now carries the thread's policy, so the question has an answer the
// model can read.
func TestPromptStatesApprovalPolicy(t *testing.T) {
	bypass := BuildPrompt(PromptOptions{PermissionMode: "bypass"})
	for _, want := range []string{"═══ Approval ═══", "是否按这个思路开始执行", "policy is bypass", "Approval is already granted"} {
		if !strings.Contains(bypass, want) {
			t.Errorf("bypass prompt does not state the approval policy (%q missing)", want)
		}
	}
	// A caller that says nothing gets no section: the console always sends a
	// mode, and a default here would state a policy nobody chose.
	if silent := BuildPrompt(PromptOptions{}); strings.Contains(silent, "═══ Approval ═══") {
		t.Error("approval section rendered without a permission mode")
	}
	// Every mode names itself, so a thread that tightens the switch mid-way
	// changes the paragraph the model reads rather than repeating "auto".
	for _, mode := range []string{"auto", "manual", "edits", "plan"} {
		if got := BuildPrompt(PromptOptions{PermissionMode: mode}); !strings.Contains(got, "policy is "+mode) {
			t.Errorf("%s prompt does not name its policy", mode)
		}
	}
}

// TestClassifyCacheKeyTracksPermission pins the cache boundary: a verdict
// reached under bypass ("dispatch it now") must not be replayed into a manual
// thread, where the same request has to wait on a card.
func TestClassifyCacheKeyTracksPermission(t *testing.T) {
	base := PromptOptions{History: []Turn{{Role: "user", Content: "删除 D:\\d 下两个文件"}}}
	bypass := base
	bypass.PermissionMode = "bypass"
	manual := base
	manual.PermissionMode = "manual"
	k1, _ := classifyCacheKey(bypass, "m", nil)
	k2, _ := classifyCacheKey(manual, "m", nil)
	if k1 == k2 {
		t.Fatal("permission mode is not part of the classification cache key")
	}
}

// TestCoreRulesRefuseForeignToolEnvelopes guards the other half of "never let
// markup stand as prose": a model that cannot reach a native tool_use will
// sometimes answer with another harness's call envelope — {"action":…,
// "tool":{"mcpServer":…}} — which nothing here executes. Rendered as a reply
// it was a wall of braces in a proportional face; the console now sets such a
// block as JSON code, but the prompt is what should stop it being emitted.
func TestCoreRulesRefuseForeignToolEnvelopes(t *testing.T) {
	for _, want := range []string{"tool-call envelope", "mcpServer", "emit a task"} {
		if !strings.Contains(coreRules, want) {
			t.Errorf("coreRules does not refuse foreign tool envelopes (%q missing)", want)
		}
	}
}
