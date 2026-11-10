// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/contract"
	"github.com/Xustalis/OpenPanda/internal/scout"
)

func toolUse(id, name, input string) commander.AgentEvent {
	return commander.AgentEvent{Ev: "tool_use", ID: id, Name: name, Input: json.RawMessage(input)}
}

func toolResult(id string, isErr bool) commander.AgentEvent {
	return commander.AgentEvent{Ev: "tool_result", ToolUseID: id, IsError: isErr}
}

func TestEvidenceRecordsCommandsAndFailures(t *testing.T) {
	e := newExecEvidence()
	e.observe(toolUse("t1", "Bash", `{"command":"go test ./..."}`))
	e.observe(toolResult("t1", false))
	e.observe(toolUse("t2", "bash", `{"command":"make lint"}`))
	e.observe(toolResult("t2", true))
	e.observe(toolUse("t3", "Edit", `{"file_path":"a.go"}`))

	block := e.render([]scout.Command{{Run: "go test ./..."}})
	for _, want := range []string{
		"go test ./...", "make lint", "[失败]", "文件改动 1 次",
		"[项目声明的验证入口] go test ./...", "本轮执行了项目的验证入口",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("evidence missing %q:\n%s", want, block)
		}
	}
}

func TestEvidenceNotesMissingVerification(t *testing.T) {
	e := newExecEvidence()
	e.observe(toolUse("t1", "Bash", `{"command":"ls -la"}`))
	block := e.render([]scout.Command{{Run: "make test"}})
	if !strings.Contains(block, "没有匹配到项目声明的验证入口") {
		t.Fatalf("missing-verification signal absent:\n%s", block)
	}

	e.reset()
	e.observe(commander.AgentEvent{Ev: "text", Text: "done"})
	block = e.render([]scout.Command{{Run: "make test"}})
	if !strings.Contains(block, "没有执行任何命令") {
		t.Fatalf("empty-round signal absent:\n%s", block)
	}
}

func TestBaselineEvidenceMarksInheritedFailures(t *testing.T) {
	block := baselineEvidence([]contract.Result{
		{CheckID: "build", Command: "make build", Status: contract.StatusPass},
		{CheckID: "test", Command: "make test", Status: contract.StatusFail},
	})
	if !strings.Contains(block, "make build") || !strings.Contains(block, "→ pass") {
		t.Fatalf("passing baseline missing:\n%s", block)
	}
	if !strings.Contains(block, "运行前就已失败") {
		t.Fatalf("inherited failure not marked:\n%s", block)
	}
	if baselineEvidence(nil) != "" {
		t.Fatal("empty baseline rendered")
	}
}

func TestEvidenceStaysSilentForOpaqueAdapters(t *testing.T) {
	e := newExecEvidence()
	e.observe(toolUse("dsh", "dsh", `{"argc":3}`))
	e.observe(toolResult("dsh", false))
	if block := e.render([]scout.Command{{Run: "make test"}}); block != "" {
		t.Fatalf("opaque adapter produced tool evidence:\n%s", block)
	}
}

func TestEvidenceRingKeepsNewestCommands(t *testing.T) {
	e := newExecEvidence()
	for i := 0; i < maxEvidenceCommands+5; i++ {
		cmd := `{"command":"echo cmd-` + strconv.Itoa(i) + `"}`
		e.observe(toolUse("t"+strconv.Itoa(i), "Bash", cmd))
	}
	block := e.render(nil)
	if len(block) == 0 {
		t.Fatal("empty evidence block")
	}
	if strings.Contains(block, "cmd-0") {
		t.Fatalf("oldest command survived the ring:\n%s", block)
	}
	if !strings.Contains(block, "cmd-"+strconv.Itoa(maxEvidenceCommands+4)) {
		t.Fatalf("newest command missing:\n%s", block)
	}
}

func TestCommandFromInputShapes(t *testing.T) {
	cases := map[string]string{
		`{"command":"make test"}`:     "make test",
		`{"cmd":"make build"}`:        "make build",
		`{"command":["make","test"]}`: "make test",
		`{"file_path":"a.go"}`:        "",
		`"just a string"`:             "",
		`{"command":"  padded  "}`:    "padded",
	}
	for input, want := range cases {
		if got := commandFromInput(json.RawMessage(input)); got != want {
			t.Fatalf("commandFromInput(%s) = %q, want %q", input, got, want)
		}
	}
}

func TestVerificationRequiresExactCompletedCommand(t *testing.T) {
	for _, tc := range []struct {
		name, input          string
		result, failed, want bool
	}{
		{"exact", `{"command":"make test"}`, true, false, true},
		{"argv", `{"cmd":["make","test"]}`, true, false, true},
		{"shell wrapper", `{"cmd":["/bin/zsh","-lc","make test"]}`, true, false, true},
		{"echo", `{"command":"echo 'make test'"}`, true, false, false},
		{"quoted argv", `{"command":["make test"]}`, true, false, false},
		{"unfinished", `{"command":"make test"}`, false, false, false},
		{"failed", `{"command":"make test"}`, true, true, false},
		{"different target", `{"command":"make test-unit"}`, true, false, false},
		{"reordered", `{"command":"test make"}`, true, false, false},
		{"shell composition", `{"command":"false && make test"}`, true, false, false},
		{"comment", `{"command":"echo done # make test"}`, true, false, false},
		{"different program", `{"command":"MAKE TEST"}`, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newExecEvidence()
			e.observe(toolUse("verify", "Bash", tc.input))
			if tc.result {
				e.observe(toolResult("verify", tc.failed))
			}
			if got := ranVerification(e.cmds, []string{"make test"}); got != tc.want {
				t.Fatalf("got=%v want=%v: %+v", got, tc.want, e.cmds)
			}
		})
	}
}

func TestVerificationCannotUseNonCommandToolOrPartialScope(t *testing.T) {
	e := newExecEvidence()
	e.observe(toolUse("test", "shell", `{"command":"go test ./internal/core"}`))
	e.observe(toolResult("test", false))
	if ranVerification(e.cmds, []string{"go test ./..."}) {
		t.Fatal("partial scope certified whole repository")
	}
}

func TestVerificationDoesNotTrustCommandArgumentOnUnknownTool(t *testing.T) {
	e := newExecEvidence()
	e.observe(toolUse("fake", "mcp_echo", `{"command":"make test"}`))
	e.observe(toolResult("fake", false))
	if ranVerification(e.cmds, []string{"make test"}) {
		t.Fatal("non-command tool counted as verification")
	}
}
