// SPDX-License-Identifier: AGPL-3.0-or-later

package commander

import (
	"context"
	"testing"
)

func TestIncrementalPromptUsesActualAdapterAffinity(t *testing.T) {
	base := WithResume(context.Background(), "checkpoint", "claude_code.py")
	ctx := WithIncrementalPrompt(base, "complete user requirements and guidance", "new user steering", "checkpoint", "claude_code.py")
	for _, tc := range []struct{ adapter, prompt, want string }{
		{"claude_code.py", "complete user requirements and guidance", "new user steering"},
		{"codex.py", "complete user requirements and guidance", "complete user requirements and guidance"},
		{"claude_code.py", "identity note\ncomplete user requirements and guidance", "identity note\nnew user steering"},
	} {
		req, _, err := buildAdapterRequest(ctx, tc.adapter, tc.prompt, "/tmp")
		if err != nil {
			t.Fatal(err)
		}
		if req.Prompt != tc.want {
			t.Fatalf("%s prompt=%q, want %q", tc.adapter, req.Prompt, tc.want)
		}
	}
	// Old/unknown checkpoints and fresh candidates must always receive full input.
	ctx = WithResume(ctx, "different", "claude_code.py")
	req, _, _ := buildAdapterRequest(ctx, "claude_code.py", "complete user requirements and guidance", "/tmp")
	if req.Prompt != "complete user requirements and guidance" {
		t.Fatal("unconfirmed session got delta")
	}
}

func TestMissingResumeDoesNotRetryTaskFailure(t *testing.T) {
	ctx := WithResume(context.Background(), "checkpoint", "claude_code.py")
	for _, tc := range []struct {
		name   string
		result AgentResult
		want   bool
	}{
		{"rejected", AgentResult{Result: "Session not found: checkpoint", ExitCode: 1}, true},
		{"cli prefix", AgentResult{Stderr: "Error: No conversation found with session ID: checkpoint.", ExitCode: 1}, true},
		{"task failure", AgentResult{Result: "test failed: session not found", ExitCode: 1}, false},
		{"other checkpoint", AgentResult{Result: "Session not found: other", ExitCode: 1}, false},
		{"observed work", AgentResult{Result: "Session not found: checkpoint", ExitCode: 1, WorkObserved: true}, false},
		{"minted session", AgentResult{Result: "Session not found: checkpoint", ExitCode: 1, SessionID: "checkpoint"}, false},
		{"task transcript", AgentResult{Result: "Session not found: checkpoint\ncommand failed", ExitCode: 1}, false},
	} {
		if got := rejectedResume(ctx, "claude_code.py", tc.result); got != tc.want {
			t.Errorf("%s: rejected=%t want %t", tc.name, got, tc.want)
		}
	}
	if rejectedResume(ctx, "codex.py", AgentResult{Result: "Session not found: checkpoint", ExitCode: 1}) {
		t.Fatal("foreign adapter rejected another adapter's checkpoint")
	}
}
