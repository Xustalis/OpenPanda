// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/contract"
	"github.com/Xustalis/OpenPanda/internal/scout"
)

// maxEvidenceCommands bounds the per-round command log carried to the judge.
// The last commands are the ones that matter (an agent usually verifies at the
// end), so the ring keeps the newest and drops the oldest.
const maxEvidenceCommands = 40

// evidenceCommand is one shell command an agent ran this round, plus whether
// its tool result came back as an error.
type evidenceCommand struct {
	text   string
	id     string
	failed bool
}

// execEvidence accumulates what an agent ACTUALLY did in one round — the
// commands it ran, whether their tool results failed, and how many file
// mutation/read tools it used. The completion judge sees this alongside the
// agent's own prose, so "done" is judged against evidence rather than against
// the summary that was written to sound done.
//
// It is fed by the same structured-event sink that records the transcript, so
// it costs no extra I/O and is bounded: the command ring is capped, and the
// tool-use map only holds ids that still have a pending command.
type execEvidence struct {
	mu     sync.Mutex
	cmds   []evidenceCommand
	byID   map[string]int // tool_use id -> index in cmds
	failed int
	edits  int
	reads  int
	// streamed records that this adapter exposed structured activity this
	// round. A plain adapter (one that runs the CLI as a single opaque call)
	// emits only its spawn/result pair, so "no commands observed" there is a
	// fact about the adapter, not about the work — the judge must not read it
	// as "the agent skipped verification".
	streamed bool
}

func newExecEvidence() *execEvidence {
	return &execEvidence{byID: map[string]int{}}
}

// reset starts a new round. Called at the head of every supervision round.
func (e *execEvidence) reset() {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.cmds = nil
	e.byID = map[string]int{}
	e.failed = 0
	e.edits = 0
	e.reads = 0
	e.streamed = false
	e.mu.Unlock()
}

// observe folds one structured event into the round's evidence.
func (e *execEvidence) observe(ev commander.AgentEvent) {
	if e == nil {
		return
	}
	switch ev.Ev {
	case "text", "thinking":
		e.mu.Lock()
		e.streamed = true
		e.mu.Unlock()
	case "tool_use":
		e.toolUse(ev)
	case "tool_result":
		if ev.IsError {
			e.toolFailed(ev.ToolUseID)
		}
	}
}

func (e *execEvidence) toolUse(ev commander.AgentEvent) {
	name := strings.ToLower(ev.Name)
	cmd := commandFromInput(ev.Input)

	e.mu.Lock()
	defer e.mu.Unlock()
	switch {
	case isEditTool(name):
		e.edits++
		e.streamed = true
	case isReadTool(name):
		e.reads++
		e.streamed = true
	}
	if cmd == "" {
		return
	}
	e.streamed = true
	// Keep the newest commands: drop the oldest once the ring is full, and
	// shift the id index with it.
	if len(e.cmds) >= maxEvidenceCommands {
		drop := e.cmds[0].id
		delete(e.byID, drop)
		e.cmds = e.cmds[1:]
		for id, idx := range e.byID {
			if idx == 0 {
				delete(e.byID, id)
			} else {
				e.byID[id] = idx - 1
			}
		}
	}
	e.byID[ev.ID] = len(e.cmds)
	e.cmds = append(e.cmds, evidenceCommand{text: cmd, id: ev.ID})
}

func (e *execEvidence) toolFailed(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	idx, ok := e.byID[id]
	if !ok || idx >= len(e.cmds) {
		// Not a command this round (a plain adapter's opaque run, an Edit, a
		// Read): the verdict must not accuse a command that never existed.
		return
	}
	e.cmds[idx].failed = true
	e.failed++
}

// render builds the judge-facing evidence block. verify lists the project's
// own verification entries (from the workspace scout); when it is non-empty,
// the block states whether the round actually ran one of them, which is the
// fact a judge otherwise cannot check. Empty when the round produced nothing
// worth reporting.
func (e *execEvidence) render(verify []scout.Command) string {
	if e == nil {
		return ""
	}
	e.mu.Lock()
	cmds := make([]evidenceCommand, len(e.cmds))
	copy(cmds, e.cmds)
	failed, edits, reads, streamed := e.failed, e.edits, e.reads, e.streamed
	e.mu.Unlock()

	if !streamed {
		// The adapter ran the CLI as an opaque call; it exposed no tool-level
		// activity, so there is nothing truthful to say about commands.
		return ""
	}
	if len(cmds) == 0 && edits == 0 && reads == 0 && failed == 0 && len(verify) == 0 {
		return ""
	}
	var b strings.Builder
	if len(cmds) > 0 {
		b.WriteString("[本轮实际执行的命令]（共 ")
		b.WriteString(strconv.Itoa(len(cmds)))
		b.WriteString(" 条")
		if failed > 0 {
			b.WriteString("，工具结果标记失败的 ")
			b.WriteString(strconv.Itoa(failed))
			b.WriteString(" 条")
		}
		b.WriteString("；按执行顺序，最多列出最后 20 条）")
		start := 0
		if len(cmds) > 20 {
			start = len(cmds) - 20
		}
		for _, c := range cmds[start:] {
			b.WriteString("\n- ")
			b.WriteString(c.text)
			if c.failed {
				b.WriteString("  [失败]")
			}
		}
	} else {
		b.WriteString("[本轮实际执行的命令] 无 —— 本轮没有执行任何 shell 命令")
	}
	if edits > 0 || reads > 0 {
		b.WriteString("\n[本轮工具用量] 文件改动 ")
		b.WriteString(strconv.Itoa(edits))
		b.WriteString(" 次，读取/检索 ")
		b.WriteString(strconv.Itoa(reads))
		b.WriteString(" 次")
	}
	if len(verify) > 0 {
		entries := make([]string, 0, len(verify))
		for _, c := range verify {
			if c.Run != "" {
				entries = append(entries, c.Run)
			}
		}
		if len(entries) > 0 {
			b.WriteString("\n[项目声明的验证入口] ")
			b.WriteString(strings.Join(entries, "；"))
			switch {
			case len(cmds) == 0:
				b.WriteString("\n[验证证据] 本轮没有执行任何命令，无法证明验证入口跑过")
			case ranVerification(cmds, entries):
				b.WriteString("\n[验证证据] 本轮执行了项目的验证入口")
			default:
				b.WriteString("\n[验证证据] 本轮执行了命令，但没有匹配到项目声明的验证入口")
			}
		}
	}
	return b.String()
}

// baselineEvidence renders the delivery contract's pre-run results. The
// judge otherwise reads a failing check as this run's fault; a check that was
// already red before the agent touched the tree is inherited debt, and the
// task's success definition may explicitly be to fix it or not.
func baselineEvidence(results []contract.Result) string {
	if len(results) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[验收基线—— agent 动手前]")
	for _, r := range results {
		b.WriteString("\n- ")
		if r.CheckID != "" {
			b.WriteString(r.CheckID)
		} else {
			b.WriteString(r.Command)
		}
		if r.Command != "" && r.CheckID != "" {
			b.WriteString(": ")
			b.WriteString(r.Command)
		}
		b.WriteString(" → ")
		b.WriteString(string(r.Status))
		if r.Status != contract.StatusPass && r.Status != contract.StatusFlaky {
			b.WriteString("（运行前就已失败，不要把这一失败归因于本次改动，除非任务本身要求修复它）")
		}
	}
	return b.String()
}

// ranVerification reports whether any executed command names one of the
// project's verification entries. The match is deliberately loose — an agent
// may run `make -j8 test` or `go test ./internal/...` where the entry is
// `make test` / `go test ./...` — so an entry matches when its full text
// appears, or when every token of the entry's leading command appears in the
// executed command.
func ranVerification(cmds []evidenceCommand, entries []string) bool {
	for _, c := range cmds {
		cmd := strings.ToLower(c.text)
		for _, entry := range entries {
			e := strings.ToLower(entry)
			if strings.Contains(cmd, e) {
				return true
			}
			tokens := strings.Fields(e)
			if len(tokens) < 2 {
				continue
			}
			all := true
			for _, tok := range tokens {
				if !strings.Contains(cmd, tok) {
					all = false
					break
				}
			}
			if all {
				return true
			}
		}
	}
	return false
}

// commandFromInput extracts the shell command from a tool_use input blob.
// Shapes differ across CLIs (a string, an argv array, `cmd` instead of
// `command`); a non-shell tool or an unreadable blob yields "". The result is
// capped so one pathological command cannot dominate the judge prompt.
func commandFromInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var probe struct {
		Command json.RawMessage `json:"command"`
		Cmd     json.RawMessage `json:"cmd"`
		Script  string          `json:"script"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	for _, field := range []json.RawMessage{probe.Command, probe.Cmd} {
		if len(field) == 0 {
			continue
		}
		var s string
		if err := json.Unmarshal(field, &s); err == nil {
			return clipCommand(s)
		}
		var argv []string
		if err := json.Unmarshal(field, &argv); err == nil && len(argv) > 0 {
			return clipCommand(strings.Join(argv, " "))
		}
	}
	return clipCommand(probe.Script)
}

func clipCommand(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if r := []rune(s); len(r) > 300 {
		return string(r[:300]) + "…"
	}
	return s
}

func isEditTool(name string) bool {
	switch {
	case strings.Contains(name, "edit"), strings.Contains(name, "write"),
		strings.Contains(name, "patch"), strings.Contains(name, "create"),
		strings.Contains(name, "apply"):
		return true
	}
	return false
}

func isReadTool(name string) bool {
	switch {
	case strings.Contains(name, "read"), strings.Contains(name, "view"),
		strings.Contains(name, "grep"), strings.Contains(name, "glob"),
		strings.Contains(name, "search"), strings.Contains(name, "cat"):
		return true
	}
	return false
}
